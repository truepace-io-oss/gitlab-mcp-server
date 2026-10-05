package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/truepace-io-oss/gitlab-mcp-server/internal/config"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/instances"
)

// startProbeMux reproduces main.go's probe handlers against a registry, so the
// readiness semantics are covered by a test rather than by intent alone.
func startProbeMux(t *testing.T, f *fakeGitLab) *httptest.Server {
	t.Helper()
	cfg := &config.Config{
		LogLevel:        "error",
		MetricsAddr:     "off",
		DefaultInstance: "gl",
		Instances: []config.Instance{{
			Name: "gl", URL: f.Server.URL, Token: "glpat-test", Timeout: "5s",
			Permissions: defaultPerms(),
			Pagination:  config.Pagination{PerPage: 100, MaxPages: 3},
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	reg, err := instances.Build(cfg)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		w.WriteHeader(http.StatusOK)
		if status, perr := reg.Default().Ping(ctx); perr != nil {
			_, _ = fmt.Fprintf(w, "ready (default GitLab instance %q is NOT reachable: %v — tools will return this error; see gmcp_instance_up)\n",
				reg.DefaultName(), perr)
			return
		} else {
			_, _ = fmt.Fprintf(w, "ready (default GitLab instance %q: %s)\n", reg.DefaultName(), status)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestHealthzIsAlwaysOK(t *testing.T) {
	f := newFakeGitLab(t)
	srv := startProbeMux(t, f)
	code, body := get(t, srv.URL+"/healthz")
	if code != http.StatusOK || !strings.Contains(body, "ok") {
		t.Fatalf("healthz = %d %q", code, body)
	}
}

func TestReadyzIsOKWhenGitLabIsHealthy(t *testing.T) {
	f := newFakeGitLab(t)
	srv := startProbeMux(t, f)
	code, body := get(t, srv.URL+"/readyz")
	if code != http.StatusOK {
		t.Fatalf("readyz = %d, want 200", code)
	}
	if !strings.Contains(body, "ready") || !strings.Contains(body, "reachable (GitLab 17.8.1-ee)") {
		t.Fatalf("readyz body should report upstream health: %q", body)
	}
}

// The regression this file exists for.
//
// Readiness must NOT depend on GitLab. Gating it on an external API means a bad
// token takes the pod out of the Service, which removes the ingress route and
// drops the pod from metric scraping — so instances_list and gmcp_instance_up,
// the two things that explain the failure, both become unreachable exactly when
// they are needed.
func TestReadyzStaysOKWhenGitLabIsBroken(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"401 bad token": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"401 Unauthorized"}`))
		},
		"403 forbidden": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"403 Forbidden"}`))
		},
		"500 outage": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		},
		"429 rate limited": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"rate limited"}`))
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeGitLab(t)
			f.route(http.MethodGet, "/version", h)
			srv := startProbeMux(t, f)

			code, body := get(t, srv.URL+"/readyz")
			if code != http.StatusOK {
				t.Fatalf("readyz = %d, want 200 — readiness must not depend on GitLab, "+
					"or a bad token takes the pod out of the Service and hides every diagnostic", code)
			}
			if !strings.Contains(body, "NOT reachable") {
				t.Fatalf("readyz should still report the upstream problem in its body: %q", body)
			}
			// And it must point at where the real signal lives.
			if !strings.Contains(body, "gmcp_instance_up") {
				t.Fatalf("readyz body should name the metric that tracks upstream health: %q", body)
			}
		})
	}
}

// Liveness must be independent too: a GitLab outage must never get the container
// killed and restarted.
func TestHealthzStaysOKWhenGitLabIsBroken(t *testing.T) {
	f := newFakeGitLab(t)
	f.route(http.MethodGet, "/version", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := startProbeMux(t, f)
	if code, _ := get(t, srv.URL+"/healthz"); code != http.StatusOK {
		t.Fatalf("healthz = %d, want 200 regardless of upstream state", code)
	}
}

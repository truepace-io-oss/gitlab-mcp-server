package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/auth"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/config"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/instances"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/mcpserver"
)

// startAuthenticatedServer mirrors main.go's mux: /mcp is wrapped by the auth
// middleware, the probes and the metadata endpoint stay open.
func startAuthenticatedServer(t *testing.T, f *fakeGitLab, authCfg config.Auth) *httptest.Server {
	return startAuthenticatedServerCtx(t, context.Background(), f, authCfg)
}

// startAuthenticatedServerCtx is startAuthenticatedServer with an explicit
// context, so a test can inject the HTTP client OIDC discovery should use.
func startAuthenticatedServerCtx(t *testing.T, ctx context.Context, f *fakeGitLab, authCfg config.Auth) *httptest.Server {
	t.Helper()
	cfg := &config.Config{
		LogLevel:        "error",
		MetricsAddr:     "off",
		DefaultInstance: "gl",
		Instances: []config.Instance{{
			Name: "gl", URL: f.Server.URL, Token: "glpat-test", Timeout: "10s",
			Permissions: defaultPerms(),
			Pagination:  config.Pagination{PerPage: 100, MaxPages: 3},
		}},
		Auth: authCfg,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	reg, err := instances.Build(cfg)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	built, err := auth.Build(ctx, cfg.Auth)
	if err != nil {
		t.Fatalf("auth.Build: %v", err)
	}

	mcpSrv := mcpserver.New(reg, cfg).MCPServer()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpSrv }, nil)

	mux := http.NewServeMux()
	mux.Handle("/mcp", built.Middleware(handler))
	if built.MetadataHandler != nil {
		mux.Handle(built.MetadataPath, built.MetadataHandler)
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ready"))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// post issues a raw MCP initialize request with the given headers and returns
// the status code.
func postMCP(t *testing.T, url string, header http.Header) (int, http.Header) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
		`"protocolVersion":"2025-06-18","capabilities":{},` +
		`"clientInfo":{"name":"e2e","version":"1"}}}`
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, resp.Header
}

func TestStaticTokenAuth(t *testing.T) {
	f := newFakeGitLab(t)

	// A file-backed token is the deployed shape: it is re-read per request, so a
	// rotated secret needs no restart.
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("s3cret-agent-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	srv := startAuthenticatedServer(t, f, config.Auth{
		Enabled: true,
		Static: config.AuthStatic{
			Enabled: true,
			Tokens:  []config.AuthToken{{Name: "ci", TokenFile: tokenFile}},
		},
	})

	t.Run("no token is rejected", func(t *testing.T) {
		code, _ := postMCP(t, srv.URL+"/mcp", nil)
		if code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("wrong token is rejected", func(t *testing.T) {
		h := http.Header{"Authorization": []string{"Bearer nope"}}
		code, _ := postMCP(t, srv.URL+"/mcp", h)
		if code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("correct token is accepted", func(t *testing.T) {
		h := http.Header{"Authorization": []string{"Bearer s3cret-agent-token"}}
		code, _ := postMCP(t, srv.URL+"/mcp", h)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
	})

	t.Run("a rotated token file is picked up without a restart", func(t *testing.T) {
		if err := os.WriteFile(tokenFile, []byte("rotated-token"), 0o600); err != nil {
			t.Fatal(err)
		}
		if code, _ := postMCP(t, srv.URL+"/mcp",
			http.Header{"Authorization": []string{"Bearer rotated-token"}}); code != http.StatusOK {
			t.Fatalf("rotated token rejected: %d", code)
		}
		if code, _ := postMCP(t, srv.URL+"/mcp",
			http.Header{"Authorization": []string{"Bearer s3cret-agent-token"}}); code != http.StatusUnauthorized {
			t.Fatalf("the old token should no longer work: %d", code)
		}
	})

	t.Run("health probes stay open", func(t *testing.T) {
		for _, p := range []string{"/healthz", "/readyz"} {
			resp, err := http.Get(srv.URL + p)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s status = %d, want 200", p, resp.StatusCode)
			}
		}
	})
}

// With auth disabled the transport is open — which is why the config emits a
// warning and the docs insist on a protected ingress.
func TestAuthDisabledLeavesTransportOpen(t *testing.T) {
	f := newFakeGitLab(t)
	srv := startAuthenticatedServer(t, f, config.Auth{Enabled: false})
	if code, _ := postMCP(t, srv.URL+"/mcp", nil); code != http.StatusOK {
		t.Fatalf("status = %d, want 200 when auth is disabled", code)
	}
}

// The RFC 9728 metadata must advertise the canonical public URL as `resource`,
// NOT the token audience. Conflating the two is the bug the upstream fix
// addressed; this test is the regression guard.
func TestProtectedResourceMetadataUsesResourceNotAudience(t *testing.T) {
	f := newFakeGitLab(t)
	issuer, ctx := startFakeIssuer(t)

	srv := startAuthenticatedServerCtx(t, ctx, f, config.Auth{
		Enabled: true,
		OIDC: config.AuthOIDC{
			Enabled:  true,
			Issuer:   issuer,
			Audience: "gitlab-mcp",
			Resource: "https://gitlab-mcp.example.com/mcp",
		},
	})

	resp, err := http.Get(srv.URL + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metadata status = %d", resp.StatusCode)
	}

	var md struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		ResourceName         string   `json:"resource_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&md); err != nil {
		t.Fatal(err)
	}
	if md.Resource != "https://gitlab-mcp.example.com/mcp" {
		t.Fatalf("resource = %q, want the canonical public URL (not the audience)", md.Resource)
	}
	if md.Resource == "gitlab-mcp" {
		t.Fatal("resource must never be the token audience")
	}
	if len(md.AuthorizationServers) != 1 || md.AuthorizationServers[0] != issuer {
		t.Fatalf("authorization_servers = %v, want [%s]", md.AuthorizationServers, issuer)
	}
	if md.ResourceName != "gitlab-mcp" {
		t.Fatalf("resource_name = %q", md.ResourceName)
	}
}

// An unauthenticated request must point the client at the metadata document, so
// a compliant client can discover where to log in.
func TestUnauthorizedResponseAdvertisesResourceMetadata(t *testing.T) {
	f := newFakeGitLab(t)
	issuer, ctx := startFakeIssuer(t)

	srv := startAuthenticatedServerCtx(t, ctx, f, config.Auth{
		Enabled: true,
		OIDC: config.AuthOIDC{
			Enabled:  true,
			Issuer:   issuer,
			Audience: "gitlab-mcp",
			Resource: "https://gitlab-mcp.example.com/mcp",
		},
	})

	code, header := postMCP(t, srv.URL+"/mcp", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", code)
	}
	wa := header.Get("WWW-Authenticate")
	if !strings.Contains(wa, "resource_metadata") {
		t.Fatalf("WWW-Authenticate = %q, want a resource_metadata pointer", wa)
	}
}

// startFakeIssuer serves the minimum OIDC discovery document the verifier needs
// at construction time. It is TLS-backed because the config (rightly) refuses a
// plaintext issuer, and returns a context carrying the client that trusts it.
func startFakeIssuer(t *testing.T) (string, context.Context) {
	t.Helper()
	mux := http.NewServeMux()
	var issuerURL string
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuerURL,
			"authorization_endpoint":                issuerURL + "/authorize",
			"token_endpoint":                        issuerURL + "/token",
			"jwks_uri":                              issuerURL + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	issuerURL = srv.URL
	return srv.URL, oidc.ClientContext(context.Background(), srv.Client())
}

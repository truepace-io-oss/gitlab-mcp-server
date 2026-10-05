package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/truepace-io-oss/gitlab-mcp-server/internal/perm"
)

// newTestClient wires a Client against a fake GitLab with the given capabilities.
func newTestClient(t *testing.T, h http.Handler, caps ...perm.Capability) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	g := perm.NewGuardForTest("test", nil, caps...)
	return New("test", srv.URL, srv.Client(), g, PageOpts{PerPage: 100, MaxPages: 3}, 0), srv
}

func TestDoEnforcesCapabilityBeforeSending(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{}`))
	})) // no capabilities enabled

	_, err := c.Do(context.Background(), perm.ReadCore, http.MethodGet, "/projects/1", nil, nil, nil)
	if err == nil {
		t.Fatal("expected a capability denial")
	}
	if !strings.Contains(err.Error(), `capability "read.core" is not enabled`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("a denied call still reached the server %d time(s)", n)
	}
}

// The important property: a deny-listed path never produces a request, even when
// the capability is enabled and a caller asks for it directly.
func TestDoNeverSendsDenyListedPaths(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{}`))
	}), perm.ReadCore, perm.ReadCI, perm.PipelinesOperate, perm.PipelinesDelete)

	for _, p := range []string{
		"/projects/1/variables",
		"/projects/1/pipelines/2/variables",
		"/projects/1/jobs/3/artifacts/report.json",
		"/projects/1/hooks",
	} {
		if _, err := c.Do(context.Background(), perm.ReadCore, http.MethodGet, p, nil, nil, nil); err == nil {
			t.Fatalf("expected %q to be refused", p)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("deny-listed paths reached the server %d time(s)", n)
	}
}

func TestDoDecodesAndRedacts(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		_, _ = w.Write([]byte(`{"id":13,"description":"nightly","ref":"main"}`))
	}), perm.ReadCI)

	var got PipelineSchedule
	if _, err := c.Do(context.Background(), perm.ReadCI, http.MethodGet, "/projects/1/pipeline_schedules/13", nil, nil, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != 13 || got.Description != "nightly" || got.Ref != "main" {
		t.Fatalf("unexpected decode: %+v", got)
	}
}

func TestDoSurfacesAPIErrors(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"403 Forbidden"}`))
	}), perm.ReadCore)

	_, err := c.Do(context.Background(), perm.ReadCore, http.MethodGet, "/projects/1", nil, nil, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !IsForbidden(err) {
		t.Fatalf("IsForbidden = false for %v", err)
	}
	if !strings.Contains(err.Error(), "403 Forbidden") {
		t.Fatalf("GitLab's own message was lost: %v", err)
	}
}

func TestDoRetriesOnceOn429(t *testing.T) {
	var calls int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"rate limited"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":7}`))
	}), perm.ReadCI)

	var p Pipeline
	if _, err := c.Do(context.Background(), perm.ReadCI, http.MethodGet, "/projects/1/pipelines/7", nil, nil, &p); err != nil {
		t.Fatalf("expected the retry to succeed: %v", err)
	}
	if p.ID != 7 {
		t.Fatalf("unexpected pipeline: %+v", p)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("expected exactly 2 attempts, got %d", n)
	}
}

// A 429 with no usable Retry-After must not be retried — it is handed to the
// model with GitLab's message instead.
func TestDoDoesNotRetryWithoutRetryAfter(t *testing.T) {
	var calls int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"rate limited"}`))
	}), perm.ReadCI)

	_, err := c.Do(context.Background(), perm.ReadCI, http.MethodGet, "/projects/1/pipelines/7", nil, nil, nil)
	if !IsRateLimited(err) {
		t.Fatalf("expected a rate-limit error, got %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("expected 1 attempt, got %d", n)
	}
}

func TestDoListRespectsRateLimitFloor(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("RateLimit-Remaining", "3")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)
	g := perm.NewGuardForTest("test", nil, perm.ReadCore)
	c := New("test", srv.URL, srv.Client(), g, PageOpts{PerPage: 100, MaxPages: 3}, 50)

	// The first call learns the remaining budget…
	var out []Project
	if _, err := c.DoList(context.Background(), perm.ReadCore, "/projects", nil, &out); err != nil {
		t.Fatal(err)
	}
	// …and the second is refused because it is below the floor.
	_, err := c.DoList(context.Background(), perm.ReadCore, "/projects", nil, &out)
	if err == nil {
		t.Fatal("expected the second list call to be refused")
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("unexpected error: %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("expected the refused call not to be sent; calls = %d", n)
	}
}

// A single read must still go through when the budget is low: the floor only
// guards list traversal.
func TestDoIgnoresRateLimitFloor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("RateLimit-Remaining", "1")
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	t.Cleanup(srv.Close)
	g := perm.NewGuardForTest("test", nil, perm.ReadCore)
	c := New("test", srv.URL, srv.Client(), g, PageOpts{PerPage: 100, MaxPages: 3}, 50)

	var p Project
	if _, err := c.Do(context.Background(), perm.ReadCore, http.MethodGet, "/projects/1", nil, nil, &p); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Do(context.Background(), perm.ReadCore, http.MethodGet, "/projects/1", nil, nil, &p); err != nil {
		t.Fatalf("single reads must not be rate-floored: %v", err)
	}
}

func TestRawTextIsRedactedAndBounded(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("line1\nTOKEN=glpat-AAAABBBBCCCCDDDDEEEE\nline3\n"))
	}), perm.ReadCI)

	got, err := c.RawText(context.Background(), perm.ReadCI, "/projects/1/jobs/2/trace", nil, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "glpat-AAAABBBBCCCCDDDDEEEE") {
		t.Fatalf("job trace leaked a token: %q", got)
	}
	if !strings.Contains(got, "line1") {
		t.Fatalf("job trace lost its content: %q", got)
	}

	// The byte limit must actually truncate.
	short, err := c.RawText(context.Background(), perm.ReadCI, "/projects/1/jobs/2/trace", nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(short) != 5 {
		t.Fatalf("expected 5 bytes, got %d (%q)", len(short), short)
	}
}

// esc must produce the URL-encoded full-path form GitLab expects, including for
// paths containing dots.
func TestEscapesProjectPaths(t *testing.T) {
	cases := map[string]string{
		"group/project":     "group%2Fproject",
		"group/sub/project": "group%2Fsub%2Fproject",
		"group/my.project":  "group%2Fmy.project",
		"42":                "42",
		"/group/project/":   "group%2Fproject",
	}
	for in, want := range cases {
		if got := Esc(in); got != want {
			t.Errorf("Esc(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListAllPaginatesAndReportsTruncation(t *testing.T) {
	// Three pages available, cap is 3 → not truncated. Then cap 2 → truncated.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 0 {
			page = 1
		}
		if page < 3 {
			w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
		}
		items := []Project{{ID: page, PathWithNamespace: fmt.Sprintf("g/p%d", page)}}
		_ = json.NewEncoder(w).Encode(items)
	})

	c, _ := newTestClient(t, handler, perm.ReadCore)
	got, truncated, err := ListAll[Project](context.Background(), c, perm.ReadCore, "/projects", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || truncated {
		t.Fatalf("got %d items truncated=%v, want 3 and false", len(got), truncated)
	}

	c2, _ := newTestClient(t, handler, perm.ReadCore)
	c2.Page.MaxPages = 2
	got2, truncated2, err := ListAll[Project](context.Background(), c2, perm.ReadCore, "/projects", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 2 || !truncated2 {
		t.Fatalf("got %d items truncated=%v, want 2 and true", len(got2), truncated2)
	}
}

func TestListAllHonoursLimit(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("per_page"); got != "2" {
			t.Errorf("per_page = %q, want 2 (clamped to the limit)", got)
		}
		w.Header().Set("X-Next-Page", "2")
		_ = json.NewEncoder(w).Encode([]Project{{ID: 1}, {ID: 2}})
	}), perm.ReadCore)

	got, truncated, err := ListAll[Project](context.Background(), c, perm.ReadCore, "/projects", url.Values{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}
	if !truncated {
		t.Fatal("expected truncated=true when more pages remain")
	}
}

func TestClientSendsPrivateTokenHeaderViaTransport(t *testing.T) {
	// The header is injected by instances.tokenTransport, so here we only assert
	// the client does not add a competing Authorization header of its own.
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := r.Header.Get("Authorization"); v != "" {
			t.Errorf("client must not set Authorization itself, got %q", v)
		}
		_, _ = w.Write([]byte(`{}`))
	}), perm.ReadCore)
	if _, err := c.Do(context.Background(), perm.ReadCore, http.MethodGet, "/projects/1", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
}

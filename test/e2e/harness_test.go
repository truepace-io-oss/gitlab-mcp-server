// Package e2e drives the gitlab-mcp server end to end: the real MCP server
// behind the real streamable-HTTP transport, driven by the official MCP client,
// talking to a fake GitLab that records every request it receives.
//
// The request recorder is what makes the security assertions meaningful: it lets
// a test prove that a deny-listed endpoint was never contacted, rather than
// merely that the tool returned an error.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/config"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/instances"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/mcpserver"
)

// fakeGitLab is a minimal stand-in for the subset of the GitLab API the tools
// use. Handlers are registered per path prefix; everything else 404s with a
// GitLab-shaped body.
type fakeGitLab struct {
	*httptest.Server

	mu       sync.Mutex
	requests []recordedRequest
	routes   map[string]http.HandlerFunc
}

type recordedRequest struct {
	Method string
	Path   string
	Query  string
	Body   string
}

func newFakeGitLab(t *testing.T) *fakeGitLab {
	t.Helper()
	f := &fakeGitLab{routes: map[string]http.HandlerFunc{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Server.Close)
	f.installDefaults()
	return f
}

func (f *fakeGitLab) serve(w http.ResponseWriter, r *http.Request) {
	body := ""
	if r.Body != nil {
		buf := make([]byte, 1<<16)
		n, _ := r.Body.Read(buf)
		body = string(buf[:n])
	}
	// EscapedPath keeps %2F intact. Real GitLab treats an encoded project path as
	// one path segment, so matching on the decoded path would make the fake
	// accept requests the real API would reject.
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4")

	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{
		Method: r.Method, Path: path, Query: r.URL.RawQuery, Body: body,
	})
	handler, ok := f.routes[r.Method+" "+path]
	f.mu.Unlock()

	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"message":"404 Not Found: %s %s"}`, r.Method, path)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	handler(w, r)
}

// route registers a handler for an exact method+path.
func (f *fakeGitLab) route(method, path string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[method+" "+path] = h
}

// json registers a handler that returns a fixed JSON document.
func (f *fakeGitLab) json(method, path, body string) {
	f.route(method, path, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
}

// recorded returns every request seen so far.
func (f *fakeGitLab) recorded() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

// reset clears the recorded requests, so a test can assert on a single call.
func (f *fakeGitLab) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = nil
}

// assertNeverRequested fails if any recorded path contains one of the fragments.
// This is how the deny list is verified: not "the tool errored", but "the bytes
// never left the process".
func (f *fakeGitLab) assertNeverRequested(t *testing.T, fragments ...string) {
	t.Helper()
	for _, r := range f.recorded() {
		for _, frag := range fragments {
			if strings.Contains(r.Path, frag) {
				t.Errorf("a request reached GitLab that must never be sent: %s %s", r.Method, r.Path)
			}
		}
	}
}

const (
	projectPath = "example-group/app"
	// projectEsc is the URL-encoded form GitLab expects as :id.
	projectEsc = "example-group%2Fapp"
)

// installDefaults wires the handlers nearly every test needs.
func (f *fakeGitLab) installDefaults() {
	f.json(http.MethodGet, "/version", `{"version":"17.8.1-ee","revision":"abc123"}`)
	f.json(http.MethodGet, "/personal_access_tokens/self",
		`{"name":"gitlab-mcp","scopes":["api"],"expires_at":"","active":true,"revoked":false,"user_id":7}`)
	f.json(http.MethodGet, "/projects/"+projectEsc, fmt.Sprintf(`{
	  "id": 42, "name": "app", "path": "app", "path_with_namespace": %q,
	  "description": "the app", "default_branch": "main", "visibility": "private",
	  "archived": false, "web_url": "https://gitlab.example.com/%s",
	  "last_activity_at": "2026-01-01T00:00:00Z", "open_issues_count": 3, "star_count": 1,
	  "namespace": {"id": 9, "full_path": "example-group", "kind": "group"}
	}`, projectPath, projectPath))
}

// testServer is the MCP server under test plus its fake GitLab.
type testServer struct {
	gitlab *fakeGitLab
	http   *httptest.Server
}

// instanceOpts tunes the single configured instance.
type instanceOpts struct {
	readOnly          bool
	globalReadOnly    bool
	allowedNamespaces []string
	perms             config.Permissions
}

// defaultPerms mirrors the recommended deployment profile: read everything,
// operate pipelines, no pipeline deletion, no CI variables.
func defaultPerms() config.Permissions {
	return config.Permissions{
		Read:      config.ReadPerms{Core: true, CI: true},
		Pipelines: config.PipelinePerms{Operate: true, Delete: false, AllowVariables: false},
	}
}

func startServer(t *testing.T, f *fakeGitLab, o instanceOpts) *testServer {
	t.Helper()
	if o.perms == (config.Permissions{}) {
		o.perms = defaultPerms()
	}
	cfg := &config.Config{
		ListenAddr:      "127.0.0.1:0",
		MetricsAddr:     "off",
		LogLevel:        "error",
		ReadOnly:        o.globalReadOnly,
		DefaultInstance: "gl",
		Instances: []config.Instance{{
			Name:              "gl",
			URL:               f.Server.URL,
			Token:             "glpat-test-token",
			Timeout:           "10s",
			ReadOnly:          o.readOnly,
			AllowedNamespaces: o.allowedNamespaces,
			Permissions:       o.perms,
			Pagination:        config.Pagination{PerPage: 100, MaxPages: 3},
			RateLimit:         config.RateLimit{MinRemaining: 0},
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test config is invalid: %v", err)
	}

	reg, err := instances.Build(cfg)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	srv := mcpserver.New(reg, cfg)
	mcpSrv := srv.MCPServer()

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpSrv }, nil)
	httpSrv := httptest.NewServer(handler)
	t.Cleanup(httpSrv.Close)

	return &testServer{gitlab: f, http: httpSrv}
}

// connect returns a live MCP client session against the server.
func (ts *testServer) connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-test", Version: "1"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: ts.http.URL}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect to MCP server: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// callTool invokes a tool and returns its text content plus whether it was an
// error result.
func callTool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: transport error %v (expected failures should arrive as tool errors)", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

// mustCall fails when the tool returned an error result.
func mustCall(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	text, isErr := callTool(t, s, name, args)
	if isErr {
		t.Fatalf("call %s returned a tool error: %s", name, text)
	}
	return text
}

// mustFail requires an error result and returns its text.
func mustFail(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	text, isErr := callTool(t, s, name, args)
	if !isErr {
		t.Fatalf("call %s was expected to fail but returned: %s", name, text)
	}
	return text
}

// toolNames lists the tools the server advertises.
func toolNames(t *testing.T, s *mcp.ClientSession) map[string]bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := s.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	out := map[string]bool{}
	for _, tool := range res.Tools {
		out[tool.Name] = true
	}
	return out
}

// jsonBody is a convenience for building fake responses in tests.
func jsonBody(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

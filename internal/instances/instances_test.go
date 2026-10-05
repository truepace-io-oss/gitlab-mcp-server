package instances

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truepace-io-oss/gitlab-mcp-server/internal/config"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/perm"
)

func testInstanceConfig(name, url string) config.Instance {
	return config.Instance{
		Name:       name,
		URL:        url,
		Token:      "glpat-test",
		Timeout:    "10s",
		Pagination: config.Pagination{PerPage: 100, MaxPages: 3},
		RateLimit:  config.RateLimit{MinRemaining: 0},
		Permissions: config.Permissions{
			Read: config.ReadPerms{Core: true, CI: true},
		},
	}
}

// The token header must be injected, and injected as PRIVATE-TOKEN — not as an
// Authorization bearer, which would collide with the agent-auth header.
func TestTokenTransportInjectsPrivateToken(t *testing.T) {
	var got http.Header
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = r.Header.Clone()
		mu.Unlock()
		_, _ = w.Write([]byte(`{"version":"17.0.0"}`))
	}))
	t.Cleanup(srv.Close)

	in, err := newInstance(false, testInstanceConfig("gl", srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if v := got.Get("PRIVATE-TOKEN"); v != "glpat-test" {
		t.Fatalf("PRIVATE-TOKEN = %q, want glpat-test", v)
	}
	if v := got.Get("Authorization"); v != "" {
		t.Fatalf("the GitLab token must not be sent as Authorization, got %q", v)
	}
}

// A file-backed token is re-read on every request, so rotating the secret needs
// no restart. This is the behaviour the deployment depends on.
func TestTokenFileIsRereadPerRequest(t *testing.T) {
	var seen []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("PRIVATE-TOKEN"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"version":"17.0.0"}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("first-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := testInstanceConfig("gl", srv.URL)
	cfg.Token = ""
	cfg.TokenFile = tokenFile
	in, err := newInstance(false, cfg)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := in.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenFile, []byte("second-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(seen))
	}
	if seen[0] != "first-token" {
		t.Errorf("first request sent %q", seen[0])
	}
	if seen[1] != "second-token" {
		t.Errorf("the rotated token was not picked up; second request sent %q", seen[1])
	}
}

// A trailing newline in a mounted secret file is common and must be tolerated.
func TestTokenFileIsTrimmed(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "token")
	if err := os.WriteFile(f, []byte("  glpat-padded\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := valueOrFile("", f)
	if err != nil {
		t.Fatal(err)
	}
	if got != "glpat-padded" {
		t.Fatalf("valueOrFile = %q", got)
	}
}

func TestTokenFileErrorIsSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	cfg := testInstanceConfig("gl", srv.URL)
	cfg.Token = ""
	cfg.TokenFile = filepath.Join(t.TempDir(), "absent")
	in, err := newInstance(false, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = in.Ping(context.Background())
	if err == nil {
		t.Fatal("expected an error for an unreadable token file")
	}
	if !strings.Contains(err.Error(), "gitlab token") {
		t.Fatalf("the error should name the cause: %v", err)
	}
}

func TestPingReportsVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"17.8.1-ee"}`))
	}))
	t.Cleanup(srv.Close)

	in, err := newInstance(false, testInstanceConfig("gl", srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	got, err := in.Ping(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "17.8.1-ee") {
		t.Fatalf("Ping = %q", got)
	}
}

func TestTokenInfoParsesExpiry(t *testing.T) {
	future := time.Now().AddDate(0, 0, 40).Format("2006-01-02")
	soon := time.Now().AddDate(0, 0, 3).Format("2006-01-02")
	past := time.Now().AddDate(0, 0, -1).Format("2006-01-02")

	cases := []struct {
		name      string
		expiresAt string
		active    bool
		hasExpiry bool
		wantWarn  string
	}{
		{"no expiry", "", true, false, ""},
		{"far future", future, true, true, ""},
		{"expiring soon", soon, true, true, "expires in"},
		{"expired", past, true, true, "has expired"},
		{"revoked", future, false, true, "inactive or revoked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"name":"gitlab-mcp","scopes":["api"],"expires_at":"` +
					tc.expiresAt + `","active":` + boolStr(tc.active) + `}`))
			}))
			t.Cleanup(srv.Close)

			in, err := newInstance(false, testInstanceConfig("gl", srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			st, err := in.TokenInfo(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if st.HasExpiry != tc.hasExpiry {
				t.Errorf("HasExpiry = %v, want %v", st.HasExpiry, tc.hasExpiry)
			}
			warn := st.Warning()
			if tc.wantWarn == "" && warn != "" {
				t.Errorf("unexpected warning: %q", warn)
			}
			if tc.wantWarn != "" && !strings.Contains(warn, tc.wantWarn) {
				t.Errorf("Warning() = %q, want it to contain %q", warn, tc.wantWarn)
			}
		})
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestRegistryGetAndDefault(t *testing.T) {
	cfg := &config.Config{
		DefaultInstance: "two",
		Instances: []config.Instance{
			testInstanceConfig("one", "https://one.example.com"),
			testInstanceConfig("two", "https://two.example.com"),
		},
	}
	reg, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}

	if reg.DefaultName() != "two" {
		t.Fatalf("DefaultName = %q", reg.DefaultName())
	}
	// An empty name resolves to the default.
	in, err := reg.Get("")
	if err != nil || in.Name != "two" {
		t.Fatalf("Get(\"\") = %v, %v", in, err)
	}
	if in, err := reg.Get("one"); err != nil || in.Name != "one" {
		t.Fatalf("Get(\"one\") = %v, %v", in, err)
	}
	if reg.Default().Name != "two" {
		t.Fatalf("Default() = %q", reg.Default().Name)
	}

	// A miss must list the valid names, so the model can correct itself.
	_, err = reg.Get("three")
	if err == nil {
		t.Fatal("expected an error for an unknown instance")
	}
	for _, want := range []string{`unknown GitLab instance "three"`, "one", "two"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err, want)
		}
	}
}

// All() must be sorted, so instances_list output is stable between calls.
func TestRegistryAllIsSorted(t *testing.T) {
	cfg := &config.Config{
		DefaultInstance: "zebra",
		Instances: []config.Instance{
			testInstanceConfig("zebra", "https://z.example.com"),
			testInstanceConfig("alpha", "https://a.example.com"),
			testInstanceConfig("mid", "https://m.example.com"),
		},
	}
	reg, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := reg.All()
	want := []string{"alpha", "mid", "zebra"}
	if len(got) != len(want) {
		t.Fatalf("All() returned %d instances", len(got))
	}
	for i, n := range want {
		if got[i].Name != n {
			t.Fatalf("All() = %q at %d, want %q", got[i].Name, i, n)
		}
	}
	// Names() keeps configuration order.
	if names := reg.Names(); names[0] != "zebra" {
		t.Fatalf("Names() should preserve config order, got %v", names)
	}
}

func TestBuildRejectsMissingDefault(t *testing.T) {
	cfg := &config.Config{
		DefaultInstance: "absent",
		Instances:       []config.Instance{testInstanceConfig("one", "https://one.example.com")},
	}
	if _, err := Build(cfg); err == nil {
		t.Fatal("expected Build to reject an unknown defaultInstance")
	}
}

// Build must not contact GitLab: an unreachable instance still constructs, so a
// degraded remote cannot prevent startup.
func TestBuildDoesNotContactGitLab(t *testing.T) {
	cfg := &config.Config{
		DefaultInstance: "unreachable",
		Instances: []config.Instance{
			testInstanceConfig("unreachable", "https://127.0.0.1:1/"),
		},
	}
	reg, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build must succeed for an unreachable instance: %v", err)
	}
	// …and the failure shows up at Ping time instead.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := reg.Default().Ping(ctx); err == nil {
		t.Fatal("expected Ping to fail for an unreachable instance")
	}
}

func TestNewForTestWiresAGuard(t *testing.T) {
	g := perm.NewGuardForTest("gl", []string{"team"}, perm.ReadCore)
	in := NewForTest("gl", "http://127.0.0.1:1", g)
	if in.Guard != g {
		t.Fatal("NewForTest should keep the supplied guard")
	}
	if !in.Guard.Enabled(perm.ReadCore) {
		t.Fatal("the guard lost its capability")
	}
}

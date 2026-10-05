package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimal returns a config that passes Validate, as a base for mutation.
func minimal() *Config {
	return &Config{
		LogLevel:        "info",
		DefaultInstance: "gl",
		Instances: []Instance{{
			Name:       "gl",
			URL:        "https://gitlab.example.com",
			Token:      "glpat-test",
			Timeout:    "30s",
			Pagination: Pagination{PerPage: 100, MaxPages: 10},
			RateLimit:  RateLimit{MinRemaining: 50},
		}},
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
defaultInstance: gl
instances:
  - name: gl
    url: https://gitlab.example.com
    token: glpat-test
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "0.0.0.0:9090" {
		t.Errorf("ListenAddr = %q", cfg.ListenAddr)
	}
	if cfg.MetricsAddr != ":9091" {
		t.Errorf("MetricsAddr = %q", cfg.MetricsAddr)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q", cfg.LogLevel)
	}
	in := cfg.Instances[0]
	if in.Timeout != DefaultTimeout {
		t.Errorf("Timeout = %q", in.Timeout)
	}
	if in.Pagination.PerPage != DefaultPerPage || in.Pagination.MaxPages != DefaultMaxPages {
		t.Errorf("Pagination = %+v", in.Pagination)
	}
	if in.RateLimit.MinRemaining != DefaultMinRemaining {
		t.Errorf("RateLimit = %+v", in.RateLimit)
	}
	if cfg.Auth.OIDC.GroupsClaim != "groups" || cfg.Auth.OIDC.UsernameClaim != "preferred_username" {
		t.Errorf("OIDC claim defaults not applied: %+v", cfg.Auth.OIDC)
	}
}

func TestSingleInstanceBecomesDefault(t *testing.T) {
	c := minimal()
	c.DefaultInstance = ""
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.DefaultInstance != "gl" {
		t.Fatalf("DefaultInstance = %q", c.DefaultInstance)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"bad log level", func(c *Config) { c.LogLevel = "trace" }, "invalid logLevel"},
		{"no instances", func(c *Config) { c.Instances = nil }, "no GitLab instances configured"},
		{"empty name", func(c *Config) { c.Instances[0].Name = "" }, "empty name"},
		{"bad name", func(c *Config) { c.Instances[0].Name = "Not_A_Label" }, "must be a DNS label"},
		{"duplicate name", func(c *Config) {
			c.Instances = append(c.Instances, c.Instances[0])
		}, "duplicate instance name"},
		{"no url", func(c *Config) { c.Instances[0].URL = "" }, "url is required"},
		{"no host", func(c *Config) { c.Instances[0].URL = "https://" }, "must include a host"},
		{"plaintext remote", func(c *Config) { c.Instances[0].URL = "http://gitlab.example.com" }, "must be https"},
		{"bad scheme", func(c *Config) { c.Instances[0].URL = "ftp://gitlab.example.com" }, "must be http or https"},
		{"no token", func(c *Config) { c.Instances[0].Token = "" }, "exactly one of token or tokenFile"},
		{"both tokens", func(c *Config) { c.Instances[0].TokenFile = "/tmp/t" }, "exactly one of token or tokenFile"},
		{"bad timeout", func(c *Config) { c.Instances[0].Timeout = "soon" }, "invalid timeout"},
		{"bad namespace", func(c *Config) { c.Instances[0].AllowedNamespaces = []string{"has space"} }, "not a valid namespace path"},
		{"perPage too high", func(c *Config) { c.Instances[0].Pagination.PerPage = 500 }, "perPage must be between"},
		{"maxPages zero", func(c *Config) { c.Instances[0].Pagination.MaxPages = -1 }, "maxPages must be at least"},
		{"insecure plus CA", func(c *Config) {
			c.Instances[0].TLS.InsecureSkipTLSVerify = true
			c.Instances[0].TLS.CAFile = "/tmp/ca.crt"
		}, "must not be combined with a CA"},
		{"unknown default", func(c *Config) { c.DefaultInstance = "nope" }, "is not one of the configured instances"},
		{"multi instance without default", func(c *Config) {
			c.Instances = append(c.Instances, Instance{
				Name: "two", URL: "https://two.example.com", Token: "t", Timeout: "30s",
				Pagination: Pagination{PerPage: 100, MaxPages: 10},
			})
			c.DefaultInstance = ""
		}, "defaultInstance must be set"},

		// --- auth ---
		{"auth without verifier", func(c *Config) { c.Auth.Enabled = true }, "neither auth.static nor auth.oidc"},
		{"static without tokens", func(c *Config) {
			c.Auth.Enabled = true
			c.Auth.Static.Enabled = true
		}, "no tokens configured"},
		{"static token without name", func(c *Config) {
			c.Auth.Enabled = true
			c.Auth.Static.Enabled = true
			c.Auth.Static.Tokens = []AuthToken{{Token: "x"}}
		}, "name is required"},
		{"static duplicate name", func(c *Config) {
			c.Auth.Enabled = true
			c.Auth.Static.Enabled = true
			c.Auth.Static.Tokens = []AuthToken{{Name: "a", Token: "x"}, {Name: "a", Token: "y"}}
		}, "duplicate name"},
		{"static both token forms", func(c *Config) {
			c.Auth.Enabled = true
			c.Auth.Static.Enabled = true
			c.Auth.Static.Tokens = []AuthToken{{Name: "a", Token: "x", TokenFile: "/tmp/t"}}
		}, "exactly one of token or tokenFile"},
		{"oidc without issuer", func(c *Config) {
			c.Auth.Enabled = true
			c.Auth.OIDC = AuthOIDC{Enabled: true}
		}, "issuer is empty"},
		{"oidc plaintext issuer", func(c *Config) {
			c.Auth.Enabled = true
			c.Auth.OIDC = AuthOIDC{Enabled: true, Issuer: "http://auth.example.com"}
		}, "must be an https URL"},
		{"oidc without audience", func(c *Config) {
			c.Auth.Enabled = true
			c.Auth.OIDC = AuthOIDC{Enabled: true, Issuer: "https://auth.example.com"}
		}, "audience is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := minimal()
			tc.mutate(c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("expected an error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

// A loopback URL is the one case where plaintext is acceptable: it is how the
// e2e harness points the client at an httptest server.
func TestLoopbackHTTPIsAllowed(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:8080", "http://localhost:8080"} {
		c := minimal()
		c.Instances[0].URL = u
		if err := c.Validate(); err != nil {
			t.Errorf("%s should be allowed: %v", u, err)
		}
	}
}

// The OAuth resource identifier must be an absolute https URI whenever the
// RFC 9728 metadata endpoint is served. Because the audience here is typically a
// bare client id, the audience fallback is deliberately not a usable config.
func TestOIDCResourceValidation(t *testing.T) {
	base := func() *Config {
		c := minimal()
		c.Auth.Enabled = true
		c.Auth.OIDC = AuthOIDC{Enabled: true, Issuer: "https://auth.example.com", Audience: "gitlab-mcp"}
		return c
	}

	t.Run("bare audience fallback is rejected", func(t *testing.T) {
		c := base()
		err := c.Validate()
		if err == nil {
			t.Fatal("expected the bare-audience fallback to be rejected")
		}
		if !strings.Contains(err.Error(), "auth.oidc.resource must be an absolute https URI") {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(err.Error(), `effective value "gitlab-mcp"`) {
			t.Fatalf("error should name the effective value: %v", err)
		}
	})

	t.Run("explicit resource is accepted", func(t *testing.T) {
		c := base()
		c.Auth.OIDC.Resource = "https://gitlab-mcp.example.com/mcp"
		if err := c.Validate(); err != nil {
			t.Fatalf("expected a valid config: %v", err)
		}
	})

	t.Run("audience fallback works when it is already a URI", func(t *testing.T) {
		c := base()
		c.Auth.OIDC.Audience = "https://gitlab-mcp.example.com/mcp"
		if err := c.Validate(); err != nil {
			t.Fatalf("expected the URI audience to be accepted: %v", err)
		}
		if got := c.Auth.OIDC.ResourceIdentifier(); got != "https://gitlab-mcp.example.com/mcp" {
			t.Fatalf("ResourceIdentifier = %q", got)
		}
	})

	for _, bad := range []string{"gitlab-mcp", "/mcp", "http://gitlab-mcp.example.com/mcp", "https://gitlab-mcp.example.com/mcp#frag"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			c := base()
			c.Auth.OIDC.Resource = bad
			if err := c.Validate(); err == nil {
				t.Fatalf("expected %q to be rejected", bad)
			}
		})
	}

	t.Run("not validated when metadata is disabled", func(t *testing.T) {
		c := base()
		off := false
		c.Auth.OIDC.ResourceMetadata = &off
		if err := c.Validate(); err != nil {
			t.Fatalf("expected no resource requirement without metadata: %v", err)
		}
	})
}

func TestResourceIdentifierPrefersResource(t *testing.T) {
	o := AuthOIDC{Audience: "aud", Resource: "https://x.example.com/mcp"}
	if got := o.ResourceIdentifier(); got != "https://x.example.com/mcp" {
		t.Fatalf("ResourceIdentifier = %q", got)
	}
	o2 := AuthOIDC{Audience: "aud"}
	if got := o2.ResourceIdentifier(); got != "aud" {
		t.Fatalf("fallback ResourceIdentifier = %q", got)
	}
}

func TestServeResourceMetadataDefaultsTrue(t *testing.T) {
	if !(AuthOIDC{}).ServeResourceMetadata() {
		t.Fatal("resource metadata should default to enabled")
	}
	off := false
	if (AuthOIDC{ResourceMetadata: &off}).ServeResourceMetadata() {
		t.Fatal("explicit false should disable resource metadata")
	}
}

func TestApplyEnvOverrides(t *testing.T) {
	t.Setenv("GMCP_LISTEN_ADDR", "127.0.0.1:1234")
	t.Setenv("GMCP_METRICS_ADDR", "off")
	t.Setenv("GMCP_LOG_LEVEL", "debug")
	t.Setenv("GMCP_READ_ONLY", "true")
	t.Setenv("GMCP_DEFAULT_INSTANCE", "gl")
	t.Setenv("GMCP_AUTH_ENABLED", "true")
	t.Setenv("GMCP_AUTH_STATIC_TOKEN", "shared-secret")
	t.Setenv("GMCP_AUTH_OIDC_ISSUER", "https://auth.example.com")
	t.Setenv("GMCP_AUTH_OIDC_AUDIENCE", "gitlab-mcp")
	t.Setenv("GMCP_AUTH_OIDC_RESOURCE", "https://gitlab-mcp.example.com/mcp")

	c := minimal()
	c.applyEnv()
	if c.ListenAddr != "127.0.0.1:1234" || c.MetricsAddr != "off" || c.LogLevel != "debug" || !c.ReadOnly {
		t.Fatalf("basic overrides not applied: %+v", c)
	}
	if !c.Auth.Enabled || !c.Auth.Static.Enabled || len(c.Auth.Static.Tokens) != 1 {
		t.Fatalf("auth overrides not applied: %+v", c.Auth)
	}
	if c.Auth.OIDC.Issuer != "https://auth.example.com" ||
		c.Auth.OIDC.Audience != "gitlab-mcp" ||
		c.Auth.OIDC.Resource != "https://gitlab-mcp.example.com/mcp" {
		t.Fatalf("oidc overrides not applied: %+v", c.Auth.OIDC)
	}
}

// GMCP_TOKEN alone must produce a usable, read-only instance so the server can
// run with no config file.
func TestGMCPTokenSynthesisesReadOnlyInstance(t *testing.T) {
	t.Setenv("GMCP_TOKEN", "glpat-env")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Instances) != 1 {
		t.Fatalf("expected 1 synthesised instance, got %d", len(cfg.Instances))
	}
	in := cfg.Instances[0]
	if in.Name != "gitlab" || in.URL != "https://gitlab.com" || in.Token != "glpat-env" {
		t.Fatalf("unexpected instance: %+v", in)
	}
	if !in.Permissions.Read.Core || !in.Permissions.Read.CI {
		t.Fatal("synthesised instance should allow reads")
	}
	if in.Permissions.Pipelines.Operate || in.Permissions.Pipelines.Delete {
		t.Fatal("synthesised instance must not allow writes")
	}
	if cfg.DefaultInstance != "gitlab" {
		t.Fatalf("DefaultInstance = %q", cfg.DefaultInstance)
	}
}

func TestGMCPTokenHonoursGMCPURL(t *testing.T) {
	t.Setenv("GMCP_TOKEN", "glpat-env")
	t.Setenv("GMCP_URL", "https://gitlab.example.com")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Instances[0].URL != "https://gitlab.example.com" {
		t.Fatalf("URL = %q", cfg.Instances[0].URL)
	}
}

// GMCP_TOKEN must not silently override a configured instance.
func TestGMCPTokenDoesNotOverrideConfiguredInstances(t *testing.T) {
	t.Setenv("GMCP_TOKEN", "glpat-env")
	c := minimal()
	c.applyEnv()
	if len(c.Instances) != 1 || c.Instances[0].Token != "glpat-test" {
		t.Fatalf("configured instance was altered: %+v", c.Instances)
	}
}

func TestWarnings(t *testing.T) {
	c := minimal()
	c.Instances[0].Permissions.Pipelines.Delete = true
	c.Instances[0].Permissions.Pipelines.AllowVariables = true
	c.Instances[0].TLS.InsecureSkipTLSVerify = true
	c.Auth.Static.Enabled = true
	c.Auth.Static.Tokens = []AuthToken{{Name: "ci", Token: "inline"}}

	joined := strings.Join(c.Warnings(), "\n")
	for _, want := range []string{
		"inline token is discouraged",
		"allowedNamespaces is empty",
		"insecureSkipTLSVerify=true",
		"allowVariables=true",
		"pipelines.delete=true",
		"requires the Owner role",
		"auth is disabled",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings missing %q:\n%s", want, joined)
		}
	}
}

func TestWarningsQuietWhenConfiguredWell(t *testing.T) {
	c := minimal()
	c.Instances[0].Token = ""
	c.Instances[0].TokenFile = "/etc/gmcp/instances/gl/token"
	c.Instances[0].AllowedNamespaces = []string{"example-group"}
	c.Auth.Enabled = true

	joined := strings.Join(c.Warnings(), "\n")
	for _, unwanted := range []string{"inline token", "allowedNamespaces is empty", "allowVariables", "pipelines.delete"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("unexpected warning %q:\n%s", unwanted, joined)
		}
	}
	if !strings.Contains(joined, "auth is enabled") {
		t.Errorf("the TLS advisory should still be present:\n%s", joined)
	}
}

func TestInstanceStringRedactsToken(t *testing.T) {
	in := Instance{Name: "gl", URL: "https://gitlab.example.com", Token: "glpat-supersecret"}
	s := in.String()
	if strings.Contains(s, "glpat-supersecret") {
		t.Fatalf("String() leaked the token: %s", s)
	}
	if !strings.Contains(s, "token=inline(redacted)") {
		t.Fatalf("String() = %s", s)
	}

	withFile := Instance{Name: "gl", URL: "https://gitlab.example.com", TokenFile: "/etc/t"}
	if !strings.Contains(withFile.String(), "token=file") {
		t.Fatalf("String() = %s", withFile.String())
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected an error for a missing config file")
	}
}

func TestLoadRejectsMalformedYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("instances: [oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected a parse error")
	}
}

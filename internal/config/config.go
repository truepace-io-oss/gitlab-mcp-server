// Package config loads and validates the gitlab-mcp server configuration: the
// listen address, logging, a global read-only kill-switch, and the registry of
// GitLab instances this server can reach.
//
// An "instance" is one GitLab installation addressed over plain outbound HTTPS
// with a personal access token. The server contains no authorization logic of
// its own beyond a capability allowlist and a hard deny list: GitLab's own
// token permissions remain the authoritative gate.
//
// Authentication of the AI agent to the MCP (static bearer tokens / OIDC) is a
// separate concern handled by the auth package.
package config

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// Config is the top-level server configuration.
type Config struct {
	ListenAddr      string     `json:"listenAddr"`
	MetricsAddr     string     `json:"metricsAddr"`
	LogLevel        string     `json:"logLevel"`
	ReadOnly        bool       `json:"readOnly"` // global kill-switch for every mutating tool
	DefaultInstance string     `json:"defaultInstance"`
	Instances       []Instance `json:"instances"`
	Auth            Auth       `json:"auth"`
}

// Instance describes one GitLab installation and how to reach it.
type Instance struct {
	Name      string `json:"name"`
	URL       string `json:"url"`                 // e.g. https://gitlab.com
	Token     string `json:"token,omitempty"`     // inline, discouraged
	TokenFile string `json:"tokenFile,omitempty"` // preferred (re-read per request)
	Timeout   string `json:"timeout,omitempty"`   // Go duration; default 30s
	ReadOnly  bool   `json:"readOnly,omitempty"`

	// AllowedNamespaces bounds every project/group-addressing tool to these
	// top-level paths. Empty means "anything the token can see" (discouraged).
	AllowedNamespaces []string `json:"allowedNamespaces,omitempty"`

	Permissions Permissions `json:"permissions"`
	Pagination  Pagination  `json:"pagination,omitempty"`
	RateLimit   RateLimit   `json:"rateLimit,omitempty"`
	TLS         TLS         `json:"tls,omitempty"`
}

// Permissions is the capability allowlist for one instance. It is checked before
// a request is built, independent of what the token would permit.
type Permissions struct {
	Read      ReadPerms     `json:"read"`
	Pipelines PipelinePerms `json:"pipelines"`
}

// ReadPerms gates the read tool families.
type ReadPerms struct {
	Core bool `json:"core"`
	CI   bool `json:"ci"`
}

// PipelinePerms gates the only write surface this server exposes.
type PipelinePerms struct {
	Operate bool `json:"operate"`
	// Delete gates DELETE /projects/:id/pipelines/:id. GitLab restricts this to
	// the Owner role and it is irreversible, so it is a separate switch.
	Delete bool `json:"delete"`
	// AllowVariables permits passing CI variables when creating a pipeline or
	// playing a job. That injects behaviour into the run, so it is off by default.
	AllowVariables bool `json:"allowVariables"`
}

// Pagination bounds list traversal so one tool call cannot flood the model.
type Pagination struct {
	PerPage  int `json:"perPage"`
	MaxPages int `json:"maxPages"`
}

// RateLimit configures the client-side floor below which non-essential list
// calls are refused, leaving headroom for single reads and pipeline operations.
type RateLimit struct {
	MinRemaining int `json:"minRemaining"`
}

// TLS configures trust for a self-managed GitLab behind a private CA.
type TLS struct {
	InsecureSkipTLSVerify bool   `json:"insecureSkipTLSVerify,omitempty"`
	CAFile                string `json:"caFile,omitempty"`
	CAData                string `json:"caData,omitempty"` // base64 (PEM)
}

// Auth configures how the AI agent authenticates to this MCP server (the
// client-side link). It is independent of the GitLab token: this decides who may
// talk to the MCP, while the token decides what the MCP may do at GitLab.
type Auth struct {
	Enabled bool       `json:"enabled"`
	Static  AuthStatic `json:"static"`
	OIDC    AuthOIDC   `json:"oidc"`
}

// AuthStatic configures one or more shared bearer tokens.
type AuthStatic struct {
	Enabled bool        `json:"enabled"`
	Tokens  []AuthToken `json:"tokens"`
}

// AuthToken is a single shared bearer token. Exactly one of Token/TokenFile.
type AuthToken struct {
	Name      string `json:"name"`
	Token     string `json:"token,omitempty"`     // inline, discouraged
	TokenFile string `json:"tokenFile,omitempty"` // preferred (ESO / rotatable)
}

// AuthOIDC configures the MCP as an OAuth 2.1 resource server validating JWT
// access tokens from an OIDC provider.
type AuthOIDC struct {
	Enabled bool   `json:"enabled"`
	Issuer  string `json:"issuer"`
	// Audience is the expected access-token `aud`. With providers that use the
	// client id as the audience this is an opaque string, not a URI.
	Audience string `json:"audience"`
	// Resource is the RFC 9728 protected-resource identifier: this server's
	// canonical public URL. It is deliberately independent of Audience.
	Resource       string   `json:"resource,omitempty"`
	JWKSURL        string   `json:"jwksUrl,omitempty"`
	RequiredScopes []string `json:"requiredScopes,omitempty"`
	RequiredGroups []string `json:"requiredGroups,omitempty"`
	GroupsClaim    string   `json:"groupsClaim,omitempty"`
	UsernameClaim  string   `json:"usernameClaim,omitempty"`
	// ResourceMetadata controls serving /.well-known/oauth-protected-resource.
	// Defaults to true (enabled) when unset.
	ResourceMetadata *bool `json:"resourceMetadata,omitempty"`
}

// ServeResourceMetadata reports whether the protected-resource-metadata endpoint
// should be served (defaults to true).
func (o AuthOIDC) ServeResourceMetadata() bool {
	return o.ResourceMetadata == nil || *o.ResourceMetadata
}

// ResourceIdentifier returns the RFC 9728 protected-resource identifier. The
// audience fallback keeps existing installations working when their token
// audience is already an absolute resource URI.
func (o AuthOIDC) ResourceIdentifier() string {
	if o.Resource != "" {
		return o.Resource
	}
	return o.Audience
}

var (
	nameRe      = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	namespaceRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*(/[a-zA-Z0-9][a-zA-Z0-9_.-]*)*$`)
)

// Defaults applied by applyDefaults.
const (
	DefaultPerPage      = 100
	DefaultMaxPages     = 10
	DefaultMinRemaining = 50
	DefaultTimeout      = "30s"
)

// Load reads config from path (if non-empty), applies GMCP_* environment
// overrides, fills defaults and validates the result.
func Load(path string) (*Config, error) {
	cfg := &Config{}
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config %q: %w", path, err)
		}
		if err := yaml.Unmarshal(raw, cfg); err != nil {
			return nil, fmt.Errorf("parse config %q: %w", path, err)
		}
	}
	cfg.applyEnv()
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyEnv() {
	if v := os.Getenv("GMCP_LISTEN_ADDR"); v != "" {
		c.ListenAddr = v
	}
	if v := os.Getenv("GMCP_METRICS_ADDR"); v != "" {
		c.MetricsAddr = v
	}
	if v := os.Getenv("GMCP_LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}
	if v := os.Getenv("GMCP_DEFAULT_INSTANCE"); v != "" {
		c.DefaultInstance = v
	}
	if v := os.Getenv("GMCP_READ_ONLY"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.ReadOnly = b
		}
	}
	if v := os.Getenv("GMCP_AUTH_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Auth.Enabled = b
		}
	}
	if v := os.Getenv("GMCP_AUTH_STATIC_TOKEN"); v != "" {
		c.Auth.Static.Enabled = true
		c.Auth.Static.Tokens = append(c.Auth.Static.Tokens, AuthToken{Name: "env", Token: v})
	}
	if v := os.Getenv("GMCP_AUTH_OIDC_ISSUER"); v != "" {
		c.Auth.OIDC.Enabled = true
		c.Auth.OIDC.Issuer = v
	}
	if v := os.Getenv("GMCP_AUTH_OIDC_AUDIENCE"); v != "" {
		c.Auth.OIDC.Audience = v
	}
	if v := os.Getenv("GMCP_AUTH_OIDC_RESOURCE"); v != "" {
		c.Auth.OIDC.Resource = v
	}
	// GMCP_TOKEN (+ optional GMCP_URL) synthesises a single read-only instance so
	// the server is usable with no config file at all.
	if v := os.Getenv("GMCP_TOKEN"); v != "" && len(c.Instances) == 0 {
		u := os.Getenv("GMCP_URL")
		if u == "" {
			u = "https://gitlab.com"
		}
		c.Instances = append(c.Instances, Instance{
			Name:        "gitlab",
			URL:         u,
			Token:       v,
			Permissions: Permissions{Read: ReadPerms{Core: true, CI: true}},
		})
	}
}

func (c *Config) applyDefaults() {
	if c.ListenAddr == "" {
		c.ListenAddr = "0.0.0.0:9090"
	}
	if c.MetricsAddr == "" {
		c.MetricsAddr = ":9091" // separate port; set "off" to disable
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	for i := range c.Instances {
		in := &c.Instances[i]
		if in.Timeout == "" {
			in.Timeout = DefaultTimeout
		}
		if in.Pagination.PerPage == 0 {
			in.Pagination.PerPage = DefaultPerPage
		}
		if in.Pagination.MaxPages == 0 {
			in.Pagination.MaxPages = DefaultMaxPages
		}
		if in.RateLimit.MinRemaining == 0 {
			in.RateLimit.MinRemaining = DefaultMinRemaining
		}
	}
	// If exactly one instance is defined and no default is set, use it.
	if c.DefaultInstance == "" && len(c.Instances) == 1 {
		c.DefaultInstance = c.Instances[0].Name
	}
	if c.Auth.OIDC.GroupsClaim == "" {
		c.Auth.OIDC.GroupsClaim = "groups"
	}
	if c.Auth.OIDC.UsernameClaim == "" {
		c.Auth.OIDC.UsernameClaim = "preferred_username"
	}
}

// Validate enforces the invariants documented on Config/Instance. It returns the
// first violation found.
func (c *Config) Validate() error {
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid logLevel %q (want debug|info|warn|error)", c.LogLevel)
	}
	if len(c.Instances) == 0 {
		return fmt.Errorf("no GitLab instances configured")
	}

	seen := map[string]bool{}
	for _, in := range c.Instances {
		if in.Name == "" {
			return fmt.Errorf("instance with empty name")
		}
		if !nameRe.MatchString(in.Name) {
			return fmt.Errorf("instance %q: name must be a DNS label (lowercase alphanumeric and '-')", in.Name)
		}
		if seen[in.Name] {
			return fmt.Errorf("duplicate instance name %q", in.Name)
		}
		seen[in.Name] = true

		if in.URL == "" {
			return fmt.Errorf("instance %q: url is required", in.Name)
		}
		u, err := url.Parse(in.URL)
		if err != nil {
			return fmt.Errorf("instance %q: invalid url: %w", in.Name, err)
		}
		if u.Host == "" {
			return fmt.Errorf("instance %q: url must include a host", in.Name)
		}
		switch u.Scheme {
		case "https":
		case "http":
			// Plaintext is only sensible against a loopback test server.
			if h := u.Hostname(); h != "127.0.0.1" && h != "localhost" && h != "::1" {
				return fmt.Errorf("instance %q: url scheme must be https (http is only allowed for localhost)", in.Name)
			}
		default:
			return fmt.Errorf("instance %q: url scheme must be http or https", in.Name)
		}

		if (in.Token == "") == (in.TokenFile == "") {
			return fmt.Errorf("instance %q: set exactly one of token or tokenFile", in.Name)
		}
		if _, err := time.ParseDuration(in.Timeout); err != nil {
			return fmt.Errorf("instance %q: invalid timeout %q: %w", in.Name, in.Timeout, err)
		}
		for _, ns := range in.AllowedNamespaces {
			if !namespaceRe.MatchString(ns) {
				return fmt.Errorf("instance %q: allowedNamespaces entry %q is not a valid namespace path", in.Name, ns)
			}
		}
		if in.Pagination.PerPage < 1 || in.Pagination.PerPage > 100 {
			return fmt.Errorf("instance %q: pagination.perPage must be between 1 and 100", in.Name)
		}
		if in.Pagination.MaxPages < 1 {
			return fmt.Errorf("instance %q: pagination.maxPages must be at least 1", in.Name)
		}
		if in.TLS.InsecureSkipTLSVerify && (in.TLS.CAFile != "" || in.TLS.CAData != "") {
			return fmt.Errorf("instance %q: insecureSkipTLSVerify must not be combined with a CA", in.Name)
		}
	}

	if c.DefaultInstance == "" {
		return fmt.Errorf("defaultInstance must be set when more than one instance is configured")
	}
	if !seen[c.DefaultInstance] {
		return fmt.Errorf("defaultInstance %q is not one of the configured instances", c.DefaultInstance)
	}

	return c.Auth.validate()
}

func (a Auth) validate() error {
	if !a.Enabled {
		return nil
	}
	if !a.Static.Enabled && !a.OIDC.Enabled {
		return fmt.Errorf("auth.enabled is true but neither auth.static nor auth.oidc is enabled")
	}
	if a.Static.Enabled {
		if len(a.Static.Tokens) == 0 {
			return fmt.Errorf("auth.static.enabled is true but no tokens configured")
		}
		names := map[string]bool{}
		for i, t := range a.Static.Tokens {
			if t.Name == "" {
				return fmt.Errorf("auth.static.tokens[%d]: name is required", i)
			}
			if names[t.Name] {
				return fmt.Errorf("auth.static.tokens: duplicate name %q", t.Name)
			}
			names[t.Name] = true
			if (t.Token == "") == (t.TokenFile == "") {
				return fmt.Errorf("auth.static.tokens[%q]: set exactly one of token or tokenFile", t.Name)
			}
		}
	}
	if a.OIDC.Enabled {
		if a.OIDC.Issuer == "" {
			return fmt.Errorf("auth.oidc.enabled is true but issuer is empty")
		}
		if !strings.HasPrefix(a.OIDC.Issuer, "https://") {
			return fmt.Errorf("auth.oidc.issuer must be an https URL")
		}
		if a.OIDC.Audience == "" {
			return fmt.Errorf("auth.oidc.enabled is true but audience is empty")
		}
		if a.OIDC.ServeResourceMetadata() {
			resource := a.OIDC.ResourceIdentifier()
			u, err := url.Parse(resource)
			if err != nil || !u.IsAbs() || u.Scheme != "https" || u.Host == "" || strings.Contains(resource, "#") {
				return fmt.Errorf("auth.oidc.resource must be an absolute https URI without a fragment when resource metadata is enabled (effective value %q)", resource)
			}
		}
	}
	return nil
}

// Warnings returns non-fatal advisories (loud but not blocking). Callers log
// these at startup.
func (c *Config) Warnings() []string {
	var w []string
	for _, in := range c.Instances {
		if in.Token != "" {
			w = append(w, fmt.Sprintf("instance %q: inline token is discouraged; prefer tokenFile (re-read per request, so rotation needs no restart)", in.Name))
		}
		if len(in.AllowedNamespaces) == 0 {
			w = append(w, fmt.Sprintf("instance %q: allowedNamespaces is empty — this instance can reach every project the token can see; set allowedNamespaces to bound it", in.Name))
		}
		if in.TLS.InsecureSkipTLSVerify {
			w = append(w, fmt.Sprintf("instance %q: insecureSkipTLSVerify=true — TLS verification disabled, do not use in production", in.Name))
		}
		if in.Permissions.Pipelines.AllowVariables {
			w = append(w, fmt.Sprintf("instance %q: pipelines.allowVariables=true — agents may inject CI variables into runs, which changes pipeline behaviour", in.Name))
		}
		if in.Permissions.Pipelines.Delete {
			w = append(w, fmt.Sprintf("instance %q: pipelines.delete=true — requires the Owner role for the token's user and permanently destroys pipeline history (there is no restore)", in.Name))
		}
	}
	if c.Auth.Static.Enabled {
		for _, t := range c.Auth.Static.Tokens {
			if t.Token != "" {
				w = append(w, fmt.Sprintf("auth.static.tokens[%q]: inline token is discouraged; prefer tokenFile (rotatable)", t.Name))
			}
		}
	}
	if c.Auth.Enabled {
		w = append(w, "auth is enabled — ensure the transport is TLS-protected (internal ingress or server TLS); bearer tokens over plaintext are insecure")
	} else {
		w = append(w, "auth is disabled — the MCP transport is unauthenticated and must be protected by the deployment (internal or OIDC-gated ingress)")
	}
	return w
}

// InstanceNames returns the configured instance names in order.
func (c *Config) InstanceNames() []string {
	names := make([]string, 0, len(c.Instances))
	for _, in := range c.Instances {
		names = append(names, in.Name)
	}
	return names
}

// String redacts secrets for safe logging.
func (i Instance) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "name=%s url=%s", i.Name, i.URL)
	switch {
	case i.TokenFile != "":
		b.WriteString(" token=file")
	case i.Token != "":
		b.WriteString(" token=inline(redacted)")
	default:
		b.WriteString(" token=none")
	}
	if len(i.AllowedNamespaces) > 0 {
		fmt.Fprintf(&b, " namespaces=%s", strings.Join(i.AllowedNamespaces, ","))
	}
	fmt.Fprintf(&b, " readOnly=%t", i.ReadOnly)
	return b.String()
}

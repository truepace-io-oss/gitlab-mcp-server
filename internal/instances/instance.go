// Package instances holds the ready-to-use GitLab clients for every instance
// this MCP manages, and a registry to look them up by name.
//
// Building an instance never contacts the server, so an unreachable or
// misconfigured remote does not break startup; reachability is reported by Ping
// and by the background prober that feeds gmcp_instance_up.
package instances

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/truepace-io-oss/gitlab-mcp-server/internal/config"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/gitlab"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/metrics"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/perm"
)

// Instance is one GitLab installation with a ready client.
type Instance struct {
	Name     string
	URL      string
	ReadOnly bool
	Guard    *perm.Guard
	Client   *gitlab.Client
}

// newInstance constructs the client for one instance from its config.
func newInstance(globalReadOnly bool, c config.Instance) (*Instance, error) {
	httpClient, err := buildHTTPClient(c)
	if err != nil {
		return nil, err
	}
	guard := perm.NewGuard(c.Name, globalReadOnly, c)
	client := gitlab.New(c.Name, c.URL, httpClient, guard,
		gitlab.PageOpts{PerPage: c.Pagination.PerPage, MaxPages: c.Pagination.MaxPages},
		c.RateLimit.MinRemaining)
	return &Instance{
		Name:     c.Name,
		URL:      c.URL,
		ReadOnly: c.ReadOnly,
		Guard:    guard,
		Client:   client,
	}, nil
}

// NewForTest builds an Instance against an arbitrary base URL (an httptest
// server), so unit tests need no config file and no real GitLab.
func NewForTest(name, baseURL string, guard *perm.Guard) *Instance {
	client := gitlab.New(name, baseURL, &http.Client{Timeout: 10 * time.Second}, guard,
		gitlab.PageOpts{PerPage: config.DefaultPerPage, MaxPages: config.DefaultMaxPages},
		0)
	return &Instance{Name: name, URL: baseURL, Guard: guard, Client: client}
}

// Ping reports whether the GitLab API is reachable and authenticated.
func (i *Instance) Ping(ctx context.Context) (string, error) {
	v, err := i.Client.Version(ctx)
	if err != nil {
		return "", err
	}
	if v.Version == "" {
		return "reachable", nil
	}
	return "reachable (GitLab " + v.Version + ")", nil
}

// TokenStatus describes the instance's own access token.
type TokenStatus struct {
	Name      string
	Scopes    []string
	ExpiresAt string
	Active    bool
	// ExpiresIn is the time until expiry; zero when the token never expires.
	ExpiresIn time.Duration
	HasExpiry bool
}

// Warning returns a human-readable advisory when the token is close to expiry or
// already unusable, and "" otherwise.
func (t TokenStatus) Warning() string {
	switch {
	case !t.Active:
		return "WARNING: this token is inactive or revoked"
	case t.HasExpiry && t.ExpiresIn <= 0:
		return "WARNING: this token has expired"
	case t.HasExpiry && t.ExpiresIn < 14*24*time.Hour:
		return fmt.Sprintf("WARNING: this token expires in %d days — rotate it", int(t.ExpiresIn.Hours()/24))
	default:
		return ""
	}
}

// TokenInfo reads the instance's own token metadata and updates the expiry
// gauge. It is best effort: a token that cannot introspect itself is still
// usable for everything else.
func (i *Instance) TokenInfo(ctx context.Context) (*TokenStatus, error) {
	info, err := i.Client.SelfToken(ctx)
	if err != nil {
		return nil, err
	}
	st := &TokenStatus{
		Name:      info.Name,
		Scopes:    info.Scopes,
		ExpiresAt: info.ExpiresAt,
		Active:    info.Active && !info.Revoked,
	}
	if info.ExpiresAt != "" {
		// GitLab returns a date (YYYY-MM-DD) for PAT expiry.
		if exp, perr := time.Parse("2006-01-02", info.ExpiresAt); perr == nil {
			st.HasExpiry = true
			st.ExpiresIn = time.Until(exp)
			metrics.SetTokenExpiresIn(i.Name, st.ExpiresIn.Seconds())
		}
	}
	if !st.HasExpiry {
		metrics.ClearTokenExpiresIn(i.Name)
	}
	return st, nil
}

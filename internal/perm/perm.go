// Package perm implements the MCP's own capability allowlist and namespace
// guard. It is layer 2 of the permission model: the GitLab token is the
// authoritative gate, but every tool declares a capability that must be enabled
// for the target instance before a request is built. This is defense in depth,
// so an over-privileged token cannot be used for an operation the operator
// intends to be unavailable.
package perm

import (
	"fmt"
	"strings"

	"github.com/truepace-io-oss/gitlab-mcp-server/internal/config"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/metrics"
)

// Capability is the closed set of things this server can be allowed to do.
type Capability string

const (
	// ReadCore covers projects, groups, members, repository browsing, search,
	// issues and merge requests.
	ReadCore Capability = "read.core"
	// ReadCI covers pipelines, jobs, job logs, test reports, schedules and
	// ci_lint.
	ReadCI Capability = "read.ci"
	// PipelinesOperate covers pipeline create/update/retry/cancel, job
	// retry/cancel/play and schedule play.
	PipelinesOperate Capability = "pipelines.operate"
	// PipelinesDelete covers DELETE /projects/:id/pipelines/:id only.
	PipelinesDelete Capability = "pipelines.delete"
)

// All returns every capability in a stable order, for listing and tests.
func All() []Capability {
	return []Capability{ReadCore, ReadCI, PipelinesOperate, PipelinesDelete}
}

// IsWrite reports whether exercising this capability mutates state at GitLab.
func (c Capability) IsWrite() bool {
	return c == PipelinesOperate || c == PipelinesDelete
}

// Reasons recorded on gmcp_writes_blocked_total.
const (
	ReasonGlobalReadOnly   = "global_readonly"
	ReasonInstanceReadOnly = "instance_readonly"
	ReasonCapabilityDenied = "capability_denied"
	ReasonNamespaceDenied  = "namespace_denied"
	ReasonDenyListed       = "deny_listed"
	ReasonRateLimited      = "rate_limited"
	ReasonConfirmMissing   = "confirmation_missing"
)

// Guard answers "may this instance do X?" for one GitLab instance.
type Guard struct {
	instance          string
	globalReadOnly    bool
	instanceReadOnly  bool
	enabled           map[Capability]bool
	allowVariables    bool
	allowedNamespaces []string
}

// NewGuard builds a Guard for one instance.
func NewGuard(instance string, globalReadOnly bool, in config.Instance) *Guard {
	p := in.Permissions
	return &Guard{
		instance:         instance,
		globalReadOnly:   globalReadOnly,
		instanceReadOnly: in.ReadOnly,
		enabled: map[Capability]bool{
			ReadCore:         p.Read.Core,
			ReadCI:           p.Read.CI,
			PipelinesOperate: p.Pipelines.Operate,
			PipelinesDelete:  p.Pipelines.Delete,
		},
		allowVariables:    p.Pipelines.AllowVariables,
		allowedNamespaces: in.AllowedNamespaces,
	}
}

// NewGuardForTest builds a Guard with the given capabilities enabled.
func NewGuardForTest(instance string, allowedNamespaces []string, caps ...Capability) *Guard {
	g := &Guard{
		instance:          instance,
		enabled:           map[Capability]bool{},
		allowedNamespaces: allowedNamespaces,
	}
	for _, c := range caps {
		g.enabled[c] = true
	}
	return g
}

// SetAllowVariables is used by tests to flip the CI-variable switch.
func (g *Guard) SetAllowVariables(v bool) { g.allowVariables = v }

// AllowVariables reports whether CI variables may be supplied to a run.
func (g *Guard) AllowVariables() bool { return g.allowVariables }

// Allow reports whether the capability may be exercised. For write capabilities
// it applies the two read-only kill-switches first. Every denial increments
// gmcp_writes_blocked_total with a bounded reason label.
func (g *Guard) Allow(c Capability) error {
	if c.IsWrite() {
		if g.globalReadOnly {
			metrics.RecordWriteBlocked(g.instance, ReasonGlobalReadOnly)
			return fmt.Errorf("this MCP instance is configured read-only (writes disabled globally)")
		}
		if g.instanceReadOnly {
			metrics.RecordWriteBlocked(g.instance, ReasonInstanceReadOnly)
			return fmt.Errorf("writes are disabled for GitLab instance %q (readOnly)", g.instance)
		}
	}
	if !g.enabled[c] {
		metrics.RecordWriteBlocked(g.instance, ReasonCapabilityDenied)
		return fmt.Errorf("capability %q is not enabled for GitLab instance %q", c, g.instance)
	}
	return nil
}

// Enabled reports whether a capability is on, without recording a denial.
func (g *Guard) Enabled(c Capability) bool { return g.enabled[c] }

// EffectiveList returns the enabled capabilities in a stable order.
func (g *Guard) EffectiveList() []Capability {
	var out []Capability
	for _, c := range All() {
		if g.enabled[c] {
			out = append(out, c)
		}
	}
	return out
}

// AllowedNamespaces returns the configured namespace allowlist.
func (g *Guard) AllowedNamespaces() []string { return g.allowedNamespaces }

// AllowNamespace reports whether a full project or group path lies inside the
// allowlist. Matching is on path-segment boundaries, so an allowlist entry
// "team" permits "team/app" but not "teamwork/app".
func (g *Guard) AllowNamespace(fullPath string) error {
	if len(g.allowedNamespaces) == 0 {
		return nil
	}
	p := strings.Trim(fullPath, "/")
	for _, ns := range g.allowedNamespaces {
		ns = strings.Trim(ns, "/")
		if p == ns || strings.HasPrefix(p, ns+"/") {
			return nil
		}
	}
	metrics.RecordWriteBlocked(g.instance, ReasonNamespaceDenied)
	return fmt.Errorf("%q is outside the allowed namespaces [%s] for GitLab instance %q",
		fullPath, strings.Join(g.allowedNamespaces, ", "), g.instance)
}

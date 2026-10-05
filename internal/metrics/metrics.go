// Package metrics defines the Prometheus metrics for the MCP server. Metrics are
// served on a separate, unauthenticated port (see main); labels are deliberately
// low-cardinality — never a project path, namespace, user or API path.
package metrics

import (
	"runtime"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// The label is mcp_instance (not instance) to avoid colliding with
	// Prometheus' own target label, which would otherwise overwrite it.
	toolCalls = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gmcp_tool_calls_total",
		Help: "MCP tool calls by tool, target GitLab instance and result (ok|error|forbidden|blocked|not_found|rate_limited|conflict).",
	}, []string{"tool", "mcp_instance", "result"})

	toolDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gmcp_tool_call_duration_seconds",
		Help:    "MCP tool call latency by tool and GitLab instance.",
		Buckets: prometheus.DefBuckets,
	}, []string{"tool", "mcp_instance"})

	authRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gmcp_auth_requests_total",
		Help: "Agent authentication attempts by method (static|oidc|none) and result (allow|deny).",
	}, []string{"method", "result"})

	instanceUp = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gmcp_instance_up",
		Help: "GitLab instance reachability (1 = /version reachable, 0 = not), by instance.",
	}, []string{"mcp_instance"})

	writesBlocked = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gmcp_writes_blocked_total",
		Help: "Tool calls blocked by a guard, by instance and reason (global_readonly|instance_readonly|capability_denied|namespace_denied|deny_listed|rate_limited|confirmation_missing).",
	}, []string{"mcp_instance", "reason"})

	operations = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gmcp_operations_total",
		Help: "Mutating GitLab operations requested through the MCP by instance, operation and result.",
	}, []string{"mcp_instance", "op", "result"})

	gitlabRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gmcp_gitlab_requests_total",
		Help: "Outbound GitLab API requests by instance, HTTP method and status code.",
	}, []string{"mcp_instance", "method", "code"})

	gitlabDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gmcp_gitlab_request_duration_seconds",
		Help:    "Outbound GitLab API request latency by instance and HTTP method.",
		Buckets: prometheus.DefBuckets,
	}, []string{"mcp_instance", "method"})

	rateLimitRemaining = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gmcp_gitlab_rate_limit_remaining",
		Help: "Remaining GitLab API request budget as reported by the RateLimit-Remaining header.",
	}, []string{"mcp_instance"})

	tokenExpiresIn = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gmcp_token_expires_in_seconds",
		Help: "Seconds until the instance's personal access token expires (negative when expired, absent when it never expires or could not be read).",
	}, []string{"mcp_instance"})

	buildInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gmcp_build_info",
		Help: "Build information; constant 1.",
	}, []string{"version", "goversion"})
)

// RecordTool records a tool call outcome and latency.
func RecordTool(tool, instance, result string, d time.Duration) {
	if instance == "" {
		instance = "-"
	}
	toolCalls.WithLabelValues(tool, instance, result).Inc()
	toolDuration.WithLabelValues(tool, instance).Observe(d.Seconds())
}

// RecordAuth records an agent-authentication outcome.
func RecordAuth(method, result string) { authRequests.WithLabelValues(method, result).Inc() }

// RecordWriteBlocked records a call refused by one of the guards.
func RecordWriteBlocked(instance, reason string) {
	if instance == "" {
		instance = "-"
	}
	writesBlocked.WithLabelValues(instance, reason).Inc()
}

// RecordOperation records a mutating GitLab operation requested through the MCP.
func RecordOperation(instance, op, result string) {
	if instance == "" {
		instance = "-"
	}
	operations.WithLabelValues(instance, op, result).Inc()
}

// RecordGitLabRequest records one outbound API call.
func RecordGitLabRequest(instance, method, code string, d time.Duration) {
	gitlabRequests.WithLabelValues(instance, method, code).Inc()
	gitlabDuration.WithLabelValues(instance, method).Observe(d.Seconds())
}

// SetRateLimitRemaining publishes the remaining API budget for an instance.
func SetRateLimitRemaining(instance string, remaining float64) {
	rateLimitRemaining.WithLabelValues(instance).Set(remaining)
}

// SetInstanceUp sets the reachability gauge for an instance.
func SetInstanceUp(instance string, up bool) {
	v := 0.0
	if up {
		v = 1
	}
	instanceUp.WithLabelValues(instance).Set(v)
}

// SetTokenExpiresIn publishes the token lifetime for an instance.
func SetTokenExpiresIn(instance string, seconds float64) {
	tokenExpiresIn.WithLabelValues(instance).Set(seconds)
}

// ClearTokenExpiresIn removes the gauge for a token with no expiry, so "never
// expires" is not reported as "expires now".
func ClearTokenExpiresIn(instance string) {
	tokenExpiresIn.DeleteLabelValues(instance)
}

// SetBuildInfo publishes the build-info gauge.
func SetBuildInfo(version string) {
	buildInfo.WithLabelValues(version, runtime.Version()).Set(1)
}

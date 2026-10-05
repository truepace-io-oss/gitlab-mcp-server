package mcpserver

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/gitlab"
)

// maxMessageLen caps free-form text (titles, descriptions, error messages) so one
// verbose object cannot flood the model's context.
const maxMessageLen = 300

// textResult wraps a plain string into an MCP tool result.
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// errorResult wraps an error as an MCP *tool* error (IsError), so the model sees
// the message — including GitLab's own 403/404 text — rather than a transport
// failure it cannot reason about.
func errorResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
	}
}

// truncate shortens long free-form text deterministically.
func truncate(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + " …(truncated)"
}

// dash returns "-" for empty strings so columns stay aligned.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// truncationNotice is appended whenever a listing was cut short. A silently
// partial list reads to the model as a complete one, which is worse than no list.
func truncationNotice(truncated bool, hint string) string {
	if !truncated {
		return ""
	}
	return fmt.Sprintf("\n… results truncated. %s\n", hint)
}

// histogram renders a map of counts as a compact "key=n" summary.
func histogram(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}

// shortSHA abbreviates a commit SHA.
func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return dash(s)
}

// age renders an RFC3339 timestamp as a human-readable age.
func age(ts string) string {
	if ts == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// projectsTable renders a project listing with a leading count.
func projectsTable(instance string, ps []gitlab.Project, truncated bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d project(s) on %s:\n", len(ps), instance)
	for _, p := range ps {
		fmt.Fprintf(&b, "- %s (id=%d) default=%s visibility=%s", p.PathWithNamespace, p.ID, dash(p.DefaultBranch), dash(p.Visibility))
		if p.Archived {
			b.WriteString(" ARCHIVED")
		}
		fmt.Fprintf(&b, " activity=%s", age(p.LastActivityAt))
		if p.Description != "" {
			fmt.Fprintf(&b, "\n    %s", truncate(p.Description, maxMessageLen))
		}
		b.WriteString("\n")
	}
	b.WriteString(truncationNotice(truncated, "Narrow with `search`, `group` or a smaller `limit`."))
	return b.String()
}

// pipelinesTable renders a pipeline listing with a status histogram — the shape
// an operator scans first.
func pipelinesTable(project string, ps []gitlab.Pipeline, truncated bool) string {
	status := map[string]int{}
	for _, p := range ps {
		status[dash(p.Status)]++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d pipeline(s) in %s (%s):\n", len(ps), project, histogram(status))
	for _, p := range ps {
		fmt.Fprintf(&b, "- #%d %s ref=%s sha=%s source=%s", p.ID, dash(p.Status), dash(p.Ref), shortSHA(p.SHA), dash(p.Source))
		if p.Duration > 0 {
			fmt.Fprintf(&b, " duration=%ds", p.Duration)
		}
		fmt.Fprintf(&b, " created=%s", age(p.CreatedAt))
		if p.User.Username != "" {
			fmt.Fprintf(&b, " by=%s", p.User.Username)
		}
		if p.WebURL != "" {
			fmt.Fprintf(&b, "\n    %s", p.WebURL)
		}
		b.WriteString("\n")
	}
	b.WriteString(truncationNotice(truncated, "Narrow with `ref`, `status` or `updatedAfter`."))
	return b.String()
}

// jobsTable renders a job listing grouped by stage order of appearance.
func jobsTable(header string, js []gitlab.Job, truncated bool) string {
	status := map[string]int{}
	for _, j := range js {
		status[dash(j.Status)]++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %d job(s) (%s):\n", header, len(js), histogram(status))
	for _, j := range js {
		fmt.Fprintf(&b, "- #%d %s [%s] %s", j.ID, dash(j.Name), dash(j.Stage), dash(j.Status))
		if j.AllowFailure {
			b.WriteString(" allow_failure")
		}
		if j.FailureReason != "" {
			fmt.Fprintf(&b, " reason=%s", j.FailureReason)
		}
		if j.Duration > 0 {
			fmt.Fprintf(&b, " duration=%.0fs", j.Duration)
		}
		b.WriteString("\n")
	}
	b.WriteString(truncationNotice(truncated, "Use `scope` to filter by job status."))
	return b.String()
}

// pipelineSummary renders one pipeline as a key/value block.
func pipelineSummary(project string, p *gitlab.Pipeline) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Pipeline #%d in %s\n", p.ID, project)
	fmt.Fprintf(&b, "  status:    %s\n", dash(p.Status))
	if p.Name != "" {
		fmt.Fprintf(&b, "  name:      %s\n", p.Name)
	}
	fmt.Fprintf(&b, "  ref:       %s\n", dash(p.Ref))
	fmt.Fprintf(&b, "  sha:       %s\n", dash(p.SHA))
	fmt.Fprintf(&b, "  source:    %s\n", dash(p.Source))
	fmt.Fprintf(&b, "  created:   %s (%s)\n", dash(p.CreatedAt), age(p.CreatedAt))
	if p.StartedAt != "" {
		fmt.Fprintf(&b, "  started:   %s\n", p.StartedAt)
	}
	if p.FinishedAt != "" {
		fmt.Fprintf(&b, "  finished:  %s\n", p.FinishedAt)
	}
	if p.Duration > 0 {
		fmt.Fprintf(&b, "  duration:  %ds\n", p.Duration)
	}
	if p.User.Username != "" {
		fmt.Fprintf(&b, "  triggered: %s\n", p.User.Username)
	}
	if p.WebURL != "" {
		fmt.Fprintf(&b, "  url:       %s\n", p.WebURL)
	}
	return b.String()
}

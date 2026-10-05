package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/gitlab"
)

const (
	// maxTraceBytes bounds how much of a job log is fetched before tailing.
	maxTraceBytes = 2 << 20
	// ciLintMaxBytes bounds the CI configuration an agent may submit. This is the
	// only tool that accepts file content at all.
	ciLintMaxBytes = 1 << 20
)

func (s *Server) registerReadCITools(m *mcp.Server) {
	addTool(m, s, "pipelines_list",
		"List a project's pipelines with status, ref, SHA, source and duration. Filter by status, ref, SHA, source or updatedAfter.",
		s.pipelinesList)
	addTool(m, s, "pipeline_get",
		"Get one pipeline by id, or the latest pipeline for a ref. Pipeline variables are never returned — they are permanently blocked.",
		s.pipelineGet)
	addTool(m, s, "pipeline_jobs",
		"List a pipeline's jobs with stage, status and failure reason. This is the usual way to find out which job failed.",
		s.pipelineJobs)
	addTool(m, s, "job_get", "Get one job: stage, status, timing, runner and failure reason.", s.jobGet)
	addTool(m, s, "job_log",
		"Read the tail of a job's log. Credential-shaped strings are redacted, but masking is best effort — fix the pipeline if a secret appears. This is also how to reach a CI security scanner's output, since this server does no scanning of its own.",
		s.jobLog)
	addTool(m, s, "pipeline_test_report",
		"Get a pipeline's test summary: totals plus per-suite success, failure, skip and error counts.",
		s.pipelineTestReport)
	addTool(m, s, "pipeline_schedules_list",
		"List a project's pipeline schedules with their cron, next run and owner. Schedule variables are redacted.",
		s.pipelineSchedulesList)
	addTool(m, s, "pipeline_wait",
		"Poll a pipeline until it reaches a terminal status (success, failed, canceled, skipped or manual) or the timeout expires. Read-only.",
		s.pipelineWait)
	addTool(m, s, "ci_lint",
		"Validate GitLab CI/CD configuration in project context — GitLab resolves `include:`, project variables and `extends:` server-side, which cannot be reproduced locally. Pass the content of an uncommitted .gitlab-ci.yml to check it before pushing. With dryRun it simulates pipeline creation, catching rules:/needs: errors a static check misses. The content is sent to GitLab (that is the point of the call); no variable value is ever returned.",
		s.ciLint)
}

type pipelinesListParam struct {
	projectParam
	limitParam
	Status       string `json:"status,omitempty" jsonschema:"filter by status: created, waiting_for_resource, preparing, pending, running, success, failed, canceled, skipped, manual or scheduled"`
	Ref          string `json:"ref,omitempty" jsonschema:"filter by branch or tag"`
	SHA          string `json:"sha,omitempty" jsonschema:"filter by commit SHA"`
	Source       string `json:"source,omitempty" jsonschema:"filter by trigger source: push, web, schedule, api, merge_request_event, pipeline, trigger, …"`
	UpdatedAfter string `json:"updatedAfter,omitempty" jsonschema:"only pipelines updated after this ISO 8601 timestamp"`
	Username     string `json:"username,omitempty" jsonschema:"only pipelines triggered by this username"`
}

func (s *Server) pipelinesList(ctx context.Context, _ *mcp.CallToolRequest, in pipelinesListParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	ps, truncated, err := inst.Client.Pipelines(ctx, p.PathWithNamespace, gitlab.PipelinesOpts{
		Status: in.Status, Ref: in.Ref, SHA: in.SHA, Source: in.Source,
		UpdatedAfter: in.UpdatedAfter, Username: in.Username, Limit: in.resolvedLimit(),
	})
	if err != nil {
		return errorResult(err), nil, nil
	}
	return textResult(pipelinesTable(p.PathWithNamespace, ps, truncated)), nil, nil
}

type pipelineGetParam struct {
	projectParam
	ID  int    `json:"id,omitempty" jsonschema:"the pipeline id; omit it and pass ref instead to get the latest pipeline for that ref"`
	Ref string `json:"ref,omitempty" jsonschema:"when id is omitted, return the latest pipeline for this branch or tag"`
}

func (s *Server) pipelineGet(ctx context.Context, _ *mcp.CallToolRequest, in pipelineGetParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	var (
		pl  *gitlab.Pipeline
		err error
	)
	if in.ID > 0 {
		pl, err = inst.Client.Pipeline(ctx, p.PathWithNamespace, in.ID)
	} else {
		pl, err = inst.Client.LatestPipeline(ctx, p.PathWithNamespace, in.Ref)
	}
	if err != nil {
		return errorResult(err), nil, nil
	}
	return textResult(pipelineSummary(p.PathWithNamespace, pl)), nil, nil
}

type pipelineJobsParam struct {
	projectParam
	limitParam
	ID             int    `json:"id" jsonschema:"the pipeline id"`
	Scope          string `json:"scope,omitempty" jsonschema:"only jobs in this state: created, pending, running, failed, success, canceled, skipped or manual"`
	IncludeRetried bool   `json:"includeRetried,omitempty" jsonschema:"also include superseded retried jobs"`
}

func (s *Server) pipelineJobs(ctx context.Context, _ *mcp.CallToolRequest, in pipelineJobsParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	js, truncated, err := inst.Client.PipelineJobs(ctx, p.PathWithNamespace, in.ID, in.Scope, in.IncludeRetried, in.resolvedLimit())
	if err != nil {
		return errorResult(err), nil, nil
	}
	header := fmt.Sprintf("Pipeline #%d in %s", in.ID, p.PathWithNamespace)
	return textResult(jobsTable(header, js, truncated)), nil, nil
}

type jobRefParam struct {
	projectParam
	ID int `json:"id" jsonschema:"the job id"`
}

func (s *Server) jobGet(ctx context.Context, _ *mcp.CallToolRequest, in jobRefParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	j, err := inst.Client.Job(ctx, p.PathWithNamespace, in.ID)
	if err != nil {
		return errorResult(err), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Job #%d %q in %s\n", j.ID, j.Name, p.PathWithNamespace)
	fmt.Fprintf(&b, "  stage:    %s\n", dash(j.Stage))
	fmt.Fprintf(&b, "  status:   %s", dash(j.Status))
	if j.AllowFailure {
		b.WriteString(" (allow_failure)")
	}
	b.WriteString("\n")
	if j.FailureReason != "" {
		fmt.Fprintf(&b, "  failure:  %s\n", j.FailureReason)
	}
	fmt.Fprintf(&b, "  pipeline: #%d %s ref=%s\n", j.Pipeline.ID, dash(j.Pipeline.Status), dash(j.Pipeline.Ref))
	if j.Duration > 0 {
		fmt.Fprintf(&b, "  duration: %.0fs (queued %.0fs)\n", j.Duration, j.QueuedDuration)
	}
	if j.Runner != nil {
		fmt.Fprintf(&b, "  runner:   %s\n", dash(j.Runner.Description))
	}
	if len(j.Artifacts) > 0 {
		names := make([]string, 0, len(j.Artifacts))
		for _, a := range j.Artifacts {
			if a.Filename != "" {
				names = append(names, a.Filename)
			}
		}
		if len(names) > 0 {
			fmt.Fprintf(&b, "  artifacts: %s (not downloadable through this server)\n", strings.Join(names, ", "))
		}
	}
	if j.WebURL != "" {
		fmt.Fprintf(&b, "  url:      %s\n", j.WebURL)
	}
	return textResult(b.String()), nil, nil
}

type jobLogParam struct {
	projectParam
	ID        int `json:"id" jsonschema:"the job id"`
	TailLines int `json:"tailLines,omitempty" jsonschema:"how many trailing lines to return (default 200, maximum 2000)"`
}

func (s *Server) jobLog(ctx context.Context, _ *mcp.CallToolRequest, in jobLogParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	tail := in.TailLines
	switch {
	case tail <= 0:
		tail = 200
	case tail > 2000:
		tail = 2000
	}
	trace, err := inst.Client.JobTrace(ctx, p.PathWithNamespace, in.ID, maxTraceBytes)
	if err != nil {
		return errorResult(err), nil, nil
	}
	lines := strings.Split(strings.TrimRight(trace, "\n"), "\n")
	total := len(lines)
	if total > tail {
		lines = lines[total-tail:]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Job #%d log in %s — showing last %d of %d line(s)\n\n", in.ID, p.PathWithNamespace, len(lines), total)
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("\n")
	return textResult(b.String()), nil, nil
}

type pipelineIDParam struct {
	projectParam
	ID int `json:"id" jsonschema:"the pipeline id"`
}

func (s *Server) pipelineTestReport(ctx context.Context, _ *mcp.CallToolRequest, in pipelineIDParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	t, err := inst.Client.TestReportSummary(ctx, p.PathWithNamespace, in.ID)
	if err != nil {
		return errorResult(err), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Test report for pipeline #%d in %s\n", in.ID, p.PathWithNamespace)
	fmt.Fprintf(&b, "  total: %d  success: %d  failed: %d  skipped: %d  error: %d  time: %.1fs\n",
		t.Total.Count, t.Total.Success, t.Total.Failed, t.Total.Skipped, t.Total.Error, t.Total.Time)
	if t.Total.SuiteError != "" {
		fmt.Fprintf(&b, "  suite error: %s\n", t.Total.SuiteError)
	}
	for _, su := range t.TestSuites {
		fmt.Fprintf(&b, "- %s: total=%d success=%d failed=%d skipped=%d error=%d time=%.1fs\n",
			dash(su.Name), su.TotalCount, su.SuccessCount, su.FailedCount, su.SkippedCount, su.ErrorCount, su.TotalTime)
	}
	return textResult(b.String()), nil, nil
}

type pipelineSchedulesParam struct {
	projectParam
	limitParam
}

func (s *Server) pipelineSchedulesList(ctx context.Context, _ *mcp.CallToolRequest, in pipelineSchedulesParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	scs, truncated, err := inst.Client.PipelineSchedules(ctx, p.PathWithNamespace, in.resolvedLimit())
	if err != nil {
		return errorResult(err), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d pipeline schedule(s) in %s:\n", len(scs), p.PathWithNamespace)
	for _, sc := range scs {
		fmt.Fprintf(&b, "- #%d %q ref=%s cron=%q (%s) active=%t next=%s owner=%s\n",
			sc.ID, sc.Description, dash(sc.Ref), sc.Cron, dash(sc.CronTimezone), sc.Active,
			dash(sc.NextRunAt), dash(sc.Owner.Username))
	}
	b.WriteString(truncationNotice(truncated, "Raise `limit` to see more."))
	b.WriteString("\nSchedule variables are not shown: they are pipeline secrets.\n")
	return textResult(b.String()), nil, nil
}

type pipelineWaitParam struct {
	projectParam
	ID           int `json:"id" jsonschema:"the pipeline id to watch"`
	TimeoutSec   int `json:"timeoutSeconds,omitempty" jsonschema:"how long to wait in seconds (default 300, maximum 1800)"`
	PollInterval int `json:"pollIntervalSeconds,omitempty" jsonschema:"seconds between polls (default 10, minimum 5)"`
}

// terminalStatuses are the pipeline states that end a wait.
var terminalStatuses = map[string]bool{
	"success": true, "failed": true, "canceled": true, "skipped": true, "manual": true,
}

func (s *Server) pipelineWait(ctx context.Context, _ *mcp.CallToolRequest, in pipelineWaitParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	timeout := time.Duration(in.TimeoutSec) * time.Second
	switch {
	case timeout <= 0:
		timeout = 5 * time.Minute
	case timeout > 30*time.Minute:
		timeout = 30 * time.Minute
	}
	interval := time.Duration(in.PollInterval) * time.Second
	if interval < 5*time.Second {
		interval = 10 * time.Second
	}

	deadline := time.Now().Add(timeout)
	for {
		pl, err := inst.Client.Pipeline(ctx, p.PathWithNamespace, in.ID)
		if err != nil {
			return errorResult(err), nil, nil
		}
		if terminalStatuses[pl.Status] {
			return textResult("Pipeline reached a terminal status.\n\n" + pipelineSummary(p.PathWithNamespace, pl)), nil, nil
		}
		if time.Now().After(deadline) {
			return textResult(fmt.Sprintf("Timed out after %s; pipeline is still %q.\n\n%s",
				timeout, pl.Status, pipelineSummary(p.PathWithNamespace, pl))), nil, nil
		}
		select {
		case <-ctx.Done():
			return errorResult(ctx.Err()), nil, nil
		case <-time.After(interval):
		}
	}
}

type ciLintParam struct {
	projectParam
	Content           string `json:"content" jsonschema:"the CI/CD configuration to validate — typically the full text of a .gitlab-ci.yml the agent holds locally. Maximum 1 MiB"`
	Ref               string `json:"ref,omitempty" jsonschema:"with dryRun, the branch or tag context to validate against"`
	DryRun            bool   `json:"dryRun,omitempty" jsonschema:"simulate pipeline creation instead of a static check — catches rules:/needs: problems a static check misses"`
	IncludeJobs       bool   `json:"includeJobs,omitempty" jsonschema:"also list the jobs the configuration would produce"`
	IncludeMergedYAML bool   `json:"includeMergedYaml,omitempty" jsonschema:"also return the fully merged configuration with all includes resolved (large)"`
}

func (s *Server) ciLint(ctx context.Context, _ *mcp.CallToolRequest, in ciLintParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	if strings.TrimSpace(in.Content) == "" {
		return errorResult(fmt.Errorf("`content` is required: pass the CI/CD configuration to validate")), nil, nil
	}
	if len(in.Content) > ciLintMaxBytes {
		return errorResult(fmt.Errorf("`content` is %d bytes, which exceeds the %d byte limit for ci_lint",
			len(in.Content), ciLintMaxBytes)), nil, nil
	}

	r, err := inst.Client.Lint(ctx, p.PathWithNamespace, gitlab.LintRequest{
		Content:     in.Content,
		DryRun:      in.DryRun,
		IncludeJobs: in.IncludeJobs,
		Ref:         in.Ref,
	})
	if err != nil {
		return errorResult(err), nil, nil
	}

	var b strings.Builder
	mode := "static check"
	if in.DryRun {
		mode = "pipeline simulation"
	}
	fmt.Fprintf(&b, "CI lint for %s (%s): valid=%t\n", p.PathWithNamespace, mode, r.Valid)
	if len(r.Errors) > 0 {
		b.WriteString("\nErrors:\n")
		for _, e := range r.Errors {
			fmt.Fprintf(&b, "- %s\n", e)
		}
	}
	if len(r.Warnings) > 0 {
		b.WriteString("\nWarnings:\n")
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}
	if len(r.Includes) > 0 {
		b.WriteString("\nResolved includes:\n")
		for _, i := range r.Includes {
			fmt.Fprintf(&b, "- [%s] %s", dash(i.Type), dash(i.Location))
			if i.ContextProject != "" {
				fmt.Fprintf(&b, " (from %s)", i.ContextProject)
			}
			b.WriteString("\n")
		}
	}
	if in.IncludeJobs && len(r.Jobs) > 0 {
		b.WriteString("\nJobs:\n")
		for _, j := range r.Jobs {
			fmt.Fprintf(&b, "- %s: %s", dash(j.Stage), dash(j.Name))
			if j.When != "" && j.When != "on_success" {
				fmt.Fprintf(&b, " when=%s", j.When)
			}
			if j.AllowFailure {
				b.WriteString(" allow_failure")
			}
			b.WriteString("\n")
		}
	}
	if in.IncludeMergedYAML && r.MergedYAML != "" {
		b.WriteString("\nMerged configuration:\n")
		b.WriteString(gitlab.RedactText(r.MergedYAML))
		b.WriteString("\n")
	}
	return textResult(b.String()), nil, nil
}

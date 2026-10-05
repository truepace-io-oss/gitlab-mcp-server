package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/gitlab"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/instances"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/metrics"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/perm"
)

// Pipelines are the only write surface this server exposes. Everything else —
// projects, repository, issues, merge requests — is read-only.

func (s *Server) registerPipelineTools(m *mcp.Server) {
	addTool(m, s, "pipeline_create",
		"Start a new pipeline on a ref. Use this for a full re-run; `pipeline_retry` only re-runs the failed and canceled jobs of an existing pipeline.",
		s.pipelineCreate)
	addTool(m, s, "pipeline_retry",
		"Retry a pipeline. GitLab re-runs only its failed and canceled jobs — for a complete fresh run use `pipeline_create` on the same ref.",
		s.pipelineRetry)
	addTool(m, s, "pipeline_cancel",
		"Cancel a pipeline's running and pending jobs. Prefer this over `pipeline_delete` when the intent is merely to stop a run.",
		s.pipelineCancel)
	addTool(m, s, "pipeline_update",
		"Set a pipeline's name. This is the only field-level update GitLab offers on a pipeline; the pipeline's definition lives in .gitlab-ci.yml, which this server cannot write.",
		s.pipelineUpdate)
	addTool(m, s, "pipeline_delete",
		"Permanently delete a pipeline together with its jobs and their logs. IRREVERSIBLE — unlike a project, a deleted pipeline cannot be restored. Requires the Owner role at GitLab and is usually disabled; use `pipeline_cancel` to stop a run instead.",
		s.pipelineDelete)
	addTool(m, s, "job_retry", "Retry a single job.", s.jobRetry)
	addTool(m, s, "job_cancel", "Cancel a single running job.", s.jobCancel)
	addTool(m, s, "job_play", "Run a manual job.", s.jobPlay)
	addTool(m, s, "pipeline_schedule_play", "Run a pipeline schedule immediately, without waiting for its cron.", s.pipelineSchedulePlay)
}

// variablesParam is embedded by the two tools that can pass CI variables.
type variablesParam struct {
	Variables []variableInput `json:"variables,omitempty" jsonschema:"CI/CD variables for this run. Rejected unless the instance enables pipelines.allowVariables, because injecting variables changes what the pipeline does"`
}

type variableInput struct {
	Key   string `json:"key" jsonschema:"the variable name"`
	Value string `json:"value" jsonschema:"the variable value"`
	Type  string `json:"type,omitempty" jsonschema:"env_var (default) or file"`
}

// assertVariablesAllowed refuses supplied variables unless the instance opts in.
func assertVariablesAllowed(in *instances.Instance, vars []variableInput) error {
	if len(vars) == 0 {
		return nil
	}
	if !in.Guard.AllowVariables() {
		metrics.RecordWriteBlocked(in.Name, perm.ReasonCapabilityDenied)
		return fmt.Errorf("supplying CI variables is disabled for GitLab instance %q "+
			"(set permissions.pipelines.allowVariables to enable it)", in.Name)
	}
	return nil
}

func toAPIVariables(vars []variableInput) []gitlab.PipelineVariable {
	out := make([]gitlab.PipelineVariable, 0, len(vars))
	for _, v := range vars {
		out = append(out, gitlab.PipelineVariable{Key: v.Key, Value: v.Value, VariableType: v.Type})
	}
	return out
}

// logOperation records who asked for a mutation. GitLab attributes the action to
// the service account behind the token, so this is the only place the human
// identity is preserved.
func logOperation(ctx context.Context, instance, op, project, target string) {
	slog.Info("pipeline operation",
		"initiator", initiator(ctx),
		"instance", instance,
		"op", op,
		"project", project,
		"target", target)
}

// --- pipeline lifecycle -----------------------------------------------------

type pipelineCreateParam struct {
	projectParam
	variablesParam
	Ref string `json:"ref" jsonschema:"the branch or tag to run the pipeline on"`
}

func (s *Server) pipelineCreate(ctx context.Context, _ *mcp.CallToolRequest, in pipelineCreateParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	if in.Ref == "" {
		return errorResult(fmt.Errorf("`ref` is required: name the branch or tag to run")), nil, nil
	}
	if err := assertVariablesAllowed(inst, in.Variables); err != nil {
		return errorResult(err), nil, nil
	}

	pl, err := inst.Client.CreatePipeline(ctx, p.PathWithNamespace, gitlab.CreatePipelineRequest{
		Ref:       in.Ref,
		Variables: toAPIVariables(in.Variables),
	})
	if err != nil {
		metrics.RecordOperation(inst.Name, "pipeline_create", "error")
		return errorResult(err), nil, nil
	}
	metrics.RecordOperation(inst.Name, "pipeline_create", "ok")
	logOperation(ctx, inst.Name, "pipeline_create", p.PathWithNamespace, in.Ref)
	return textResult("Pipeline created.\n\n" + pipelineSummary(p.PathWithNamespace, pl)), nil, nil
}

func (s *Server) pipelineRetry(ctx context.Context, _ *mcp.CallToolRequest, in pipelineIDParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	pl, err := inst.Client.RetryPipeline(ctx, p.PathWithNamespace, in.ID)
	if err != nil {
		metrics.RecordOperation(inst.Name, "pipeline_retry", "error")
		return errorResult(err), nil, nil
	}
	metrics.RecordOperation(inst.Name, "pipeline_retry", "ok")
	logOperation(ctx, inst.Name, "pipeline_retry", p.PathWithNamespace, fmt.Sprint(in.ID))
	return textResult("Pipeline retried (GitLab re-runs its failed and canceled jobs only).\n\n" +
		pipelineSummary(p.PathWithNamespace, pl)), nil, nil
}

func (s *Server) pipelineCancel(ctx context.Context, _ *mcp.CallToolRequest, in pipelineIDParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	pl, err := inst.Client.CancelPipeline(ctx, p.PathWithNamespace, in.ID)
	if err != nil {
		metrics.RecordOperation(inst.Name, "pipeline_cancel", "error")
		return errorResult(err), nil, nil
	}
	metrics.RecordOperation(inst.Name, "pipeline_cancel", "ok")
	logOperation(ctx, inst.Name, "pipeline_cancel", p.PathWithNamespace, fmt.Sprint(in.ID))
	return textResult("Pipeline canceled.\n\n" + pipelineSummary(p.PathWithNamespace, pl)), nil, nil
}

type pipelineUpdateParam struct {
	projectParam
	ID   int    `json:"id" jsonschema:"the pipeline id"`
	Name string `json:"name" jsonschema:"the new pipeline name"`
}

func (s *Server) pipelineUpdate(ctx context.Context, _ *mcp.CallToolRequest, in pipelineUpdateParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	if strings.TrimSpace(in.Name) == "" {
		return errorResult(fmt.Errorf("`name` is required")), nil, nil
	}
	pl, err := inst.Client.UpdatePipelineMetadata(ctx, p.PathWithNamespace, in.ID, in.Name)
	if err != nil {
		metrics.RecordOperation(inst.Name, "pipeline_update", "error")
		return errorResult(err), nil, nil
	}
	metrics.RecordOperation(inst.Name, "pipeline_update", "ok")
	logOperation(ctx, inst.Name, "pipeline_update", p.PathWithNamespace, fmt.Sprint(in.ID))
	return textResult("Pipeline name updated.\n\n" + pipelineSummary(p.PathWithNamespace, pl)), nil, nil
}

type pipelineDeleteParam struct {
	projectParam
	ID                int `json:"id" jsonschema:"the pipeline id to delete"`
	ConfirmPipelineID int `json:"confirmPipelineId" jsonschema:"must repeat the same pipeline id — a deliberate second step, because deletion cannot be undone"`
}

func (s *Server) pipelineDelete(ctx context.Context, _ *mcp.CallToolRequest, in pipelineDeleteParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	// Check the capability before the confirmation, so a disabled instance gives
	// the capability message rather than a confirmation nag.
	if err := inst.Guard.Allow(perm.PipelinesDelete); err != nil {
		return errorResult(err), nil, nil
	}
	if in.ConfirmPipelineID != in.ID {
		metrics.RecordWriteBlocked(inst.Name, perm.ReasonConfirmMissing)
		return errorResult(fmt.Errorf(
			"refusing to delete pipeline #%d: `confirmPipelineId` must repeat the same id (got %d). "+
				"Deleting a pipeline permanently removes its jobs and logs and cannot be undone",
			in.ID, in.ConfirmPipelineID)), nil, nil
	}

	if err := inst.Client.DeletePipeline(ctx, p.PathWithNamespace, in.ID); err != nil {
		metrics.RecordOperation(inst.Name, "pipeline_delete", "error")
		if gitlab.IsForbidden(err) {
			return errorResult(fmt.Errorf("%w — deleting a pipeline requires the Owner role for the token's user", err)), nil, nil
		}
		return errorResult(err), nil, nil
	}
	metrics.RecordOperation(inst.Name, "pipeline_delete", "ok")
	logOperation(ctx, inst.Name, "pipeline_delete", p.PathWithNamespace, fmt.Sprint(in.ID))
	return textResult(fmt.Sprintf("Pipeline #%d in %s was permanently deleted, together with its jobs and logs. This cannot be undone.",
		in.ID, p.PathWithNamespace)), nil, nil
}

// --- jobs -------------------------------------------------------------------

func (s *Server) jobRetry(ctx context.Context, _ *mcp.CallToolRequest, in jobRefParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	j, err := inst.Client.RetryJob(ctx, p.PathWithNamespace, in.ID)
	if err != nil {
		metrics.RecordOperation(inst.Name, "job_retry", "error")
		return errorResult(err), nil, nil
	}
	metrics.RecordOperation(inst.Name, "job_retry", "ok")
	logOperation(ctx, inst.Name, "job_retry", p.PathWithNamespace, fmt.Sprint(in.ID))
	return textResult(jobResult("Job retried", p.PathWithNamespace, j)), nil, nil
}

func (s *Server) jobCancel(ctx context.Context, _ *mcp.CallToolRequest, in jobRefParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	j, err := inst.Client.CancelJob(ctx, p.PathWithNamespace, in.ID)
	if err != nil {
		metrics.RecordOperation(inst.Name, "job_cancel", "error")
		return errorResult(err), nil, nil
	}
	metrics.RecordOperation(inst.Name, "job_cancel", "ok")
	logOperation(ctx, inst.Name, "job_cancel", p.PathWithNamespace, fmt.Sprint(in.ID))
	return textResult(jobResult("Job canceled", p.PathWithNamespace, j)), nil, nil
}

type jobPlayParam struct {
	projectParam
	variablesParam
	ID int `json:"id" jsonschema:"the manual job id to run"`
}

func (s *Server) jobPlay(ctx context.Context, _ *mcp.CallToolRequest, in jobPlayParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	if err := assertVariablesAllowed(inst, in.Variables); err != nil {
		return errorResult(err), nil, nil
	}
	j, err := inst.Client.PlayJob(ctx, p.PathWithNamespace, in.ID, gitlab.PlayJobRequest{
		JobVariablesAttributes: toAPIVariables(in.Variables),
	})
	if err != nil {
		metrics.RecordOperation(inst.Name, "job_play", "error")
		return errorResult(err), nil, nil
	}
	metrics.RecordOperation(inst.Name, "job_play", "ok")
	logOperation(ctx, inst.Name, "job_play", p.PathWithNamespace, fmt.Sprint(in.ID))
	return textResult(jobResult("Job started", p.PathWithNamespace, j)), nil, nil
}

type schedulePlayParam struct {
	projectParam
	ID int `json:"id" jsonschema:"the pipeline schedule id"`
}

func (s *Server) pipelineSchedulePlay(ctx context.Context, _ *mcp.CallToolRequest, in schedulePlayParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	if err := inst.Client.PlayPipelineSchedule(ctx, p.PathWithNamespace, in.ID); err != nil {
		metrics.RecordOperation(inst.Name, "schedule_play", "error")
		return errorResult(err), nil, nil
	}
	metrics.RecordOperation(inst.Name, "schedule_play", "ok")
	logOperation(ctx, inst.Name, "schedule_play", p.PathWithNamespace, fmt.Sprint(in.ID))
	return textResult(fmt.Sprintf("Pipeline schedule #%d in %s was queued. GitLab runs it asynchronously — use pipelines_list to find the resulting pipeline.",
		in.ID, p.PathWithNamespace)), nil, nil
}

func jobResult(action, project string, j *gitlab.Job) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s.\n\n", action)
	fmt.Fprintf(&b, "Job #%d %q in %s\n", j.ID, j.Name, project)
	fmt.Fprintf(&b, "  stage:    %s\n", dash(j.Stage))
	fmt.Fprintf(&b, "  status:   %s\n", dash(j.Status))
	fmt.Fprintf(&b, "  pipeline: #%d\n", j.Pipeline.ID)
	if j.WebURL != "" {
		fmt.Fprintf(&b, "  url:      %s\n", j.WebURL)
	}
	return b.String()
}

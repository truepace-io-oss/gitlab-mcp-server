package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/truepace-io-oss/gitlab-mcp-server/internal/perm"
)

// One typed function per endpoint. The capability constant appears at the call
// site so the tool-to-permission mapping is readable here rather than implied.

// --- meta -------------------------------------------------------------------

// Version reads GET /version. It needs no capability: it is the reachability
// probe and carries no project data.
func (c *Client) Version(ctx context.Context) (*Version, error) {
	var v Version
	_, err := c.do(ctx, http.MethodGet, "/version", nil, nil, &v)
	return &v, err
}

// SelfToken reads GET /personal_access_tokens/self — the server's own token
// metadata (name, scopes, expiry). Explicitly allow-listed in denylist.go.
func (c *Client) SelfToken(ctx context.Context) (*TokenInfo, error) {
	var t TokenInfo
	_, err := c.do(ctx, http.MethodGet, "/personal_access_tokens/self", nil, nil, &t)
	return &t, err
}

// --- groups & projects ------------------------------------------------------

// Groups lists groups the token can see.
func (c *Client) Groups(ctx context.Context, search string, topLevelOnly bool, limit int) ([]Group, bool, error) {
	q := url.Values{}
	if search != "" {
		q.Set("search", search)
	}
	if topLevelOnly {
		q.Set("top_level_only", "true")
	}
	q.Set("order_by", "path")
	q.Set("sort", "asc")
	return ListAll[Group](ctx, c, perm.ReadCore, "/groups", q, limit)
}

// Group reads one group.
func (c *Client) Group(ctx context.Context, ref string) (*Group, error) {
	var g Group
	_, err := c.Do(ctx, perm.ReadCore, http.MethodGet, "/groups/"+esc(ref), nil, nil, &g)
	return &g, err
}

// ProjectsOpts filters a project listing.
type ProjectsOpts struct {
	Group      string
	Search     string
	Archived   *bool
	Visibility string
	OrderBy    string
	Membership bool
	Limit      int
}

// Projects lists projects, optionally scoped to a group.
func (c *Client) Projects(ctx context.Context, o ProjectsOpts) ([]Project, bool, error) {
	q := url.Values{}
	if o.Search != "" {
		q.Set("search", o.Search)
	}
	if o.Archived != nil {
		q.Set("archived", strconv.FormatBool(*o.Archived))
	}
	if o.Visibility != "" {
		q.Set("visibility", o.Visibility)
	}
	if o.OrderBy != "" {
		q.Set("order_by", o.OrderBy)
	} else {
		q.Set("order_by", "last_activity_at")
	}
	if o.Membership {
		q.Set("membership", "true")
	}
	path := "/projects"
	if o.Group != "" {
		path = "/groups/" + esc(o.Group) + "/projects"
		q.Set("include_subgroups", "true")
	}
	return ListAll[Project](ctx, c, perm.ReadCore, path, q, o.Limit)
}

// Project reads one project by numeric id or full path.
func (c *Client) Project(ctx context.Context, ref string) (*Project, error) {
	var p Project
	_, err := c.Do(ctx, perm.ReadCore, http.MethodGet, "/projects/"+esc(ref), nil, nil, &p)
	return &p, err
}

// --- repository -------------------------------------------------------------

// Tree lists a repository directory.
func (c *Client) Tree(ctx context.Context, project, path, ref string, recursive bool, limit int) ([]TreeEntry, bool, error) {
	q := url.Values{}
	if path != "" {
		q.Set("path", path)
	}
	if ref != "" {
		q.Set("ref", ref)
	}
	if recursive {
		q.Set("recursive", "true")
	}
	return ListAll[TreeEntry](ctx, c, perm.ReadCore, "/projects/"+esc(project)+"/repository/tree", q, limit)
}

// FileRaw reads a repository file's raw content, bounded by limit bytes.
func (c *Client) FileRaw(ctx context.Context, project, filePath, ref string, limit int64) (string, error) {
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	p := fmt.Sprintf("/projects/%s/repository/files/%s/raw", esc(project), url.PathEscape(filePath))
	return c.RawText(ctx, perm.ReadCore, p, q, limit)
}

// Branches lists branches.
func (c *Client) Branches(ctx context.Context, project, search string, limit int) ([]Branch, bool, error) {
	q := url.Values{}
	if search != "" {
		q.Set("search", search)
	}
	return ListAll[Branch](ctx, c, perm.ReadCore, "/projects/"+esc(project)+"/repository/branches", q, limit)
}

// Tags lists tags.
func (c *Client) Tags(ctx context.Context, project, search string, limit int) ([]Tag, bool, error) {
	q := url.Values{}
	if search != "" {
		q.Set("search", search)
	}
	return ListAll[Tag](ctx, c, perm.ReadCore, "/projects/"+esc(project)+"/repository/tags", q, limit)
}

// CommitsOpts filters a commit listing.
type CommitsOpts struct {
	Ref   string
	Path  string
	Since string
	Until string
	Limit int
}

// Commits lists commits.
func (c *Client) Commits(ctx context.Context, project string, o CommitsOpts) ([]Commit, bool, error) {
	q := url.Values{}
	if o.Ref != "" {
		q.Set("ref_name", o.Ref)
	}
	if o.Path != "" {
		q.Set("path", o.Path)
	}
	if o.Since != "" {
		q.Set("since", o.Since)
	}
	if o.Until != "" {
		q.Set("until", o.Until)
	}
	return ListAll[Commit](ctx, c, perm.ReadCore, "/projects/"+esc(project)+"/repository/commits", q, o.Limit)
}

// Commit reads one commit.
func (c *Client) Commit(ctx context.Context, project, sha string) (*Commit, error) {
	var cm Commit
	_, err := c.Do(ctx, perm.ReadCore, http.MethodGet,
		fmt.Sprintf("/projects/%s/repository/commits/%s", esc(project), url.PathEscape(sha)), nil, nil, &cm)
	return &cm, err
}

// CommitDiff reads one commit's diff.
func (c *Client) CommitDiff(ctx context.Context, project, sha string, limit int) ([]Diff, bool, error) {
	return ListAll[Diff](ctx, c, perm.ReadCore,
		fmt.Sprintf("/projects/%s/repository/commits/%s/diff", esc(project), url.PathEscape(sha)), nil, limit)
}

// CompareRefs compares two refs.
func (c *Client) CompareRefs(ctx context.Context, project, from, to string, straight bool) (*Compare, error) {
	q := url.Values{}
	q.Set("from", from)
	q.Set("to", to)
	if straight {
		q.Set("straight", "true")
	}
	var cmp Compare
	_, err := c.Do(ctx, perm.ReadCore, http.MethodGet, "/projects/"+esc(project)+"/repository/compare", q, nil, &cmp)
	return &cmp, err
}

// --- search -----------------------------------------------------------------

// Search runs a scoped search at instance, group or project level. The result is
// returned undecoded so each scope can be rendered by its own shape.
func (c *Client) Search(ctx context.Context, scope, term, group, project string, limit int) ([]map[string]any, bool, error) {
	q := url.Values{}
	q.Set("scope", scope)
	q.Set("search", term)

	path := "/search"
	switch {
	case project != "":
		path = "/projects/" + esc(project) + "/search"
	case group != "":
		path = "/groups/" + esc(group) + "/search"
	}
	return ListAll[map[string]any](ctx, c, perm.ReadCore, path, q, limit)
}

// --- issues -----------------------------------------------------------------

// IssuesOpts filters an issue listing.
type IssuesOpts struct {
	Project string
	Group   string
	State   string
	Labels  string
	Search  string
	Limit   int
}

// Issues lists issues.
func (c *Client) Issues(ctx context.Context, o IssuesOpts) ([]Issue, bool, error) {
	q := url.Values{}
	if o.State != "" {
		q.Set("state", o.State)
	}
	if o.Labels != "" {
		q.Set("labels", o.Labels)
	}
	if o.Search != "" {
		q.Set("search", o.Search)
	}
	q.Set("order_by", "updated_at")

	path := "/issues"
	switch {
	case o.Project != "":
		path = "/projects/" + esc(o.Project) + "/issues"
	case o.Group != "":
		path = "/groups/" + esc(o.Group) + "/issues"
	}
	return ListAll[Issue](ctx, c, perm.ReadCore, path, q, o.Limit)
}

// Issue reads one issue.
func (c *Client) Issue(ctx context.Context, project string, iid int) (*Issue, error) {
	var i Issue
	_, err := c.Do(ctx, perm.ReadCore, http.MethodGet,
		fmt.Sprintf("/projects/%s/issues/%d", esc(project), iid), nil, nil, &i)
	return &i, err
}

// IssueNotes lists an issue's comments.
func (c *Client) IssueNotes(ctx context.Context, project string, iid, limit int) ([]Note, bool, error) {
	q := url.Values{}
	q.Set("order_by", "created_at")
	q.Set("sort", "asc")
	return ListAll[Note](ctx, c, perm.ReadCore,
		fmt.Sprintf("/projects/%s/issues/%d/notes", esc(project), iid), q, limit)
}

// --- merge requests ---------------------------------------------------------

// MergeRequestsOpts filters a merge-request listing.
type MergeRequestsOpts struct {
	Project      string
	Group        string
	State        string
	TargetBranch string
	SourceBranch string
	Search       string
	Limit        int
}

// MergeRequests lists merge requests.
func (c *Client) MergeRequests(ctx context.Context, o MergeRequestsOpts) ([]MergeRequest, bool, error) {
	q := url.Values{}
	if o.State != "" {
		q.Set("state", o.State)
	}
	if o.TargetBranch != "" {
		q.Set("target_branch", o.TargetBranch)
	}
	if o.SourceBranch != "" {
		q.Set("source_branch", o.SourceBranch)
	}
	if o.Search != "" {
		q.Set("search", o.Search)
	}
	q.Set("order_by", "updated_at")

	path := "/merge_requests"
	switch {
	case o.Project != "":
		path = "/projects/" + esc(o.Project) + "/merge_requests"
	case o.Group != "":
		path = "/groups/" + esc(o.Group) + "/merge_requests"
	}
	return ListAll[MergeRequest](ctx, c, perm.ReadCore, path, q, o.Limit)
}

// MergeRequest reads one merge request.
func (c *Client) MergeRequest(ctx context.Context, project string, iid int) (*MergeRequest, error) {
	var mr MergeRequest
	_, err := c.Do(ctx, perm.ReadCore, http.MethodGet,
		fmt.Sprintf("/projects/%s/merge_requests/%d", esc(project), iid), nil, nil, &mr)
	return &mr, err
}

// MergeRequestDiffs reads a merge request's changed files.
func (c *Client) MergeRequestDiffs(ctx context.Context, project string, iid, limit int) ([]Diff, bool, error) {
	return ListAll[Diff](ctx, c, perm.ReadCore,
		fmt.Sprintf("/projects/%s/merge_requests/%d/diffs", esc(project), iid), nil, limit)
}

// MergeRequestNotes lists a merge request's comments.
func (c *Client) MergeRequestNotes(ctx context.Context, project string, iid, limit int) ([]Note, bool, error) {
	q := url.Values{}
	q.Set("order_by", "created_at")
	q.Set("sort", "asc")
	return ListAll[Note](ctx, c, perm.ReadCore,
		fmt.Sprintf("/projects/%s/merge_requests/%d/notes", esc(project), iid), q, limit)
}

// --- members ----------------------------------------------------------------

// Members lists project or group members.
func (c *Client) Members(ctx context.Context, project, group string, limit int) ([]Member, bool, error) {
	path := "/projects/" + esc(project) + "/members/all"
	if group != "" {
		path = "/groups/" + esc(group) + "/members"
	}
	return ListAll[Member](ctx, c, perm.ReadCore, path, nil, limit)
}

// --- pipelines (read) -------------------------------------------------------

// PipelinesOpts filters a pipeline listing.
type PipelinesOpts struct {
	Status       string
	Ref          string
	SHA          string
	Source       string
	UpdatedAfter string
	Username     string
	Limit        int
}

// Pipelines lists pipelines.
func (c *Client) Pipelines(ctx context.Context, project string, o PipelinesOpts) ([]Pipeline, bool, error) {
	q := url.Values{}
	if o.Status != "" {
		q.Set("status", o.Status)
	}
	if o.Ref != "" {
		q.Set("ref", o.Ref)
	}
	if o.SHA != "" {
		q.Set("sha", o.SHA)
	}
	if o.Source != "" {
		q.Set("source", o.Source)
	}
	if o.UpdatedAfter != "" {
		q.Set("updated_after", o.UpdatedAfter)
	}
	if o.Username != "" {
		q.Set("username", o.Username)
	}
	q.Set("order_by", "id")
	q.Set("sort", "desc")
	return ListAll[Pipeline](ctx, c, perm.ReadCI, "/projects/"+esc(project)+"/pipelines", q, o.Limit)
}

// Pipeline reads one pipeline.
func (c *Client) Pipeline(ctx context.Context, project string, id int) (*Pipeline, error) {
	var p Pipeline
	_, err := c.Do(ctx, perm.ReadCI, http.MethodGet,
		fmt.Sprintf("/projects/%s/pipelines/%d", esc(project), id), nil, nil, &p)
	return &p, err
}

// LatestPipeline reads the latest pipeline for a ref.
func (c *Client) LatestPipeline(ctx context.Context, project, ref string) (*Pipeline, error) {
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	var p Pipeline
	_, err := c.Do(ctx, perm.ReadCI, http.MethodGet, "/projects/"+esc(project)+"/pipelines/latest", q, nil, &p)
	return &p, err
}

// PipelineJobs lists a pipeline's jobs.
func (c *Client) PipelineJobs(ctx context.Context, project string, id int, scope string, includeRetried bool, limit int) ([]Job, bool, error) {
	q := url.Values{}
	if scope != "" {
		q.Set("scope[]", scope)
	}
	if includeRetried {
		q.Set("include_retried", "true")
	}
	return ListAll[Job](ctx, c, perm.ReadCI,
		fmt.Sprintf("/projects/%s/pipelines/%d/jobs", esc(project), id), q, limit)
}

// Job reads one job.
func (c *Client) Job(ctx context.Context, project string, id int) (*Job, error) {
	var j Job
	_, err := c.Do(ctx, perm.ReadCI, http.MethodGet,
		fmt.Sprintf("/projects/%s/jobs/%d", esc(project), id), nil, nil, &j)
	return &j, err
}

// JobTrace reads a job's log, bounded by limit bytes and redacted.
func (c *Client) JobTrace(ctx context.Context, project string, id int, limit int64) (string, error) {
	return c.RawText(ctx, perm.ReadCI,
		fmt.Sprintf("/projects/%s/jobs/%d/trace", esc(project), id), nil, limit)
}

// TestReportSummary reads a pipeline's test summary.
func (c *Client) TestReportSummary(ctx context.Context, project string, id int) (*TestReportSummary, error) {
	var t TestReportSummary
	_, err := c.Do(ctx, perm.ReadCI, http.MethodGet,
		fmt.Sprintf("/projects/%s/pipelines/%d/test_report_summary", esc(project), id), nil, nil, &t)
	return &t, err
}

// PipelineSchedules lists pipeline schedules.
func (c *Client) PipelineSchedules(ctx context.Context, project string, limit int) ([]PipelineSchedule, bool, error) {
	return ListAll[PipelineSchedule](ctx, c, perm.ReadCI,
		"/projects/"+esc(project)+"/pipeline_schedules", nil, limit)
}

// --- CI lint ----------------------------------------------------------------

// Lint validates CI/CD configuration content in project context.
func (c *Client) Lint(ctx context.Context, project string, req LintRequest) (*LintResult, error) {
	var r LintResult
	_, err := c.Do(ctx, perm.ReadCI, http.MethodPost, "/projects/"+esc(project)+"/ci/lint", nil, req, &r)
	return &r, err
}

// --- pipelines (write) ------------------------------------------------------

// CreatePipeline starts a new pipeline on a ref.
func (c *Client) CreatePipeline(ctx context.Context, project string, req CreatePipelineRequest) (*Pipeline, error) {
	var p Pipeline
	_, err := c.Do(ctx, perm.PipelinesOperate, http.MethodPost, "/projects/"+esc(project)+"/pipeline", nil, req, &p)
	return &p, err
}

// RetryPipeline retries the failed and canceled jobs of a pipeline.
func (c *Client) RetryPipeline(ctx context.Context, project string, id int) (*Pipeline, error) {
	var p Pipeline
	_, err := c.Do(ctx, perm.PipelinesOperate, http.MethodPost,
		fmt.Sprintf("/projects/%s/pipelines/%d/retry", esc(project), id), nil, nil, &p)
	return &p, err
}

// CancelPipeline cancels a pipeline's running jobs.
func (c *Client) CancelPipeline(ctx context.Context, project string, id int) (*Pipeline, error) {
	var p Pipeline
	_, err := c.Do(ctx, perm.PipelinesOperate, http.MethodPost,
		fmt.Sprintf("/projects/%s/pipelines/%d/cancel", esc(project), id), nil, nil, &p)
	return &p, err
}

// UpdatePipelineMetadata sets a pipeline's name.
func (c *Client) UpdatePipelineMetadata(ctx context.Context, project string, id int, name string) (*Pipeline, error) {
	var p Pipeline
	_, err := c.Do(ctx, perm.PipelinesOperate, http.MethodPut,
		fmt.Sprintf("/projects/%s/pipelines/%d/metadata", esc(project), id), nil,
		PipelineMetadataRequest{Name: name}, &p)
	return &p, err
}

// DeletePipeline permanently removes a pipeline, its jobs and their logs.
// GitLab restricts this to the Owner role.
func (c *Client) DeletePipeline(ctx context.Context, project string, id int) error {
	_, err := c.Do(ctx, perm.PipelinesDelete, http.MethodDelete,
		fmt.Sprintf("/projects/%s/pipelines/%d", esc(project), id), nil, nil, nil)
	return err
}

// RetryJob retries a single job.
func (c *Client) RetryJob(ctx context.Context, project string, id int) (*Job, error) {
	var j Job
	_, err := c.Do(ctx, perm.PipelinesOperate, http.MethodPost,
		fmt.Sprintf("/projects/%s/jobs/%d/retry", esc(project), id), nil, nil, &j)
	return &j, err
}

// CancelJob cancels a single job.
func (c *Client) CancelJob(ctx context.Context, project string, id int) (*Job, error) {
	var j Job
	_, err := c.Do(ctx, perm.PipelinesOperate, http.MethodPost,
		fmt.Sprintf("/projects/%s/jobs/%d/cancel", esc(project), id), nil, nil, &j)
	return &j, err
}

// PlayJob runs a manual job.
func (c *Client) PlayJob(ctx context.Context, project string, id int, req PlayJobRequest) (*Job, error) {
	var j Job
	var body any
	if len(req.JobVariablesAttributes) > 0 {
		body = req
	}
	_, err := c.Do(ctx, perm.PipelinesOperate, http.MethodPost,
		fmt.Sprintf("/projects/%s/jobs/%d/play", esc(project), id), nil, body, &j)
	return &j, err
}

// PlayPipelineSchedule runs a schedule immediately.
func (c *Client) PlayPipelineSchedule(ctx context.Context, project string, id int) error {
	_, err := c.Do(ctx, perm.PipelinesOperate, http.MethodPost,
		fmt.Sprintf("/projects/%s/pipeline_schedules/%d/play", esc(project), id), nil, nil, nil)
	return err
}

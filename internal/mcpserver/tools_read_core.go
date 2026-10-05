package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/gitlab"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/instances"
)

// maxFileBytes caps repo_file_read output. Reading source is a normal operation,
// but one huge generated file should not consume the model's context.
const maxFileBytes = 128 << 10

func (s *Server) registerReadCoreTools(m *mcp.Server) {
	addTool(m, s, "groups_list", "List GitLab groups visible to this server, optionally filtered by a search term.", s.groupsList)
	addTool(m, s, "group_get", "Get one GitLab group: path, visibility, description and web URL.", s.groupGet)
	addTool(m, s, "projects_list", "List projects, optionally scoped to a group (including subgroups) and filtered by search term, visibility or archived state.", s.projectsList)
	addTool(m, s, "project_get", "Get one project: id, paths, default branch, visibility, archived state, topics and activity.", s.projectGet)
	addTool(m, s, "repo_tree", "List a directory in a project's repository. Use `recursive` sparingly on large repositories.", s.repoTree)
	addTool(m, s, "repo_file_read", "Read one file from a project's repository at a ref. Text only; capped at 128 KiB.", s.repoFileRead)
	addTool(m, s, "repo_branches", "List a project's branches with their protection state and last commit.", s.repoBranches)
	addTool(m, s, "repo_tags", "List a project's tags with their target commit.", s.repoTags)
	addTool(m, s, "repo_commits", "List commits on a ref, optionally limited to a path or a date range.", s.repoCommits)
	addTool(m, s, "repo_commit_get", "Get one commit with its message, author and (optionally) its diff.", s.repoCommitGet)
	addTool(m, s, "repo_compare", "Compare two refs and return the commits and changed files between them. Useful for 'what differs between my local branch and the default branch'.", s.repoCompare)
	addTool(m, s, "search", "Search GitLab at instance, group or project scope. Scopes: blobs (code), commits, issues, merge_requests, notes, wiki_blobs, milestones, users, projects.", s.search)
	addTool(m, s, "issues_list", "List issues for a project or group, filtered by state, labels or a search term.", s.issuesList)
	addTool(m, s, "issue_get", "Get one issue by its project-scoped iid, optionally with its comments.", s.issueGet)
	addTool(m, s, "mrs_list", "List merge requests for a project or group, filtered by state, branches or a search term.", s.mrsList)
	addTool(m, s, "mr_get", "Get one merge request by its project-scoped iid, optionally with its comments. Read-only: this server cannot create, comment on, approve or merge.", s.mrGet)
	addTool(m, s, "mr_diff", "Get the changed files of a merge request.", s.mrDiff)
	addTool(m, s, "members_list", "List the members of a project (including inherited) or of a group, with their role.", s.membersList)
}

// --- groups -----------------------------------------------------------------

type groupsListParam struct {
	instanceParam
	limitParam
	Search       string `json:"search,omitempty" jsonschema:"only groups whose name or path matches this term"`
	TopLevelOnly bool   `json:"topLevelOnly,omitempty" jsonschema:"only top-level groups, excluding subgroups"`
}

func (s *Server) groupsList(ctx context.Context, _ *mcp.CallToolRequest, in groupsListParam) (*mcp.CallToolResult, any, error) {
	inst, err := s.resolveInstance(in.Instance)
	if err != nil {
		return errorResult(err), nil, nil
	}
	gs, truncated, err := inst.Client.Groups(ctx, in.Search, in.TopLevelOnly, in.resolvedLimit())
	if err != nil {
		return errorResult(err), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d group(s) on %s:\n", len(gs), inst.Name)
	for _, g := range gs {
		fmt.Fprintf(&b, "- %s (id=%d) visibility=%s", g.FullPath, g.ID, dash(g.Visibility))
		if g.Description != "" {
			fmt.Fprintf(&b, "\n    %s", truncate(g.Description, maxMessageLen))
		}
		b.WriteString("\n")
	}
	b.WriteString(truncationNotice(truncated, "Narrow with `search` or raise `limit`."))
	return textResult(b.String()), nil, nil
}

type groupGetParam struct {
	instanceParam
	Group string `json:"group" jsonschema:"the group: numeric id or full path such as 'group/subgroup'"`
}

func (s *Server) groupGet(ctx context.Context, _ *mcp.CallToolRequest, in groupGetParam) (*mcp.CallToolResult, any, error) {
	inst, err := s.resolveInstance(in.Instance)
	if err != nil {
		return errorResult(err), nil, nil
	}
	g, err := s.resolveGroup(ctx, inst, in.Group)
	if err != nil {
		return errorResult(err), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Group %s\n", g.FullPath)
	fmt.Fprintf(&b, "  id:         %d\n", g.ID)
	fmt.Fprintf(&b, "  visibility: %s\n", dash(g.Visibility))
	if g.WebURL != "" {
		fmt.Fprintf(&b, "  url:        %s\n", g.WebURL)
	}
	if g.Description != "" {
		fmt.Fprintf(&b, "  description: %s\n", truncate(g.Description, maxMessageLen))
	}
	return textResult(b.String()), nil, nil
}

// --- projects ---------------------------------------------------------------

type projectsListParam struct {
	instanceParam
	limitParam
	Group      string `json:"group,omitempty" jsonschema:"only projects in this group, including its subgroups"`
	Search     string `json:"search,omitempty" jsonschema:"only projects whose name or path matches this term"`
	Archived   *bool  `json:"archived,omitempty" jsonschema:"filter by archived state; omit for both"`
	Visibility string `json:"visibility,omitempty" jsonschema:"filter by visibility: private, internal or public"`
	OrderBy    string `json:"orderBy,omitempty" jsonschema:"sort field: last_activity_at (default), name, path, created_at or updated_at"`
}

func (s *Server) projectsList(ctx context.Context, _ *mcp.CallToolRequest, in projectsListParam) (*mcp.CallToolResult, any, error) {
	inst, err := s.resolveInstance(in.Instance)
	if err != nil {
		return errorResult(err), nil, nil
	}
	// When the caller scopes to a group, apply the namespace guard to it first so
	// an out-of-bounds listing is refused before it is issued.
	if in.Group != "" {
		if err := inst.Guard.AllowNamespace(in.Group); err != nil {
			return errorResult(err), nil, nil
		}
	}
	ps, truncated, err := inst.Client.Projects(ctx, gitlab.ProjectsOpts{
		Group: in.Group, Search: in.Search, Archived: in.Archived,
		Visibility: in.Visibility, OrderBy: in.OrderBy, Limit: in.resolvedLimit(),
	})
	if err != nil {
		return errorResult(err), nil, nil
	}
	// Without a group scope GitLab returns everything the token can see, which
	// may include namespaces this instance is not allowed to touch. Filter so the
	// listing never advertises a project the other tools would refuse.
	kept := make([]gitlab.Project, 0, len(ps))
	for _, p := range ps {
		if inst.Guard.AllowNamespace(p.PathWithNamespace) == nil {
			kept = append(kept, p)
		}
	}
	return textResult(projectsTable(inst.Name, kept, truncated)), nil, nil
}

func (s *Server) projectGet(ctx context.Context, _ *mcp.CallToolRequest, in projectParam) (*mcp.CallToolResult, any, error) {
	inst, err := s.resolveInstance(in.Instance)
	if err != nil {
		return errorResult(err), nil, nil
	}
	p, err := s.resolveProject(ctx, inst, in.Project)
	if err != nil {
		return errorResult(err), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Project %s (id=%d)\n", p.PathWithNamespace, p.ID)
	fmt.Fprintf(&b, "  default branch: %s\n", dash(p.DefaultBranch))
	fmt.Fprintf(&b, "  visibility:     %s\n", dash(p.Visibility))
	fmt.Fprintf(&b, "  archived:       %t\n", p.Archived)
	fmt.Fprintf(&b, "  open issues:    %d\n", p.OpenIssuesCount)
	fmt.Fprintf(&b, "  stars:          %d\n", p.StarCount)
	if len(p.Topics) > 0 {
		fmt.Fprintf(&b, "  topics:         %s\n", strings.Join(p.Topics, ", "))
	}
	fmt.Fprintf(&b, "  last activity:  %s (%s)\n", dash(p.LastActivityAt), age(p.LastActivityAt))
	if p.WebURL != "" {
		fmt.Fprintf(&b, "  url:            %s\n", p.WebURL)
	}
	if p.Description != "" {
		fmt.Fprintf(&b, "  description:    %s\n", truncate(p.Description, maxMessageLen))
	}
	return textResult(b.String()), nil, nil
}

// --- repository -------------------------------------------------------------

type repoTreeParam struct {
	projectParam
	limitParam
	Path      string `json:"path,omitempty" jsonschema:"directory to list; defaults to the repository root"`
	Ref       string `json:"ref,omitempty" jsonschema:"branch, tag or commit SHA; defaults to the project's default branch"`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"descend into subdirectories — expensive on large repositories"`
}

func (s *Server) repoTree(ctx context.Context, _ *mcp.CallToolRequest, in repoTreeParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	entries, truncated, err := inst.Client.Tree(ctx, p.PathWithNamespace, in.Path, in.Ref, in.Recursive, in.resolvedLimit())
	if err != nil {
		return errorResult(err), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d entr(ies) in %s:%s@%s:\n", len(entries), p.PathWithNamespace, dash(in.Path), dash(firstNonEmpty(in.Ref, p.DefaultBranch)))
	for _, e := range entries {
		kind := "file"
		if e.Type == "tree" {
			kind = "dir "
		}
		fmt.Fprintf(&b, "- [%s] %s\n", kind, e.Path)
	}
	b.WriteString(truncationNotice(truncated, "Narrow with `path`, or raise `limit`."))
	return textResult(b.String()), nil, nil
}

type repoFileReadParam struct {
	projectParam
	Path     string `json:"path" jsonschema:"path of the file within the repository, e.g. 'src/main.go'"`
	Ref      string `json:"ref,omitempty" jsonschema:"branch, tag or commit SHA; defaults to the project's default branch"`
	MaxBytes int    `json:"maxBytes,omitempty" jsonschema:"maximum bytes to return (default and hard ceiling 131072)"`
}

func (s *Server) repoFileRead(ctx context.Context, _ *mcp.CallToolRequest, in repoFileReadParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	limit := int64(maxFileBytes)
	if in.MaxBytes > 0 && int64(in.MaxBytes) < limit {
		limit = int64(in.MaxBytes)
	}
	content, err := inst.Client.FileRaw(ctx, p.PathWithNamespace, in.Path, in.Ref, limit+1)
	if err != nil {
		return errorResult(err), nil, nil
	}
	if strings.ContainsRune(content, 0) {
		return errorResult(fmt.Errorf("%s appears to be a binary file; this tool returns text only", in.Path)), nil, nil
	}
	notice := ""
	if int64(len(content)) > limit {
		content = content[:limit]
		notice = fmt.Sprintf("\n… truncated at %d bytes. Raise `maxBytes` (ceiling %d) or read a narrower path.\n", limit, maxFileBytes)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s:%s@%s (%d bytes)\n\n", p.PathWithNamespace, in.Path, dash(firstNonEmpty(in.Ref, p.DefaultBranch)), len(content))
	b.WriteString(content)
	b.WriteString(notice)
	return textResult(b.String()), nil, nil
}

type repoRefsParam struct {
	projectParam
	limitParam
	Search string `json:"search,omitempty" jsonschema:"only refs whose name matches this term"`
}

func (s *Server) repoBranches(ctx context.Context, _ *mcp.CallToolRequest, in repoRefsParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	bs, truncated, err := inst.Client.Branches(ctx, p.PathWithNamespace, in.Search, in.resolvedLimit())
	if err != nil {
		return errorResult(err), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d branch(es) in %s:\n", len(bs), p.PathWithNamespace)
	for _, br := range bs {
		flags := []string{}
		if br.Default {
			flags = append(flags, "default")
		}
		if br.Protected {
			flags = append(flags, "protected")
		}
		if br.Merged {
			flags = append(flags, "merged")
		}
		fmt.Fprintf(&b, "- %s", br.Name)
		if len(flags) > 0 {
			fmt.Fprintf(&b, " [%s]", strings.Join(flags, ","))
		}
		fmt.Fprintf(&b, " %s %s\n", shortSHA(br.Commit.ID), truncate(br.Commit.Title, 80))
	}
	b.WriteString(truncationNotice(truncated, "Narrow with `search`."))
	return textResult(b.String()), nil, nil
}

func (s *Server) repoTags(ctx context.Context, _ *mcp.CallToolRequest, in repoRefsParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	ts, truncated, err := inst.Client.Tags(ctx, p.PathWithNamespace, in.Search, in.resolvedLimit())
	if err != nil {
		return errorResult(err), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d tag(s) in %s:\n", len(ts), p.PathWithNamespace)
	for _, t := range ts {
		fmt.Fprintf(&b, "- %s %s %s\n", t.Name, shortSHA(t.Commit.ID), truncate(firstNonEmpty(t.Message, t.Commit.Title), 80))
	}
	b.WriteString(truncationNotice(truncated, "Narrow with `search`."))
	return textResult(b.String()), nil, nil
}

type repoCommitsParam struct {
	projectParam
	limitParam
	Ref   string `json:"ref,omitempty" jsonschema:"branch, tag or commit SHA; defaults to the default branch"`
	Path  string `json:"path,omitempty" jsonschema:"only commits touching this path"`
	Since string `json:"since,omitempty" jsonschema:"only commits after this ISO 8601 timestamp"`
	Until string `json:"until,omitempty" jsonschema:"only commits before this ISO 8601 timestamp"`
}

func (s *Server) repoCommits(ctx context.Context, _ *mcp.CallToolRequest, in repoCommitsParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	cs, truncated, err := inst.Client.Commits(ctx, p.PathWithNamespace, gitlab.CommitsOpts{
		Ref: in.Ref, Path: in.Path, Since: in.Since, Until: in.Until, Limit: in.resolvedLimit(),
	})
	if err != nil {
		return errorResult(err), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d commit(s) in %s:\n", len(cs), p.PathWithNamespace)
	for _, c := range cs {
		fmt.Fprintf(&b, "- %s %s — %s (%s)\n", shortSHA(c.ID), truncate(c.Title, 100), dash(c.AuthorName), age(c.CommittedDate))
	}
	b.WriteString(truncationNotice(truncated, "Narrow with `since`/`until`/`path`."))
	return textResult(b.String()), nil, nil
}

type repoCommitGetParam struct {
	projectParam
	SHA         string `json:"sha" jsonschema:"the commit SHA (full or abbreviated)"`
	IncludeDiff bool   `json:"includeDiff,omitempty" jsonschema:"also return the commit's changed files and patch"`
	limitParam
}

func (s *Server) repoCommitGet(ctx context.Context, _ *mcp.CallToolRequest, in repoCommitGetParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	c, err := inst.Client.Commit(ctx, p.PathWithNamespace, in.SHA)
	if err != nil {
		return errorResult(err), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Commit %s in %s\n", c.ID, p.PathWithNamespace)
	fmt.Fprintf(&b, "  author:    %s <%s>\n", dash(c.AuthorName), dash(c.AuthorEmail))
	fmt.Fprintf(&b, "  committed: %s (%s)\n", dash(c.CommittedDate), age(c.CommittedDate))
	if c.WebURL != "" {
		fmt.Fprintf(&b, "  url:       %s\n", c.WebURL)
	}
	fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(c.Message))

	if in.IncludeDiff {
		diffs, truncated, derr := inst.Client.CommitDiff(ctx, p.PathWithNamespace, in.SHA, in.resolvedLimit())
		if derr != nil {
			return errorResult(derr), nil, nil
		}
		b.WriteString("\n")
		b.WriteString(diffsTable(diffs, truncated))
	}
	return textResult(b.String()), nil, nil
}

type repoCompareParam struct {
	projectParam
	From     string `json:"from" jsonschema:"the base ref (branch, tag or SHA)"`
	To       string `json:"to" jsonschema:"the ref to compare against the base"`
	Straight bool   `json:"straight,omitempty" jsonschema:"compare directly (from..to) instead of using the merge base (from...to)"`
}

func (s *Server) repoCompare(ctx context.Context, _ *mcp.CallToolRequest, in repoCompareParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	cmp, err := inst.Client.CompareRefs(ctx, p.PathWithNamespace, in.From, in.To, in.Straight)
	if err != nil {
		return errorResult(err), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Comparing %s...%s in %s: %d commit(s), %d changed file(s)\n",
		in.From, in.To, p.PathWithNamespace, len(cmp.Commits), len(cmp.Diffs))
	if cmp.CompareSameRef {
		b.WriteString("(the two refs are identical)\n")
	}
	if cmp.CompareTimeout {
		b.WriteString("WARNING: GitLab timed out computing this comparison; the result may be incomplete\n")
	}
	for _, c := range cmp.Commits {
		fmt.Fprintf(&b, "- %s %s — %s\n", shortSHA(c.ID), truncate(c.Title, 100), dash(c.AuthorName))
	}
	if len(cmp.Diffs) > 0 {
		b.WriteString("\n")
		b.WriteString(diffsTable(cmp.Diffs, false))
	}
	return textResult(b.String()), nil, nil
}

// --- search -----------------------------------------------------------------

type searchParam struct {
	instanceParam
	limitParam
	Scope   string `json:"scope" jsonschema:"what to search: blobs (code), commits, issues, merge_requests, notes, wiki_blobs, milestones, users or projects"`
	Term    string `json:"term" jsonschema:"the search term"`
	Group   string `json:"group,omitempty" jsonschema:"restrict the search to this group"`
	Project string `json:"project,omitempty" jsonschema:"restrict the search to this project"`
}

func (s *Server) search(ctx context.Context, _ *mcp.CallToolRequest, in searchParam) (*mcp.CallToolResult, any, error) {
	inst, err := s.resolveInstance(in.Instance)
	if err != nil {
		return errorResult(err), nil, nil
	}
	if in.Scope == "" || in.Term == "" {
		return errorResult(fmt.Errorf("both `scope` and `term` are required")), nil, nil
	}
	// Scoped searches must respect the namespace allowlist.
	project := in.Project
	if project != "" {
		p, perr := s.resolveProject(ctx, inst, project)
		if perr != nil {
			return errorResult(perr), nil, nil
		}
		project = p.PathWithNamespace
	}
	if in.Group != "" {
		if gerr := inst.Guard.AllowNamespace(in.Group); gerr != nil {
			return errorResult(gerr), nil, nil
		}
	}
	// An unscoped search would reach every namespace the token can see, which
	// would leak results the other tools refuse to return.
	if project == "" && in.Group == "" && len(inst.Guard.AllowedNamespaces()) > 0 {
		return errorResult(fmt.Errorf(
			"instance %q is bounded to the namespaces [%s]; pass `group` or `project` to scope the search",
			inst.Name, strings.Join(inst.Guard.AllowedNamespaces(), ", "))), nil, nil
	}

	hits, truncated, err := inst.Client.Search(ctx, in.Scope, in.Term, in.Group, project, in.resolvedLimit())
	if err != nil {
		return errorResult(err), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s hit(s) for %q:\n", len(hits), in.Scope, in.Term)
	for _, h := range hits {
		b.WriteString("- ")
		b.WriteString(renderSearchHit(in.Scope, h))
		b.WriteString("\n")
	}
	b.WriteString(truncationNotice(truncated, "Refine the term or narrow the scope."))
	return textResult(b.String()), nil, nil
}

// renderSearchHit renders the handful of fields that matter per scope, falling
// back to a few well-known keys for scopes not special-cased.
func renderSearchHit(scope string, h map[string]any) string {
	str := func(k string) string {
		if v, ok := h[k].(string); ok {
			return v
		}
		return ""
	}
	num := func(k string) string {
		if v, ok := h[k].(float64); ok {
			return fmt.Sprintf("%d", int(v))
		}
		return ""
	}
	switch scope {
	case "blobs", "wiki_blobs":
		return fmt.Sprintf("%s:%s (ref %s)\n    %s", dash(str("path")), dash(num("startline")), dash(str("ref")),
			truncate(str("data"), maxMessageLen))
	case "commits":
		return fmt.Sprintf("%s %s — %s", shortSHA(str("id")), truncate(str("title"), 100), dash(str("author_name")))
	case "issues", "merge_requests":
		return fmt.Sprintf("!%s %s [%s] %s", dash(num("iid")), truncate(str("title"), 100), dash(str("state")), dash(str("web_url")))
	case "users":
		return fmt.Sprintf("%s (%s)", dash(str("username")), dash(str("name")))
	case "projects":
		return fmt.Sprintf("%s — %s", dash(str("path_with_namespace")), truncate(str("description"), 120))
	case "notes":
		return truncate(str("body"), maxMessageLen)
	case "milestones":
		return fmt.Sprintf("%s [%s] due %s", dash(str("title")), dash(str("state")), dash(str("due_date")))
	default:
		for _, k := range []string{"title", "name", "path", "body"} {
			if v := str(k); v != "" {
				return truncate(v, maxMessageLen)
			}
		}
		return "(unrecognised result shape)"
	}
}

// --- issues & merge requests ------------------------------------------------

type issuesListParam struct {
	instanceParam
	limitParam
	Project string `json:"project,omitempty" jsonschema:"list issues of this project"`
	Group   string `json:"group,omitempty" jsonschema:"list issues of this group instead of a single project"`
	State   string `json:"state,omitempty" jsonschema:"opened, closed or all (default all)"`
	Labels  string `json:"labels,omitempty" jsonschema:"comma-separated label names that must all be present"`
	Search  string `json:"search,omitempty" jsonschema:"only issues whose title or description matches this term"`
}

func (s *Server) issuesList(ctx context.Context, _ *mcp.CallToolRequest, in issuesListParam) (*mcp.CallToolResult, any, error) {
	inst, err := s.resolveInstance(in.Instance)
	if err != nil {
		return errorResult(err), nil, nil
	}
	o := gitlab.IssuesOpts{State: in.State, Labels: in.Labels, Search: in.Search, Limit: in.resolvedLimit()}
	scope := "all visible projects"
	switch {
	case in.Project != "":
		p, perr := s.resolveProject(ctx, inst, in.Project)
		if perr != nil {
			return errorResult(perr), nil, nil
		}
		o.Project = p.PathWithNamespace
		scope = p.PathWithNamespace
	case in.Group != "":
		if gerr := inst.Guard.AllowNamespace(in.Group); gerr != nil {
			return errorResult(gerr), nil, nil
		}
		o.Group = in.Group
		scope = "group " + in.Group
	default:
		if len(inst.Guard.AllowedNamespaces()) > 0 {
			return errorResult(fmt.Errorf("instance %q is bounded to the namespaces [%s]; pass `project` or `group`",
				inst.Name, strings.Join(inst.Guard.AllowedNamespaces(), ", "))), nil, nil
		}
	}
	is, truncated, err := inst.Client.Issues(ctx, o)
	if err != nil {
		return errorResult(err), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d issue(s) in %s:\n", len(is), scope)
	for _, i := range is {
		fmt.Fprintf(&b, "- #%d [%s] %s", i.IID, dash(i.State), truncate(i.Title, 120))
		if len(i.Labels) > 0 {
			fmt.Fprintf(&b, " labels=%s", strings.Join(i.Labels, ","))
		}
		fmt.Fprintf(&b, " updated=%s\n", age(i.UpdatedAt))
	}
	b.WriteString(truncationNotice(truncated, "Narrow with `state`, `labels` or `search`."))
	return textResult(b.String()), nil, nil
}

type issueGetParam struct {
	projectParam
	IID          int  `json:"iid" jsonschema:"the project-scoped issue number (iid), not the global id"`
	IncludeNotes bool `json:"includeNotes,omitempty" jsonschema:"also return the issue's comments"`
	limitParam
}

func (s *Server) issueGet(ctx context.Context, _ *mcp.CallToolRequest, in issueGetParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	i, err := inst.Client.Issue(ctx, p.PathWithNamespace, in.IID)
	if err != nil {
		return errorResult(err), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Issue #%d in %s: %s\n", i.IID, p.PathWithNamespace, i.Title)
	fmt.Fprintf(&b, "  state:   %s\n", dash(i.State))
	fmt.Fprintf(&b, "  author:  %s\n", dash(i.Author.Username))
	if len(i.Assignees) > 0 {
		names := make([]string, 0, len(i.Assignees))
		for _, a := range i.Assignees {
			names = append(names, a.Username)
		}
		fmt.Fprintf(&b, "  assigned: %s\n", strings.Join(names, ", "))
	}
	if len(i.Labels) > 0 {
		fmt.Fprintf(&b, "  labels:  %s\n", strings.Join(i.Labels, ", "))
	}
	fmt.Fprintf(&b, "  created: %s (%s)\n", dash(i.CreatedAt), age(i.CreatedAt))
	if i.WebURL != "" {
		fmt.Fprintf(&b, "  url:     %s\n", i.WebURL)
	}
	if i.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", truncate(i.Description, 2000))
	}
	if in.IncludeNotes {
		notes, truncated, nerr := inst.Client.IssueNotes(ctx, p.PathWithNamespace, in.IID, in.resolvedLimit())
		if nerr != nil {
			return errorResult(nerr), nil, nil
		}
		b.WriteString("\n")
		b.WriteString(notesTable(notes, truncated))
	}
	return textResult(b.String()), nil, nil
}

type mrsListParam struct {
	instanceParam
	limitParam
	Project      string `json:"project,omitempty" jsonschema:"list merge requests of this project"`
	Group        string `json:"group,omitempty" jsonschema:"list merge requests of this group instead of a single project"`
	State        string `json:"state,omitempty" jsonschema:"opened, closed, merged, locked or all (default all)"`
	TargetBranch string `json:"targetBranch,omitempty" jsonschema:"only merge requests targeting this branch"`
	SourceBranch string `json:"sourceBranch,omitempty" jsonschema:"only merge requests from this branch"`
	Search       string `json:"search,omitempty" jsonschema:"only merge requests whose title or description matches this term"`
}

func (s *Server) mrsList(ctx context.Context, _ *mcp.CallToolRequest, in mrsListParam) (*mcp.CallToolResult, any, error) {
	inst, err := s.resolveInstance(in.Instance)
	if err != nil {
		return errorResult(err), nil, nil
	}
	o := gitlab.MergeRequestsOpts{
		State: in.State, TargetBranch: in.TargetBranch, SourceBranch: in.SourceBranch,
		Search: in.Search, Limit: in.resolvedLimit(),
	}
	scope := "all visible projects"
	switch {
	case in.Project != "":
		p, perr := s.resolveProject(ctx, inst, in.Project)
		if perr != nil {
			return errorResult(perr), nil, nil
		}
		o.Project = p.PathWithNamespace
		scope = p.PathWithNamespace
	case in.Group != "":
		if gerr := inst.Guard.AllowNamespace(in.Group); gerr != nil {
			return errorResult(gerr), nil, nil
		}
		o.Group = in.Group
		scope = "group " + in.Group
	default:
		if len(inst.Guard.AllowedNamespaces()) > 0 {
			return errorResult(fmt.Errorf("instance %q is bounded to the namespaces [%s]; pass `project` or `group`",
				inst.Name, strings.Join(inst.Guard.AllowedNamespaces(), ", "))), nil, nil
		}
	}
	mrs, truncated, err := inst.Client.MergeRequests(ctx, o)
	if err != nil {
		return errorResult(err), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d merge request(s) in %s:\n", len(mrs), scope)
	for _, mr := range mrs {
		fmt.Fprintf(&b, "- !%d [%s] %s", mr.IID, dash(mr.State), truncate(mr.Title, 110))
		if mr.Draft {
			b.WriteString(" DRAFT")
		}
		fmt.Fprintf(&b, " %s→%s", dash(mr.SourceBranch), dash(mr.TargetBranch))
		if mr.Pipeline != nil {
			fmt.Fprintf(&b, " pipeline=%s", mr.Pipeline.Status)
		}
		fmt.Fprintf(&b, " updated=%s\n", age(mr.UpdatedAt))
	}
	b.WriteString(truncationNotice(truncated, "Narrow with `state`, `targetBranch` or `search`."))
	return textResult(b.String()), nil, nil
}

type mrGetParam struct {
	projectParam
	IID          int  `json:"iid" jsonschema:"the project-scoped merge request number (iid)"`
	IncludeNotes bool `json:"includeNotes,omitempty" jsonschema:"also return the merge request's comments"`
	limitParam
}

func (s *Server) mrGet(ctx context.Context, _ *mcp.CallToolRequest, in mrGetParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	mr, err := inst.Client.MergeRequest(ctx, p.PathWithNamespace, in.IID)
	if err != nil {
		return errorResult(err), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Merge request !%d in %s: %s\n", mr.IID, p.PathWithNamespace, mr.Title)
	fmt.Fprintf(&b, "  state:   %s", dash(mr.State))
	if mr.Draft {
		b.WriteString(" (draft)")
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "  branches: %s → %s\n", dash(mr.SourceBranch), dash(mr.TargetBranch))
	fmt.Fprintf(&b, "  author:  %s\n", dash(mr.Author.Username))
	fmt.Fprintf(&b, "  merge:   status=%s conflicts=%t\n", dash(mr.MergeStatus), mr.HasConflicts)
	if mr.Pipeline != nil {
		fmt.Fprintf(&b, "  pipeline: #%d %s\n", mr.Pipeline.ID, mr.Pipeline.Status)
	}
	fmt.Fprintf(&b, "  updated: %s (%s)\n", dash(mr.UpdatedAt), age(mr.UpdatedAt))
	if mr.WebURL != "" {
		fmt.Fprintf(&b, "  url:     %s\n", mr.WebURL)
	}
	if mr.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", truncate(mr.Description, 2000))
	}
	if in.IncludeNotes {
		notes, truncated, nerr := inst.Client.MergeRequestNotes(ctx, p.PathWithNamespace, in.IID, in.resolvedLimit())
		if nerr != nil {
			return errorResult(nerr), nil, nil
		}
		b.WriteString("\n")
		b.WriteString(notesTable(notes, truncated))
	}
	return textResult(b.String()), nil, nil
}

type mrDiffParam struct {
	projectParam
	limitParam
	IID int `json:"iid" jsonschema:"the project-scoped merge request number (iid)"`
}

func (s *Server) mrDiff(ctx context.Context, _ *mcp.CallToolRequest, in mrDiffParam) (*mcp.CallToolResult, any, error) {
	inst, p, res := s.project(ctx, in.Instance, in.Project)
	if res != nil {
		return res, nil, nil
	}
	diffs, truncated, err := inst.Client.MergeRequestDiffs(ctx, p.PathWithNamespace, in.IID, in.resolvedLimit())
	if err != nil {
		return errorResult(err), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Merge request !%d in %s\n", in.IID, p.PathWithNamespace)
	b.WriteString(diffsTable(diffs, truncated))
	return textResult(b.String()), nil, nil
}

// --- members ----------------------------------------------------------------

type membersListParam struct {
	instanceParam
	limitParam
	Project string `json:"project,omitempty" jsonschema:"list the members of this project, including inherited members"`
	Group   string `json:"group,omitempty" jsonschema:"list the members of this group instead of a project"`
}

func (s *Server) membersList(ctx context.Context, _ *mcp.CallToolRequest, in membersListParam) (*mcp.CallToolResult, any, error) {
	inst, err := s.resolveInstance(in.Instance)
	if err != nil {
		return errorResult(err), nil, nil
	}
	var project, scope string
	switch {
	case in.Project != "":
		p, perr := s.resolveProject(ctx, inst, in.Project)
		if perr != nil {
			return errorResult(perr), nil, nil
		}
		project = p.PathWithNamespace
		scope = project
	case in.Group != "":
		if gerr := inst.Guard.AllowNamespace(in.Group); gerr != nil {
			return errorResult(gerr), nil, nil
		}
		scope = "group " + in.Group
	default:
		return errorResult(fmt.Errorf("pass either `project` or `group`")), nil, nil
	}

	ms, truncated, err := inst.Client.Members(ctx, project, in.Group, in.resolvedLimit())
	if err != nil {
		return errorResult(err), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d member(s) of %s:\n", len(ms), scope)
	for _, mm := range ms {
		fmt.Fprintf(&b, "- %s (%s) %s", dash(mm.Username), dash(mm.Name), gitlab.AccessLevelName(mm.AccessLevel))
		if mm.ExpiresAt != "" {
			fmt.Fprintf(&b, " expires=%s", mm.ExpiresAt)
		}
		b.WriteString("\n")
	}
	b.WriteString(truncationNotice(truncated, "Raise `limit` to see more."))
	return textResult(b.String()), nil, nil
}

// --- shared helpers ---------------------------------------------------------

// project resolves the instance and the project in one step; on failure it
// returns the tool-error result the caller should hand back.
func (s *Server) project(ctx context.Context, instance, ref string) (*instances.Instance, *gitlab.Project, *mcp.CallToolResult) {
	inst, err := s.resolveInstance(instance)
	if err != nil {
		return nil, nil, errorResult(err)
	}
	p, err := s.resolveProject(ctx, inst, ref)
	if err != nil {
		return nil, nil, errorResult(err)
	}
	return inst, p, nil
}

func diffsTable(diffs []gitlab.Diff, truncated bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d changed file(s):\n", len(diffs))
	for _, d := range diffs {
		flag := "M"
		switch {
		case d.NewFile:
			flag = "A"
		case d.DeletedFile:
			flag = "D"
		case d.RenamedFile:
			flag = "R"
		}
		path := d.NewPath
		if d.RenamedFile {
			path = d.OldPath + " → " + d.NewPath
		}
		fmt.Fprintf(&b, "- [%s] %s\n", flag, path)
	}
	b.WriteString(truncationNotice(truncated, "Raise `limit` to see more files."))
	return b.String()
}

func notesTable(notes []gitlab.Note, truncated bool) string {
	var b strings.Builder
	shown := 0
	for _, n := range notes {
		if n.System {
			continue // skip "changed title", "added label" noise
		}
		shown++
		fmt.Fprintf(&b, "- %s (%s): %s\n", dash(n.Author.Username), age(n.CreatedAt), truncate(n.Body, 500))
	}
	head := fmt.Sprintf("%d comment(s):\n", shown)
	out := head + b.String()
	return out + truncationNotice(truncated, "Raise `limit` to see more comments.")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

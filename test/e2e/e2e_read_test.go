package e2e

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestToolSurface(t *testing.T) {
	f := newFakeGitLab(t)
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	got := toolNames(t, s)

	// The advertised surface is a contract with the model; pin it.
	want := []string{
		"instances_list", "project_resolve",
		"groups_list", "group_get", "projects_list", "project_get",
		"repo_tree", "repo_file_read", "repo_branches", "repo_tags",
		"repo_commits", "repo_commit_get", "repo_compare",
		"search", "issues_list", "issue_get", "mrs_list", "mr_get", "mr_diff", "members_list",
		"pipelines_list", "pipeline_get", "pipeline_jobs", "job_get", "job_log",
		"pipeline_test_report", "pipeline_schedules_list", "pipeline_wait", "ci_lint",
		"pipeline_create", "pipeline_retry", "pipeline_cancel", "pipeline_update",
		"pipeline_delete", "job_retry", "job_cancel", "job_play", "pipeline_schedule_play",
	}
	for _, n := range want {
		if !got[n] {
			t.Errorf("tool %q is missing", n)
		}
	}
	if len(got) != len(want) {
		t.Errorf("tool count = %d, want %d; advertised: %v", len(got), len(want), got)
	}

	// Surfaces this server deliberately does not offer. A future contributor
	// adding one of these should have to delete an assertion first.
	for _, n := range []string{
		"project_create", "project_update", "project_delete", "project_restore",
		"scan_local_secrets", "scan_local_tree", "scan_ci_findings", "scan_local_dependencies",
		"job_artifact_read", "variables_list", "issue_create", "mr_create", "repo_file_write",
	} {
		if got[n] {
			t.Errorf("tool %q must not exist: this server is read-only except for pipelines", n)
		}
	}
}

func TestInstancesList(t *testing.T) {
	f := newFakeGitLab(t)
	ts := startServer(t, f, instanceOpts{allowedNamespaces: []string{"example-group"}})
	s := ts.connect(t)

	out := mustCall(t, s, "instances_list", nil)

	for _, want := range []string{
		"gl", "(default)", "GitLab 17.8.1-ee",
		"read.core", "read.ci", "pipelines.operate",
		"pipelines.delete",     // listed as disabled
		"example-group",        // the namespace bound
		"gitlab-mcp",           // token name
		"CI variables may NOT", // the opt-in state
		"Pipelines are the only write surface",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("instances_list output missing %q:\n%s", want, out)
		}
	}
}

func TestInstancesListReportsUnreachable(t *testing.T) {
	f := newFakeGitLab(t)
	f.route(http.MethodGet, "/version", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"500 Internal Server Error"}`))
	})
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	out := mustCall(t, s, "instances_list", nil)
	if !strings.Contains(out, "UNREACHABLE") {
		t.Fatalf("expected an UNREACHABLE marker:\n%s", out)
	}
}

func TestInstancesListWarnsOnExpiringToken(t *testing.T) {
	f := newFakeGitLab(t)
	f.json(http.MethodGet, "/personal_access_tokens/self",
		`{"name":"gitlab-mcp","scopes":["api"],"expires_at":"2020-01-01","active":true}`)
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	out := mustCall(t, s, "instances_list", nil)
	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "expired") {
		t.Fatalf("an expired token should be flagged:\n%s", out)
	}
}

func TestProjectResolveAcceptsRemoteURLs(t *testing.T) {
	f := newFakeGitLab(t)
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	for _, ref := range []string{
		projectPath,
		"git@gitlab.example.com:" + projectPath + ".git",
		"https://gitlab.example.com/" + projectPath + ".git",
		"ssh://git@gitlab.example.com/" + projectPath,
	} {
		t.Run(ref, func(t *testing.T) {
			out := mustCall(t, s, "project_resolve", map[string]any{"project": ref})
			if !strings.Contains(out, projectPath) || !strings.Contains(out, "id:             42") {
				t.Fatalf("unexpected output for %q:\n%s", ref, out)
			}
			if !strings.Contains(out, "main") {
				t.Fatalf("default branch missing:\n%s", out)
			}
		})
	}
}

func TestRepoFileReadRejectsBinary(t *testing.T) {
	f := newFakeGitLab(t)
	f.route(http.MethodGet, "/projects/"+projectEsc+"/repository/files/logo.png/raw",
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0x01})
		})
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	out := mustFail(t, s, "repo_file_read", map[string]any{"project": projectPath, "path": "logo.png"})
	if !strings.Contains(out, "binary") {
		t.Fatalf("expected a binary-file refusal:\n%s", out)
	}
}

func TestRepoFileReadTruncates(t *testing.T) {
	f := newFakeGitLab(t)
	f.route(http.MethodGet, "/projects/"+projectEsc+"/repository/files/big.txt/raw",
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
		})
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	out := mustCall(t, s, "repo_file_read", map[string]any{
		"project": projectPath, "path": "big.txt", "maxBytes": 100,
	})
	if !strings.Contains(out, "truncated at 100 bytes") {
		t.Fatalf("expected a truncation notice:\n%s", out[:min(400, len(out))])
	}
}

func TestPipelinesListRendersHistogramAndTruncation(t *testing.T) {
	f := newFakeGitLab(t)
	// Two pages available, cap is 3, limit forces a single page → truncated.
	f.route(http.MethodGet, "/projects/"+projectEsc+"/pipelines", func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 0 {
			page = 1
		}
		w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
		_, _ = fmt.Fprintf(w, `[
		  {"id":%d,"status":"failed","ref":"main","sha":"deadbeefcafe","source":"push","duration":12,"created_at":"2026-01-01T00:00:00Z","web_url":"https://gitlab.example.com/p/%d"},
		  {"id":%d,"status":"success","ref":"main","sha":"cafedeadbeef","source":"schedule","duration":34,"created_at":"2026-01-01T00:00:00Z"}
		]`, page*10, page*10, page*10+1)
	})
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	out := mustCall(t, s, "pipelines_list", map[string]any{"project": projectPath, "limit": 2})
	if !strings.Contains(out, "failed=1") || !strings.Contains(out, "success=1") {
		t.Errorf("status histogram missing:\n%s", out)
	}
	if !strings.Contains(out, "results truncated") {
		t.Errorf("truncation notice missing — a partial list must say so:\n%s", out)
	}
	if !strings.Contains(out, "deadbeef") {
		t.Errorf("short SHA missing:\n%s", out)
	}
}

func TestJobLogRedactsAndTails(t *testing.T) {
	f := newFakeGitLab(t)
	var lines []string
	for i := 1; i <= 50; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	lines = append(lines, "TOKEN=glpat-AAAABBBBCCCCDDDDEEEE")
	f.route(http.MethodGet, "/projects/"+projectEsc+"/jobs/7/trace", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Join(lines, "\n")))
	})
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	out := mustCall(t, s, "job_log", map[string]any{"project": projectPath, "id": 7, "tailLines": 5})
	if strings.Contains(out, "glpat-AAAABBBBCCCCDDDDEEEE") {
		t.Fatalf("job log leaked a token:\n%s", out)
	}
	if !strings.Contains(out, "showing last 5 of 51") {
		t.Fatalf("tail accounting missing:\n%s", out)
	}
	if strings.Contains(out, "line 1\n") && strings.Contains(out, "line 2\n") {
		t.Fatalf("tail did not actually trim the head:\n%s", out)
	}
}

// A pipeline schedule response embeds a variables array. The tool must render
// the schedule without it.
func TestPipelineSchedulesRedactVariables(t *testing.T) {
	f := newFakeGitLab(t)
	f.json(http.MethodGet, "/projects/"+projectEsc+"/pipeline_schedules", `[{
	  "id": 13, "description": "nightly", "ref": "main", "cron": "0 2 * * *",
	  "cron_timezone": "UTC", "next_run_at": "2026-01-02T02:00:00Z", "active": true,
	  "owner": {"username": "bot"},
	  "variables": [{"key":"DEPLOY_TOKEN","value":"glpat-AAAABBBBCCCCDDDDEEEE"}]
	}]`)
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	out := mustCall(t, s, "pipeline_schedules_list", map[string]any{"project": projectPath})
	if strings.Contains(out, "glpat-AAAABBBBCCCCDDDDEEEE") || strings.Contains(out, "DEPLOY_TOKEN") {
		t.Fatalf("schedule variables leaked:\n%s", out)
	}
	if !strings.Contains(out, "nightly") || !strings.Contains(out, "0 2 * * *") {
		t.Fatalf("schedule details missing:\n%s", out)
	}
	if !strings.Contains(out, "pipeline secrets") {
		t.Fatalf("expected a note that variables are withheld:\n%s", out)
	}
}

func TestGitLabErrorsBecomeToolErrors(t *testing.T) {
	f := newFakeGitLab(t)
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	// An unrouted project 404s with a GitLab-shaped body.
	out := mustFail(t, s, "project_get", map[string]any{"project": "example-group/missing"})
	if !strings.Contains(out, "404") {
		t.Fatalf("GitLab's status should reach the model:\n%s", out)
	}
}

func TestUnknownInstanceListsValidNames(t *testing.T) {
	f := newFakeGitLab(t)
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	out := mustFail(t, s, "project_get", map[string]any{"instance": "nope", "project": projectPath})
	if !strings.Contains(out, `unknown GitLab instance "nope"`) || !strings.Contains(out, "gl") {
		t.Fatalf("the error should list the configured instances:\n%s", out)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

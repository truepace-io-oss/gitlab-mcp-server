package e2e

import (
	"net/http"
	"strings"
	"testing"

	"github.com/truepace-io-oss/gitlab-mcp-server/internal/config"
)

// The assertions in this file are the server's security contract. Each one
// should fail loudly if a future change weakens a guard.

// A disabled capability must produce a tool error naming it — not a missing
// tool, and not a request to GitLab.
func TestCapabilityDenialIsExplicitAndSilent(t *testing.T) {
	f := newFakeGitLab(t)
	ts := startServer(t, f, instanceOpts{perms: config.Permissions{
		Read: config.ReadPerms{Core: true}, // read.ci and pipelines.* are off
	}})
	s := ts.connect(t)

	// The tools still exist…
	tools := toolNames(t, s)
	for _, n := range []string{"pipelines_list", "pipeline_retry", "ci_lint"} {
		if !tools[n] {
			t.Fatalf("tool %q should still be advertised even when its capability is off", n)
		}
	}

	cases := map[string]map[string]any{
		"pipelines_list": {"project": projectPath},
		"pipeline_get":   {"project": projectPath, "id": 1},
		"job_log":        {"project": projectPath, "id": 1},
		"ci_lint":        {"project": projectPath, "content": "stages: [build]"},
		"pipeline_retry": {"project": projectPath, "id": 1},
	}
	for tool, args := range cases {
		t.Run(tool, func(t *testing.T) {
			out := mustFail(t, s, tool, args)
			if !strings.Contains(out, "is not enabled for GitLab instance") {
				t.Fatalf("expected a capability denial, got:\n%s", out)
			}
		})
	}

	// …and nothing CI-related was ever requested.
	f.assertNeverRequested(t, "/pipelines", "/jobs/", "/ci/lint")
}

// The deny list is unconditional. Even with every capability enabled, no tool
// path can reach a secret-bearing endpoint.
func TestNoRequestEverReachesDenyListedPaths(t *testing.T) {
	f := newFakeGitLab(t)
	ts := startServer(t, f, instanceOpts{perms: config.Permissions{
		Read:      config.ReadPerms{Core: true, CI: true},
		Pipelines: config.PipelinePerms{Operate: true, Delete: true, AllowVariables: true},
	}})
	s := ts.connect(t)

	// Exercise a broad sweep of tools so many code paths run.
	f.json(http.MethodGet, "/projects/"+projectEsc+"/pipelines", `[]`)
	f.json(http.MethodGet, "/projects/"+projectEsc+"/pipeline_schedules", `[]`)
	f.json(http.MethodGet, "/projects/"+projectEsc+"/repository/branches", `[]`)
	f.json(http.MethodGet, "/projects/"+projectEsc+"/merge_requests", `[]`)
	f.json(http.MethodGet, "/projects/"+projectEsc+"/issues", `[]`)
	f.json(http.MethodGet, "/projects/"+projectEsc+"/members/all", `[]`)
	f.json(http.MethodGet, "/projects/"+projectEsc+"/repository/tree", `[]`)

	for tool, args := range map[string]map[string]any{
		"instances_list":          nil,
		"project_get":             {"project": projectPath},
		"pipelines_list":          {"project": projectPath},
		"pipeline_schedules_list": {"project": projectPath},
		"repo_branches":           {"project": projectPath},
		"mrs_list":                {"project": projectPath},
		"issues_list":             {"project": projectPath},
		"members_list":            {"project": projectPath},
		"repo_tree":               {"project": projectPath},
	} {
		callTool(t, s, tool, args) // outcome is irrelevant here
	}

	f.assertNeverRequested(t,
		"/variables",
		"/secure_files",
		"/access_tokens",
		"/deploy_tokens",
		"/deploy_keys",
		"/hooks",
		"/integrations",
		"/services",
		"/artifacts",
		"reset_registration_token",
		"reset_authentication_token",
		"/erase",
	)

	// The one personal_access_tokens path that IS allowed must be the self one.
	for _, r := range f.recorded() {
		if strings.Contains(r.Path, "personal_access_tokens") && r.Path != "/personal_access_tokens/self" {
			t.Errorf("unexpected token endpoint request: %s %s", r.Method, r.Path)
		}
	}
}

// Both read-only kill switches must block writes while leaving reads working.
func TestReadOnlyKillSwitches(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts instanceOpts
		want string
	}{
		{"global", instanceOpts{globalReadOnly: true}, "writes disabled globally"},
		{"per instance", instanceOpts{readOnly: true}, `writes are disabled for GitLab instance "gl"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeGitLab(t)
			f.json(http.MethodGet, "/projects/"+projectEsc+"/pipelines", `[]`)
			ts := startServer(t, f, tc.opts)
			s := ts.connect(t)

			// Reads still work.
			mustCall(t, s, "pipelines_list", map[string]any{"project": projectPath})

			// Writes do not.
			out := mustFail(t, s, "pipeline_retry", map[string]any{"project": projectPath, "id": 1})
			if !strings.Contains(out, tc.want) {
				t.Fatalf("expected %q in:\n%s", tc.want, out)
			}
			f.assertNeverRequested(t, "/retry")
		})
	}
}

// The namespace guard bounds every project-addressing tool, including when the
// caller passes a numeric id rather than a path.
func TestNamespaceGuard(t *testing.T) {
	f := newFakeGitLab(t)
	// A project outside the allowlist that the token can nonetheless see.
	f.json(http.MethodGet, "/projects/other-group%2Fsecret", `{
	  "id": 99, "path_with_namespace": "other-group/secret", "default_branch": "main",
	  "namespace": {"full_path": "other-group", "kind": "group"}
	}`)
	f.json(http.MethodGet, "/projects/99", `{
	  "id": 99, "path_with_namespace": "other-group/secret", "default_branch": "main",
	  "namespace": {"full_path": "other-group", "kind": "group"}
	}`)
	ts := startServer(t, f, instanceOpts{allowedNamespaces: []string{"example-group"}})
	s := ts.connect(t)

	// In-bounds works.
	mustCall(t, s, "project_get", map[string]any{"project": projectPath})

	// Out of bounds is refused whether addressed by path or by numeric id. (The
	// schema types `project` as a string, so an id arrives as "99".)
	for _, ref := range []any{"other-group/secret", "99"} {
		out := mustFail(t, s, "project_get", map[string]any{"project": ref})
		if !strings.Contains(out, "outside the allowed namespaces") {
			t.Fatalf("expected a namespace denial for %v:\n%s", ref, out)
		}
		if !strings.Contains(out, "example-group") {
			t.Fatalf("the denial should name the allowed namespaces:\n%s", out)
		}
	}
}

// A project listing must not advertise projects the other tools would refuse.
func TestProjectsListFiltersOutOfBoundsProjects(t *testing.T) {
	f := newFakeGitLab(t)
	f.json(http.MethodGet, "/projects", `[
	  {"id":42,"path_with_namespace":"example-group/app","default_branch":"main"},
	  {"id":99,"path_with_namespace":"other-group/secret","default_branch":"main"}
	]`)
	ts := startServer(t, f, instanceOpts{allowedNamespaces: []string{"example-group"}})
	s := ts.connect(t)

	out := mustCall(t, s, "projects_list", nil)
	if !strings.Contains(out, "example-group/app") {
		t.Fatalf("in-bounds project missing:\n%s", out)
	}
	if strings.Contains(out, "other-group/secret") {
		t.Fatalf("listing leaked an out-of-bounds project:\n%s", out)
	}
}

// An unscoped search would reach every namespace the token can see, so it is
// refused when the instance is bounded.
func TestUnscopedSearchRefusedWhenBounded(t *testing.T) {
	f := newFakeGitLab(t)
	ts := startServer(t, f, instanceOpts{allowedNamespaces: []string{"example-group"}})
	s := ts.connect(t)

	out := mustFail(t, s, "search", map[string]any{"scope": "blobs", "term": "password"})
	if !strings.Contains(out, "pass `group` or `project` to scope the search") {
		t.Fatalf("expected a scoping requirement:\n%s", out)
	}
	f.assertNeverRequested(t, "/search")
}

func TestScopedSearchWorks(t *testing.T) {
	f := newFakeGitLab(t)
	f.json(http.MethodGet, "/projects/"+projectEsc+"/search",
		`[{"path":"src/main.go","startline":12,"ref":"main","data":"func main() {"}]`)
	ts := startServer(t, f, instanceOpts{allowedNamespaces: []string{"example-group"}})
	s := ts.connect(t)

	out := mustCall(t, s, "search", map[string]any{
		"scope": "blobs", "term": "func main", "project": projectPath,
	})
	if !strings.Contains(out, "src/main.go") {
		t.Fatalf("search hit missing:\n%s", out)
	}
}

// A plain unbounded instance may search instance-wide; only bounded ones are
// forced to scope.
func TestUnscopedSearchAllowedWhenUnbounded(t *testing.T) {
	f := newFakeGitLab(t)
	f.json(http.MethodGet, "/search", `[{"path":"a.go","startline":1,"ref":"main","data":"x"}]`)
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	out := mustCall(t, s, "search", map[string]any{"scope": "blobs", "term": "x"})
	if !strings.Contains(out, "a.go") {
		t.Fatalf("search hit missing:\n%s", out)
	}
}

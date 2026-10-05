package gitlab

import (
	"net/http"
	"testing"
)

// The deny list is the only guard that cannot be switched off by configuration,
// so it gets the most explicit table in the repo. Every entry here is a promise
// made in the README and in docs/permissions.md.
func TestCheckDenyList(t *testing.T) {
	denied := []string{
		"/projects/42/variables",
		"/projects/42/variables/MY_SECRET",
		"/projects/group%2Fproject/variables",
		"/groups/7/variables",
		"/admin/ci/variables",
		"/projects/42/pipelines/9/variables",
		"/projects/42/secure_files",
		"/projects/42/secure_files/3/download",
		"/projects/42/access_tokens",
		"/groups/7/access_tokens",
		"/personal_access_tokens",
		"/personal_access_tokens/123",
		"/users/5/personal_access_tokens",
		"/projects/42/deploy_tokens",
		"/groups/7/deploy_tokens",
		"/deploy_tokens",
		"/projects/42/deploy_keys",
		"/deploy_keys",
		"/projects/42/hooks",
		"/groups/7/hooks",
		"/hooks",
		"/projects/42/integrations/slack",
		"/projects/42/services/jira",
		"/projects/42/cluster_agents/1/tokens",
		"/runners/3/reset_authentication_token",
		"/projects/42/runners/reset_registration_token",
		"/groups/7/runners/reset_registration_token",
		"/projects/42/jobs/9/artifacts",
		"/projects/42/jobs/9/artifacts/report.json",
		"/projects/42/jobs/artifacts/main/raw/report.json",
		"/projects/42/artifacts",
		"/projects/42/jobs/9/erase",
		// The /api/v4 prefix and a query string must not smuggle anything past.
		"/api/v4/projects/42/variables",
		"/projects/42/variables?per_page=100",
		"/projects/42/variables/",
	}
	for _, p := range denied {
		t.Run("deny "+p, func(t *testing.T) {
			if err := CheckDenyList(http.MethodGet, p); err == nil {
				t.Fatalf("expected %q to be denied", p)
			} else if !IsDenied(err) {
				t.Fatalf("expected a deniedError for %q, got %T", p, err)
			}
		})
	}

	allowed := []string{
		"/version",
		"/personal_access_tokens/self", // the documented exception
		"/projects",
		"/projects/42",
		"/projects/42/pipelines",
		"/projects/42/pipelines/9",
		"/projects/42/pipelines/9/jobs",
		"/projects/42/pipelines/9/retry",
		"/projects/42/pipelines/9/cancel",
		"/projects/42/pipelines/9/metadata",
		"/projects/42/pipelines/9/test_report_summary",
		"/projects/42/pipeline",
		"/projects/42/pipeline_schedules",
		"/projects/42/pipeline_schedules/3/play",
		"/projects/42/jobs/9",
		"/projects/42/jobs/9/trace",
		"/projects/42/jobs/9/retry",
		"/projects/42/jobs/9/cancel",
		"/projects/42/jobs/9/play",
		"/projects/42/ci/lint",
		"/projects/42/repository/tree",
		"/projects/42/repository/files/README.md/raw",
		"/projects/42/repository/branches",
		"/projects/42/repository/commits",
		"/projects/42/repository/compare",
		"/projects/42/merge_requests/1/diffs",
		"/projects/42/issues/1/notes",
		"/projects/42/members/all",
		"/groups/7/members",
		"/search",
	}
	for _, p := range allowed {
		t.Run("allow "+p, func(t *testing.T) {
			if err := CheckDenyList(http.MethodGet, p); err != nil {
				t.Fatalf("expected %q to be allowed, got %v", p, err)
			}
		})
	}
}

// The self-token exception must win over the personal_access_tokens deny rule,
// and must not accidentally open the rest of that namespace.
func TestSelfTokenExceptionIsNarrow(t *testing.T) {
	if err := CheckDenyList(http.MethodGet, "/personal_access_tokens/self"); err != nil {
		t.Fatalf("self token must be readable: %v", err)
	}
	for _, p := range []string{"/personal_access_tokens", "/personal_access_tokens/7", "/personal_access_tokens/self/extra"} {
		if err := CheckDenyList(http.MethodGet, p); err == nil {
			t.Fatalf("expected %q to stay denied", p)
		}
	}
}

// Denial is method-agnostic: reading a secret is as unacceptable as writing one.
func TestDenyListIgnoresMethod(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if err := CheckDenyList(m, "/projects/42/variables"); err == nil {
			t.Fatalf("expected %s on variables to be denied", m)
		}
	}
}

func TestNormalizePath(t *testing.T) {
	cases := map[string]string{
		"/api/v4/projects/1":      "/projects/1",
		"projects/1":              "/projects/1",
		"/projects/1?per_page=10": "/projects/1",
		"/projects/1/":            "/projects/1",
		"/":                       "/",
	}
	for in, want := range cases {
		if got := normalizePath(in); got != want {
			t.Errorf("normalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

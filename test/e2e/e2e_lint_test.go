package e2e

import (
	"net/http"
	"strings"
	"testing"
)

const validCI = "stages: [build]\nbuild:\n  stage: build\n  script: [\"true\"]\n"

func TestCILintValid(t *testing.T) {
	f := newFakeGitLab(t)
	f.route(http.MethodPost, "/projects/"+projectEsc+"/ci/lint", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
		  "valid": true, "errors": [], "warnings": [],
		  "includes": [{"type":"file","location":"/templates/build.yml","context_project":"example-group/ci-templates"}],
		  "jobs": [{"name":"build","stage":"build","when":"on_success"}],
		  "merged_yaml": "stages:\n- build\n"
		}`))
	})
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	out := mustCall(t, s, "ci_lint", map[string]any{
		"project": projectPath, "content": validCI, "includeJobs": true,
	})
	for _, want := range []string{"valid=true", "static check", "/templates/build.yml", "build: build"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// merged_yaml must be withheld unless explicitly requested.
	if strings.Contains(out, "Merged configuration") {
		t.Errorf("merged_yaml should not be returned unless asked for:\n%s", out)
	}

	req := assertRequested(t, f, http.MethodPost, "/projects/"+projectEsc+"/ci/lint")
	if !strings.Contains(req.Body, "stages") {
		t.Errorf("the content was not forwarded: %s", req.Body)
	}
}

func TestCILintInvalidReportsErrors(t *testing.T) {
	f := newFakeGitLab(t)
	f.json(http.MethodPost, "/projects/"+projectEsc+"/ci/lint", `{
	  "valid": false,
	  "errors": ["jobs:build script can't be blank", "jobs:build:needs unknown job"],
	  "warnings": ["jobs:build may allow failure"]
	}`)
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	// An invalid config is a successful tool call reporting valid=false — the
	// lint ran, the answer is "no".
	out := mustCall(t, s, "ci_lint", map[string]any{"project": projectPath, "content": "build:\n"})
	for _, want := range []string{"valid=false", "script can't be blank", "needs unknown job", "may allow failure"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestCILintDryRunIsForwarded(t *testing.T) {
	f := newFakeGitLab(t)
	f.json(http.MethodPost, "/projects/"+projectEsc+"/ci/lint", `{"valid":true}`)
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	out := mustCall(t, s, "ci_lint", map[string]any{
		"project": projectPath, "content": validCI, "dryRun": true, "ref": "main",
	})
	if !strings.Contains(out, "pipeline simulation") {
		t.Errorf("the mode should be reported:\n%s", out)
	}
	req := assertRequested(t, f, http.MethodPost, "/projects/"+projectEsc+"/ci/lint")
	if !strings.Contains(req.Body, `"dry_run":true`) {
		t.Errorf("dry_run was not forwarded: %s", req.Body)
	}
	if !strings.Contains(req.Body, `"ref":"main"`) {
		t.Errorf("ref was not forwarded: %s", req.Body)
	}
}

// merged_yaml can embed resolved values, so it is redacted on the way out.
func TestCILintRedactsMergedYAML(t *testing.T) {
	f := newFakeGitLab(t)
	f.json(http.MethodPost, "/projects/"+projectEsc+"/ci/lint",
		`{"valid":true,"merged_yaml":"script:\n- echo glpat-AAAABBBBCCCCDDDDEEEE\n"}`)
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	out := mustCall(t, s, "ci_lint", map[string]any{
		"project": projectPath, "content": validCI, "includeMergedYaml": true,
	})
	if !strings.Contains(out, "Merged configuration") {
		t.Fatalf("merged_yaml should be present when requested:\n%s", out)
	}
	if strings.Contains(out, "glpat-AAAABBBBCCCCDDDDEEEE") {
		t.Fatalf("merged_yaml leaked a token:\n%s", out)
	}
}

func TestCILintRequiresContent(t *testing.T) {
	f := newFakeGitLab(t)
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	out := mustFail(t, s, "ci_lint", map[string]any{"project": projectPath, "content": "   "})
	if !strings.Contains(out, "`content` is required") {
		t.Fatalf("unexpected output:\n%s", out)
	}
	f.assertNeverRequested(t, "/ci/lint")
}

// The size cap is the only limit on the one content-in tool, so it is enforced
// before anything is sent.
func TestCILintEnforcesSizeCap(t *testing.T) {
	f := newFakeGitLab(t)
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	out := mustFail(t, s, "ci_lint", map[string]any{
		"project": projectPath,
		"content": strings.Repeat("x", (1<<20)+1),
	})
	if !strings.Contains(out, "exceeds the") || !strings.Contains(out, "byte limit") {
		t.Fatalf("expected a size-cap refusal:\n%s", out)
	}
	f.assertNeverRequested(t, "/ci/lint")
}

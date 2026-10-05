package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/truepace-io-oss/gitlab-mcp-server/internal/config"
)

// pipelineJSON is a reusable pipeline response.
func pipelineJSON(id int, status string) string {
	return fmt.Sprintf(`{
	  "id": %d, "iid": 1, "project_id": 42, "status": %q, "source": "push",
	  "ref": "main", "sha": "deadbeefcafebabe", "web_url": "https://gitlab.example.com/p/%d",
	  "created_at": "2026-01-01T00:00:00Z", "duration": 42,
	  "user": {"username": "bot"}
	}`, id, status, id)
}

func jobJSON(id int, name, status string) string {
	return fmt.Sprintf(`{
	  "id": %d, "name": %q, "stage": "build", "status": %q,
	  "web_url": "https://gitlab.example.com/j/%d",
	  "pipeline": {"id": 100, "status": "running", "ref": "main"}
	}`, id, name, status, id)
}

func TestPipelineLifecycle(t *testing.T) {
	f := newFakeGitLab(t)
	f.json(http.MethodPost, "/projects/"+projectEsc+"/pipeline", pipelineJSON(100, "created"))
	f.json(http.MethodPost, "/projects/"+projectEsc+"/pipelines/100/cancel", pipelineJSON(100, "canceled"))
	f.json(http.MethodPost, "/projects/"+projectEsc+"/pipelines/100/retry", pipelineJSON(100, "running"))
	f.json(http.MethodPut, "/projects/"+projectEsc+"/pipelines/100/metadata", pipelineJSON(100, "running"))
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	t.Run("create", func(t *testing.T) {
		f.reset()
		out := mustCall(t, s, "pipeline_create", map[string]any{"project": projectPath, "ref": "main"})
		if !strings.Contains(out, "Pipeline created") || !strings.Contains(out, "#100") {
			t.Fatalf("unexpected output:\n%s", out)
		}
		assertRequested(t, f, http.MethodPost, "/projects/"+projectEsc+"/pipeline")
	})

	t.Run("cancel", func(t *testing.T) {
		f.reset()
		out := mustCall(t, s, "pipeline_cancel", map[string]any{"project": projectPath, "id": 100})
		if !strings.Contains(out, "Pipeline canceled") {
			t.Fatalf("unexpected output:\n%s", out)
		}
		assertRequested(t, f, http.MethodPost, "/projects/"+projectEsc+"/pipelines/100/cancel")
	})

	t.Run("retry explains its semantics", func(t *testing.T) {
		f.reset()
		out := mustCall(t, s, "pipeline_retry", map[string]any{"project": projectPath, "id": 100})
		// The distinction between retry and a fresh run is a common model error,
		// so the result states it.
		if !strings.Contains(out, "failed and canceled jobs only") {
			t.Fatalf("retry semantics should be stated:\n%s", out)
		}
		assertRequested(t, f, http.MethodPost, "/projects/"+projectEsc+"/pipelines/100/retry")
	})

	t.Run("update metadata", func(t *testing.T) {
		f.reset()
		out := mustCall(t, s, "pipeline_update", map[string]any{
			"project": projectPath, "id": 100, "name": "nightly rebuild",
		})
		if !strings.Contains(out, "Pipeline name updated") {
			t.Fatalf("unexpected output:\n%s", out)
		}
		req := assertRequested(t, f, http.MethodPut, "/projects/"+projectEsc+"/pipelines/100/metadata")
		if !strings.Contains(req.Body, "nightly rebuild") {
			t.Fatalf("the new name was not sent: %s", req.Body)
		}
	})
}

func TestPipelineCreateRequiresRef(t *testing.T) {
	f := newFakeGitLab(t)
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	// Omitting `ref` entirely is caught by the tool's JSON schema, before the
	// handler runs.
	out := mustFail(t, s, "pipeline_create", map[string]any{"project": projectPath})
	if !strings.Contains(out, "ref") {
		t.Fatalf("expected the schema to require ref:\n%s", out)
	}

	// An empty ref reaches the handler, which refuses it with its own message.
	out = mustFail(t, s, "pipeline_create", map[string]any{"project": projectPath, "ref": ""})
	if !strings.Contains(out, "`ref` is required") {
		t.Fatalf("unexpected output:\n%s", out)
	}
	f.assertNeverRequested(t, "/pipeline")
}

// pipeline_delete is off in the shipped profile, needs an echo confirmation, and
// is irreversible — all three are asserted here.
func TestPipelineDelete(t *testing.T) {
	t.Run("denied by default", func(t *testing.T) {
		f := newFakeGitLab(t)
		ts := startServer(t, f, instanceOpts{})
		s := ts.connect(t)

		out := mustFail(t, s, "pipeline_delete", map[string]any{
			"project": projectPath, "id": 100, "confirmPipelineId": 100,
		})
		if !strings.Contains(out, `capability "pipelines.delete" is not enabled`) {
			t.Fatalf("expected a capability denial:\n%s", out)
		}
		f.assertNeverRequested(t, "/pipelines/100")
	})

	t.Run("requires a matching confirmation", func(t *testing.T) {
		f := newFakeGitLab(t)
		ts := startServer(t, f, instanceOpts{perms: config.Permissions{
			Read:      config.ReadPerms{Core: true, CI: true},
			Pipelines: config.PipelinePerms{Operate: true, Delete: true},
		}})
		s := ts.connect(t)

		out := mustFail(t, s, "pipeline_delete", map[string]any{
			"project": projectPath, "id": 100, "confirmPipelineId": 99,
		})
		if !strings.Contains(out, "must repeat the same id") {
			t.Fatalf("expected a confirmation refusal:\n%s", out)
		}
		if !strings.Contains(out, "cannot be undone") {
			t.Fatalf("the refusal should say deletion is irreversible:\n%s", out)
		}
		f.assertNeverRequested(t, "/pipelines/100")
	})

	t.Run("succeeds when enabled and confirmed", func(t *testing.T) {
		f := newFakeGitLab(t)
		f.route(http.MethodDelete, "/projects/"+projectEsc+"/pipelines/100",
			func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
		ts := startServer(t, f, instanceOpts{perms: config.Permissions{
			Read:      config.ReadPerms{Core: true, CI: true},
			Pipelines: config.PipelinePerms{Operate: true, Delete: true},
		}})
		s := ts.connect(t)

		out := mustCall(t, s, "pipeline_delete", map[string]any{
			"project": projectPath, "id": 100, "confirmPipelineId": 100,
		})
		if !strings.Contains(out, "permanently deleted") || !strings.Contains(out, "cannot be undone") {
			t.Fatalf("unexpected output:\n%s", out)
		}
		assertRequested(t, f, http.MethodDelete, "/projects/"+projectEsc+"/pipelines/100")
	})

	t.Run("explains the Owner requirement on 403", func(t *testing.T) {
		f := newFakeGitLab(t)
		f.route(http.MethodDelete, "/projects/"+projectEsc+"/pipelines/100",
			func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"403 Forbidden"}`))
			})
		ts := startServer(t, f, instanceOpts{perms: config.Permissions{
			Read:      config.ReadPerms{Core: true, CI: true},
			Pipelines: config.PipelinePerms{Operate: true, Delete: true},
		}})
		s := ts.connect(t)

		out := mustFail(t, s, "pipeline_delete", map[string]any{
			"project": projectPath, "id": 100, "confirmPipelineId": 100,
		})
		if !strings.Contains(out, "Owner role") {
			t.Fatalf("a 403 should explain the Owner requirement:\n%s", out)
		}
	})
}

func TestJobOperations(t *testing.T) {
	f := newFakeGitLab(t)
	f.json(http.MethodPost, "/projects/"+projectEsc+"/jobs/7/retry", jobJSON(8, "build", "pending"))
	f.json(http.MethodPost, "/projects/"+projectEsc+"/jobs/7/cancel", jobJSON(7, "build", "canceled"))
	f.json(http.MethodPost, "/projects/"+projectEsc+"/jobs/7/play", jobJSON(7, "deploy", "pending"))
	f.route(http.MethodPost, "/projects/"+projectEsc+"/pipeline_schedules/13/play",
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) })
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	for _, tc := range []struct {
		tool, want, path string
		args             map[string]any
	}{
		{"job_retry", "Job retried", "/jobs/7/retry", map[string]any{"project": projectPath, "id": 7}},
		{"job_cancel", "Job canceled", "/jobs/7/cancel", map[string]any{"project": projectPath, "id": 7}},
		{"job_play", "Job started", "/jobs/7/play", map[string]any{"project": projectPath, "id": 7}},
		{"pipeline_schedule_play", "was queued", "/pipeline_schedules/13/play", map[string]any{"project": projectPath, "id": 13}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			f.reset()
			out := mustCall(t, s, tc.tool, tc.args)
			if !strings.Contains(out, tc.want) {
				t.Fatalf("unexpected output:\n%s", out)
			}
			found := false
			for _, r := range f.recorded() {
				if strings.Contains(r.Path, tc.path) {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected a request to %s; saw %v", tc.path, f.recorded())
			}
		})
	}
}

// CI variables inject behaviour into a run, so they are opt-in.
func TestCIVariablesAreOptIn(t *testing.T) {
	vars := []any{map[string]any{"key": "DEPLOY", "value": "true"}}

	t.Run("refused by default", func(t *testing.T) {
		f := newFakeGitLab(t)
		f.json(http.MethodPost, "/projects/"+projectEsc+"/pipeline", pipelineJSON(100, "created"))
		ts := startServer(t, f, instanceOpts{})
		s := ts.connect(t)

		out := mustFail(t, s, "pipeline_create", map[string]any{
			"project": projectPath, "ref": "main", "variables": vars,
		})
		if !strings.Contains(out, "supplying CI variables is disabled") {
			t.Fatalf("expected a variables refusal:\n%s", out)
		}
		f.assertNeverRequested(t, "/pipeline")
	})

	t.Run("accepted when enabled", func(t *testing.T) {
		f := newFakeGitLab(t)
		f.json(http.MethodPost, "/projects/"+projectEsc+"/pipeline", pipelineJSON(100, "created"))
		ts := startServer(t, f, instanceOpts{perms: config.Permissions{
			Read:      config.ReadPerms{Core: true, CI: true},
			Pipelines: config.PipelinePerms{Operate: true, AllowVariables: true},
		}})
		s := ts.connect(t)

		f.reset()
		mustCall(t, s, "pipeline_create", map[string]any{
			"project": projectPath, "ref": "main", "variables": vars,
		})
		req := assertRequested(t, f, http.MethodPost, "/projects/"+projectEsc+"/pipeline")
		if !strings.Contains(req.Body, "DEPLOY") {
			t.Fatalf("the variable was not forwarded: %s", req.Body)
		}
	})

	t.Run("job_play honours the same switch", func(t *testing.T) {
		f := newFakeGitLab(t)
		ts := startServer(t, f, instanceOpts{})
		s := ts.connect(t)

		out := mustFail(t, s, "job_play", map[string]any{
			"project": projectPath, "id": 7, "variables": vars,
		})
		if !strings.Contains(out, "supplying CI variables is disabled") {
			t.Fatalf("expected a variables refusal:\n%s", out)
		}
		f.assertNeverRequested(t, "/jobs/7/play")
	})
}

func TestPipelineWaitReturnsOnTerminalStatus(t *testing.T) {
	f := newFakeGitLab(t)
	f.json(http.MethodGet, "/projects/"+projectEsc+"/pipelines/100", pipelineJSON(100, "success"))
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	out := mustCall(t, s, "pipeline_wait", map[string]any{"project": projectPath, "id": 100})
	if !strings.Contains(out, "terminal status") || !strings.Contains(out, "success") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestPipelineWaitTimesOut(t *testing.T) {
	f := newFakeGitLab(t)
	f.json(http.MethodGet, "/projects/"+projectEsc+"/pipelines/100", pipelineJSON(100, "running"))
	ts := startServer(t, f, instanceOpts{})
	s := ts.connect(t)

	// A 1-second timeout is clamped up to the 5s minimum poll interval, so the
	// first poll already exceeds the deadline and the tool returns promptly.
	out := mustCall(t, s, "pipeline_wait", map[string]any{
		"project": projectPath, "id": 100, "timeoutSeconds": 1,
	})
	if !strings.Contains(out, "Timed out") || !strings.Contains(out, "running") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

// assertRequested finds a recorded request and returns it.
func assertRequested(t *testing.T, f *fakeGitLab, method, path string) recordedRequest {
	t.Helper()
	for _, r := range f.recorded() {
		if r.Method == method && r.Path == path {
			return r
		}
	}
	t.Fatalf("expected %s %s; recorded: %v", method, path, f.recorded())
	return recordedRequest{}
}

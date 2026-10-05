package perm

import (
	"strings"
	"testing"

	"github.com/truepace-io-oss/gitlab-mcp-server/internal/config"
)

func allCaps() config.Permissions {
	return config.Permissions{
		Read:      config.ReadPerms{Core: true, CI: true},
		Pipelines: config.PipelinePerms{Operate: true, Delete: true},
	}
}

func TestAllowRespectsCapabilitySwitches(t *testing.T) {
	g := NewGuard("gl", false, config.Instance{Name: "gl", Permissions: config.Permissions{
		Read: config.ReadPerms{Core: true},
	}})

	if err := g.Allow(ReadCore); err != nil {
		t.Fatalf("read.core should be allowed: %v", err)
	}
	for _, c := range []Capability{ReadCI, PipelinesOperate, PipelinesDelete} {
		err := g.Allow(c)
		if err == nil {
			t.Fatalf("%s should be denied", c)
		}
		if !strings.Contains(err.Error(), string(c)) {
			t.Errorf("denial for %s should name the capability: %v", c, err)
		}
		if !strings.Contains(err.Error(), `instance "gl"`) {
			t.Errorf("denial for %s should name the instance: %v", c, err)
		}
	}
}

// Both kill-switches must block writes while leaving reads working, so an
// operator can freeze mutations without losing visibility.
func TestReadOnlyKillSwitches(t *testing.T) {
	t.Run("global", func(t *testing.T) {
		g := NewGuard("gl", true, config.Instance{Name: "gl", Permissions: allCaps()})
		if err := g.Allow(ReadCore); err != nil {
			t.Fatalf("reads must still work: %v", err)
		}
		err := g.Allow(PipelinesOperate)
		if err == nil || !strings.Contains(err.Error(), "writes disabled globally") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("per instance", func(t *testing.T) {
		g := NewGuard("gl", false, config.Instance{Name: "gl", ReadOnly: true, Permissions: allCaps()})
		if err := g.Allow(ReadCI); err != nil {
			t.Fatalf("reads must still work: %v", err)
		}
		err := g.Allow(PipelinesDelete)
		if err == nil || !strings.Contains(err.Error(), `writes are disabled for GitLab instance "gl"`) {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestIsWrite(t *testing.T) {
	writes := map[Capability]bool{
		ReadCore:         false,
		ReadCI:           false,
		PipelinesOperate: true,
		PipelinesDelete:  true,
	}
	for c, want := range writes {
		if got := c.IsWrite(); got != want {
			t.Errorf("%s.IsWrite() = %v, want %v", c, got, want)
		}
	}
}

// The namespace matcher must compare on path-segment boundaries. A naive prefix
// check would let "team" authorise "teamwork/app", which is the classic bug.
func TestAllowNamespaceSegmentBoundaries(t *testing.T) {
	g := NewGuardForTest("gl", []string{"team", "other/sub"}, ReadCore)

	allowed := []string{
		"team",
		"team/app",
		"team/sub/app",
		"other/sub",
		"other/sub/app",
	}
	for _, p := range allowed {
		if err := g.AllowNamespace(p); err != nil {
			t.Errorf("expected %q to be allowed: %v", p, err)
		}
	}

	denied := []string{
		"teamwork/app", // the prefix-bug case
		"team-two/app",
		"teamx",
		"other", // parent of an allowed subgroup is not itself allowed
		"other/sub2/app",
		"unrelated/app",
	}
	for _, p := range denied {
		err := g.AllowNamespace(p)
		if err == nil {
			t.Errorf("expected %q to be denied", p)
			continue
		}
		if !strings.Contains(err.Error(), "outside the allowed namespaces") {
			t.Errorf("denial for %q has the wrong message: %v", p, err)
		}
	}
}

func TestAllowNamespaceEmptyAllowlistPermitsEverything(t *testing.T) {
	g := NewGuardForTest("gl", nil, ReadCore)
	for _, p := range []string{"anything", "any/thing", "a/b/c"} {
		if err := g.AllowNamespace(p); err != nil {
			t.Errorf("an empty allowlist should permit %q: %v", p, err)
		}
	}
}

func TestAllowNamespaceIgnoresSurroundingSlashes(t *testing.T) {
	g := NewGuardForTest("gl", []string{"/team/"}, ReadCore)
	if err := g.AllowNamespace("/team/app/"); err != nil {
		t.Fatalf("slashes should be normalised: %v", err)
	}
}

func TestEffectiveListIsOrderedAndFiltered(t *testing.T) {
	g := NewGuard("gl", false, config.Instance{Name: "gl", Permissions: config.Permissions{
		Read:      config.ReadPerms{Core: true, CI: true},
		Pipelines: config.PipelinePerms{Operate: true},
	}})
	got := g.EffectiveList()
	want := []Capability{ReadCore, ReadCI, PipelinesOperate}
	if len(got) != len(want) {
		t.Fatalf("EffectiveList() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("EffectiveList() = %v, want %v", got, want)
		}
	}
}

func TestAllowVariablesIsOptIn(t *testing.T) {
	off := NewGuard("gl", false, config.Instance{Name: "gl", Permissions: allCaps()})
	if off.AllowVariables() {
		t.Fatal("allowVariables must default to false")
	}
	on := NewGuard("gl", false, config.Instance{Name: "gl", Permissions: config.Permissions{
		Pipelines: config.PipelinePerms{Operate: true, AllowVariables: true},
	}})
	if !on.AllowVariables() {
		t.Fatal("allowVariables should be true when configured")
	}
}

func TestAllReturnsEveryCapability(t *testing.T) {
	if len(All()) != 4 {
		t.Fatalf("All() = %v; the capability set is meant to be exactly four", All())
	}
	seen := map[Capability]bool{}
	for _, c := range All() {
		if seen[c] {
			t.Fatalf("All() repeats %s", c)
		}
		seen[c] = true
	}
}

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// Reloading a milestone that holds other services up used to stop them.
// updateDependencies dropped every edge and rebuilt, and RmDep releases
// its target synchronously — a target whose requiredBy reaches zero stops
// right then, before AddDep can require it again. A guard skipped the
// rebuild for unchanged descriptions but deliberately gave up on
// dependency directories, which is exactly how milestones are written.
//
// Measured in the demo VM before the fix: one `slinitctl reload
// all-services` took it from 42 running services to 6.
func TestReloadMilestoneWithDirDepsKeepsHeldServicesRunning(t *testing.T) {
	dir := t.TempDir()
	depsDir := filepath.Join(dir, "milestone.d")
	if err := os.MkdirAll(depsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	held := []string{"held-one", "held-two", "held-three"}
	for _, n := range held {
		writeServiceFile(t, dir, n, "type = internal\n")
		// Directory membership is the only thing naming them.
		if err := os.WriteFile(filepath.Join(depsDir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeServiceFile(t, dir, "milestone", "type = internal\nwaits-for.d: milestone.d\n")

	ss := service.NewServiceSet(&testConsumerLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	ms, err := loader.LoadService("milestone")
	if err != nil {
		t.Fatalf("loading milestone: %v", err)
	}
	ss.StartService(ms)
	ss.ProcessQueues()

	for _, n := range held {
		svc := ss.FindService(n, false)
		if svc == nil {
			t.Fatalf("%s was never loaded", n)
		}
		if svc.State() != service.StateStarted {
			t.Fatalf("%s is %v before the reload, wanted STARTED", n, svc.State())
		}
	}

	if _, err := loader.ReloadService(ms); err != nil {
		t.Fatalf("reloading the milestone: %v", err)
	}
	ss.ProcessQueues()

	for _, n := range held {
		svc := ss.FindService(n, false)
		if svc == nil {
			t.Fatalf("%s disappeared across the reload", n)
		}
		if svc.State() != service.StateStarted {
			t.Errorf("%s is %v after reloading the milestone that holds it, wanted STARTED",
				n, svc.State())
		}
	}
}

// A directory whose membership actually changed is the normal reason to
// reload one, and the case the old guard could not cover: the entries that
// stay must keep running, the new one must be picked up, and the removed
// one must be let go.
func TestReloadMilestoneWithChangedDirDeps(t *testing.T) {
	dir := t.TempDir()
	depsDir := filepath.Join(dir, "milestone.d")
	if err := os.MkdirAll(depsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, n := range []string{"stays", "goes", "arrives"} {
		writeServiceFile(t, dir, n, "type = internal\n")
	}
	for _, n := range []string{"stays", "goes"} {
		if err := os.WriteFile(filepath.Join(depsDir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeServiceFile(t, dir, "milestone", "type = internal\nwaits-for.d: milestone.d\n")

	ss := service.NewServiceSet(&testConsumerLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	ms, err := loader.LoadService("milestone")
	if err != nil {
		t.Fatal(err)
	}
	ss.StartService(ms)
	ss.ProcessQueues()

	if got := ss.FindService("stays", false); got == nil || got.State() != service.StateStarted {
		t.Fatal("stays should be running before the reload")
	}

	// Swap the membership the way an operator would.
	if err := os.Remove(filepath.Join(depsDir, "goes")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(depsDir, "arrives"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := loader.ReloadService(ms); err != nil {
		t.Fatalf("reloading after a directory change: %v", err)
	}
	ss.ProcessQueues()

	if svc := ss.FindService("stays", false); svc == nil || svc.State() != service.StateStarted {
		state := service.ServiceState(0)
		if svc != nil {
			state = svc.State()
		}
		t.Errorf("stays is %v after the reload, wanted STARTED — an entry that did not change must not be disturbed", state)
	}
	// The new entry becomes a dependency but is not started retroactively:
	// AddDep on an already-running parent does not activate a soft dep,
	// before this change or after it. Asserting the edge rather than the
	// state keeps this test about the reload diff instead of quietly
	// deciding a separate question.
	var sawArrives bool
	for _, d := range ms.Record().Dependencies() {
		if d.To.Name() == "arrives" {
			sawArrives = true
		}
	}
	if !sawArrives {
		t.Errorf("arrives was added to the directory but is not a dependency after the reload")
	}

	// `goes` is no longer named, so the milestone must not still hold it.
	msDeps := ms.Record().Dependencies()
	for _, d := range msDeps {
		if d.To.Name() == "goes" {
			t.Errorf("the milestone still depends on goes after it left the directory")
		}
	}
}

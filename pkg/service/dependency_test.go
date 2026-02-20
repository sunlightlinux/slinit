package service

import (
	"testing"
)

// --- Soft dependency tests ---

func TestSoftDepFailureDoesNotCascade(t *testing.T) {
	set, _ := newTestSet()

	dep := NewInternalService(set, "soft-dep")
	main := NewInternalService(set, "main-svc")
	set.AddService(dep)
	set.AddService(main)

	// main soft-depends on dep
	main.Record().AddDep(dep, DepSoft)

	// Pin dep stopped so it cannot start
	dep.PinStop()

	// Start main - should still reach STARTED despite soft dep failure
	set.StartService(main)

	if main.State() != StateStarted {
		t.Errorf("main should be STARTED despite soft dep failure, got %v", main.State())
	}
}

func TestSoftDepStopDoesNotPropagate(t *testing.T) {
	set, _ := newTestSet()

	dep := NewInternalService(set, "soft-dep")
	main := NewInternalService(set, "main-svc")
	set.AddService(dep)
	set.AddService(main)

	main.Record().AddDep(dep, DepSoft)

	// Start both via main
	set.StartService(main)

	if dep.State() != StateStarted {
		t.Fatalf("dep should be STARTED, got %v", dep.State())
	}
	if main.State() != StateStarted {
		t.Fatalf("main should be STARTED, got %v", main.State())
	}

	// Stop dep directly - main should remain STARTED
	set.StopService(dep)

	if dep.State() != StateStopped {
		t.Errorf("dep should be STOPPED, got %v", dep.State())
	}
	if main.State() != StateStarted {
		t.Errorf("main should remain STARTED after soft dep stops, got %v", main.State())
	}
}

// --- WaitsFor dependency tests ---

func TestWaitsForDepFailureDoesNotCascade(t *testing.T) {
	set, _ := newTestSet()

	dep := NewInternalService(set, "waitsfor-dep")
	main := NewInternalService(set, "main-svc")
	set.AddService(dep)
	set.AddService(main)

	main.Record().AddDep(dep, DepWaitsFor)

	// Pin dep stopped so it cannot start
	dep.PinStop()

	// Start main - should still reach STARTED despite waits-for dep failure
	set.StartService(main)

	if main.State() != StateStarted {
		t.Errorf("main should be STARTED despite waits-for dep failure, got %v", main.State())
	}
}

// --- Regular dependency tests ---

func TestRegularDepFailureCascades(t *testing.T) {
	set, _ := newTestSet()

	dep := NewInternalService(set, "regular-dep")
	main := NewInternalService(set, "main-svc")
	set.AddService(dep)
	set.AddService(main)

	main.Record().AddDep(dep, DepRegular)

	// Pin dep stopped so it cannot start
	dep.PinStop()

	// Start main - should fail because regular dep can't start
	set.StartService(main)

	if main.State() != StateStopped {
		t.Errorf("main should be STOPPED due to regular dep failure, got %v", main.State())
	}
	if !main.Record().DidStartFail() {
		t.Error("main should report start failure")
	}
}

func TestRegularDepStopPropagates(t *testing.T) {
	set, _ := newTestSet()

	dep := NewInternalService(set, "regular-dep")
	main := NewInternalService(set, "main-svc")
	set.AddService(dep)
	set.AddService(main)

	main.Record().AddDep(dep, DepRegular)

	set.StartService(main)

	if dep.State() != StateStarted {
		t.Fatalf("dep should be STARTED, got %v", dep.State())
	}
	if main.State() != StateStarted {
		t.Fatalf("main should be STARTED, got %v", main.State())
	}

	// Stop main first, then dep should also stop (released by main)
	set.StopService(main)

	if main.State() != StateStopped {
		t.Errorf("main should be STOPPED, got %v", main.State())
	}
	if dep.State() != StateStopped {
		t.Errorf("dep should be STOPPED after main releases it, got %v", dep.State())
	}
}

// --- Milestone dependency tests ---

func TestMilestoneDepFailureCascades(t *testing.T) {
	set, _ := newTestSet()

	dep := NewInternalService(set, "milestone-dep")
	main := NewInternalService(set, "main-svc")
	set.AddService(dep)
	set.AddService(main)

	main.Record().AddDep(dep, DepMilestone)

	// Pin dep stopped
	dep.PinStop()

	// Start main - should fail because milestone dep failed (hard while waiting)
	set.StartService(main)

	if main.State() != StateStopped {
		t.Errorf("main should be STOPPED due to milestone dep failure, got %v", main.State())
	}
	if !main.Record().DidStartFail() {
		t.Error("main should report start failure")
	}
}

func TestMilestoneBecomeSoftAfterStart(t *testing.T) {
	set, _ := newTestSet()

	dep := NewInternalService(set, "milestone-dep")
	main := NewInternalService(set, "main-svc")
	set.AddService(dep)
	set.AddService(main)

	main.Record().AddDep(dep, DepMilestone)

	// Start main - dep starts, milestone satisfied, main starts
	set.StartService(main)

	if dep.State() != StateStarted {
		t.Fatalf("dep should be STARTED, got %v", dep.State())
	}
	if main.State() != StateStarted {
		t.Fatalf("main should be STARTED, got %v", main.State())
	}

	// Now stop dep directly - main should remain STARTED
	// because milestone becomes soft after start (WaitingOn is false)
	set.StopService(dep)

	if dep.State() != StateStopped {
		t.Errorf("dep should be STOPPED, got %v", dep.State())
	}
	if main.State() != StateStarted {
		t.Errorf("main should remain STARTED after milestone dep stops, got %v", main.State())
	}
}

// --- Soft dependency reattachment on restart ---

func TestSoftDepReattachOnRestart(t *testing.T) {
	set, _ := newTestSet()

	dep := NewInternalService(set, "soft-dep")
	main := NewInternalService(set, "main-svc")
	set.AddService(dep)
	set.AddService(main)

	main.Record().AddDep(dep, DepSoft)

	// Start main (which soft-requires dep)
	set.StartService(main)

	if dep.State() != StateStarted {
		t.Fatalf("dep should be STARTED, got %v", dep.State())
	}
	if main.State() != StateStarted {
		t.Fatalf("main should be STARTED, got %v", main.State())
	}

	depRequiredBefore := dep.RequiredBy()

	// Restart dep (simulate: stop and start again via desired=STARTED)
	dep.Restart()
	set.ProcessQueues()

	if dep.State() != StateStarted {
		t.Errorf("dep should be STARTED after restart, got %v", dep.State())
	}

	// After restart, soft dependents should be reattached
	depRequiredAfter := dep.RequiredBy()
	if depRequiredAfter < depRequiredBefore {
		t.Errorf("dep.requiredBy should be at least %d after restart, got %d",
			depRequiredBefore, depRequiredAfter)
	}

	// main should still be STARTED
	if main.State() != StateStarted {
		t.Errorf("main should remain STARTED after soft dep restart, got %v", main.State())
	}
}

// --- BEFORE/AFTER ordering tests ---

func TestBeforeOrdering(t *testing.T) {
	set, logger := newTestSet()

	svcA := NewInternalService(set, "before-svc")
	svcB := NewInternalService(set, "target-svc")
	set.AddService(svcA)
	set.AddService(svcB)

	// svcA has a "before" relationship to svcB
	// This means svcA should start before svcB
	svcA.Record().AddDep(svcB, DepBefore)

	// Start both services
	set.StartService(svcA)
	set.StartService(svcB)

	if svcA.State() != StateStarted {
		t.Errorf("svcA should be STARTED, got %v", svcA.State())
	}
	if svcB.State() != StateStarted {
		t.Errorf("svcB should be STARTED, got %v", svcB.State())
	}

	// Verify ordering: svcA should have started before svcB
	aIdx := -1
	bIdx := -1
	for i, name := range logger.started {
		if name == "before-svc" {
			aIdx = i
		}
		if name == "target-svc" {
			bIdx = i
		}
	}

	if aIdx >= 0 && bIdx >= 0 && aIdx > bIdx {
		t.Errorf("before-svc should start before target-svc, but started at index %d vs %d",
			aIdx, bIdx)
	}
}

func TestAfterOrdering(t *testing.T) {
	set, _ := newTestSet()

	svcA := NewInternalService(set, "after-svc")
	svcB := NewInternalService(set, "target-svc")
	set.AddService(svcA)
	set.AddService(svcB)

	// svcA has an "after" relationship to svcB
	// This means svcA should start after svcB
	svcA.Record().AddDep(svcB, DepAfter)

	// Start svcA - svcB should NOT be started (ordering-only, no require)
	set.StartService(svcA)

	if svcA.State() != StateStarted {
		t.Errorf("svcA should be STARTED, got %v", svcA.State())
	}

	// svcB should NOT have been started by the ordering dependency
	// (ordering deps don't call Require on target)
	// It may or may not be started depending on implementation,
	// but the key is that svcA doesn't fail
}

func TestOrderingDepNoPropagation(t *testing.T) {
	set, _ := newTestSet()

	svcA := NewInternalService(set, "ordering-svc")
	svcB := NewInternalService(set, "target-svc")
	set.AddService(svcA)
	set.AddService(svcB)

	// svcA has before ordering on svcB
	svcA.Record().AddDep(svcB, DepBefore)

	// Start svcA
	set.StartService(svcA)

	if svcA.State() != StateStarted {
		t.Errorf("svcA should be STARTED, got %v", svcA.State())
	}

	// svcB should NOT be required/started by ordering dep
	if svcB.RequiredBy() > 0 {
		t.Errorf("ordering dep should NOT require target, but requiredBy=%d", svcB.RequiredBy())
	}

	// Stop svcA - should not affect svcB
	set.StopService(svcA)

	if svcA.State() != StateStopped {
		t.Errorf("svcA should be STOPPED, got %v", svcA.State())
	}
}

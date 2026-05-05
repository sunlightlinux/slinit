package service

import "testing"

// TestManualSoloStaysStopped: a manual service with no incoming dep
// chain stays STOPPED on its own (the simple "I exist, opt-in" case).
// startCheckDependencies-driven cases are covered separately because
// a manual service in a waits-for / depends-on chain has different
// behaviour for the *dependent*; this test isolates the manual
// service's own behaviour.
func TestManualSoloStaysStopped(t *testing.T) {
	set, _ := newTestSet()

	manualSvc := NewInternalService(set, "manual-svc")
	manualSvc.Record().SetManualStart(true)
	set.AddService(manualSvc)

	// Loader path didn't run; just confirm it isn't somehow
	// auto-started by AddService alone.
	if manualSvc.State() != StateStopped {
		t.Errorf("manual-svc state=%v, want STOPPED", manualSvc.State())
	}
}

// TestManualRequireKeepsItStopped: when a soft-dependent is started
// and Require() is propagated to a manual service, the manual service
// stays STOPPED (auto-activation refused) — the requiredBy counter is
// still bumped to keep HoldingAcq bookkeeping consistent, but no
// doStart is queued. The dependent's *own* state is documented to
// stall in STARTING in this case (manual services should not be put
// into waits-for chains; see slinit-service(5)).
func TestManualRequireKeepsItStopped(t *testing.T) {
	set, _ := newTestSet()

	manualSvc := NewInternalService(set, "manual-svc")
	manualSvc.Record().SetManualStart(true)
	set.AddService(manualSvc)

	// Drive Require() directly to isolate from dependent-state
	// concerns (the dependent's behaviour is a separate story).
	manualSvc.Record().Require()

	if manualSvc.State() != StateStopped {
		t.Errorf("after Require: state=%v, want STOPPED", manualSvc.State())
	}
	if manualSvc.Record().RequiredBy() != 1 {
		t.Errorf("after Require: requiredBy=%d, want 1 (bookkeeping must still bump)",
			manualSvc.Record().RequiredBy())
	}
}

// TestManualBlocksHardDep: a service marked manual stays STOPPED even
// when a hard (depends-on) dependent activates. The dependent's start
// blocks because its hard dep is unsatisfied.
func TestManualBlocksHardDep(t *testing.T) {
	set, _ := newTestSet()

	manualSvc := NewInternalService(set, "manual-svc")
	manualSvc.Record().SetManualStart(true)
	set.AddService(manualSvc)

	dependent := NewInternalService(set, "dependent")
	set.AddService(dependent)
	dependent.Record().AddDep(manualSvc, DepRegular)

	set.StartService(dependent)

	if manualSvc.State() != StateStopped {
		t.Errorf("manual-svc state=%v, want STOPPED", manualSvc.State())
	}
	if dependent.State() == StateStarted {
		t.Errorf("dependent reached STARTED with manual hard dep STOPPED")
	}
}

// TestManualExplicitStartWorks: `slinitctl start <manual-svc>` (which
// reaches us as StartService → Start) bypasses the manual block and
// activates the service normally.
func TestManualExplicitStartWorks(t *testing.T) {
	set, _ := newTestSet()

	manualSvc := NewInternalService(set, "manual-svc")
	manualSvc.Record().SetManualStart(true)
	set.AddService(manualSvc)

	set.StartService(manualSvc)

	if manualSvc.State() != StateStarted {
		t.Errorf("explicit StartService on manual: state=%v, want STARTED", manualSvc.State())
	}
	if !manualSvc.Record().IsMarkedActive() {
		t.Errorf("explicit start should set startExplicit=true")
	}
}

// TestManualExplicitStartUnblocksHardDep: once the manual service is
// explicitly started, a hard-dependent can subsequently start.
func TestManualExplicitStartUnblocksHardDep(t *testing.T) {
	set, _ := newTestSet()

	manualSvc := NewInternalService(set, "manual-svc")
	manualSvc.Record().SetManualStart(true)
	set.AddService(manualSvc)

	dependent := NewInternalService(set, "dependent")
	set.AddService(dependent)
	dependent.Record().AddDep(manualSvc, DepRegular)

	// Operator first starts manual explicitly, then dependent.
	set.StartService(manualSvc)
	if manualSvc.State() != StateStarted {
		t.Fatalf("manual-svc not STARTED after explicit start")
	}
	set.StartService(dependent)

	if dependent.State() != StateStarted {
		t.Errorf("dependent state=%v, want STARTED", dependent.State())
	}
}

// TestManualWakeReturnsFalse: Wake on a manual service that has not
// been explicitly started returns false — operator must use start,
// not wake.
func TestManualWakeReturnsFalse(t *testing.T) {
	set, _ := newTestSet()

	manualSvc := NewInternalService(set, "manual-svc")
	manualSvc.Record().SetManualStart(true)
	set.AddService(manualSvc)

	dependent := NewInternalService(set, "dependent")
	set.AddService(dependent)
	dependent.Record().AddDep(manualSvc, DepWaitsFor)
	set.StartService(dependent)

	if ok := set.WakeService(manualSvc); ok {
		t.Errorf("Wake on manual returned true, want false")
	}
	if manualSvc.State() != StateStopped {
		t.Errorf("manual-svc state=%v, want STOPPED", manualSvc.State())
	}
}

// TestManualStopAfterExplicitStart: an explicitly-started manual
// service stops normally via StopService.
func TestManualStopAfterExplicitStart(t *testing.T) {
	set, _ := newTestSet()

	manualSvc := NewInternalService(set, "manual-svc")
	manualSvc.Record().SetManualStart(true)
	set.AddService(manualSvc)

	set.StartService(manualSvc)
	if manualSvc.State() != StateStarted {
		t.Fatalf("manual-svc not STARTED")
	}

	set.StopService(manualSvc)
	if manualSvc.State() != StateStopped {
		t.Errorf("manual-svc state=%v after StopService, want STOPPED", manualSvc.State())
	}
}

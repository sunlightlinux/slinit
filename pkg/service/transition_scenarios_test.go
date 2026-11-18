package service

import "testing"

// New scenarios on the transition harness (testsvc_test.go), not ports.
//
// The ported dinit cases bought protection but caught nothing: reinstating
// the stopCheckDependents defect failed only the one scenario written for
// it. These cases go after the same blind spot from other directions —
// a request that arrives while a service is observably STARTING or
// STOPPING, which no InternalService-based test can produce. Each
// expectation is dinit 0.23.0's (service.cc: do_start, do_stop, stopped,
// failed_to_start, release), since slinit keeps dinit's state machine.

func settle(set *ServiceSet) { set.ProcessQueues() }

// A start that arrives while the service is STOPPING (and the stop cannot
// be interrupted) must not be lost and must not bring the service up
// before it has finished stopping: dinit's do_start leaves it STOPPING
// with desired STARTED, and stopped() then restarts it.
func TestScenarioStartDuringStopIsHonouredAfterStop(t *testing.T) {
	set, _ := newTestSet()
	s := addTestSvcs(t, set, "svc")[0]
	s.autoStop = false

	set.StartService(s)
	settle(set)
	if s.State() != StateStarted {
		t.Fatalf("want STARTED, got %v", s.State())
	}

	set.StopService(s)
	settle(set)
	if s.State() != StateStopping || s.downCount != 1 {
		t.Fatalf("want STOPPING with one BringDown, got %v / downCount=%d", s.State(), s.downCount)
	}

	set.StartService(s)
	settle(set)
	if s.upCount != 1 {
		t.Errorf("BringUp issued while still STOPPING (upCount=%d)", s.upCount)
	}
	if s.State() != StateStopping {
		t.Errorf("a non-interruptible stop must finish first, got %v", s.State())
	}

	s.Stopped()
	settle(set)
	if s.upCount != 2 {
		t.Errorf("the start requested during the stop was lost (upCount=%d)", s.upCount)
	}
	if s.State() != StateStarted {
		t.Errorf("want STARTED after the stop completed, got %v", s.State())
	}
}

// A stop that arrives while a non-interruptible start is under way must
// not be lost: dinit lets the start finish, then stops the service.
func TestScenarioStopDuringUninterruptibleStartIsHonouredAfterStart(t *testing.T) {
	set, _ := newTestSet()
	s := addTestSvcs(t, set, "svc")[0]
	s.autoStart = false
	s.interruptible = false

	set.StartService(s)
	settle(set)
	if s.State() != StateStarting {
		t.Fatalf("want STARTING, got %v", s.State())
	}

	set.StopService(s)
	settle(set)
	if s.State() != StateStarting {
		t.Errorf("an uninterruptible start must continue, got %v", s.State())
	}
	if s.downCount != 0 {
		t.Errorf("BringDown issued during an uninterruptible start (downCount=%d)", s.downCount)
	}

	s.Started()
	settle(set)
	if s.downCount != 1 {
		t.Errorf("the stop requested during the start was lost (downCount=%d)", s.downCount)
	}
	if s.State() != StateStopped {
		t.Errorf("want STOPPED, got %v", s.State())
	}
	if set.CountActiveServices() != 0 {
		t.Errorf("active services = %d, want 0", set.CountActiveServices())
	}
}

// An interruptible start is cut short by a stop: the service goes down
// without ever reporting STARTED.
func TestScenarioStopInterruptsInterruptibleStart(t *testing.T) {
	set, _ := newTestSet()
	s := addTestSvcs(t, set, "svc")[0]
	s.autoStart = false
	s.interruptible = true

	set.StartService(s)
	settle(set)
	set.StopService(s)
	settle(set)

	if s.State() != StateStopped {
		t.Errorf("want STOPPED, got %v", s.State())
	}
	if s.downCount != 1 {
		t.Errorf("want one BringDown after the interrupt, got %d", s.downCount)
	}
	if set.CountActiveServices() != 0 {
		t.Errorf("active services = %d, want 0", set.CountActiveServices())
	}
}

// A hard dependency that fails while its dependent waits takes the
// dependent down with DEPFAILED, and the dependent is never brought up.
func TestScenarioHardDepFailureFailsWaitingDependent(t *testing.T) {
	set, _ := newTestSet()
	svcs := addTestSvcs(t, set, "dep", "app")
	dep, app := svcs[0], svcs[1]
	dep.autoStart = false
	app.Record().AddDep(dep, DepRegular)

	set.StartService(app)
	settle(set)
	if dep.State() != StateStarting || app.State() != StateStarting {
		t.Fatalf("want both STARTING, got dep=%v app=%v", dep.State(), app.State())
	}

	dep.failedToStart(false, true)
	settle(set)

	if app.upCount != 0 {
		t.Errorf("dependent was brought up although its hard dependency failed")
	}
	if app.State() != StateStopped {
		t.Errorf("dependent should be STOPPED, got %v", app.State())
	}
	if r := app.StopReason(); r != ReasonDepFailed {
		t.Errorf("dependent stop reason = %v, want DepFailed", r)
	}
	if set.CountActiveServices() != 0 {
		t.Errorf("active services = %d, want 0", set.CountActiveServices())
	}
}

// The same failure through waits-for releases the dependent instead:
// it starts without the dependency.
func TestScenarioWaitsForFailureReleasesDependent(t *testing.T) {
	set, _ := newTestSet()
	svcs := addTestSvcs(t, set, "dep", "app")
	dep, app := svcs[0], svcs[1]
	dep.autoStart = false
	app.Record().AddDep(dep, DepWaitsFor)

	set.StartService(app)
	settle(set)
	if app.upCount != 0 {
		t.Fatalf("dependent should wait for its waits-for dependency")
	}

	dep.failedToStart(false, true)
	settle(set)

	if app.upCount != 1 || app.State() != StateStarted {
		t.Errorf("dependent should start anyway: upCount=%d state=%v", app.upCount, app.State())
	}
	if dep.State() != StateStopped {
		t.Errorf("failed dependency should be STOPPED, got %v", dep.State())
	}
}

// A dependent started while its hard dependency is STOPPING must wait for
// the stop to finish, then for the dependency to come back up, and only
// then be brought up itself.
func TestScenarioDependentWaitsOutDependencyStopThenRestart(t *testing.T) {
	set, _ := newTestSet()
	svcs := addTestSvcs(t, set, "dep", "app")
	dep, app := svcs[0], svcs[1]
	app.Record().AddDep(dep, DepRegular)

	set.StartService(dep)
	settle(set)
	dep.autoStop = false
	set.StopService(dep)
	settle(set)
	if dep.State() != StateStopping {
		t.Fatalf("want dep STOPPING, got %v", dep.State())
	}

	set.StartService(app)
	settle(set)
	if app.upCount != 0 {
		t.Errorf("dependent brought up while its dependency is STOPPING")
	}
	if dep.upCount != 1 {
		t.Errorf("dependency restarted before its stop completed (upCount=%d)", dep.upCount)
	}

	dep.Stopped()
	settle(set)
	if dep.upCount != 2 || dep.State() != StateStarted {
		t.Errorf("dependency should come back up: upCount=%d state=%v", dep.upCount, dep.State())
	}
	if app.upCount != 1 || app.State() != StateStarted {
		t.Errorf("dependent should start after its dependency: upCount=%d state=%v", app.upCount, app.State())
	}
}

// Cancelling a dependent while its dependency is mid-start must not leave
// the dependency running for nobody. The dependency's start cannot be
// interrupted, so it finishes — and then has to stop, because the only
// thing that wanted it has gone.
func TestScenarioCancelledDependentDoesNotOrphanItsDependency(t *testing.T) {
	set, _ := newTestSet()
	svcs := addTestSvcs(t, set, "dep", "app")
	dep, app := svcs[0], svcs[1]
	dep.autoStart = false
	dep.interruptible = false
	app.Record().AddDep(dep, DepRegular)

	set.StartService(app)
	settle(set)
	set.StopService(app)
	settle(set)
	if app.State() != StateStopped || app.upCount != 0 {
		t.Fatalf("app waiting on deps should stop at once: state=%v upCount=%d", app.State(), app.upCount)
	}
	if dep.State() != StateStarting {
		t.Fatalf("an uninterruptible start continues, got dep=%v", dep.State())
	}

	dep.Started()
	settle(set)
	if dep.State() != StateStopped {
		t.Errorf("dependency left %v with nothing requiring it", dep.State())
	}
	if set.CountActiveServices() != 0 {
		t.Errorf("active services = %d, want 0", set.CountActiveServices())
	}
}

// A hard dependency forced down while its dependent waits for it takes
// the dependent with it, and neither is left half-up.
func TestScenarioForcedStopOfStartingDependencyStopsWaitingDependent(t *testing.T) {
	set, _ := newTestSet()
	svcs := addTestSvcs(t, set, "dep", "app")
	dep, app := svcs[0], svcs[1]
	dep.autoStart = false
	dep.interruptible = true
	app.Record().AddDep(dep, DepRegular)

	set.StartService(app)
	settle(set)
	set.StopService(dep)
	settle(set)

	if app.upCount != 0 {
		t.Errorf("dependent brought up although its dependency was stopped")
	}
	if app.State() != StateStopped {
		t.Errorf("dependent should be STOPPED, got %v", app.State())
	}
	if dep.State() != StateStopped {
		t.Errorf("dependency should be STOPPED, got %v", dep.State())
	}
	if set.CountActiveServices() != 0 {
		t.Errorf("active services = %d, want 0", set.CountActiveServices())
	}
}

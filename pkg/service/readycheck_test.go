package service

import (
	"testing"
	"time"
)

// TestReadyCheckDefaultInterval pins the new 100ms default so a
// future accidental revert to 1s is caught by CI. The 100ms floor is
// what makes fast-readiness sockets (dbus, most listen(2) daemons)
// visible immediately on the boot console rather than adding a full
// second to boot time.
func TestReadyCheckDefaultInterval(t *testing.T) {
	if defaultReadyCheckInterval != 100*time.Millisecond {
		t.Errorf("defaultReadyCheckInterval = %v, want 100ms", defaultReadyCheckInterval)
	}
}

// TestStartedIsIdempotent verifies that calling Started() twice on
// the same session emits exactly one boot-console event. Regression
// guard for the "[ OK ] elogind" line appearing three times on
// ceres v1.10.46 boot.
//
// We drive Started() directly on a ServiceRecord and re-arm state to
// StateStarting between test lifecycle events (internal services chain
// through Stopped() after listeners fire, which would corrupt the
// state comparison this test is guarding).
func TestStartedIsIdempotent(t *testing.T) {
	set, log := newTestSet()
	svc := NewInternalService(set, "idem")
	set.AddService(svc)
	rec := svc.Record()

	// Simulate the state a service occupies during a BringUp: STARTING.
	rec.state.Store(StateStarting)
	rec.Started() // transitions STARTING → STARTED and emits.
	rec.Started() // guard must fire; no second emit.

	count := 0
	for _, n := range log.started {
		if n == "idem" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("ServiceStarted emissions for 'idem' = %d, want exactly 1", count)
	}
}

// TestStartedFlagClearsOnRestart ensures a fresh start-session emits
// its own Started() event. Without the reset the auto-restart flow
// would go silent on the boot console — worse than the original bug.
func TestStartedFlagClearsOnRestart(t *testing.T) {
	set, log := newTestSet()
	svc := NewInternalService(set, "restart-idem")
	set.AddService(svc)
	rec := svc.Record()

	rec.state.Store(StateStarting)
	rec.Started()

	// Simulate a new session: initiateStart is what restart-flow
	// re-enters through. Only the reset side-effect matters here.
	rec.initiateStart()
	// initiateStart moved us to STARTING; drive Started() again.
	rec.Started()

	count := 0
	for _, n := range log.started {
		if n == "restart-idem" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("across-session Started() emits for 'restart-idem' = %d, want 2 (one per session)", count)
	}
}

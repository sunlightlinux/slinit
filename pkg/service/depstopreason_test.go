package service

import (
	"testing"
)

// A dependency the user force-stopped did not fail, so its dependents
// did not fail either — they were taken down on purpose. Reporting
// DEPFAILED there sends an operator looking for a failure that never
// happened.
//
// slinit already gets this right, and the test exists to keep it that
// way. dinit did not: its stop_dependents() hardcoded DEPFAILED, with
// its own "FIXME is it really though?" beside it, until 9dd8343
// (2026-10-17) replaced it with the parent's reason propagated. slinit
// has the same hardcoded line, but measurably never reaches it — a
// probe inserted on that branch printed nothing for this scenario,
// because `desired` is not yet STOPPED when stopDependents runs. The
// dependent's NORMAL therefore comes from the field's zero value
// rather than from a decision, which is exactly the kind of agreement
// that breaks silently later.
func TestForceStopOfDependencyLeavesDependentStoppedNormally(t *testing.T) {
	set, _ := newTestSet()

	dep := NewInternalService(set, "the-dep")
	main := NewInternalService(set, "the-dependent")
	set.AddService(dep)
	set.AddService(main)

	main.Record().AddDep(dep, DepRegular)

	set.StartService(main)
	if dep.State() != StateStarted || main.State() != StateStarted {
		t.Fatalf("setup: want both STARTED, got dep=%v main=%v", dep.State(), main.State())
	}

	// The user force-stops the dependency. Nothing has failed.
	set.ForceStopService(dep)

	if main.State() != StateStopped {
		t.Fatalf("dependent should stop with its dependency, got %v", main.State())
	}
	if got := main.Record().StopReason(); got != ReasonNormal {
		t.Errorf("dependent reports stop reason %v; want %v — the user asked for the stop, nothing failed", got, ReasonNormal)
	}
}

// The other half of the rule, on the path that actually carries it. A
// dependency that genuinely cannot start must leave its dependent
// reporting DEPFAILED, so that "nothing failed" above is a statement
// about force-stop and not a blanket inability to report a failure.
//
// This travels through propFailure (record.go), not stopDependents —
// the two reasons are set in different places, which is why a test
// that only covered the force-stop path would prove much less than it
// appears to.
func TestUnstartableDependencyLeavesDependentDepFailed(t *testing.T) {
	set, _ := newTestSet()

	dep := NewInternalService(set, "the-dep")
	main := NewInternalService(set, "the-dependent")
	set.AddService(dep)
	set.AddService(main)

	main.Record().AddDep(dep, DepRegular)

	// Pin the dependency stopped so it cannot come up at all.
	dep.PinStop()

	set.StartService(main)

	if main.State() != StateStopped {
		t.Fatalf("dependent should not be running with an unstartable dependency, got %v", main.State())
	}
	if got := main.Record().StopReason(); got != ReasonDepFailed {
		t.Errorf("dependent reports stop reason %v; want %v — its dependency could not start", got, ReasonDepFailed)
	}
}

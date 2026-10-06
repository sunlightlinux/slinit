package service

import (
	"strings"
	"testing"
	"time"
)

// `Requisite=` means "the dependency must already be running, do not
// start it, fail if it is not". No dependency type says that — each one
// either starts the target or merely orders against it — so slinit had
// no way to express it, and the systemd mapping sent Requisite= to
// depends-on, which pulls the dependency up and succeeds: the opposite
// of what the unit asked for.
func TestAssertServiceStartedFailsWhenTheOtherIsDown(t *testing.T) {
	set, _ := newTestSet()

	other := newScripted(t, set, "backing", []string{"/bin/true"}, nil)

	svc := newScripted(t, set, "needs-backing", []string{"/bin/true"}, nil)
	svc.Record().SetPredicates([]Predicate{
		{Kind: PredServiceStarted, Param: "backing", IsAssert: true},
	})

	// backing is loaded but never started.
	set.StartService(svc)
	waitState(t, svc, StateStopped, 5*time.Second)

	if st := svc.State(); st == StateStarted {
		t.Errorf("service started while its requisite was down; state = %v", st)
	}
	if !svc.Record().DidStartFail() {
		t.Error("the start was not marked as failed")
	}
	// And the requisite must NOT have been started as a side effect.
	if st := other.State(); st != StateStopped {
		t.Errorf("the requisite was started by the service that required it "+
			"(state = %v) — that is Requires=, not Requisite=", st)
	}
}

// With the other service already up, the start proceeds.
func TestAssertServiceStartedPassesWhenTheOtherIsUp(t *testing.T) {
	set, _ := newTestSet()

	other := newScripted(t, set, "backing", []string{"/bin/true"}, nil)
	set.StartService(other)
	if got := waitState(t, other, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("backing service state = %v, want STARTED", got)
	}

	svc := newScripted(t, set, "needs-backing", []string{"/bin/true"}, nil)
	svc.Record().SetPredicates([]Predicate{
		{Kind: PredServiceStarted, Param: "backing", IsAssert: true},
	})
	set.StartService(svc)

	if st := waitState(t, svc, StateStarted, 5*time.Second); st != StateStarted {
		t.Errorf("service state = %v, want STARTED", st)
	}
}

// The condition- form skips instead of failing, which is what the
// condition/assert split means everywhere else.
func TestConditionServiceStartedSkipsRatherThanFails(t *testing.T) {
	set, _ := newTestSet()
	newScripted(t, set, "backing", []string{"/bin/true"}, nil)

	svc := newScripted(t, set, "optional", []string{"/bin/true"}, nil)
	svc.Record().SetPredicates([]Predicate{
		{Kind: PredServiceStarted, Param: "backing"},
	})
	set.StartService(svc)
	time.Sleep(300 * time.Millisecond)

	if svc.Record().DidStartFail() {
		t.Error("a condition- predicate failed the start; it must skip")
	}
}

// Negation: the named service must NOT be running.
func TestServiceStartedNegated(t *testing.T) {
	set, _ := newTestSet()
	newScripted(t, set, "rival", []string{"/bin/true"}, nil)

	svc := newScripted(t, set, "exclusive", []string{"/bin/true"}, nil)
	svc.Record().SetPredicates([]Predicate{
		{Kind: PredServiceStarted, Param: "rival", Negate: true, IsAssert: true},
	})

	set.StartService(svc)
	if st := waitState(t, svc, StateStarted, 5*time.Second); st != StateStarted {
		t.Errorf("state = %v, want STARTED while the rival is down", st)
	}
}

// A name nobody loaded is not running, and the reason should say which
// of the two it was.
func TestServiceStartedReasonsAreDistinct(t *testing.T) {
	set, _ := newTestSet()
	newScripted(t, set, "loaded-but-stopped", []string{"/bin/true"}, nil)
	host := newScripted(t, set, "host", []string{"/bin/true"}, nil)
	rec := host.Record()

	_, why := rec.checkServiceStarted(Predicate{Kind: PredServiceStarted, Param: "absent"})
	if !strings.Contains(why, "not loaded") {
		t.Errorf("missing service reason = %q, want it to say not loaded", why)
	}
	_, why = rec.checkServiceStarted(Predicate{Kind: PredServiceStarted, Param: "loaded-but-stopped"})
	if !strings.Contains(why, "STOPPED") {
		t.Errorf("stopped service reason = %q, want the state in it", why)
	}
}

func TestServiceStartedIsNotEvaluatedAsAMachineCheck(t *testing.T) {
	// Evaluate() must not report it as an unknown kind, in case some
	// future caller walks a predicate list without the service set.
	ok, why := Predicate{Kind: PredServiceStarted, Param: "x"}.Evaluate()
	if ok {
		t.Error("Evaluate() claimed success for a kind it cannot answer")
	}
	if strings.Contains(why, "unknown predicate kind") {
		t.Errorf("reported as an unknown kind: %q", why)
	}
}

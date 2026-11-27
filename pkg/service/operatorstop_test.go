package service

import (
	"testing"
	"time"
)

// MarkOperatorStop says a container that ends now was stopped on
// request (exit 0). A service that then stops on its own, not because
// anyone asked, takes that back: `slinitctl stop extra` followed later
// by a crash of the workload used to end the container with exit 0.
// A requested stop keeps it, also when the service carries a stop
// reason left by an earlier crash (a start never resets it), when it is
// stopped by a cascade from its dependency, and when the stop is forced.
func TestUnrequestedStopClearsOperatorStop(t *testing.T) {
	waitStopped := func(t *testing.T, s Service) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for s.State() != StateStopped {
			if time.Now().After(deadline) {
				t.Fatalf("%s still %v", s.Name(), s.State())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	t.Run("crash clears it", func(t *testing.T) {
		set, _ := newTestSet()
		app := NewProcessService(set, "app")
		app.SetCommand([]string{"/bin/sh", "-c", "sleep 0.2; exit 3"})
		set.AddService(app)
		set.StartService(app)
		set.MarkOperatorStop()
		waitStopped(t, app)
		if set.OperatorStopRequested() {
			t.Fatal("a crash after a stop request still counted as a stop on request")
		}
	})

	t.Run("cascade with a stale reason keeps it", func(t *testing.T) {
		set, _ := newTestSet()
		db := NewInternalService(set, "db")
		app := NewInternalService(set, "app")
		set.AddService(db)
		set.AddService(app)
		app.Record().AddDep(db, DepRegular)
		set.StartService(app)
		app.Record().stopReason = ReasonTerminated // left by an earlier crash
		set.MarkOperatorStop()
		set.StopService(db)
		waitStopped(t, app)
		if !set.OperatorStopRequested() {
			t.Fatal("a requested stop cascading through a service with an old crash reason lost the request")
		}
	})

	t.Run("forced stop with a stale reason keeps it", func(t *testing.T) {
		set, _ := newTestSet()
		app := NewInternalService(set, "app")
		set.AddService(app)
		set.StartService(app)
		app.Record().stopReason = ReasonTerminated
		set.MarkOperatorStop()
		set.ForceStopService(app)
		waitStopped(t, app)
		if !set.OperatorStopRequested() {
			t.Fatal("a forced stop of a service with an old crash reason lost the request")
		}
	})
}

// A start that fails on its own is a failure too: a restart whose start
// failed after an earlier, unrelated stop request exited 0 (the holistic
// review's F2).
func TestFailedStartClearsOperatorStop(t *testing.T) {
	set, _ := newTestSet()
	app := NewProcessService(set, "app")
	app.SetCommand([]string{"/nonexistent/binary"})
	set.AddService(app)
	set.MarkOperatorStop()
	set.StartService(app)
	deadline := time.Now().Add(5 * time.Second)
	for app.State() != StateStopped {
		if time.Now().After(deadline) {
			t.Fatalf("app still %v", app.State())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if set.OperatorStopRequested() {
		t.Fatal("a failed start left an earlier stop request standing")
	}
}

// A start the operator's own stop pin refuses is the operator's will, not
// a failure: an earlier stop request stands.
func TestPinRefusedStartKeepsOperatorStop(t *testing.T) {
	set, _ := newTestSet()
	db := NewInternalService(set, "db")
	app := NewInternalService(set, "app")
	set.AddService(db)
	set.AddService(app)
	app.Record().AddDep(db, DepRegular)
	set.PinStopService(db)
	set.MarkOperatorStop()
	set.StartService(app) // db's start, as app's dependency, is refused by the pin
	if !set.OperatorStopRequested() {
		t.Fatal("a start refused by the operator's stop pin took back the stop request")
	}
}

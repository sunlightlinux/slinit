package service

import (
	"testing"
	"time"
)

func TestRuntimeMaxStopsServiceAfterCap(t *testing.T) {
	set, _ := newTestSet()

	svc := NewInternalService(set, "capped")
	set.AddService(svc)
	svc.Record().SetRuntimeMax(50 * time.Millisecond)

	set.StartService(svc)
	if svc.State() != StateStarted {
		t.Fatalf("expected STARTED, got %v", svc.State())
	}

	// Wait for the cap timer to fire. The handler grabs queueMu and
	// runs Stop() + processQueuesLocked, so by the time we observe the
	// state from this goroutine it should be STOPPED.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if svc.State() == StateStopped {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if svc.State() != StateStopped {
		t.Errorf("service should be STOPPED after runtime-max-sec fired, got %v", svc.State())
	}
}

func TestRuntimeMaxCancelledOnEarlyStop(t *testing.T) {
	// If the operator stops the service before the cap, the timer must
	// be cancelled — otherwise the goroutine would later log a spurious
	// "runtime-max-sec reached" message and a no-op Stop call.
	set, _ := newTestSet()

	svc := NewInternalService(set, "early")
	set.AddService(svc)
	svc.Record().SetRuntimeMax(200 * time.Millisecond)

	set.StartService(svc)
	set.StopService(svc)

	if svc.Record().runtimeMaxTimer != nil {
		t.Error("runtimeMaxTimer should be nil after Stop() cancelled it")
	}

	// Sleep past the original cap window to confirm no late re-stop
	// transitions occur (would manifest as a panic from re-entry).
	time.Sleep(300 * time.Millisecond)
	if svc.State() != StateStopped {
		t.Errorf("service should remain STOPPED, got %v", svc.State())
	}
}

func TestRuntimeMaxDisabledByDefault(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "uncapped")
	set.AddService(svc)

	set.StartService(svc)
	time.Sleep(100 * time.Millisecond)

	if svc.State() != StateStarted {
		t.Errorf("uncapped service should stay STARTED, got %v", svc.State())
	}
	if svc.Record().runtimeMaxTimer != nil {
		t.Error("no timer should be armed when runtime-max-sec is unset")
	}
}

// runtime-max-sec ends the run as a timeout: with restart = yes the
// service comes back, as with systemd's RuntimeMaxSec= and Restart=
// (the daily-restart idiom); with restart = no it stays down and is
// failed. It was an operator stop, which no restart setting undoes.
func TestRuntimeMaxEndsTheRunAsAFailure(t *testing.T) {
	t.Run("restart = yes brings it back", func(t *testing.T) {
		set, _ := newTestSet()
		svc := NewProcessService(set, "daily")
		svc.SetCommand([]string{"/bin/sleep", "60"})
		svc.SetAutoRestart(RestartAlways)
		svc.Record().SetRuntimeMax(150 * time.Millisecond)
		set.AddService(svc)
		set.StartService(svc)
		defer func() {
			// Under queueMu: a restart may be reading it right now.
			set.queueMu.Lock()
			svc.Record().SetRuntimeMax(0)
			set.queueMu.Unlock()
			set.StopService(svc)
			waitForState(t, svc, StateStopped, 5*time.Second)
		}()
		waitForState(t, svc, StateStarted, 2*time.Second)
		first := svc.PID()
		deadline := time.Now().Add(3 * time.Second)
		for svc.PID() == first || svc.State() != StateStarted {
			if time.Now().After(deadline) {
				t.Fatalf("not restarted after runtime-max-sec: state %v pid %d (first %d)", svc.State(), svc.PID(), first)
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	t.Run("restart = no leaves it failed", func(t *testing.T) {
		set, _ := newTestSet()
		svc := NewProcessService(set, "once")
		svc.SetCommand([]string{"/bin/sleep", "60"})
		svc.Record().SetRuntimeMax(100 * time.Millisecond)
		set.AddService(svc)
		set.StartService(svc)
		waitForState(t, svc, StateStarted, 2*time.Second)
		if got := waitForState(t, svc, StateStopped, 3*time.Second); got != StateStopped {
			t.Fatalf("still %v after runtime-max-sec", got)
		}
		if !svc.Record().IsFailed() {
			t.Error("a run ended by runtime-max-sec is not failed")
		}
	})
}

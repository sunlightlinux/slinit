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

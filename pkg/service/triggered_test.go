package service

import (
	"testing"
)

func TestTriggeredServiceStartWithoutTrigger(t *testing.T) {
	set, _ := newTestSet()

	svc := NewTriggeredService(set, "triggered-svc")
	set.AddService(svc)

	set.StartService(svc)

	// Without trigger, should remain in STARTING state
	if svc.State() != StateStarting {
		t.Errorf("expected STARTING without trigger, got %v", svc.State())
	}
}

func TestTriggeredServiceSetTriggerWhileStarting(t *testing.T) {
	set, logger := newTestSet()

	svc := NewTriggeredService(set, "triggered-svc")
	set.AddService(svc)

	set.StartService(svc)

	if svc.State() != StateStarting {
		t.Fatalf("expected STARTING, got %v", svc.State())
	}

	// Now trigger it
	svc.SetTrigger(true)

	if svc.State() != StateStarted {
		t.Errorf("expected STARTED after trigger, got %v", svc.State())
	}
	if len(logger.started) != 1 || logger.started[0] != "triggered-svc" {
		t.Errorf("expected ServiceStarted notification")
	}
}

func TestTriggeredServicePreTriggered(t *testing.T) {
	set, _ := newTestSet()

	svc := NewTriggeredService(set, "triggered-svc")
	set.AddService(svc)

	// Trigger before starting
	svc.SetTrigger(true)

	set.StartService(svc)

	// Should go directly to STARTED
	if svc.State() != StateStarted {
		t.Errorf("expected STARTED when pre-triggered, got %v", svc.State())
	}
}

func TestTriggeredServiceStop(t *testing.T) {
	set, _ := newTestSet()

	svc := NewTriggeredService(set, "triggered-svc")
	set.AddService(svc)

	svc.SetTrigger(true)
	set.StartService(svc)

	if svc.State() != StateStarted {
		t.Fatalf("expected STARTED, got %v", svc.State())
	}

	set.StopService(svc)

	if svc.State() != StateStopped {
		t.Errorf("expected STOPPED, got %v", svc.State())
	}
}

func TestTriggeredServiceWithDependency(t *testing.T) {
	set, _ := newTestSet()

	dep := NewInternalService(set, "dep-svc")
	svc := NewTriggeredService(set, "triggered-svc")
	set.AddService(dep)
	set.AddService(svc)

	svc.Record().AddDep(dep, DepRegular)

	set.StartService(svc)

	// Dep should start, triggered service should be in STARTING (waiting for trigger)
	if dep.State() != StateStarted {
		t.Errorf("dep should be STARTED, got %v", dep.State())
	}
	if svc.State() != StateStarting {
		t.Errorf("triggered svc should be STARTING (deps ok, no trigger), got %v", svc.State())
	}

	// Now trigger
	svc.SetTrigger(true)

	if svc.State() != StateStarted {
		t.Errorf("expected STARTED after trigger with deps satisfied, got %v", svc.State())
	}
}

func TestTriggeredServiceCancelStart(t *testing.T) {
	set, _ := newTestSet()

	svc := NewTriggeredService(set, "triggered-svc")
	set.AddService(svc)

	set.StartService(svc)

	if svc.State() != StateStarting {
		t.Fatalf("expected STARTING, got %v", svc.State())
	}

	// Stop before triggering
	svc.Stop(true)
	set.ProcessQueues()

	if svc.State() != StateStopped {
		t.Errorf("expected STOPPED after cancel, got %v", svc.State())
	}
}

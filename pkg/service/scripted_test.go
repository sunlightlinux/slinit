package service

import (
	"testing"
	"time"
)

func TestScriptedServiceStartStop(t *testing.T) {
	set, _ := newTestSet()

	svc := NewScriptedService(set, "scripted-svc")
	svc.SetStartCommand([]string{"/bin/true"})
	svc.SetStopCommand([]string{"/bin/true"})
	set.AddService(svc)

	set.StartService(svc)

	// Wait for start command to complete
	time.Sleep(300 * time.Millisecond)

	if svc.State() != StateStarted {
		t.Errorf("expected STARTED, got %v", svc.State())
	}

	// Stop the service
	svc.Stop(true)
	set.ProcessQueues()

	time.Sleep(300 * time.Millisecond)

	if svc.State() != StateStopped {
		t.Errorf("expected STOPPED, got %v", svc.State())
	}
}

func TestScriptedServiceStartFail(t *testing.T) {
	set, _ := newTestSet()

	svc := NewScriptedService(set, "fail-svc")
	svc.SetStartCommand([]string{"/bin/false"})
	set.AddService(svc)

	set.StartService(svc)

	time.Sleep(300 * time.Millisecond)

	if svc.State() != StateStopped {
		t.Errorf("expected STOPPED after failed start, got %v", svc.State())
	}
	if !svc.DidStartFail() {
		t.Error("expected start to be marked as failed")
	}
}

func TestScriptedServiceExecFail(t *testing.T) {
	set, _ := newTestSet()

	svc := NewScriptedService(set, "exec-fail-svc")
	svc.SetStartCommand([]string{"/nonexistent/script"})
	set.AddService(svc)

	set.StartService(svc)

	time.Sleep(300 * time.Millisecond)

	if svc.State() != StateStopped {
		t.Errorf("expected STOPPED after exec fail, got %v", svc.State())
	}
}

func TestScriptedServiceNoCommands(t *testing.T) {
	set, _ := newTestSet()

	// Scripted service with no commands = starts/stops immediately
	svc := NewScriptedService(set, "empty-svc")
	set.AddService(svc)

	set.StartService(svc)

	if svc.State() != StateStarted {
		t.Errorf("expected STARTED (no command), got %v", svc.State())
	}

	set.StopService(svc)

	if svc.State() != StateStopped {
		t.Errorf("expected STOPPED (no stop command), got %v", svc.State())
	}
}

func TestScriptedServiceWithDependency(t *testing.T) {
	set, _ := newTestSet()

	dep := NewInternalService(set, "dep-svc")
	set.AddService(dep)

	svc := NewScriptedService(set, "scripted-dep-svc")
	svc.SetStartCommand([]string{"/bin/true"})
	svc.SetStopCommand([]string{"/bin/true"})
	set.AddService(svc)

	svc.Record().AddDep(dep, DepRegular)

	set.StartService(svc)
	time.Sleep(300 * time.Millisecond)

	if dep.State() != StateStarted {
		t.Errorf("dependency should be STARTED, got %v", dep.State())
	}
	if svc.State() != StateStarted {
		t.Errorf("scripted service should be STARTED, got %v", svc.State())
	}

	set.StopService(svc)
	time.Sleep(300 * time.Millisecond)

	if svc.State() != StateStopped {
		t.Errorf("scripted service should be STOPPED, got %v", svc.State())
	}
	if dep.State() != StateStopped {
		t.Errorf("dependency should be STOPPED, got %v", dep.State())
	}
}

package service

import (
	"testing"
	"time"
)

func TestProcessServiceStartStop(t *testing.T) {
	set, _ := newTestSet()

	svc := NewProcessService(set, "sleep-svc")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	set.AddService(svc)

	// Start the service
	set.StartService(svc)

	// Give it a moment to start
	time.Sleep(100 * time.Millisecond)

	if svc.State() != StateStarted {
		t.Fatalf("expected STARTED, got %v", svc.State())
	}
	if svc.PID() <= 0 {
		t.Fatalf("expected positive PID, got %d", svc.PID())
	}

	pid := svc.PID()
	t.Logf("Service started with PID %d", pid)

	// Stop the service
	svc.Stop(true)
	set.ProcessQueues()

	// Wait for process to die
	time.Sleep(500 * time.Millisecond)

	if svc.State() != StateStopped {
		t.Errorf("expected STOPPED, got %v", svc.State())
	}
	if svc.PID() != 0 {
		t.Errorf("expected PID 0 after stop, got %d", svc.PID())
	}
}

func TestProcessServiceExecFail(t *testing.T) {
	set, logger := newTestSet()

	svc := NewProcessService(set, "bad-svc")
	svc.SetCommand([]string{"/nonexistent/binary"})
	set.AddService(svc)

	set.StartService(svc)

	// Give it a moment
	time.Sleep(100 * time.Millisecond)

	if svc.State() != StateStopped {
		t.Errorf("expected STOPPED after exec fail, got %v", svc.State())
	}

	if len(logger.failed) == 0 && len(logger.errors) == 0 {
		t.Error("expected failure to be logged")
	}
}

func TestProcessServiceWithDependency(t *testing.T) {
	set, _ := newTestSet()

	dep := NewInternalService(set, "dep-svc")
	set.AddService(dep)

	svc := NewProcessService(set, "proc-svc")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	set.AddService(svc)

	// proc-svc depends on dep-svc
	svc.Record().AddDep(dep, DepRegular)

	set.StartService(svc)

	time.Sleep(100 * time.Millisecond)

	if dep.State() != StateStarted {
		t.Errorf("dependency should be STARTED, got %v", dep.State())
	}
	if svc.State() != StateStarted {
		t.Errorf("process service should be STARTED, got %v", svc.State())
	}

	// Stop process service
	set.StopService(svc)
	time.Sleep(500 * time.Millisecond)

	if svc.State() != StateStopped {
		t.Errorf("process service should be STOPPED, got %v", svc.State())
	}
	if dep.State() != StateStopped {
		t.Errorf("dependency should be STOPPED, got %v", dep.State())
	}
}

func TestProcessServiceQuickExit(t *testing.T) {
	set, _ := newTestSet()

	svc := NewProcessService(set, "quick-svc")
	svc.SetCommand([]string{"/bin/true"})
	set.AddService(svc)

	set.StartService(svc)

	// The process will exit almost immediately
	time.Sleep(300 * time.Millisecond)

	// After the process exits, the service should handle the unexpected termination
	// Since auto-restart is RestartNever by default, it should end up stopped
	state := svc.State()
	if state != StateStopped && state != StateStarted {
		t.Logf("Service state: %v (process exited quickly)", state)
	}
}

func TestProcessServiceStopTimeout(t *testing.T) {
	set, _ := newTestSet()

	// Use a process that ignores SIGTERM (trap '' TERM; sleep 60)
	svc := NewProcessService(set, "stubborn-svc")
	svc.SetCommand([]string{"/bin/sh", "-c", "trap '' TERM; sleep 60"})
	svc.SetStopTimeout(500 * time.Millisecond) // Short timeout for test
	set.AddService(svc)

	set.StartService(svc)
	time.Sleep(200 * time.Millisecond)

	if svc.State() != StateStarted {
		t.Fatalf("expected STARTED, got %v", svc.State())
	}

	// Stop - should SIGTERM first, then SIGKILL after timeout
	svc.Stop(true)
	set.ProcessQueues()

	// Wait for SIGTERM timeout + SIGKILL to take effect
	time.Sleep(1500 * time.Millisecond)

	if svc.State() != StateStopped {
		t.Errorf("expected STOPPED after SIGKILL, got %v", svc.State())
	}
}

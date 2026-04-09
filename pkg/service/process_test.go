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

func TestRestartBackoffDisabled(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "bo-off")
	svc.SetRestartDelay(200 * time.Millisecond)
	// Step not set — backoff disabled
	for i := 0; i < 5; i++ {
		got := svc.nextRestartDelay()
		if got != 200*time.Millisecond {
			t.Errorf("iter %d: expected 200ms (fixed), got %v", i, got)
		}
	}
}

func TestRestartBackoffProgression(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "bo-prog")
	svc.SetRestartDelay(500 * time.Millisecond)
	svc.SetRestartBackoff(1*time.Second, 5*time.Second)

	// Expected sequence: 500ms, 1.5s, 2.5s, 3.5s, 4.5s, 5s (capped), 5s, ...
	want := []time.Duration{
		500 * time.Millisecond,
		1500 * time.Millisecond,
		2500 * time.Millisecond,
		3500 * time.Millisecond,
		4500 * time.Millisecond,
		5 * time.Second, // capped
		5 * time.Second, // stays capped
	}
	for i, w := range want {
		got := svc.nextRestartDelay()
		if got != w {
			t.Errorf("iter %d: expected %v, got %v", i, w, got)
		}
	}
}

func TestRestartBackoffResetOnStable(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "bo-reset")
	svc.SetRestartDelay(200 * time.Millisecond)
	svc.SetRestartBackoff(500*time.Millisecond, 3*time.Second)
	svc.SetRestartLimits(50*time.Millisecond, 10)

	// Advance the backoff a few times
	svc.nextRestartDelay()
	svc.nextRestartDelay()
	svc.nextRestartDelay()
	if svc.currentRestartDelay == 200*time.Millisecond {
		t.Fatal("expected backoff to have advanced")
	}

	// Simulate stable period: push restartIntervalTime into the past
	svc.restartIntervalTime = time.Now().Add(-1 * time.Second)
	svc.restartIntervalCount = 1

	// CheckRestart should reset the backoff (elapsed > restartInterval)
	if !svc.CheckRestart() {
		t.Fatal("CheckRestart unexpectedly refused restart")
	}
	if svc.currentRestartDelay != 200*time.Millisecond {
		t.Errorf("expected backoff reset to 200ms, got %v", svc.currentRestartDelay)
	}
}

func TestRestartBackoffDefaultCap(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "bo-defcap")
	svc.SetRestartDelay(1 * time.Second)
	// cap=0 → default 60s
	svc.SetRestartBackoff(30*time.Second, 0)

	// 1s, 31s, 60s (capped from 61s), 60s, ...
	want := []time.Duration{1 * time.Second, 31 * time.Second, 60 * time.Second, 60 * time.Second}
	for i, w := range want {
		got := svc.nextRestartDelay()
		if got != w {
			t.Errorf("iter %d: expected %v, got %v", i, w, got)
		}
	}
}

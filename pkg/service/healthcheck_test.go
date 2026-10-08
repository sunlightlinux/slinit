package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHealthChecker_Healthy(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "hc-healthy")

	marker := filepath.Join(t.TempDir(), "healthy")
	hc := NewHealthChecker(svc, []string{"/bin/sh", "-c", "echo ok > " + marker},
		50*time.Millisecond, 0, 3, nil, set.logger, nil)

	hc.Start()
	time.Sleep(200 * time.Millisecond)
	hc.Stop()

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("health check command did not execute: %v", err)
	}
	if hc.ConsecutiveFailures() != 0 {
		t.Errorf("expected 0 failures, got %d", hc.ConsecutiveFailures())
	}
}

func TestHealthChecker_Failure(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "hc-fail")

	hc := NewHealthChecker(svc, []string{"/bin/sh", "-c", "exit 1"},
		50*time.Millisecond, 0, 0, nil, set.logger, nil)

	hc.Start()
	time.Sleep(200 * time.Millisecond)
	hc.Stop()

	if hc.ConsecutiveFailures() == 0 {
		t.Error("expected failures > 0")
	}
}

func TestHealthChecker_MaxFailuresRestart(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "hc-restart")

	restartCalled := false
	onFail := func() { restartCalled = true }

	hc := NewHealthChecker(svc, []string{"/bin/sh", "-c", "exit 1"},
		50*time.Millisecond, 0, 3, nil, set.logger, onFail)

	hc.Start()
	time.Sleep(300 * time.Millisecond)
	hc.Stop()

	if !restartCalled {
		t.Error("expected onFail callback after max failures")
	}
}

func TestHealthChecker_RecoveryResetsCounter(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "hc-recover")

	// Command that fails once then succeeds
	dir := t.TempDir()
	marker := filepath.Join(dir, "attempt")

	// Script: fail if marker doesn't exist, then create it and succeed next time
	cmd := []string{"/bin/sh", "-c",
		"if [ -f " + marker + " ]; then exit 0; else touch " + marker + " && exit 1; fi"}

	hc := NewHealthChecker(svc, cmd,
		50*time.Millisecond, 0, 5, nil, set.logger, nil)

	hc.Start()
	time.Sleep(200 * time.Millisecond)
	hc.Stop()

	// After recovery, counter should be reset to 0
	if hc.ConsecutiveFailures() != 0 {
		t.Errorf("expected 0 failures after recovery, got %d", hc.ConsecutiveFailures())
	}
}

func TestHealthChecker_UnhealthyCommand(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "hc-unhealthy")

	marker := filepath.Join(t.TempDir(), "unhealthy-ran")
	unhealthyCmd := []string{"/bin/sh", "-c", "touch " + marker}

	hc := NewHealthChecker(svc, []string{"/bin/sh", "-c", "exit 1"},
		50*time.Millisecond, 0, 0, unhealthyCmd, set.logger, nil)

	hc.Start()
	time.Sleep(150 * time.Millisecond)
	hc.Stop()

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("unhealthy-command did not execute: %v", err)
	}
}

func TestHealthChecker_InitialDelay(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "hc-delay")

	marker := filepath.Join(t.TempDir(), "delayed")
	hc := NewHealthChecker(svc, []string{"/bin/sh", "-c", "touch " + marker},
		50*time.Millisecond, 200*time.Millisecond, 0, nil, set.logger, nil)

	hc.Start()
	time.Sleep(100 * time.Millisecond)
	// Should NOT have run yet (delay is 200ms)
	if _, err := os.Stat(marker); err == nil {
		t.Error("health check ran before delay expired")
	}

	time.Sleep(200 * time.Millisecond)
	hc.Stop()

	// Now it should have run
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("health check did not run after delay: %v", err)
	}
}

func TestHealthChecker_DoubleStartStop(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "hc-double")

	hc := NewHealthChecker(svc, []string{"/bin/true"}, time.Second, 0, 0, nil, set.logger, nil)

	// Double start/stop should not panic
	hc.Start()
	hc.Start()
	hc.Stop()
	hc.Stop()
}

// An unhealthy service must go down even when a started dependent holds
// it. onFail used to call Stop(false), which only dropped the explicit
// activation: with a dependent still requiring it, nothing happened.
func TestHealthCheckStopsServiceHeldByDependent(t *testing.T) {
	set, _ := newTestSet()
	sick := NewProcessService(set, "hc-sick")
	sick.SetCommand([]string{"/bin/sh", "-c", "while :; do sleep 60; done"})
	sick.SetHealthCheck([]string{"/bin/false"}, 100*time.Millisecond, 0, 2, nil)
	set.AddService(sick)

	user := NewProcessService(set, "hc-user")
	user.SetCommand([]string{"/bin/sh", "-c", "while :; do sleep 60; done"})
	user.Record().AddDep(sick, DepRegular)
	set.AddService(user)

	set.StartService(sick)
	set.StartService(user)
	if got := waitState(t, user, StateStarted, 3*time.Second); got != StateStarted {
		t.Fatalf("dependent state = %v, want STARTED", got)
	}

	if got := waitState(t, sick, StateStopped, 5*time.Second); got != StateStopped {
		t.Fatalf("unhealthy service state = %v, want STOPPED", got)
	}
	// Its hard dependent goes down with it, as for any forced stop.
	if got := waitState(t, user, StateStopped, 5*time.Second); got != StateStopped {
		t.Errorf("hard dependent state = %v, want STOPPED", got)
	}
}

// With restart = yes the process is replaced and checking resumes: the
// checker used to refuse to Start again after its first Stop, so an
// unhealthy service was restarted once and then never checked again.
func TestHealthCheckRestartsAndKeepsChecking(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "hc-restart")
	svc.SetCommand([]string{"/bin/sh", "-c", "while :; do sleep 60; done"})
	svc.Record().SetAutoRestart(RestartAlways)
	svc.SetRestartDelay(50 * time.Millisecond)
	svc.SetRestartLimits(time.Minute, 100)
	svc.SetHealthCheck([]string{"/bin/false"}, 100*time.Millisecond, 0, 2, nil)
	set.AddService(svc)
	defer func() {
		set.StopService(svc)
		waitState(t, svc, StateStopped, 5*time.Second)
	}()

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 3*time.Second); got != StateStarted {
		t.Fatalf("state = %v, want STARTED", got)
	}

	seen := map[int]bool{svc.PID(): true}
	deadline := time.Now().Add(8 * time.Second)
	for len(seen) < 3 && time.Now().Before(deadline) {
		if pid := svc.PID(); pid > 0 {
			seen[pid] = true
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(seen) < 3 {
		t.Fatalf("saw %d distinct PIDs, want 3: the health check did not keep restarting the service", len(seen))
	}
}

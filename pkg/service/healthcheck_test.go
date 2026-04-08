package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHealthChecker_Healthy(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set,"hc-healthy")

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
	svc := NewInternalService(set,"hc-fail")

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
	svc := NewInternalService(set,"hc-restart")

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
	svc := NewInternalService(set,"hc-recover")

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
	svc := NewInternalService(set,"hc-unhealthy")

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
	svc := NewInternalService(set,"hc-delay")

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
	svc := NewInternalService(set,"hc-double")

	hc := NewHealthChecker(svc, []string{"/bin/true"}, time.Second, 0, 0, nil, set.logger, nil)

	// Double start/stop should not panic
	hc.Start()
	hc.Start()
	hc.Stop()
	hc.Stop()
}

package service

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// hcWait polls cond until it holds or 5 s pass. These tests used to sleep a
// fixed 150-300 ms and count on one to three 50 ms checks fitting inside it;
// under CI load a check ran late or, timed out at its interval, counted as a
// failure, and TestHealthChecker_Healthy failed a release's tarball check.
func hcWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func fileExists(path string) func() bool {
	return func() bool { _, err := os.Stat(path); return err == nil }
}

// A check expected to pass gets an interval, which is also its time limit, long
// enough that a loaded machine does not turn it into a failure.
const hcPassInterval = 2 * time.Second

func TestHealthChecker_Healthy(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "hc-healthy")

	marker := filepath.Join(t.TempDir(), "healthy")
	hc := NewHealthChecker(svc, []string{"/bin/sh", "-c", "echo ok > " + marker},
		hcPassInterval, 0, 3, nil, set.logger, nil)

	hc.Start()
	hcWait(t, "the health check to run", fileExists(marker))
	hc.Stop() // waits for the check under way to be counted

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
	hcWait(t, "a failure to be counted", func() bool { return hc.ConsecutiveFailures() > 0 })
	hc.Stop()
}

func TestHealthChecker_MaxFailuresRestart(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "hc-restart")

	var restartCalled atomic.Bool
	onFail := func() { restartCalled.Store(true) }

	hc := NewHealthChecker(svc, []string{"/bin/sh", "-c", "exit 1"},
		50*time.Millisecond, 0, 3, nil, set.logger, onFail)

	hc.Start()
	hcWait(t, "the restart after 3 failures", restartCalled.Load)
	hc.Stop()
}

func TestHealthChecker_RecoveryResetsCounter(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "hc-recover")

	// Fails the first time, succeeds after; "ok" marks a success having run.
	dir := t.TempDir()
	attempt, ok := filepath.Join(dir, "attempt"), filepath.Join(dir, "ok")
	cmd := []string{"/bin/sh", "-c",
		"if [ -f " + attempt + " ]; then touch " + ok + "; exit 0; else touch " + attempt + "; exit 1; fi"}

	// Short enough for the second check to come soon, long enough not to be
	// a time limit a loaded machine runs into.
	hc := NewHealthChecker(svc, cmd, 500*time.Millisecond, 0, 5, nil, set.logger, nil)

	hc.Start()
	hcWait(t, "the failing first check", func() bool { return hc.ConsecutiveFailures() > 0 })
	hcWait(t, "the passing second check", fileExists(ok))
	hc.Stop()

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
	hcWait(t, "unhealthy-command to run", fileExists(marker))
	hc.Stop()
}

func TestHealthChecker_InitialDelay(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "hc-delay")

	// A delay far longer than the probe, so a loaded machine oversleeping the
	// probe does not reach it.
	marker := filepath.Join(t.TempDir(), "delayed")
	hc := NewHealthChecker(svc, []string{"/bin/sh", "-c", "touch " + marker},
		hcPassInterval, 1500*time.Millisecond, 0, nil, set.logger, nil)

	start := time.Now()
	hc.Start()
	time.Sleep(200 * time.Millisecond)
	if fileExists(marker)() && time.Since(start) < 1500*time.Millisecond {
		t.Error("health check ran before delay expired")
	}

	hcWait(t, "the delayed health check", fileExists(marker))
	hc.Stop()
	if time.Since(start) < 1500*time.Millisecond {
		t.Errorf("health check ran %v after start, before the 1.5 s delay", time.Since(start))
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

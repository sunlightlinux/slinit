package service

import (
	"os"
	"testing"
	"time"
)

// TestWatchdogKeepalivesKeepServiceRunning runs a shell that sends an
// initial readiness byte and then keeps writing to the same fd every
// 50 ms. The watchdog timeout is 300 ms — well above the keepalive
// interval — so the service must stay STARTED for the duration of the
// test without being restarted.
func TestWatchdogKeepalivesKeepServiceRunning(t *testing.T) {
	set, _ := newTestSet()

	svc := NewProcessService(set, "wd-keepalive")
	svc.SetCommand([]string{"/bin/sh", "-c",
		"echo ready >&3; while :; do sleep 0.05; printf p >&3; done"})
	svc.SetReadyNotification(3, "")
	svc.SetWatchdogTimeout(300 * time.Millisecond)
	svc.SetStartTimeout(2 * time.Second)
	set.AddService(svc)

	set.StartService(svc)

	// Wait for readiness
	time.Sleep(300 * time.Millisecond)
	if svc.State() != StateStarted {
		t.Fatalf("expected STARTED after readiness, got %v", svc.State())
	}

	// Hold for ~3 timeout windows. With 50 ms keepalives the watchdog
	// must never expire.
	time.Sleep(900 * time.Millisecond)
	if svc.State() != StateStarted {
		t.Errorf("expected STARTED after keepalive period, got %v (watchdog fired spuriously)",
			svc.State())
	}

	set.StopService(svc)
	time.Sleep(500 * time.Millisecond)
}

// TestWatchdogTimeoutTriggersRestart runs a process that signals
// readiness once and then goes silent. The watchdog must fire and
// trigger Stop(false), which leaves the service in STOPPED (no restart
// policy is configured by default in NewProcessService).
func TestWatchdogTimeoutTriggersRestart(t *testing.T) {
	set, _ := newTestSet()

	svc := NewProcessService(set, "wd-miss")
	// Keep fd 3 open in the child so the parent's Read blocks rather
	// than seeing EOF — the watchdog timeout itself must drive the
	// test outcome.
	svc.SetCommand([]string{"/bin/sh", "-c", "echo ready >&3; sleep 60"})
	svc.SetReadyNotification(3, "")
	svc.SetWatchdogTimeout(500 * time.Millisecond)
	svc.SetStartTimeout(2 * time.Second)
	set.AddService(svc)

	set.StartService(svc)

	// Wait briefly to confirm we hit STARTED before the watchdog fires.
	time.Sleep(150 * time.Millisecond)
	if svc.State() != StateStarted {
		t.Fatalf("expected STARTED after readiness, got %v", svc.State())
	}

	// Wait past the watchdog timeout + state-machine processing slack.
	time.Sleep(900 * time.Millisecond)

	state := svc.State()
	if state != StateStopped && state != StateStopping {
		t.Errorf("expected STOPPED/STOPPING after watchdog miss, got %v", state)
	}
}

// TestWatchdogClosedOnStop ensures the watchdog goroutine releases its
// pipe and channels when the service is stopped explicitly. Mostly a
// regression guard against goroutine leaks.
func TestWatchdogClosedOnStop(t *testing.T) {
	set, _ := newTestSet()

	svc := NewProcessService(set, "wd-stop")
	svc.SetCommand([]string{"/bin/sh", "-c",
		"echo ready >&3; while :; do sleep 0.05; printf p >&3; done"})
	svc.SetReadyNotification(3, "")
	svc.SetWatchdogTimeout(500 * time.Millisecond)
	set.AddService(svc)

	set.StartService(svc)
	time.Sleep(300 * time.Millisecond)
	if svc.State() != StateStarted {
		t.Fatalf("expected STARTED, got %v", svc.State())
	}

	set.StopService(svc)
	time.Sleep(500 * time.Millisecond)

	if svc.State() != StateStopped {
		t.Errorf("expected STOPPED after explicit stop, got %v", svc.State())
	}
	// Internal references should have been cleared by stopWatchdogWatcher.
	if svc.watchdogStop != nil {
		t.Error("watchdogStop not cleared on Stop")
	}
}

// TestNoWatchdogDoesNotChangeReadySemantics is a regression test: a
// service without watchdog-timeout configured must keep the original
// dinit-compatible "single-shot ready" behavior. The service signals
// readiness, closes its end of the pipe, and lives indefinitely —
// the parent must NOT keep the pipe open or arm any timer.
func TestNoWatchdogDoesNotChangeReadySemantics(t *testing.T) {
	set, _ := newTestSet()

	svc := NewProcessService(set, "no-wd")
	svc.SetCommand([]string{"/bin/sh", "-c", "echo ready >&3; sleep 60"})
	svc.SetReadyNotification(3, "")
	// No SetWatchdogTimeout call.
	set.AddService(svc)

	set.StartService(svc)
	time.Sleep(300 * time.Millisecond)
	if svc.State() != StateStarted {
		t.Fatalf("expected STARTED, got %v", svc.State())
	}

	// Hold for several hundred ms; with no watchdog there is nothing
	// armed against the silent service.
	time.Sleep(500 * time.Millisecond)
	if svc.State() != StateStarted {
		t.Errorf("expected STARTED with no watchdog, got %v", svc.State())
	}

	set.StopService(svc)
	time.Sleep(500 * time.Millisecond)
}

// TestWatchdogTriggersRestartOnFailure regression-tests the path where
// a watchdog miss must respect the configured restart policy. Previous
// behavior went through Stop(false), which clobbered desired=Stopped
// when requiredBy was 0 (e.g. soft waits-for activation), and Release()
// further sealed the fate by preventing the post-exit restart in
// Stopped(). The fix evaluates the policy in fireWatchdogStop and
// passes withRestart through to doStop so desired stays at Started.
func TestWatchdogTriggersRestartOnFailure(t *testing.T) {
	set, _ := newTestSet()
	tmp, err := os.CreateTemp("", "wd-starts-*")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	svc := NewProcessService(set, "wd-restart")
	svc.SetCommand([]string{"/bin/sh", "-c",
		"date +%s%N >> " + tmp.Name() + "; printf r >&3; sleep 60"})
	svc.SetReadyNotification(3, "")
	svc.SetWatchdogTimeout(300 * time.Millisecond)
	svc.SetStartTimeout(2 * time.Second)
	svc.SetAutoRestart(RestartOnFailure)
	svc.SetRestartLimits(time.Minute, 5)
	svc.SetRestartDelay(50 * time.Millisecond)
	set.AddService(svc)

	set.StartService(svc)

	// First start + watchdog fires (~300ms) + SIGTERM exit + restart.
	time.Sleep(2500 * time.Millisecond)

	data, _ := os.ReadFile(tmp.Name())
	starts := 0
	for _, b := range data {
		if b == '\n' {
			starts++
		}
	}
	if starts < 2 {
		t.Errorf("expected >=2 starts, got %d (file=%q)", starts, string(data))
	}
}

func TestHasWatchdogAccessor(t *testing.T) {
	set, _ := newTestSet()

	svc := NewProcessService(set, "wd-acc")
	if svc.HasWatchdog() {
		t.Error("HasWatchdog() = true before SetWatchdogTimeout")
	}
	svc.SetWatchdogTimeout(time.Second)
	if !svc.HasWatchdog() {
		t.Error("HasWatchdog() = false after SetWatchdogTimeout")
	}
	if svc.WatchdogTimeout() != time.Second {
		t.Errorf("WatchdogTimeout() = %v, want 1s", svc.WatchdogTimeout())
	}
}

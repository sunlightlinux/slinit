package service

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProcessServiceWithReadyNotification(t *testing.T) {
	set, _ := newTestSet()

	// Use a shell script that writes to fd 3 after a short delay
	svc := NewProcessService(set, "ready-svc")
	svc.SetCommand([]string{"/bin/sh", "-c", "sleep 0.2; echo ready >&3; sleep 60"})
	svc.SetReadyNotification(3, "")
	svc.SetStartTimeout(5 * time.Second)
	set.AddService(svc)

	set.StartService(svc)

	// After 50ms, should still be STARTING (waiting for readiness)
	time.Sleep(50 * time.Millisecond)
	if svc.State() != StateStarting {
		t.Errorf("expected STARTING while waiting for readiness, got %v", svc.State())
	}

	// After 500ms, readiness should have been received (script writes at ~200ms)
	time.Sleep(450 * time.Millisecond)
	if svc.State() != StateStarted {
		t.Errorf("expected STARTED after readiness notification, got %v", svc.State())
	}

	// Clean up
	set.StopService(svc)
	time.Sleep(500 * time.Millisecond)
}

func TestReadyNotificationTimeout(t *testing.T) {
	set, _ := newTestSet()

	// Process that never writes to the notification fd
	svc := NewProcessService(set, "timeout-svc")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	svc.SetReadyNotification(3, "")
	svc.SetStartTimeout(300 * time.Millisecond) // Short timeout for test
	set.AddService(svc)

	set.StartService(svc)

	// After 50ms, should be STARTING
	time.Sleep(50 * time.Millisecond)
	if svc.State() != StateStarting {
		t.Errorf("expected STARTING, got %v", svc.State())
	}

	// After timeout (300ms) + processing time, should have failed
	time.Sleep(800 * time.Millisecond)
	state := svc.State()
	if state != StateStopped && state != StateStopping {
		t.Errorf("expected STOPPED or STOPPING after timeout, got %v", state)
	}
}

func TestReadyNotificationProcessExitsWithoutReady(t *testing.T) {
	set, _ := newTestSet()

	// Process that exits without ever writing to the notification fd
	svc := NewProcessService(set, "exit-svc")
	svc.SetCommand([]string{"/bin/sh", "-c", "exit 1"})
	svc.SetReadyNotification(3, "")
	svc.SetStartTimeout(5 * time.Second)
	set.AddService(svc)

	set.StartService(svc)

	// Wait for process to exit and state machine to process
	time.Sleep(500 * time.Millisecond)

	state := svc.State()
	// Process exited while STARTING = failed to start
	if state != StateStopped {
		t.Errorf("expected STOPPED after process exit without readiness, got %v", state)
	}
}

func TestReadyNotificationPipevar(t *testing.T) {
	set, _ := newTestSet()

	// Process reads NOTIFY_FD env var and writes to that fd
	svc := NewProcessService(set, "pipevar-svc")
	svc.SetCommand([]string{"/bin/sh", "-c", "echo ready >&$NOTIFY_FD; sleep 60"})
	svc.SetReadyNotification(-1, "NOTIFY_FD")
	svc.SetStartTimeout(5 * time.Second)
	set.AddService(svc)

	set.StartService(svc)

	// Wait for readiness
	time.Sleep(500 * time.Millisecond)
	if svc.State() != StateStarted {
		t.Errorf("expected STARTED after pipevar notification, got %v", svc.State())
	}

	// Clean up
	set.StopService(svc)
	time.Sleep(500 * time.Millisecond)
}

// TestSmoothRecoveryReadyPipeFailureTreatedAsTermination reproduces the
// class of bug dinit closed with 991fceeb (2026-08-30, "Better handling
// of smooth recovery failure"): a service with notify + smooth-recovery
// whose respawned process crashes before signalling readiness was
// silently ignored by handleReadyNotification (state != StateStarting
// early-return), leaving the service wedged at StateStarted with no
// live PID while dependents believed it was up. The fix in
// pkg/service/process.go handleReadyNotification treats ready=false
// arriving while the service is StateStarted as an unexpected
// termination.
//
// Setup: a shell command that touches a marker on first invocation
// and signals ready, but on subsequent invocations exits without
// notifying. First start → STARTED. Killing the child triggers
// doSmoothRecovery, which forks the second run; that run exits
// without ready, the pipe watcher sends ready=false, and — with the
// fix — handleUnexpectedTerminationLocked drives the state machine
// out of StateStarted. Without the fix the assertion at the end
// would find state=StateStarted with pid<=0.
func TestSmoothRecoveryReadyPipeFailureTreatedAsTermination(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran-once")
	set, logger := newTestSet()
	svc := NewProcessService(set, "smooth-notify-fail")
	// First run signals ready and sleeps normally. Second run (from
	// smooth recovery) closes fd 3 WITHOUT writing, then keeps
	// running — this is what triggers the buggy path: readyPipe
	// watcher sees EOF and fires false while the child is still
	// alive (no exit event yet racing with it). Fast-exit variants
	// let the exit event beat the ready-false event in the monitor
	// goroutine's select, orphaning readyCh entirely.
	svc.SetCommand([]string{"/bin/sh", "-c", fmt.Sprintf(
		`if [ ! -e %[1]s ]; then
			touch %[1]s
			echo ready >&3
			sleep 60
		else
			exec 3>&-
			sleep 60
		fi`, marker)})
	svc.SetReadyNotification(3, "")
	svc.SetSmoothRecovery(true)
	svc.SetStartTimeout(5 * time.Second)
	// Small restart-delay so doSmoothRecovery takes the immediate
	// branch instead of arming a timer (default 200ms would delay
	// the second-run fork past our observation window and the test
	// would time out without ever exercising the fix path).
	svc.SetRestartDelay(10 * time.Millisecond)
	set.AddService(svc)

	set.StartService(svc)
	// Wait for first-run readiness → STARTED.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && svc.State() != StateStarted {
		time.Sleep(20 * time.Millisecond)
	}
	if svc.State() != StateStarted {
		t.Fatalf("first run did not reach STARTED within 2s (got %v)", svc.State())
	}

	// Kill the first-run child to trigger smooth recovery. Read the
	// PID under queueMu because ProcessService.PID() documents an
	// ambient race with the scheduler; the lock avoids a spurious
	// race-detector report unrelated to what this test exercises.
	set.queueMu.Lock()
	pid := svc.PID()
	set.queueMu.Unlock()
	if pid <= 0 {
		t.Fatalf("expected first-run PID > 0, got %d", pid)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill first-run child pid %d: %v", pid, err)
	}

	// Give the state machine time to observe the exit, invoke
	// doSmoothRecovery → startProcess → second-run child, and for
	// that child's `exec 3>&-` to close fd 3 so the pipe watcher
	// signals ready=false.
	time.Sleep(1500 * time.Millisecond)

	// Quiesce state-machine goroutines so the log-slice read below
	// isn't racing with concurrent logger writes from restart cycles.
	set.StopService(svc)
	time.Sleep(300 * time.Millisecond)
	set.queueMu.Lock()
	errsCopy := append([]string(nil), logger.errors...)
	set.queueMu.Unlock()

	// Load-bearing invariant: the fix's "treating as unexpected
	// termination" log line must have been emitted at least once,
	// proving the state=StateStarted + ready=false branch fired.
	// Without the fix, handleReadyNotification would have returned
	// silently and left the service wedged.
	found := false
	for _, e := range errsCopy {
		if strings.Contains(e, "readiness pipe closed during smooth recovery") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'readiness pipe closed during smooth recovery' log — fix path never fired")
		t.Logf("logger.errors seen:")
		for _, e := range errsCopy {
			t.Logf("  %s", e)
		}
	}
}

func TestProcessServiceWithoutReadyNotification(t *testing.T) {
	// Verify that services without ready-notification still start immediately
	set, _ := newTestSet()

	svc := NewProcessService(set, "immediate-svc")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	// No SetReadyNotification - default behavior
	set.AddService(svc)

	set.StartService(svc)

	time.Sleep(100 * time.Millisecond)
	if svc.State() != StateStarted {
		t.Errorf("expected immediate STARTED without readiness protocol, got %v", svc.State())
	}

	// Clean up
	set.StopService(svc)
	time.Sleep(500 * time.Millisecond)
}

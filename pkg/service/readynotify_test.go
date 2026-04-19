package service

import (
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

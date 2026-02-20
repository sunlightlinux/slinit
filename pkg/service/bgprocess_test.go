package service

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// bgTestDaemonScript creates a shell script that simulates a self-backgrounding daemon:
// 1. Forks a background child (sleep)
// 2. Writes the child's PID to the PID file
// 3. Exits (launcher completes)
func bgTestDaemonScript(pidFile string, sleepSecs int) []string {
	script := fmt.Sprintf(
		`sleep %d & echo $! > %s; exit 0`,
		sleepSecs, pidFile,
	)
	return []string{"/bin/sh", "-c", script}
}

func TestBGProcessServiceStartStop(t *testing.T) {
	set, logger := newTestSet()

	pidFile := filepath.Join(t.TempDir(), "daemon.pid")

	svc := NewBGProcessService(set, "bg-svc")
	svc.SetCommand(bgTestDaemonScript(pidFile, 60))
	svc.SetPIDFile(pidFile)
	set.AddService(svc)

	set.StartService(svc)

	// Wait for launcher to exit and PID file to be read
	time.Sleep(500 * time.Millisecond)

	if svc.State() != StateStarted {
		t.Fatalf("expected STARTED, got %v", svc.State())
	}

	daemonPID := svc.PID()
	if daemonPID <= 0 {
		t.Fatalf("expected positive daemon PID, got %d", daemonPID)
	}
	t.Logf("BGProcess daemon PID: %d", daemonPID)

	if len(logger.started) != 1 || logger.started[0] != "bg-svc" {
		t.Errorf("expected ServiceStarted notification for bg-svc")
	}

	// Stop the service
	svc.Stop(true)
	set.ProcessQueues()

	// Need to wait for SIGTERM to kill daemon + polling interval (1s) to detect death
	time.Sleep(2500 * time.Millisecond)

	if svc.State() != StateStopped {
		t.Errorf("expected STOPPED, got %v", svc.State())
	}
}

func TestBGProcessServiceNoPIDFile(t *testing.T) {
	set, _ := newTestSet()

	svc := NewBGProcessService(set, "bg-svc-no-pid")
	svc.SetCommand([]string{"/bin/true"})
	// Intentionally NOT setting PID file
	set.AddService(svc)

	set.StartService(svc)
	time.Sleep(200 * time.Millisecond)

	// Should fail because no PID file specified
	if svc.State() == StateStarted {
		t.Error("should NOT be STARTED without PID file")
	}
}

func TestBGProcessServiceBadPIDFile(t *testing.T) {
	set, _ := newTestSet()

	pidFile := filepath.Join(t.TempDir(), "daemon.pid")

	// Write garbage to the PID file before starting
	os.WriteFile(pidFile, []byte("not-a-pid\n"), 0644)

	// Script that exits successfully but PID file has garbage
	svc := NewBGProcessService(set, "bg-svc-bad-pid")
	svc.SetCommand([]string{"/bin/true"})
	svc.SetPIDFile(pidFile)
	set.AddService(svc)

	set.StartService(svc)
	time.Sleep(500 * time.Millisecond)

	// Should fail because PID file is invalid
	if svc.State() == StateStarted {
		t.Error("should NOT be STARTED with bad PID file")
	}
	if svc.State() != StateStopped {
		t.Errorf("expected STOPPED after PID file failure, got %v", svc.State())
	}
}

func TestBGProcessServiceDaemonDies(t *testing.T) {
	set, _ := newTestSet()

	pidFile := filepath.Join(t.TempDir(), "daemon.pid")

	// Daemon that dies after 1 second
	svc := NewBGProcessService(set, "bg-svc-dies")
	svc.SetCommand(bgTestDaemonScript(pidFile, 1))
	svc.SetPIDFile(pidFile)
	set.AddService(svc)

	set.StartService(svc)
	time.Sleep(500 * time.Millisecond)

	if svc.State() != StateStarted {
		t.Fatalf("expected STARTED, got %v", svc.State())
	}

	// Wait for daemon to die (1s sleep) + polling interval (1s) + margin
	time.Sleep(3 * time.Second)

	if svc.State() != StateStopped {
		t.Errorf("expected STOPPED after daemon dies, got %v", svc.State())
	}
}

func TestBGProcessServiceWithDependency(t *testing.T) {
	set, _ := newTestSet()

	pidFile := filepath.Join(t.TempDir(), "daemon.pid")

	dep := NewInternalService(set, "dep-svc")
	svc := NewBGProcessService(set, "bg-svc-dep")
	svc.SetCommand(bgTestDaemonScript(pidFile, 60))
	svc.SetPIDFile(pidFile)

	set.AddService(dep)
	set.AddService(svc)

	svc.Record().AddDep(dep, DepRegular)

	set.StartService(svc)
	time.Sleep(500 * time.Millisecond)

	if dep.State() != StateStarted {
		t.Errorf("dep should be STARTED, got %v", dep.State())
	}
	if svc.State() != StateStarted {
		t.Errorf("bg-svc should be STARTED, got %v", svc.State())
	}

	// Stop
	svc.Stop(true)
	set.ProcessQueues()
	time.Sleep(2500 * time.Millisecond)

	if svc.State() != StateStopped {
		t.Errorf("bg-svc should be STOPPED, got %v", svc.State())
	}
	if dep.State() != StateStopped {
		t.Errorf("dep should be STOPPED, got %v", dep.State())
	}
}

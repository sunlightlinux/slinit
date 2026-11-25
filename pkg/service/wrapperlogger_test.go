package service

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// An output-logger is often a wrapper script. If the wrapper's child
// holds the pipe and stops reading, killing the wrapper alone does not
// make the rotator's blocked sink write fail: the rotator's reader kept
// holding lr.mu, and a restart's LogRotator.Close — under queueMu —
// waited for it for good. Stop now kills the logger's process group,
// and Close cuts off a sink write still in progress.
func TestWrapperLoggerChildStalledKeepsRestartAndQueueMu(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "svc.log")
	loggerSh := filepath.Join(dir, "logger.sh")
	writeExec(t, loggerSh, "#!/bin/sh\nsleep 1000\n") // no exec: the child holds stdin

	set, _ := newTestSet()
	svc := NewProcessService(set, "rot")
	svc.SetCommand([]string{"/bin/sh", "-c", "exec yes 0123456789012345678901234567890123456789"})
	svc.SetLogType(LogToFile)
	svc.SetLogFileDetails(logFile, 0o600, -1, -1)
	svc.SetOutputLogger([]string{loggerSh})
	svc.SetAutoRestart(RestartAlways)
	svc.SetRestartDelay(50 * time.Millisecond)
	set.AddService(svc)
	set.StartService(svc)
	t.Cleanup(func() { set.StopService(svc) })
	if got := waitState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("service did not start: %v", got)
	}
	time.Sleep(500 * time.Millisecond) // let the pipe to the logger fill
	pid := svc.PID()
	_ = syscall.Kill(pid, syscall.SIGKILL)
	time.Sleep(2 * time.Second)
	if p := svc.PID(); p <= 0 || p == pid {
		t.Errorf("auto-restart did not happen: state=%v pid=%d", svc.State(), p)
	}
	if !queueMuFree(set, 2*time.Second) {
		t.Fatal("queueMu held across the restart (wrapper logger whose child stopped reading)")
	}
}

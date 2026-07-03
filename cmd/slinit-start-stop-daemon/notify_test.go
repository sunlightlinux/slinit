package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestApplyNotifyReadinessNoneReturnsOK(t *testing.T) {
	if code := applyNotify(Options{Notify: "readiness=none"}, os.Getpid()); code != exitOK {
		t.Errorf("readiness=none: got %d", code)
	}
}

func TestApplyNotifyMalformedIsUnsupported(t *testing.T) {
	if code := applyNotify(Options{Notify: "bogus"}, os.Getpid()); code != exitUnsupported {
		t.Errorf("malformed: got %d, want %d", code, exitUnsupported)
	}
	if code := applyNotify(Options{Notify: "readiness=fd:1"}, os.Getpid()); code != exitUnsupported {
		t.Errorf("readiness=fd:N: got %d, want %d", code, exitUnsupported)
	}
}

func TestApplyNotifyPidfileRequiresFlag(t *testing.T) {
	if code := applyNotify(Options{Notify: "readiness=pidfile"}, os.Getpid()); code != exitBadUsage {
		t.Errorf("pidfile without --pidfile: got %d, want %d", code, exitBadUsage)
	}
}

// TestApplyNotifyPidfileWaits verifies that readiness=pidfile blocks
// until the pidfile appears and returns exitOK.
func TestApplyNotifyPidfileWaits(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "svc.pid")

	// Spawn a child so processAlive() has a real target for the poll loop.
	cmd := exec.Command(sleep, "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	// Drop the pidfile from a goroutine after 200ms.
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = os.WriteFile(pidfile, []byte("123\n"), 0644)
	}()

	opts := Options{Notify: "readiness=pidfile", PidFile: pidfile, Wait: 5000}
	start := time.Now()
	code := applyNotify(opts, cmd.Process.Pid)
	elapsed := time.Since(start)
	if code != exitOK {
		t.Errorf("got %d, want %d", code, exitOK)
	}
	if elapsed < 150*time.Millisecond || elapsed > 2*time.Second {
		t.Errorf("elapsed=%v, expected between 150ms and 2s", elapsed)
	}
}

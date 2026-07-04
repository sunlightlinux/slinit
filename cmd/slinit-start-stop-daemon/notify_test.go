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

func TestApplyNotifyReadinessManualReturnsOK(t *testing.T) {
	// "manual" is application-owned readiness; we do not wait for
	// anything and treat the start as successful once the child is
	// exec'd. Same effect as "none" but preserves the semantic
	// distinction for scripts that document their intent.
	if code := applyNotify(Options{Notify: "readiness=manual"}, os.Getpid()); code != exitOK {
		t.Errorf("readiness=manual: got %d", code)
	}
}

func TestApplyNotifyMalformedIsUnsupported(t *testing.T) {
	if code := applyNotify(Options{Notify: "bogus"}, os.Getpid()); code != exitUnsupported {
		t.Errorf("malformed: got %d, want %d", code, exitUnsupported)
	}
	// fd:1 is out of range — parseNotify rejects it, applyNotify surfaces
	// it as unsupported.
	if code := applyNotify(Options{Notify: "readiness=fd:1"}, os.Getpid()); code != exitUnsupported {
		t.Errorf("readiness=fd:1 (out of range): got %d, want %d", code, exitUnsupported)
	}
	// A well-formed fd/stderr/signal mode reached via applyNotify (no
	// spawn state) should also error — those modes are pre-fork only.
	for _, spec := range []string{"readiness=fd:3", "readiness=stderr", "readiness=signal"} {
		if code := applyNotify(Options{Notify: spec}, os.Getpid()); code != exitUnsupported {
			t.Errorf("%s via legacy applyNotify: got %d, want %d",
				spec, code, exitUnsupported)
		}
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

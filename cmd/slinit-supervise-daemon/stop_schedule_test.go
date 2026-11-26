package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// termProofDaemon is a daemon that ignores SIGTERM.
func termProofDaemon(t *testing.T) []string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	return []string{"--exec", sh, "--", "-c", "trap '' TERM; exec sleep 60"}
}

// assertGone checks that pid has exited (within a short grace).
func assertGone(t *testing.T, what string, pid int) {
	t.Helper()
	if !waitExit(pid, time.Second) {
		t.Errorf("%s pid %d still running", what, pid)
	}
}

// --retry given to --stop was only --stop's wait budget: the supervisor
// kept --start's schedule (TERM/5/KILL/5), --stop gave up after the
// shorter budget, SIGKILLed the supervisor and returned 0 — leaving the
// daemon running under init with nothing tracking it.
func TestStopRetryReachesSupervisor(t *testing.T) {
	bin := buildBinary(t)
	pidfile := filepath.Join(t.TempDir(), "svc.pid")
	super, daemon := startSupervised(t, bin, pidfile, termProofDaemon(t)...)
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	code, out := runBin(t, bin, nil, "svc", "--stop", "--pidfile", pidfile, "--retry", "1")
	if code != exitOK {
		t.Errorf("--stop --retry 1: exit %d: %s", code, out)
	}
	assertGone(t, "supervisor", super)
	assertGone(t, "daemon", daemon)
	if el := time.Since(start); el > 4*time.Second {
		t.Errorf("--stop --retry 1 took %s; the schedule kills at 1s", el)
	}
	for _, p := range []string{pidfile, pidfile + ".daemon", pidfile + ".retry"} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s left behind", p)
		}
	}
}

// A longer --stop schedule must be honoured, not cut short by the
// --start one: the daemon here needs 2s to exit on SIGTERM and --start
// said KILL after 1s.
func TestStopRetryLongerThanStartSchedule(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	bin := buildBinary(t)
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "svc.pid")
	marker := filepath.Join(dir, "graceful")
	super, daemon := startSupervised(t, bin, pidfile, "--retry", "TERM/1/KILL/1",
		"--exec", sh, "--", "-c",
		`trap 'sleep 2; : > "$0"; exit 0' TERM; while :; do sleep 0.1; done`, marker)
	time.Sleep(100 * time.Millisecond)

	code, out := runBin(t, bin, nil, "svc", "--stop", "--pidfile", pidfile, "--retry", "TERM/5/KILL/1")
	if code != exitOK {
		t.Errorf("--stop: exit %d: %s", code, out)
	}
	assertGone(t, "supervisor", super)
	assertGone(t, "daemon", daemon)
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("daemon was killed before its SIGTERM handler finished (--start's schedule ran)")
	}
}

// A schedule that cannot stop the daemon (no KILL) is a failed stop,
// not a success.
func TestStopFailedScheduleIsNotSuccess(t *testing.T) {
	bin := buildBinary(t)
	pidfile := filepath.Join(t.TempDir(), "svc.pid")
	_, daemon := startSupervised(t, bin, pidfile, termProofDaemon(t)...)
	time.Sleep(100 * time.Millisecond)

	code, _ := runBin(t, bin, nil, "svc", "--stop", "--pidfile", pidfile, "--retry", "TERM/1")
	if code == exitOK {
		t.Errorf("--stop reported success with daemon pid %d still running", daemon)
	}
	if !processAlive(daemon) {
		t.Errorf("daemon killed although the schedule had no KILL")
	}
}

// A supervisor that does not respond is killed — and its daemon with
// it, instead of being orphaned to init.
func TestStopHungSupervisorTakesDaemonDown(t *testing.T) {
	bin := buildBinary(t)
	pidfile := filepath.Join(t.TempDir(), "svc.pid")
	super, daemon := startSupervised(t, bin, pidfile, termProofDaemon(t)...)
	time.Sleep(100 * time.Millisecond)
	if err := syscall.Kill(super, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}

	code, out := runBin(t, bin, nil, "svc", "--stop", "--pidfile", pidfile, "--retry", "1")
	if code != exitOK {
		t.Errorf("--stop: exit %d: %s", code, out)
	}
	assertGone(t, "supervisor", super)
	assertGone(t, "daemon", daemon)
	for _, p := range []string{pidfile, pidfile + ".daemon"} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s left behind", p)
		}
	}
}

package main

import (
	"bytes"
	"os"
	"syscall"
	"testing"
	"time"
)

type killCall struct {
	pid int
	sig syscall.Signal
}

// newFakeKillSleep returns replacement killFunc/sleepFunc plus the
// recorder slices so tests can assert what was signalled and how long
// we slept for. Restoring the originals is the caller's responsibility.
func newFakeKillSleep() (*[]killCall, *time.Duration, func(int, syscall.Signal) error, func(time.Duration)) {
	calls := []killCall{}
	var slept time.Duration
	fk := func(pid int, sig syscall.Signal) error {
		calls = append(calls, killCall{pid, sig})
		return nil
	}
	fs := func(d time.Duration) { slept = d }
	return &calls, &slept, fk, fs
}

func saveAndRestore(t *testing.T) func() {
	origKill := killFunc
	origSleep := sleepFunc
	return func() {
		killFunc = origKill
		sleepFunc = origSleep
	}
}

// Discard stderr writes during tests so the output stays clean.
func devNull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestRun_TermThenKill(t *testing.T) {
	t.Cleanup(saveAndRestore(t))
	calls, slept, fk, fs := newFakeKillSleep()
	killFunc = fk
	sleepFunc = fs

	if rc := run([]string{"--grace", "500ms"}, devNull(t)); rc != 0 {
		t.Errorf("rc = %d, want 0", rc)
	}
	if len(*calls) != 2 {
		t.Fatalf("got %d calls, want 2 (SIGTERM then SIGKILL)", len(*calls))
	}
	if (*calls)[0].sig != syscall.SIGTERM || (*calls)[0].pid != -1 {
		t.Errorf("call[0] = %+v, want kill(-1, SIGTERM)", (*calls)[0])
	}
	if (*calls)[1].sig != syscall.SIGKILL || (*calls)[1].pid != -1 {
		t.Errorf("call[1] = %+v, want kill(-1, SIGKILL)", (*calls)[1])
	}
	if *slept != 500*time.Millisecond {
		t.Errorf("slept %v, want 500ms", *slept)
	}
}

func TestRun_KillOnly(t *testing.T) {
	t.Cleanup(saveAndRestore(t))
	calls, slept, fk, fs := newFakeKillSleep()
	killFunc = fk
	sleepFunc = fs

	if rc := run([]string{"-9"}, devNull(t)); rc != 0 {
		t.Errorf("rc = %d, want 0", rc)
	}
	if len(*calls) != 1 {
		t.Fatalf("got %d calls, want 1 (SIGKILL only)", len(*calls))
	}
	if (*calls)[0].sig != syscall.SIGKILL {
		t.Errorf("call[0].sig = %v, want SIGKILL", (*calls)[0].sig)
	}
	if *slept != 0 {
		t.Errorf("sleep should be zero with -9, got %v", *slept)
	}
}

func TestRun_ESRCHIsNotFatal(t *testing.T) {
	t.Cleanup(saveAndRestore(t))
	calls := []killCall{}
	killFunc = func(pid int, sig syscall.Signal) error {
		calls = append(calls, killCall{pid, sig})
		return syscall.ESRCH
	}
	sleepFunc = func(time.Duration) {}

	if rc := run(nil, devNull(t)); rc != 0 {
		t.Errorf("ESRCH should yield rc=0, got %d", rc)
	}
	if len(calls) != 2 {
		t.Errorf("expected 2 kill attempts, got %d", len(calls))
	}
}

func TestRun_SIGKILLFailureIsFatal(t *testing.T) {
	t.Cleanup(saveAndRestore(t))
	killFunc = func(pid int, sig syscall.Signal) error {
		if sig == syscall.SIGKILL {
			return syscall.EPERM
		}
		return nil
	}
	sleepFunc = func(time.Duration) {}

	var err bytes.Buffer
	_ = err // compile-time reminder that stderr is discarded here
	if rc := run(nil, devNull(t)); rc != 1 {
		t.Errorf("rc = %d, want 1 on SIGKILL failure", rc)
	}
}

func TestRun_BadFlagReturns2(t *testing.T) {
	t.Cleanup(saveAndRestore(t))
	killFunc = func(int, syscall.Signal) error { return nil }
	sleepFunc = func(time.Duration) {}

	if rc := run([]string{"--nonexistent"}, devNull(t)); rc != 2 {
		t.Errorf("bad flag → rc=%d, want 2", rc)
	}
}

func TestRun_ZeroGraceStillTerms(t *testing.T) {
	t.Cleanup(saveAndRestore(t))
	calls, slept, fk, fs := newFakeKillSleep()
	killFunc = fk
	sleepFunc = fs

	if rc := run([]string{"--grace", "0"}, devNull(t)); rc != 0 {
		t.Errorf("rc = %d, want 0", rc)
	}
	// Still two signals; just no sleep between them.
	if len(*calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(*calls))
	}
	if *slept != 0 {
		t.Errorf("slept %v, want 0", *slept)
	}
}

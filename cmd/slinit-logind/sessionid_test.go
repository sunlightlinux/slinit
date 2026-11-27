package main

import (
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// withLiveSessions points stateRoot at a fresh directory holding records
// for sessions c1..cN, with the counter where allocating them left it.
func withLiveSessions(t *testing.T, n int) {
	t.Helper()
	orig := stateRoot
	stateRoot = t.TempDir()
	t.Cleanup(func() { stateRoot = orig })
	for i := 0; i < n; i++ {
		id, err := nextSessionID()
		if err != nil {
			t.Fatal(err)
		}
		if err := writeJSONAtomic(sessionFile(id), SessionRecord{ID: id, UserID: 1000, LeaderPID: 4242}); err != nil {
			t.Fatal(err)
		}
	}
}

// The counter was rewritten in place: open with O_TRUNC, then write. A
// write that failed — /run full — left it empty, the empty file read back
// as 0, and the next login was handed c1 again, whose record CreateSession
// then overwrote although that session was still running.
func TestSessionIDCounterSurvivesAFailedWrite(t *testing.T) {
	if inFreshProcess(t) {
		return
	}
	withLiveSessions(t, 3)

	// /run out of space, modelled by RLIMIT_FSIZE=0: the open succeeds
	// and the write fails with EFBIG.
	signal.Ignore(syscall.SIGXFSZ)
	defer signal.Reset(syscall.SIGXFSZ)
	var old unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &unix.Rlimit{Cur: 0, Max: old.Max}); err != nil {
		t.Skipf("cannot lower RLIMIT_FSIZE: %v", err)
	}
	_, err := nextSessionID()
	if rerr := unix.Setrlimit(unix.RLIMIT_FSIZE, &old); rerr != nil {
		t.Fatal(rerr)
	}
	if err == nil {
		t.Fatal("allocation succeeded with no space to record it")
	}

	id, err := nextSessionID()
	if err != nil {
		t.Fatal(err)
	}
	if id != "c4" {
		t.Errorf("next id after the failed write is %s, want c4 (c1-c3 are live)", id)
	}
}

// Whatever the counter says, an id a live session holds is never handed
// out again.
func TestSessionIDSkipsLiveSessions(t *testing.T) {
	withLiveSessions(t, 3)
	if err := os.WriteFile(filepath.Join(stateRoot, "next-session-id"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := nextSessionID()
	if err != nil {
		t.Fatal(err)
	}
	if sessionExistsLocked(id) {
		t.Errorf("allocated %s, which a live session holds", id)
	}
}

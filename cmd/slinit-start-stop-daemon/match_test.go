package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestReadPIDFileBasic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "svc.pid")
	if err := os.WriteFile(path, []byte("1234\n"), 0644); err != nil {
		t.Fatal(err)
	}
	pid, err := readPIDFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if pid != 1234 {
		t.Errorf("pid=%d, want 1234", pid)
	}
}

func TestReadPIDFileMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "svc.pid")
	if err := os.WriteFile(path, []byte("garbage"), 0644); err != nil {
		t.Fatal(err)
	}
	// Malformed file → treat as "not running" (pid=0, no error).
	pid, err := readPIDFile(path)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if pid != 0 {
		t.Errorf("pid=%d, want 0", pid)
	}
}

func TestReadPIDFileEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "svc.pid")
	if err := os.WriteFile(path, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	pid, err := readPIDFile(path)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if pid != 0 {
		t.Errorf("pid=%d, want 0", pid)
	}
}

func TestWriteAndReadPIDFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "svc.pid")
	if err := writePIDFile(path, 42); err != nil {
		t.Fatal(err)
	}
	pid, err := readPIDFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if pid != 42 {
		t.Errorf("pid=%d, want 42", pid)
	}
}

// TestFindMatchingPIDsSelfExec proves the match logic against a real
// running process: our own /proc/self/exe.
func TestFindMatchingPIDsSelfExec(t *testing.T) {
	self, err := os.Readlink("/proc/self/exe")
	if err != nil {
		t.Skipf("cannot read /proc/self/exe: %v", err)
	}
	pids, err := FindMatchingPIDs(MatchCriteria{Exec: self, UID: -1})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	me := os.Getpid()
	for _, p := range pids {
		if p == me {
			// The scan excludes self by design; a match should still be
			// impossible for our own pid.
			found = true
		}
	}
	if found {
		t.Error("scan returned our own pid, but self-exclusion should have hidden it")
	}
}

func TestFindMatchingPIDsPidFileStale(t *testing.T) {
	// Pidfile pointing at a pid we can be certain is missing.
	dir := t.TempDir()
	path := filepath.Join(dir, "svc.pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(999_999)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	pids, err := FindMatchingPIDs(MatchCriteria{PidFile: path, UID: -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(pids) != 0 {
		t.Errorf("stale pidfile should yield no matches, got %v", pids)
	}
}

func TestFindMatchingPIDsMissingPidFile(t *testing.T) {
	pids, err := FindMatchingPIDs(MatchCriteria{PidFile: "/nonexistent/path.pid", UID: -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(pids) != 0 {
		t.Errorf("missing pidfile should yield no matches, got %v", pids)
	}
}

// TestFindMatchingPIDsNeverMatchesInit: with only --pidfile, whatever
// pid the file held was signalled — PID 1 included, where SIGTERM is
// a reboot under slinit. Neither init nor the caller's own process
// tree (our pid, our parent) may ever be a match.
func TestFindMatchingPIDsNeverMatchesInit(t *testing.T) {
	for _, pid := range []int{1, os.Getpid(), os.Getppid()} {
		path := filepath.Join(t.TempDir(), "svc.pid")
		if err := os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		os.Chmod(path, 0o644) // umask-proof
		got, err := FindMatchingPIDs(MatchCriteria{PidFile: path, UID: -1})
		if err != nil {
			t.Fatalf("pid %d: %v", pid, err)
		}
		if len(got) != 0 {
			t.Errorf("pidfile holding %d matched %v", pid, got)
		}
	}
}

// TestFindMatchingPIDsRefusesWorldWritablePidfile: anyone could
// write a world-writable pidfile and choose what we signal. Debian's
// start-stop-daemon refuses it ("matching on world-writable pidfile
// is insecure"); so do we.
func TestFindMatchingPIDsRefusesWorldWritablePidfile(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	path := filepath.Join(t.TempDir(), "svc.pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := FindMatchingPIDs(MatchCriteria{PidFile: path, UID: -1}); err != nil || len(got) != 1 {
		t.Fatalf("0644 pidfile: got %v, %v; want the sleep pid", got, err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if got, err := FindMatchingPIDs(MatchCriteria{PidFile: path, UID: -1}); err == nil {
		t.Errorf("world-writable pidfile accepted: matched %v", got)
	}
}

// TestPidfileInsecure: matching on nothing but a pidfile someone
// else owns lets that someone pick the pid we signal as root. As in
// Debian's start-stop-daemon (dpkg 1.19.3), such a pidfile is only
// trusted with another criterion to cross-check the process.
func TestPidfileInsecure(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		uid, gid, mode       uint32
		pidfileOnly, wantBad bool
	}{
		{"root-owned 0644", 0, 0, 0o644, true, false},
		{"own 0644", 1000, 1000, 0o644, true, false},
		{"daemon-owned, pidfile only", 107, 0, 0o644, true, true},
		{"daemon-owned, with --exec", 107, 0, 0o644, false, false},
		{"group-writable foreign group", 0, 107, 0o664, true, true},
		{"group-writable root group", 0, 0, 0o664, true, false},
		{"world-writable, with --exec", 0, 0, 0o666, false, true},
	} {
		st := &syscall.Stat_t{Uid: tc.uid, Gid: tc.gid, Mode: syscall.S_IFREG | tc.mode}
		bad := pidfileInsecure(st, 1000, 1000, tc.pidfileOnly) != ""
		if bad != tc.wantBad {
			t.Errorf("%s: insecure=%v, want %v", tc.name, bad, tc.wantBad)
		}
	}
}

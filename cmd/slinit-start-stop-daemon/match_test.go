package main

import (
	"os"
	"path/filepath"
	"strconv"
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

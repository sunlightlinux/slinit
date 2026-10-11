package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestWriteOwnPidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slinit.pid")
	if err := writeOwnPidFile(path); err != nil {
		t.Fatalf("writeOwnPidFile: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	got, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("contents %q are not a pid: %v", b, err)
	}
	if got != os.Getpid() {
		t.Errorf("pidfile says %d, want %d", got, os.Getpid())
	}
}

// The file is renamed into place, so a reader polling it never sees a
// partial write — an empty read is indistinguishable from "not started
// yet", which is the one thing a pidfile must not be ambiguous about.
func TestWriteOwnPidFileLeavesNoTemporaries(t *testing.T) {
	dir := t.TempDir()
	if err := writeOwnPidFile(filepath.Join(dir, "slinit.pid")); err != nil {
		t.Fatalf("writeOwnPidFile: %v", err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 || ents[0].Name() != "slinit.pid" {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only slinit.pid", names)
	}
}

// Overwriting an existing file is the normal case on restart.
func TestWriteOwnPidFileOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slinit.pid")
	if err := os.WriteFile(path, []byte("999999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeOwnPidFile(path); err != nil {
		t.Fatalf("writeOwnPidFile over an existing file: %v", err)
	}
	b, _ := os.ReadFile(path)
	if strings.TrimSpace(string(b)) == "999999" {
		t.Error("stale pid left in place")
	}
}

func TestWriteOwnPidFileUnwritableDir(t *testing.T) {
	if err := writeOwnPidFile("/nonexistent-dir-slinit/x.pid"); err == nil {
		t.Error("expected an error for an unwritable directory")
	}
}

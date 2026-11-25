package main

import (
	"path/filepath"
	"testing"
)

// TestCursorFileResumesAfterLastEntry — the cursor file holds the last
// entry already shown; the next run starts after it. It used to start
// at it, so every run printed that entry again (measured: a second run
// with nothing new printed "msg 19").
func TestCursorFileResumesAfterLastEntry(t *testing.T) {
	sock := startFakeJournalServer(t)
	cf := filepath.Join(t.TempDir(), "cursor")
	out, err := queryLive(t, sock, "--cursor-file", cf, "-n", "2")
	if err != nil || out != "msg 18\nmsg 19\n" {
		t.Fatalf("first run: %q, %v", out, err)
	}
	out, err = queryLive(t, sock, "--cursor-file", cf)
	if err != nil || out != "" {
		t.Fatalf("second run with nothing new: %q, %v; want nothing", out, err)
	}
}

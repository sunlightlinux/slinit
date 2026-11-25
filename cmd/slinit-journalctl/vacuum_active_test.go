package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestCurrentJournalFilesNewestDatedPerFormat — the active file is the
// newest one still carrying its plain date name, whatever today's date
// is; rotated <date>.<ns> files and older dates are not protected.
func TestCurrentJournalFilesNewestDatedPerFormat(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{
		"2026-11-20.jsonl", "2026-11-23.jsonl", "2026-11-22.1795300000000000000.jsonl",
		"2026-11-21.journal", "2026-11-21.1795200000000000000.journal",
		"shutdown-20261121-101010-abc.journal", "2026-11-22.jsonl.gz",
	} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := currentJournalFiles(dir)
	want := []string{filepath.Join(dir, "2026-11-23.jsonl"), filepath.Join(dir, "2026-11-21.journal")}
	if !slices.Equal(got, want) {
		t.Fatalf("currentJournalFiles = %q, want %q", got, want)
	}
}

// TestVacuumKeepsActiveFileAfterMidnight — the daemon's active file is
// named by the date it was opened and rotates by age, so once the UTC
// date changes today's name is absent. Vacuum used to protect only
// today's name and deleted the file the daemon was writing (measured:
// --vacuum-files=1 removed 2 files, the active one among them).
func TestVacuumKeepsActiveFileAfterMidnight(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-72 * time.Hour)
	files := []struct {
		name  string
		mtime time.Time
	}{
		{"2026-11-22.1795300000000000000.jsonl", old},
		{"2026-11-23.1795400000000000000.jsonl", old.Add(time.Hour)},
		// Active: opened two days ago, written to a minute ago.
		{"2026-11-23.jsonl", time.Now().Add(-time.Minute)},
		{"2026-11-22.1795300000000000000.journal", old},
		{"2026-11-23.journal", time.Now().Add(-time.Minute)},
	}
	for _, f := range files {
		p := filepath.Join(dir, f.name)
		if err := os.WriteFile(p, []byte(strings.Repeat("x", 100)), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, f.mtime, f.mtime); err != nil {
			t.Fatal(err)
		}
	}
	if err := runVacuum(options{directory: dir, vacuumFiles: 1, vacuumSet: true, quiet: true}); err != nil {
		t.Fatal(err)
	}
	for _, active := range []string{"2026-11-23.jsonl", "2026-11-23.journal"} {
		if _, err := os.Stat(filepath.Join(dir, active)); err != nil {
			t.Errorf("vacuum removed the active file %s: %v", active, err)
		}
	}
	// Rotated files are still vacuumed: only one is kept.
	left, _ := filepath.Glob(filepath.Join(dir, "*.*.*"))
	if len(left) != 1 {
		t.Errorf("rotated files left = %q, want exactly 1", left)
	}
}

// TestOpenJournalFilesFindsHeldFile — a file the daemon holds open is
// protected even when its name says nothing about being active.
func TestOpenJournalFilesFindsHeldFile(t *testing.T) {
	comm, err := os.ReadFile("/proc/self/comm")
	if err != nil {
		t.Skip("no /proc")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "odd-name.jsonl")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got := openJournalFiles("/proc", dir, strings.TrimSpace(string(comm)))
	if !slices.Contains(got, p) {
		t.Fatalf("openJournalFiles = %q, want it to contain %q", got, p)
	}
	if got := openJournalFiles("/proc", dir, "no-such-comm"); len(got) != 0 {
		t.Errorf("another comm matched: %q", got)
	}
	if got := openJournalFiles(filepath.Join(dir, "noproc"), dir, "x"); got != nil {
		t.Errorf("unreadable proc root returned %q", got)
	}
}

// TestCurrentJournalFilesAsksProc — currentJournalFiles protects what a
// running slinit-journald holds open, not only the dated names: the test
// process takes the daemon's name for the call and holds a file whose
// name says nothing about being active.
func TestCurrentJournalFilesAsksProc(t *testing.T) {
	saved, err := os.ReadFile("/proc/self/comm")
	if err != nil {
		t.Skip("no /proc")
	}
	if err := os.WriteFile("/proc/self/comm", []byte("slinit-journald"), 0); err != nil {
		t.Skipf("cannot rename the test process: %v", err)
	}
	defer os.WriteFile("/proc/self/comm", saved, 0)
	dir := t.TempDir()
	p := filepath.Join(dir, "odd-name.journal")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got := currentJournalFiles(dir); !slices.Contains(got, p) {
		t.Fatalf("currentJournalFiles = %q, want it to contain the held %q", got, p)
	}
}

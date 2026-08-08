package journald

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProbeWritableHappyPath(t *testing.T) {
	dir := t.TempDir()
	if err := ProbeWritable(dir); err != nil {
		t.Fatalf("ProbeWritable: %v", err)
	}
	// Probe file should be gone.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("probe file left behind: %v", entries)
	}
}

func TestProbeWritableMissingParent(t *testing.T) {
	// A nested path whose parents don't exist yet — ProbeWritable
	// should MkdirAll them and succeed.
	dir := filepath.Join(t.TempDir(), "a", "b", "c")
	if err := ProbeWritable(dir); err != nil {
		t.Fatalf("ProbeWritable: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dir not created: %v", err)
	}
}

func TestProbeWritableDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere; skip permission-denied path")
	}
	// A path root-owned + 0500 = read/exec only; MkdirAll into a
	// subdir will fail.
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := ProbeWritable(filepath.Join(dir, "child")); err == nil {
		t.Error("expected permission error")
	}
}

func TestMigrateJournalArtefacts(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	// Seed a mix: journal files, sidecar idx, and a non-journal file
	// (must be left alone).
	files := map[string]string{
		"2026-08-01.jsonl":         "day1 jsonl",
		"2026-08-01.jsonl.idx":     "day1 idx",
		"2026-08-02.jsonl.gz":      "day2 gz",
		"2026-08-03.journal":       "day3 binary",
		"ignore-me.txt":            "not a journal",
		"lock":                     "not a journal",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	moved, err := Migrate(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 4 {
		t.Errorf("moved = %d, want 4", moved)
	}
	// Check journal artefacts landed in dst and are gone from src.
	for _, name := range []string{
		"2026-08-01.jsonl",
		"2026-08-01.jsonl.idx",
		"2026-08-02.jsonl.gz",
		"2026-08-03.journal",
	} {
		if _, err := os.Stat(filepath.Join(dst, name)); err != nil {
			t.Errorf("missing at dst: %s (%v)", name, err)
		}
		if _, err := os.Stat(filepath.Join(src, name)); err == nil {
			t.Errorf("still at src: %s", name)
		}
	}
	// Non-journal files must remain at src.
	for _, name := range []string{"ignore-me.txt", "lock"} {
		if _, err := os.Stat(filepath.Join(src, name)); err != nil {
			t.Errorf("non-journal file missing at src: %s (%v)", name, err)
		}
		if _, err := os.Stat(filepath.Join(dst, name)); err == nil {
			t.Errorf("non-journal file incorrectly migrated: %s", name)
		}
	}
}

func TestMigrateMissingSrcIsFine(t *testing.T) {
	dst := t.TempDir()
	moved, err := Migrate(filepath.Join(t.TempDir(), "nope"), dst)
	if err != nil {
		t.Errorf("missing src should not error: %v", err)
	}
	if moved != 0 {
		t.Errorf("moved = %d, want 0", moved)
	}
}

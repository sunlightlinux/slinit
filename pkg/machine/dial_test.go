package machine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostRootPrefersExplicitRoot(t *testing.T) {
	m := &Machine{Name: "x", PID: 42, Root: "/mnt/rootfs"}
	got := m.hostRoot()
	if got != "/mnt/rootfs" {
		t.Errorf("hostRoot with explicit Root = %q, want /mnt/rootfs", got)
	}
}

func TestHostRootFallsBackToProc(t *testing.T) {
	m := &Machine{Name: "x", PID: 42}
	got := m.hostRoot()
	if got != "/proc/42/root" {
		t.Errorf("hostRoot with no Root = %q, want /proc/42/root", got)
	}
}

func TestListJournalFilesEmptyRootReturnsNil(t *testing.T) {
	dir := t.TempDir()
	m := &Machine{Name: "empty", PID: 42, Root: dir}
	files, err := m.ListJournalFiles()
	if err != nil {
		t.Fatal(err)
	}
	if files != nil {
		t.Errorf("files = %v, want nil for empty root", files)
	}
}

func TestListJournalFilesPicksUpJSONL(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, "var/log/slinit-journal")
	if err := os.MkdirAll(journal, 0o755); err != nil {
		t.Fatal(err)
	}
	// Files that should be picked up
	for _, f := range []string{"a.jsonl", "b.jsonl.gz", "c.slj"} {
		if err := os.WriteFile(filepath.Join(journal, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Files that should NOT be picked up
	for _, f := range []string{"README", "index.idx"} {
		if err := os.WriteFile(filepath.Join(journal, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := &Machine{Name: "e", PID: 42, Root: dir}
	files, err := m.ListJournalFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("files=%v, want 3 entries", files)
	}
	// Ordering is lex (which == chrono for YYYY-MM-DD-style names)
	if !strings.HasSuffix(files[0], "a.jsonl") ||
		!strings.HasSuffix(files[1], "b.jsonl.gz") ||
		!strings.HasSuffix(files[2], "c.slj") {
		t.Errorf("ordering off: %v", files)
	}
}

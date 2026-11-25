package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journald"
)

// unwritableDir is a path under a regular file, so MkdirAll fails even
// for root.
func unwritableDir(t *testing.T) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(f, "journal")
}

func countSuffix(t *testing.T, dir, suffix string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), suffix) {
			n++
		}
	}
	return n
}

// A sink that fell back to the volatile dir must vacuum that dir: the
// hook used to be bound to the unwritable primary, so the volatile
// files were never pruned and /run filled.
func TestJSONLFallbackVacuumsVolatileDir(t *testing.T) {
	volatile := t.TempDir()
	f := sinkFactory{
		volatileDir: volatile,
		vacuum:      journald.VacuumOptions{MaxFiles: 1, Suffixes: []string{".jsonl"}},
		fileOpts:    journald.FileSinkOptions{FsyncEvery: 1, MaxSize: 32, MaxAge: time.Hour},
	}
	s, dir, err := f.jsonl(unwritableDir(t))
	if err != nil || dir != volatile {
		t.Fatalf("open: dir %q err %v, want the volatile %q", dir, err, volatile)
	}
	defer s.Close()
	for i := 0; i < 6; i++ {
		time.Sleep(2 * time.Millisecond)
		if err := s.Handle(&journal.Event{Ts: int64(i+1) * 1e9, Msg: "payload"}); err != nil {
			t.Fatal(err)
		}
	}
	// One rotated file kept, plus the active one.
	if n := countSuffix(t, volatile, ".jsonl"); n > 2 {
		t.Errorf("%d .jsonl files in the volatile dir, want at most 2", n)
	}
}

// The binary format (the default) falls back to the volatile dir like
// jsonl, instead of exiting when /var is read-only or full at start-up,
// and vacuums the dir it fell back to.
func TestBinaryFallsBackToVolatileDir(t *testing.T) {
	if err := journal.InitIDs("testhost"); err != nil {
		t.Skipf("journal IDs: %v", err)
	}
	volatile := t.TempDir()
	f := sinkFactory{
		volatileDir: volatile,
		vacuum:      journald.VacuumOptions{MaxFiles: 1, Suffixes: []string{".journal"}},
		fileOpts:    journald.FileSinkOptions{FsyncEvery: 1, MaxSize: 64, MaxAge: time.Hour},
	}
	s, dir, err := f.binary(unwritableDir(t))
	if err != nil || dir != volatile {
		t.Fatalf("open: dir %q err %v, want the volatile %q", dir, err, volatile)
	}
	defer s.Close()
	for i := 0; i < 6; i++ {
		time.Sleep(2 * time.Millisecond)
		if err := s.Handle(&journal.Event{Ts: int64(i+1) * 1e9, Msg: "payload"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := countSuffix(t, volatile, ".journal"); n == 0 || n > 2 {
		t.Errorf("%d .journal files in the volatile dir, want 1 or 2", n)
	}
}

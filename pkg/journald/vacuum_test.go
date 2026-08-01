package journald

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// writeFake creates a fake .jsonl at path with the given size and
// mtime — used by vacuum tests to skip the whole receiver/sink
// pipeline and just probe the vacuum policy math.
func writeFake(t *testing.T, path string, size int64, mtime time.Time) {
	t.Helper()
	buf := make([]byte, size)
	if err := os.WriteFile(path, buf, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestVacuumMaxFiles(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	// 5 files, ages descending by minute so sort is deterministic.
	for i := 0; i < 5; i++ {
		writeFake(t, filepath.Join(dir, "old-"+string(rune('0'+i))+".jsonl"),
			10, base.Add(time.Duration(i)*time.Minute))
	}
	removed, err := Vacuum(dir, VacuumOptions{MaxFiles: 2})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 3 {
		t.Fatalf("expected 3 removals, got %d", removed)
	}
	entries, _ := os.ReadDir(dir)
	jsonlLeft := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			jsonlLeft++
		}
	}
	if jsonlLeft != 2 {
		t.Fatalf("expected 2 files left, got %d", jsonlLeft)
	}
}

func TestVacuumMaxTotalSize(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	// 3 files of 100 bytes each = 300 total. Cap at 150 → 2 must go.
	for i := 0; i < 3; i++ {
		writeFake(t, filepath.Join(dir, "s"+string(rune('0'+i))+".jsonl"),
			100, base.Add(time.Duration(i)*time.Minute))
	}
	removed, err := Vacuum(dir, VacuumOptions{MaxTotalSize: 150})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("expected 2 removals, got %d", removed)
	}
	// Newest file (s2) should survive.
	if _, err := os.Stat(filepath.Join(dir, "s2.jsonl")); err != nil {
		t.Fatalf("newest file should survive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "s0.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("oldest file should be gone, err=%v", err)
	}
}

func TestVacuumMaxAge(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	// Two old (60 days), one recent (1 day). Cutoff 30 days.
	writeFake(t, filepath.Join(dir, "ancient1.jsonl"), 5, now.Add(-60*24*time.Hour))
	writeFake(t, filepath.Join(dir, "ancient2.jsonl"), 5, now.Add(-45*24*time.Hour))
	writeFake(t, filepath.Join(dir, "fresh.jsonl"), 5, now.Add(-24*time.Hour))

	removed, err := Vacuum(dir, VacuumOptions{MaxAge: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("expected 2 aged-out removals, got %d", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "fresh.jsonl")); err != nil {
		t.Fatalf("fresh file should survive: %v", err)
	}
}

func TestVacuumRemovesIdxCompanion(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "with-idx.jsonl")
	writeFake(t, old, 10, time.Now().Add(-time.Hour))
	writeFake(t, idxPath(old), 16, time.Now().Add(-time.Hour))

	if _, err := Vacuum(dir, VacuumOptions{MaxFiles: 0, MaxAge: 30 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf(".jsonl should be gone: %v", err)
	}
	if _, err := os.Stat(idxPath(old)); !os.IsNotExist(err) {
		t.Fatalf(".idx should be gone alongside: %v", err)
	}
}

func TestVacuumExcludesCurrent(t *testing.T) {
	dir := t.TempDir()
	// Current file — must NOT be touched even if it fits every
	// removal criterion.
	current := filepath.Join(dir, "current.jsonl")
	writeFake(t, current, 1000, time.Now().Add(-100*24*time.Hour))

	removed, err := Vacuum(dir, VacuumOptions{
		MaxFiles: 0, MaxTotalSize: 10, MaxAge: 1 * time.Hour,
	}, current)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("current file was pruned despite exclude: %d removed", removed)
	}
	if _, err := os.Stat(current); err != nil {
		t.Fatalf("current file gone: %v", err)
	}
}

func TestVacuumSuffixFilterBinary(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	// 3 .journal files + 1 .jsonl decoy. Vacuum with Suffixes=[.journal]
	// must prune 2 of the 3 .journal files and leave the .jsonl untouched.
	for i := 0; i < 3; i++ {
		writeFake(t, filepath.Join(dir, "bin"+string(rune('0'+i))+".journal"),
			10, base.Add(time.Duration(i)*time.Minute))
	}
	writeFake(t, filepath.Join(dir, "decoy.jsonl"), 10, base)

	removed, err := Vacuum(dir, VacuumOptions{
		MaxFiles: 1,
		Suffixes: []string{".journal"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("expected 2 removals, got %d", removed)
	}
	// Decoy must survive — outside the suffix filter.
	if _, err := os.Stat(filepath.Join(dir, "decoy.jsonl")); err != nil {
		t.Errorf("decoy .jsonl removed unexpectedly: %v", err)
	}
}

func TestVacuumingHookViaFileSink(t *testing.T) {
	// End-to-end: FileSink with tight rotation + tight vacuum. After
	// several rotations, only MaxFiles rotated files should remain.
	dir := t.TempDir()

	fs, err := NewFileSinkWithOptions(dir, FileSinkOptions{
		FsyncEvery:  1,
		MaxSize:     32, // rotate on every write
		MaxAge:      time.Hour,
		RotatedHook: VacuumingHook(dir, VacuumOptions{MaxFiles: 2}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	for i := 0; i < 6; i++ {
		time.Sleep(2 * time.Millisecond)
		fs.Handle(&journal.Event{Ts: int64(i + 1), Msg: "chunky payload here"})
	}

	entries, _ := os.ReadDir(dir)
	jsonlCount := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			jsonlCount++
		}
	}
	// current file + at most MaxFiles rotated = 3
	if jsonlCount > 3 {
		t.Fatalf("vacuum did not enforce MaxFiles=2: %d .jsonl files left", jsonlCount)
	}
}

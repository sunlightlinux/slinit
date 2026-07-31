package journald

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

func TestRotateOnSize(t *testing.T) {
	dir := t.TempDir()
	// MaxSize=32: each event JSON is ~40-60 bytes so every write
	// triggers rotation immediately after (totalWritten >= 32
	// after any Handle). 4 events → 4 rotations, 5 .jsonl files
	// on disk (4 rotated + 1 current, though the last may be
	// empty).
	fs, err := NewFileSinkWithOptions(dir, FileSinkOptions{
		FsyncEvery: 1,
		MaxSize:    32,
		MaxAge:     time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	for i := 0; i < 4; i++ {
		// Sleep 2ms between events so rotation nanosecond suffixes
		// don't collide (test-only defensive gap; production rotates
		// at 128 MiB so this is a non-issue).
		time.Sleep(2 * time.Millisecond)
		if err := fs.Handle(&journal.Event{Ts: int64((i + 1) * 1_000_000_000), Msg: "payload"}); err != nil {
			t.Fatal(err)
		}
	}

	if got := fs.Rotations(); got < 2 {
		t.Fatalf("expected ≥2 rotations from 4 writes over MaxSize=32, got %d", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	jsonlCount := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".jsonl" {
			jsonlCount++
		}
	}
	if jsonlCount < 3 {
		t.Fatalf("expected ≥3 .jsonl files in %s (rotated + current), got %d", dir, jsonlCount)
	}
}

func TestRotateOnAge(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileSinkWithOptions(dir, FileSinkOptions{
		FsyncEvery: 1,
		MaxSize:    1 << 30,               // effectively unlimited
		MaxAge:     10 * time.Millisecond, // trivially short
	})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	// First event lands in current — no rotation yet.
	if err := fs.Handle(&journal.Event{Ts: 1_000_000_000, Msg: "first"}); err != nil {
		t.Fatal(err)
	}
	if fs.Rotations() != 0 {
		t.Fatalf("premature rotation: %d", fs.Rotations())
	}
	// Wait past the age threshold, then next event triggers rotate.
	time.Sleep(20 * time.Millisecond)
	if err := fs.Handle(&journal.Event{Ts: 2_000_000_000, Msg: "second"}); err != nil {
		t.Fatal(err)
	}
	if fs.Rotations() != 1 {
		t.Fatalf("expected 1 age-triggered rotation, got %d", fs.Rotations())
	}
}

func TestRotateHookFires(t *testing.T) {
	dir := t.TempDir()
	var rotated []string
	fs, err := NewFileSinkWithOptions(dir, FileSinkOptions{
		FsyncEvery: 1,
		MaxSize:    50,
		MaxAge:     time.Hour,
		RotatedHook: func(rotatedPath, _ string) {
			rotated = append(rotated, rotatedPath)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	for i := 0; i < 2; i++ {
		fs.Handle(&journal.Event{Ts: int64((i + 1) * 1_000_000_000), Msg: "some payload here longer than fifty bytes total ok"})
	}
	if len(rotated) == 0 {
		t.Fatal("rotated hook never fired")
	}
	// Each rotated path must actually exist on disk.
	for _, p := range rotated {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("rotated path %s: %v", p, err)
		}
	}
}

func TestRotateDisabled(t *testing.T) {
	// MaxSize = -1 and MaxAge = -1 → disabled (any non-positive
	// non-zero value stays as-is since we only override 0 with default).
	// Simulate disabled by MaxSize+MaxAge left at zero — but that
	// picks defaults. Test that a small file with default limits
	// does NOT rotate.
	dir := t.TempDir()
	fs, err := NewFileSink(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	for i := 0; i < 10; i++ {
		fs.Handle(&journal.Event{Ts: int64(i + 1), Msg: "tiny"})
	}
	if fs.Rotations() != 0 {
		t.Fatalf("small writes should NOT rotate under 128MiB default, got %d rotations",
			fs.Rotations())
	}
}

func TestRotatedFilesReadable(t *testing.T) {
	// After rotation, the rotated jsonl + idx must still be a valid
	// pair readable by OpenIdx.
	dir := t.TempDir()
	var rotatedPath string
	fs, err := NewFileSinkWithOptions(dir, FileSinkOptions{
		FsyncEvery: 1,
		MaxSize:    30,
		MaxAge:     time.Hour,
		RotatedHook: func(p, _ string) {
			if rotatedPath == "" {
				rotatedPath = p
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	fs.Handle(&journal.Event{Ts: 1_000_000_000, Msg: "trigger rotation soon xxx"})
	fs.Handle(&journal.Event{Ts: 2_000_000_000, Msg: "after"})

	if rotatedPath == "" {
		t.Fatal("no rotation observed")
	}
	idxR, err := OpenIdx(idxPath(rotatedPath))
	if err != nil {
		t.Fatalf("open rotated idx: %v", err)
	}
	defer idxR.Close()
	if idxR.Len() < 1 {
		t.Fatalf("rotated idx should have ≥1 record, got %d", idxR.Len())
	}
}

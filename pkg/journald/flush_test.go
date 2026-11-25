package journald

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journalbin"
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
		"2026-08-01.jsonl":     "day1 jsonl",
		"2026-08-01.jsonl.idx": "day1 idx",
		"2026-08-02.jsonl.gz":  "day2 gz",
		"2026-08-03.journal":   "day3 binary",
		"ignore-me.txt":        "not a journal",
		"lock":                 "not a journal",
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

func journalEvent(i int, boot string) *journal.Event {
	return &journal.Event{Ts: time.Now().UnixNano(), Mts: int64(i + 1), BootID: boot,
		Unit: "x.service", Msg: fmt.Sprintf("event %d %s", i, strings.Repeat("p", 200))}
}

// --flush used to rename the volatile day file over the persistent one of
// the same name: the earlier boot's 3 lines for the day became 0.
func TestMigrateKeepsPersistentSameDayFile(t *testing.T) {
	prim, vol := t.TempDir(), t.TempDir()
	s1, err := NewFileSink(prim, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		s1.Handle(journalEvent(i, "boot-A"))
	}
	s1.Close()
	s2, err := NewFileSink(vol, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s2.Handle(journalEvent(100+i, "boot-B"))
	}
	s2.Close()

	if _, err := Migrate(vol, prim); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ents, _ := os.ReadDir(prim)
	var all string
	var jsonls []string
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		jsonls = append(jsonls, e.Name())
		p := filepath.Join(prim, e.Name())
		b, _ := os.ReadFile(p)
		all += string(b)
		// Each index still describes its own file.
		n := strings.Count(string(b), "\n")
		if fi, err := os.Stat(idxPath(p)); err != nil || fi.Size() != int64(n*IdxRecordSize) {
			t.Errorf("%s: idx does not match its %d lines (%v)", e.Name(), n, err)
		}
	}
	if len(jsonls) != 2 {
		t.Fatalf("want the persistent and the migrated file, got %v", jsonls)
	}
	if a, b := strings.Count(all, "boot-A"), strings.Count(all, "boot-B"); a != 3 || b != 2 {
		t.Fatalf("after flush: boot-A lines=%d (want 3), boot-B lines=%d (want 2)", a, b)
	}
	// Name order (the readers' order) still puts the earlier boot first,
	// and the migrated file keeps the current-day name the sink reopens.
	if strings.Index(all, "boot-A") > strings.Index(all, "boot-B") {
		t.Errorf("earlier boot no longer read first: %v", jsonls)
	}
	if b, _ := os.ReadFile(filepath.Join(prim, filepath.Base(s1.CurrentPath()))); !strings.Contains(string(b), "boot-B") {
		t.Errorf("current-day file is not the migrated one")
	}
	if left, _ := os.ReadDir(vol); len(left) != 0 {
		t.Errorf("left in volatile dir: %v", left)
	}
}

func TestMigrateKeepsPersistentSameDayBinary(t *testing.T) {
	prim, vol := t.TempDir(), t.TempDir()
	const bootA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const bootB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	s1, err := NewBinarySink(BinarySinkOptions{Dir: prim, FsyncEvery: 1, BootID: bootA})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		s1.Handle(journalEvent(i, bootA))
	}
	s1.Close()
	s2, err := NewBinarySink(BinarySinkOptions{Dir: vol, FsyncEvery: 1, BootID: bootB})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s2.Handle(journalEvent(100+i, bootB))
	}
	s2.Close()

	if _, err := Migrate(vol, prim); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	mr, err := journalbin.OpenDir(prim)
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	var boots []string
	if err := mr.Iter(func(e *journal.Event) bool {
		boots = append(boots, e.BootID)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{bootA, bootA, bootA, bootB, bootB}
	if fmt.Sprint(boots) != fmt.Sprint(want) {
		t.Errorf("entries after flush = %v, want %v", boots, want)
	}
}

// The copy fallback never truncates a file it finds in its way.
func TestMoveFileNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	os.WriteFile(src, []byte("new"), 0o644)
	os.WriteFile(dst, []byte("old"), 0o644)
	if err := moveFile(src, dst); err == nil {
		t.Fatal("moveFile replaced an existing file")
	}
	if b, _ := os.ReadFile(dst); string(b) != "old" {
		t.Errorf("dst = %q, want it untouched", b)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("src lost: %v", err)
	}
}

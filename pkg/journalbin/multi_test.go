package journalbin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

func TestMultiReaderMergesInTimeOrder(t *testing.T) {
	dir := t.TempDir()

	// File A: events at 1, 5, 9 (us).
	w, _ := NewWriter(filepath.Join(dir, "a.journal"), testBootID, testMachineID)
	for _, ts := range []int64{1, 5, 9} {
		w.Append(&journal.Event{Ts: ts * 1000, Msg: "A" + itoa(int(ts))})
	}
	w.Close()

	// File B: events at 2, 4, 8.
	w, _ = NewWriter(filepath.Join(dir, "b.journal"), testBootID, testMachineID)
	for _, ts := range []int64{2, 4, 8} {
		w.Append(&journal.Event{Ts: ts * 1000, Msg: "B" + itoa(int(ts))})
	}
	w.Close()

	mr, err := OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	if len(mr.Readers()) != 2 {
		t.Fatalf("expected 2 readers, got %d", len(mr.Readers()))
	}

	var order []string
	mr.Iter(func(e *journal.Event) bool {
		order = append(order, e.Msg)
		return true
	})
	want := []string{"A1", "B2", "B4", "A5", "B8", "A9"}
	if len(order) != len(want) {
		t.Fatalf("order len=%d, want %d: %v", len(order), len(want), order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("order[%d]=%q, want %q (full: %v)", i, order[i], want[i], order)
		}
	}
}

func TestMultiReaderIgnoresNonJournalFiles(t *testing.T) {
	dir := t.TempDir()
	// Create a random non-.journal file next to a valid one.
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("ignore me"), 0644)
	os.WriteFile(filepath.Join(dir, "a.jsonl"), []byte(`{"msg":"jsonl"}`), 0644)

	w, _ := NewWriter(filepath.Join(dir, "only.journal"), testBootID, testMachineID)
	w.Append(&journal.Event{Ts: 1_000_000, Msg: "kept"})
	w.Close()

	mr, err := OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	if len(mr.Readers()) != 1 {
		t.Fatalf("expected 1 reader (only .journal), got %d", len(mr.Readers()))
	}
	var got []string
	mr.Iter(func(e *journal.Event) bool {
		got = append(got, e.Msg)
		return true
	})
	if len(got) != 1 || got[0] != "kept" {
		t.Fatalf("got %v, want [kept]", got)
	}
}

func TestMultiReaderStopIterEarly(t *testing.T) {
	dir := t.TempDir()
	w, _ := NewWriter(filepath.Join(dir, "x.journal"), testBootID, testMachineID)
	for i := int64(1); i <= 5; i++ {
		w.Append(&journal.Event{Ts: i * 1_000_000, Msg: "e" + itoa(int(i))})
	}
	w.Close()
	mr, err := OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	var seen int
	mr.Iter(func(_ *journal.Event) bool {
		seen++
		return seen < 3
	})
	if seen != 3 {
		t.Fatalf("expected 3 events before stop, got %d", seen)
	}
}

func TestOpenDirRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "broken.journal"), []byte("NOPEBEEF"), 0644)
	if _, err := OpenDir(dir); err == nil {
		t.Fatal("expected error opening dir with corrupt journal")
	}
}

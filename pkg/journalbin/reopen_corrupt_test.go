package journalbin

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// closedJournal writes one entry, closes the writer and returns the
// path and the header it left.
func closedJournal(t *testing.T) (string, Header) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "c.journal")
	w, err := NewWriter(path, testBootID, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(&journal.Event{Ts: 1000, Msg: "m", Prio: journal.PriorityInfo}); err != nil {
		t.Fatal(err)
	}
	h := w.header
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path, h
}

func pokeUint64(t *testing.T, path string, at, v uint64) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	if _, err := f.WriteAt(b[:], int64(at)); err != nil {
		t.Fatal(err)
	}
}

// reopenAndAppend reopens path and appends one event within a deadline.
func reopenAndAppend(t *testing.T, path string, evt *journal.Event) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		w, err := NewWriter(path, testBootID, testMachineID)
		if err != nil {
			done <- err
			return
		}
		_, err = w.Append(evt)
		w.Close()
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reopening and appending to the file did not return")
	}
}

// A closed journal whose ENTRY_ARRAY links back to itself (a flipped
// block on disk): reopening it walked the chain forever, and
// slinit-journald hung at start.
func TestWriterReopenEntryArrayCycle(t *testing.T) {
	path, h := closedJournal(t)
	pokeUint64(t, path, h.EntryArrayOffset+ObjectHeaderSize, h.EntryArrayOffset)
	reopenAndAppend(t, path, &journal.Event{Ts: 2000, Msg: "n"})
}

// A closed journal whose DATA hash chain links a DATA object back to
// itself: the dedup walk of every append into that bucket never ended.
func TestWriterReopenHashChainCycle(t *testing.T) {
	path, _ := closedJournal(t)
	// The first DATA object follows the two hash tables.
	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	dataOff := r.header.FieldHashTableOffset + r.header.FieldHashTableSize
	table := r.header.DataHashTableOffset
	r.Close()
	n, err := nBuckets(r0(t, path), table)
	if err != nil {
		t.Fatal(err)
	}
	// Point the bucket of a payload the file does not hold at a DATA
	// object that links to itself.
	b := Hash64([]byte("MESSAGE=new")) % uint64(n)
	slot := table + ObjectHeaderSize + b*HashItemSize
	pokeUint64(t, path, slot, dataOff)
	pokeUint64(t, path, slot+8, dataOff)
	pokeUint64(t, path, dataOff+dataNextHashOffset, dataOff)
	reopenAndAppend(t, path, &journal.Event{Ts: 2000, Msg: "new"})
}

func r0(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// A closed journal whose DATA object claims a size of most of the
// address space: the dedup walk allocated it.
func TestWriterReopenHugeDataSize(t *testing.T) {
	path, _ := closedJournal(t)
	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	dataOff := r.header.FieldHashTableOffset + r.header.FieldHashTableSize
	r.Close()
	pokeUint64(t, path, dataOff+8, 1<<62)
	reopenAndAppend(t, path, &journal.Event{Ts: 1000, Msg: "m", Prio: journal.PriorityInfo})
}

// A closed sealed journal whose first ENTRY claims a huge size: the
// writer's next TAG collects the objects since the last one it could
// find, and allocated that ENTRY at its claimed size.
func TestWriterReopenSealedHugeEntrySize(t *testing.T) {
	key, err := NewFSSKey(0, DefaultFSSEpochUsec)
	if err != nil {
		t.Fatal(err)
	}
	opts := WriterOptions{BootID: testBootID, FSSKey: key, TagEvery: 2}
	path := filepath.Join(t.TempDir(), "s.journal")
	w, err := NewWriterWithOptions(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	first, err := w.Append(&journal.Event{Ts: 1000, Msg: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	pokeUint64(t, path, first+8, 1<<62)

	done := make(chan error, 1)
	go func() {
		w, err := NewWriterWithOptions(path, opts)
		if err != nil {
			done <- err
			return
		}
		defer w.Close()
		for i := 0; i < 2 && err == nil; i++ {
			_, err = w.Append(&journal.Event{Ts: 2000, Msg: "n"})
		}
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reopening and appending to the file did not return")
	}
}

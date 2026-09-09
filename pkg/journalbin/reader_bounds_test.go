package journalbin

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestReaderRejectsOversizedDataObject exercises the class of
// crash that FuzzJournalBinaryOpenReader surfaced (seed
// c959fdb198755a8a): a valid header followed by an ENTRY whose
// items point at a DATA object whose declared Size approaches
// uint64.Max. Pre-fix, readDataPayloadAt would compute
// payloadLen := oh.Size - dataFixed and call make([]byte,
// payloadLen), panicking with "makeslice: len out of range".
// Post-fix, the checkBounds guard rejects with ErrObjectBounds
// before the allocation.
func TestReaderRejectsOversizedDataObject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hostile.journal")

	// Start from a fresh, valid empty journal (Writer wires up
	// header/arena/entry-array correctly), then append one
	// hostile DATA object whose Size claims to span most of the
	// address space. We don't need Iter to reach it — the item
	// pointer path below directs a synthetic ENTRY at it.
	w, err := NewWriter(path, "boot0", "machine0")
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}

	// Grow file so a bogus offset inside a hand-written ENTRY
	// resolves to a byte range Reader will Stat as in-bounds.
	// The DATA header we write at that offset has a garbage
	// Size — that's what checkBounds must catch.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	dataOff := uint64(st.Size())
	dataHdr := ObjectHeader{
		Type: ObjectData,
		// Absurd size that survives past-EOF ReadAt as far as
		// makeslice — this is the crashy input.
		Size: 1 << 62,
	}
	hdrBuf := make([]byte, ObjectHeaderSize)
	if err := dataHdr.EncodeInto(hdrBuf); err != nil {
		t.Fatalf("encode data header: %v", err)
	}
	if _, err := f.WriteAt(hdrBuf, int64(dataOff)); err != nil {
		t.Fatalf("write bogus DATA header: %v", err)
	}
	// Pad the file to at least ObjectHeaderSize past dataOff so
	// the Reader's fileSize measurement covers the header read
	// path. checkBounds must reject on the declared Size, not
	// on the header ReadAt.
	pad := make([]byte, 64)
	if _, err := f.WriteAt(pad, int64(dataOff)+int64(ObjectHeaderSize)); err != nil {
		t.Fatalf("pad: %v", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()

	// readDataPayloadAt is unexported; drive through the same
	// call site the fuzzer hit (Iter → readEntryAt → dataOff
	// item → readDataPayloadAt) by exposing the crash surface
	// directly via a private helper call. Go's package-internal
	// test scope lets us call the unexported readDataPayloadAt.
	_, err = r.readDataPayloadAt(dataOff)
	if err == nil {
		t.Fatalf("readDataPayloadAt(bogus DATA size): want error, got nil")
	}
	if !errors.Is(err, ErrObjectBounds) {
		t.Fatalf("readDataPayloadAt: want ErrObjectBounds, got %v", err)
	}
}

// TestReaderRejectsOversizedEntryViaReadEntryAt exercises the
// same guard on the ReadEntryAt public path — a caller passes
// a raw offset (e.g. one pulled out of EntryOffsets or a cursor)
// and the DATA/ENTRY header at that offset can carry a hostile
// Size. readEntryAt allocates body := make([]byte, size), so
// unbounded size trips the same makeslice panic.
func TestReaderRejectsOversizedEntryViaReadEntryAt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hostile2.journal")

	w, err := NewWriter(path, "boot0", "machine0")
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	defer f.Close()

	st, _ := f.Stat()
	entryOff := uint64(st.Size())
	entryHdr := ObjectHeader{
		Type: ObjectEntry,
		Size: 1 << 62,
	}
	hdrBuf := make([]byte, ObjectHeaderSize)
	if err := entryHdr.EncodeInto(hdrBuf); err != nil {
		t.Fatalf("encode entry header: %v", err)
	}
	if _, err := f.WriteAt(hdrBuf, int64(entryOff)); err != nil {
		t.Fatalf("write bogus ENTRY header: %v", err)
	}
	// Belt-and-suspenders on the LE encoding — verify our raw
	// Size landed as we expect and would in fact defeat a
	// missing bounds check.
	if got := binary.LittleEndian.Uint64(hdrBuf[8:16]); got != 1<<62 {
		t.Fatalf("encoded size = %#x, want %#x", got, uint64(1<<62))
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()

	_, err = r.ReadEntryAt(entryOff)
	if err == nil {
		t.Fatalf("ReadEntryAt(bogus ENTRY size): want error, got nil")
	}
	if !errors.Is(err, ErrObjectBounds) {
		t.Fatalf("ReadEntryAt: want ErrObjectBounds, got %v", err)
	}
}

// TestReaderRejectsOffsetPastEOF exercises the first guard in
// checkBounds — an offset already past file end returns
// ErrObjectBounds without any ReadAt call.
func TestReaderRejectsOffsetPastEOF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "small.journal")
	w, err := NewWriter(path, "boot0", "machine0")
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	_ = w.Close()

	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()

	// r.fileSize + 1 is trivially past EOF.
	beyond := r.fileSize + 1
	if _, err := r.readDataPayloadAt(beyond); err == nil {
		t.Fatalf("readDataPayloadAt(past-EOF): want error, got nil")
	} else if !errors.Is(err, ErrObjectBounds) {
		t.Fatalf("readDataPayloadAt(past-EOF): want ErrObjectBounds, got %v", err)
	}
	if _, err := r.ReadEntryAt(beyond); err == nil {
		t.Fatalf("ReadEntryAt(past-EOF): want error, got nil")
	} else if !errors.Is(err, ErrObjectBounds) {
		t.Fatalf("ReadEntryAt(past-EOF): want ErrObjectBounds, got %v", err)
	}
}

package journalbin

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
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

// An ENTRY_ARRAY whose next offset points back at itself used to be
// walked forever, appending the same entries until memory ran out.
func TestWalkEntryOffsetsRejectsCycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cycle.journal")
	w, err := NewWriter(path, testBootID, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(&journal.Event{Ts: 1000, Msg: "m", Prio: journal.PriorityInfo}); err != nil {
		t.Fatal(err)
	}
	arr := w.header.EntryArrayOffset
	w.Close()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	var next [8]byte
	binary.LittleEndian.PutUint64(next[:], arr)
	if _, err := f.WriteAt(next[:], int64(arr)+ObjectHeaderSize); err != nil {
		t.Fatal(err)
	}
	f.Close()

	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	done := make(chan error, 1)
	go func() {
		_, err := r.EntryOffsets()
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("self-referential entry array walked without error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("walk of a self-referential entry array did not terminate")
	}
}

// Every item of one ENTRY can name the same large DATA object. The
// reader read and allocated that payload once per item, so a 2 MiB file
// could demand 128 GiB; an entry may now read no more payload than the
// file holds, and a repeated item is read once.
func TestReaderEntryPayloadsBoundedByFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "amp.journal")
	w, err := NewWriter(path, testBootID, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 256<<10)
	fields := map[string]string{}
	for i := 0; i < 60; i++ {
		fields[fmt.Sprintf("F%d", i)] = "v"
	}
	entryOff, err := w.Append(&journal.Event{Ts: 1000, Msg: big, Prio: journal.PriorityInfo, Fields: fields})
	if err != nil {
		t.Fatal(err)
	}
	w.Close()

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	var hdr [ObjectHeaderSize]byte
	if _, err := f.ReadAt(hdr[:], int64(entryOff)); err != nil {
		t.Fatal(err)
	}
	oh, _ := DecodeObjectHeader(hdr[:])
	itemsStart := int64(entryOff) + ObjectHeaderSize + 8 + 8 + 8 + 16 + 8
	nItems := (int64(entryOff) + int64(oh.Size) - itemsStart) / 16
	// Find the MESSAGE item (the large one) and point every item at it.
	items := make([]byte, nItems*16)
	if _, err := f.ReadAt(items, itemsStart); err != nil {
		t.Fatal(err)
	}
	var bigOff uint64
	var largest uint64
	for k := int64(0); k < nItems; k++ {
		off := binary.LittleEndian.Uint64(items[k*16:])
		var dh [ObjectHeaderSize]byte
		f.ReadAt(dh[:], int64(off))
		d, _ := DecodeObjectHeader(dh[:])
		if d.Size > largest {
			largest, bigOff = d.Size, off
		}
	}
	for k := int64(0); k < nItems; k++ {
		binary.LittleEndian.PutUint64(items[k*16:], bigOff)
	}
	if _, err := f.WriteAt(items, itemsStart); err != nil {
		t.Fatal(err)
	}
	st, _ := f.Stat()
	f.Close()

	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, _ = r.ReadEntryAt(entryOff)
	runtime.ReadMemStats(&after)
	if got := after.TotalAlloc - before.TotalAlloc; got > 2*uint64(st.Size()) {
		t.Fatalf("one entry of a %d-byte file allocated %d bytes (%d items x %d-byte payload)",
			st.Size(), got, nItems, largest)
	}
}

// Verify checked off+size against the file end, which wraps: a size of
// 2^64-1 passed and aligned up to zero, so the walk never advanced.
func TestVerifyRejectsWrappingObjectSize(t *testing.T) {
	path, key := buildSealedJournal(t, t.TempDir(), 3, 2)
	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	first := r.Header().FieldHashTableOffset + r.Header().FieldHashTableSize
	r.Close()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	var size [8]byte
	binary.LittleEndian.PutUint64(size[:], ^uint64(0))
	if _, err := f.WriteAt(size[:], int64(first)+8); err != nil {
		t.Fatal(err)
	}
	f.Close()
	done := make(chan error, 1)
	go func() {
		_, err := Verify(path, key)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("object with a wrapping size verified without error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Verify of an object with a wrapping size did not terminate")
	}
}

// ENTRY_ARRAYs only had to chain forward. Arrays 24 bytes apart overlap,
// so each re-reads its successors' headers as entry offsets: 96 KB of
// file yielded 13.5M offsets and 817 MiB. The chain may not overlap now,
// and no file yields more offsets than it could hold entries.
func TestEntryOffsetsOverlappingArraysBounded(t *testing.T) {
	const n = 4000
	const start = HeaderSize
	end := uint64(start + 24*n + 8192)
	buf := make([]byte, end)
	h := NewHeader()
	h.EntryArrayOffset = start
	h.TailObjectOffset = end
	if err := h.EncodeInto(buf); err != nil {
		t.Fatal(err)
	}
	for i := uint64(start + 24*n); i < end; i++ {
		buf[i] = 0x5a
	}
	for k := uint64(0); k < n; k++ {
		off := start + 24*k
		size := min(maxEntryArrayObjectSize, end-off)
		next := off + 24
		if k == n-1 {
			next = 0
		}
		buf[off] = byte(ObjectEntryArray)
		binary.LittleEndian.PutUint64(buf[off+8:], size)
		binary.LittleEndian.PutUint64(buf[off+16:], next)
	}
	path := filepath.Join(t.TempDir(), "overlap.journal")
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	offs, _ := r.EntryOffsets()
	if limit := end / (ObjectHeaderSize + 8 + 8 + 8 + 16 + 8); uint64(len(offs)) > limit {
		t.Fatalf("a %d-byte file yielded %d entry offsets (at most %d entries fit)", end, len(offs), limit)
	}
}

// appendItemEntries appends one tiny ENTRY per target, each with a
// single item naming that DATA offset.
func appendItemEntries(t *testing.T, f *os.File, targets []uint64) int64 {
	t.Helper()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	at := st.Size()
	ent := make([]byte, minEntryObjectSize+16)
	for i, target := range targets {
		clear(ent)
		ent[0] = byte(ObjectEntry)
		binary.LittleEndian.PutUint64(ent[8:], uint64(len(ent)))
		binary.LittleEndian.PutUint64(ent[ObjectHeaderSize+8:], uint64(2000+i))
		binary.LittleEndian.PutUint64(ent[minEntryObjectSize-8:], 0x5a5a)
		binary.LittleEndian.PutUint64(ent[minEntryObjectSize:], target)
		binary.LittleEndian.PutUint64(ent[minEntryObjectSize+8:], 0x5a5a)
		if _, err := f.WriteAt(ent, at); err != nil {
			t.Fatal(err)
		}
		at += int64(len(ent))
	}
	return at
}

// bigDataJournal writes one entry with a 64 KiB MESSAGE and returns the
// file, opened for writing, and the offset of that DATA object.
func bigDataJournal(t *testing.T) (*os.File, uint64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shared.journal")
	w, err := NewWriter(path, testBootID, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(&journal.Event{Ts: 1000, Msg: strings.Repeat("x", 64<<10), Prio: journal.PriorityInfo}); err != nil {
		t.Fatal(err)
	}
	w.Close()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	for off := uint64(HeaderSize); ; {
		var dh [ObjectHeaderSize]byte
		if _, err := f.ReadAt(dh[:], int64(off)); err != nil {
			t.Fatal("no 64 KiB DATA object")
		}
		d, _ := DecodeObjectHeader(dh[:])
		if d.Type == ObjectData && d.Size > 64<<10 {
			return f, off
		}
		off += AlignUp(d.Size)
	}
}

// iterAlloc iterates the file at path, keeping every event as
// slinit-journalctl --file does, and returns the bytes allocated.
func iterAlloc(t *testing.T, path string) (uint64, int, error) {
	t.Helper()
	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var kept []*journal.Event
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err = r.Iter(func(e *journal.Event) bool {
		kept = append(kept, e)
		return true
	})
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc, len(kept), err
}

// One DATA object may be named by many entries. Each entry's payload was
// bounded by the file, but not their sum: 2000 tiny entries sharing one
// 64 KiB DATA object made a 225 KB file hand back 124 MiB, all of it kept
// by slinit-journalctl --file. Entries naming one DATA object now share
// its payload.
func TestReaderIterSharesRepeatedPayloads(t *testing.T) {
	f, big := bigDataJournal(t)
	targets := make([]uint64, 2000)
	for i := range targets {
		targets[i] = big
	}
	size := appendItemEntries(t, f, targets)
	got, n, err := iterAlloc(t, f.Name())
	if err != nil || n != 2001 {
		t.Fatalf("Iter: %d entries, %v; want all 2001", n, err)
	}
	if got > 16<<20 {
		t.Fatalf("iterating a %d-byte file allocated %d bytes", size, got)
	}
}

// DATA objects may overlap: a header every 8 bytes inside one large
// payload gives as many distinct DATA objects, each nearly as large. The
// payloads one iteration reads may not add up to more than the file.
func TestReaderIterOverlappingDataBounded(t *testing.T) {
	f, big := bigDataJournal(t)
	const dataFixed = ObjectHeaderSize + 6*8
	st, _ := f.Stat()
	end := uint64(st.Size())
	var targets []uint64
	for off := big + dataFixed; off+dataFixed+(32<<10) <= end && len(targets) < 2000; off += 16 {
		var hdr [ObjectHeaderSize]byte
		hdr[0] = byte(ObjectData)
		binary.LittleEndian.PutUint64(hdr[8:], dataFixed+(32<<10))
		if _, err := f.WriteAt(hdr[:], int64(off)); err != nil {
			t.Fatal(err)
		}
		targets = append(targets, off)
	}
	size := appendItemEntries(t, f, targets)
	got, _, _ := iterAlloc(t, f.Name())
	if got > 16<<20 {
		t.Fatalf("iterating a %d-byte file allocated %d bytes for %d overlapping DATA objects", size, got, len(targets))
	}
}

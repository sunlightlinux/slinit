package journalbin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

const (
	testBootID    = "0123456789abcdef0123456789abcdef"
	testMachineID = "fedcba9876543210fedcba9876543210"
)

func TestWriterCreatesFreshFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.journal")
	w, err := NewWriter(path, testBootID, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// Fresh file layout: HeaderSize + DATA_HASH_TABLE + FIELD_HASH_TABLE.
	// Each table = ObjectHeader(16) + DefaultHashTableBuckets*HashItemSize(16),
	// aligned up to 8 bytes.
	tableBytes := int64(AlignUp(uint64(ObjectHeaderSize + DefaultHashTableBuckets*HashItemSize)))
	wantSize := int64(HeaderSize) + 2*tableBytes
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != wantSize {
		t.Fatalf("fresh file size = %d, want %d (header + 2*hash_table)", st.Size(), wantSize)
	}
	// Re-open as reader and verify magic + state=archived after close.
	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Header().Magic != Magic {
		t.Fatal("magic mismatch")
	}
	if r.Header().State != StateArchived {
		t.Fatalf("state after Close = %d, want %d", r.Header().State, StateArchived)
	}
}

func TestWriterReaderRoundtripSingleEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "single.journal")
	w, err := NewWriter(path, testBootID, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	orig := &journal.Event{
		Ts:               1_234_567_000_000, // ns
		Mts:              5_000_000_000,
		Msg:              "hello binary journal",
		Prio:             journal.PriorityWarning,
		Unit:             "sshd",
		Transport:        journal.TransportDriver,
		Pid:              4321,
		Uid:              0,
		Hostname:         "ceres",
		BootID:           testBootID,
		MachineID:        testMachineID,
		SyslogIdentifier: "openssh",
		Fields:           map[string]string{"CUSTOM_KEY": "cval"},
	}
	if _, err := w.Append(orig); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	var got []*journal.Event
	if err := r.Iter(func(e *journal.Event) bool {
		got = append(got, e)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	e := got[0]
	if e.Msg != orig.Msg {
		t.Errorf("Msg: got %q, want %q", e.Msg, orig.Msg)
	}
	if e.Prio != orig.Prio {
		t.Errorf("Prio: got %v, want %v", e.Prio, orig.Prio)
	}
	if e.Unit != orig.Unit {
		t.Errorf("Unit: got %q, want %q", e.Unit, orig.Unit)
	}
	if e.Transport != orig.Transport {
		t.Errorf("Transport: got %q, want %q", e.Transport, orig.Transport)
	}
	if e.Pid != orig.Pid {
		t.Errorf("Pid: got %d, want %d", e.Pid, orig.Pid)
	}
	if e.Hostname != orig.Hostname {
		t.Errorf("Hostname: got %q, want %q", e.Hostname, orig.Hostname)
	}
	if e.BootID != orig.BootID {
		t.Errorf("BootID: got %q, want %q", e.BootID, orig.BootID)
	}
	if e.SyslogIdentifier != orig.SyslogIdentifier {
		t.Errorf("SyslogIdentifier: got %q, want %q", e.SyslogIdentifier, orig.SyslogIdentifier)
	}
	if v, ok := e.Fields["CUSTOM_KEY"]; !ok || v != "cval" {
		t.Errorf("CUSTOM_KEY: got %q ok=%v, want cval", v, ok)
	}
	// Ts is stored at microsecond resolution → roundtrip loses sub-us
	// bits. Assert to that resolution.
	if e.Ts/1000 != orig.Ts/1000 {
		t.Errorf("Ts/us: got %d, want %d", e.Ts/1000, orig.Ts/1000)
	}
}

func TestWriterAppendsInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "many.journal")
	w, err := NewWriter(path, testBootID, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 5; i++ {
		if _, err := w.Append(&journal.Event{
			Ts:   i * 1_000_000_000,
			Msg:  "msg " + itoa(int(i)),
			Prio: journal.PriorityInfo,
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	var got []int64
	if err := r.Iter(func(e *journal.Event) bool {
		got = append(got, e.Ts/1000)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("expected 5 events, got %d", len(got))
	}
	for i, ts := range got {
		want := int64(i+1) * 1_000_000
		if ts != want {
			t.Errorf("event %d: Ts/us = %d, want %d", i, ts, want)
		}
	}
	if r.Header().NEntries != 5 {
		t.Errorf("NEntries = %d, want 5", r.Header().NEntries)
	}
	if r.Header().HeadEntrySeqnum != 1 || r.Header().TailEntrySeqnum != 5 {
		t.Errorf("seqnum range: head=%d tail=%d, want 1..5",
			r.Header().HeadEntrySeqnum, r.Header().TailEntrySeqnum)
	}
}

func TestWriterReopenAppendsSequentially(t *testing.T) {
	// Write 2 events, close, reopen, write 3 more. Reader should see 5.
	path := filepath.Join(t.TempDir(), "reopen.journal")

	w1, err := NewWriter(path, testBootID, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 2; i++ {
		w1.Append(&journal.Event{Ts: i * 1e9, Msg: "phase1", Prio: journal.PriorityInfo})
	}
	w1.Close()

	w2, err := NewWriter(path, testBootID, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(3); i <= 5; i++ {
		w2.Append(&journal.Event{Ts: i * 1e9, Msg: "phase2", Prio: journal.PriorityInfo})
	}
	w2.Close()

	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	var count int
	r.Iter(func(_ *journal.Event) bool { count++; return true })
	if count != 5 {
		t.Fatalf("reopen+append: got %d events, want 5", count)
	}
	if r.Header().TailEntrySeqnum != 5 {
		t.Errorf("TailEntrySeqnum after reopen = %d, want 5", r.Header().TailEntrySeqnum)
	}
}

func TestReaderRejectsBadMagic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bogus.journal")
	if err := os.WriteFile(path, []byte("NOPE1234"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReader(path); err == nil {
		t.Fatal("expected error opening file with bad magic")
	}
}

func TestReaderIterDetectsXorMismatch(t *testing.T) {
	// Write a valid entry, flip one byte in an item hash, re-open,
	// Iter should surface ErrHashMismatch.
	path := filepath.Join(t.TempDir(), "corrupt.journal")
	w, err := NewWriter(path, testBootID, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	entryOff, err := w.Append(&journal.Event{Ts: 1e9, Msg: "corrupt me", Prio: journal.PriorityInfo})
	if err != nil {
		t.Fatal(err)
	}
	w.Close()

	// Open the file rw, flip a byte in the ENTRY's first item hash.
	// Item region begins at entryOff + ObjectHeaderSize + 8 + 8 + 8 + 16 + 8 = +64
	// Each item is offset(8) + hash(8) — flip hash byte at +64 + 8 = +72.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := f.ReadAt(b[:], int64(entryOff)+72); err != nil {
		t.Fatal(err)
	}
	b[0] ^= 0x01
	if _, err := f.WriteAt(b[:], int64(entryOff)+72); err != nil {
		t.Fatal(err)
	}
	f.Close()

	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	err = r.Iter(func(_ *journal.Event) bool { return true })
	if err == nil {
		t.Fatal("expected error on corrupted entry, got nil")
	}
}

func TestReaderDetectsObjectBounds(t *testing.T) {
	// Craft a header + bogus object with Size past file end.
	path := filepath.Join(t.TempDir(), "oob.journal")
	w, err := NewWriter(path, testBootID, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	w.Append(&journal.Event{Ts: 1e9, Msg: "ok"})
	w.Close()

	// Fetch tail_object_offset from header, then poke an ENTRY hdr
	// there with an absurd size.
	r, _ := OpenReader(path)
	tail := r.header.TailObjectOffset
	r.Close()

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	oh := ObjectHeader{Type: ObjectEntry, Size: 1 << 40}
	buf := make([]byte, ObjectHeaderSize)
	oh.EncodeInto(buf)
	// Extend the file with the bogus header at tail so it lives past
	// the last valid object.
	if _, err := f.WriteAt(buf, int64(tail)); err != nil {
		t.Fatal(err)
	}
	f.Close()

	r2, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	err = r2.Iter(func(_ *journal.Event) bool { return true })
	if err == nil {
		t.Fatal("expected object-bounds error")
	}
}

func TestWriterDedupsRepeatedFields(t *testing.T) {
	// Two events sharing MESSAGE + PRIORITY + _TRANSPORT should
	// deduplicate those DATA objects — NData grows by the count of
	// UNIQUE payloads, not entry-count * fields.
	path := filepath.Join(t.TempDir(), "dedup.journal")
	w, err := NewWriter(path, testBootID, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	// Baseline NData after the hash tables are allocated (both tables
	// are counted as objects, not as data).
	baseNData := w.header.NData

	for i := 0; i < 3; i++ {
		if _, err := w.Append(&journal.Event{
			Ts:        int64(i+1) * 1_000_000_000,
			Msg:       "same-message",
			Prio:      journal.PriorityInfo,
			Transport: journal.TransportDriver,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Three fields, deduped across all three events → exactly 3 new
	// DATA objects total (MESSAGE, PRIORITY, _TRANSPORT).
	if got := w.header.NData - baseNData; got != 3 {
		t.Fatalf("NData grew by %d, want 3 (dedup broken)", got)
	}
	w.Close()

	// Read back and verify all 3 events surface distinctly (dedup
	// applies to storage, not to entry count).
	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var count int
	r.Iter(func(_ *journal.Event) bool { count++; return true })
	if count != 3 {
		t.Fatalf("iter yielded %d events, want 3", count)
	}
	if r.Header().NData != baseNData+3 {
		t.Errorf("final NData = %d, want %d", r.Header().NData, baseNData+3)
	}
}

func TestWriterDedupHandlesCollisionsAndUniques(t *testing.T) {
	// Different payloads produce different DATA objects; identical
	// payloads across different events reuse the same DATA.
	path := filepath.Join(t.TempDir(), "mixed.journal")
	w, err := NewWriter(path, testBootID, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	base := w.header.NData

	// Event 1: MESSAGE=a, _TRANSPORT=driver
	w.Append(&journal.Event{Ts: 1e9, Msg: "a", Prio: 1, Transport: journal.TransportDriver})
	// Event 2: MESSAGE=b, _TRANSPORT=driver  → +1 new DATA (b)
	w.Append(&journal.Event{Ts: 2e9, Msg: "b", Prio: 1, Transport: journal.TransportDriver})
	// Event 3: MESSAGE=a, _TRANSPORT=kernel  → +1 new DATA (kernel)
	w.Append(&journal.Event{Ts: 3e9, Msg: "a", Prio: 1, Transport: journal.TransportKernel})

	// Expected unique DATA: MESSAGE=a, MESSAGE=b, PRIORITY=1,
	// _TRANSPORT=driver, _TRANSPORT=kernel = 5.
	if got := w.header.NData - base; got != 5 {
		t.Fatalf("NData grew by %d, want 5 (mixed dedup broken)", got)
	}
	w.Close()
}

func TestDecodeUUIDHexRoundtrip(t *testing.T) {
	var out [16]byte
	if err := decodeUUIDHex(testBootID, out[:]); err != nil {
		t.Fatal(err)
	}
	back := encodeUUIDHex(out[:])
	if back != testBootID {
		t.Fatalf("roundtrip: got %q, want %q", back, testBootID)
	}
}

func TestDecodeUUIDHexRejectsBad(t *testing.T) {
	var out [16]byte
	if err := decodeUUIDHex("too-short", out[:]); err == nil {
		t.Fatal("expected error for short hex")
	}
	if err := decodeUUIDHex("zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", out[:]); err == nil {
		t.Fatal("expected error for non-hex chars")
	}
}

package journalbin

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// findObject returns the offset of the first (or last) object of a type.
func findObject(t *testing.T, path string, want ObjectType, last bool) uint64 {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	size := uint64(st.Size())
	var found uint64
	for off := uint64(HeaderSize); off < size; {
		var ohBuf [ObjectHeaderSize]byte
		if _, err := f.ReadAt(ohBuf[:], int64(off)); err != nil {
			break
		}
		oh, err := DecodeObjectHeader(ohBuf[:])
		if err != nil || oh.Size < ObjectHeaderSize {
			break
		}
		if oh.Type == want {
			found = off
			if !last {
				return found
			}
		}
		off += AlignUp(oh.Size)
	}
	return found
}

func reachableEntries(t *testing.T, path string) int {
	t.Helper()
	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	offs, err := r.EntryOffsets()
	if err != nil {
		t.Fatalf("EntryOffsets: %v", err)
	}
	return len(offs)
}

// Unlinking an entry from the ENTRY_ARRAY hides it from every query that
// goes through the array — EntryOffsets, SeekRealtime, and so
// `--since`/`--until` — without touching a single sealed byte. Arrays are
// mutable metadata and outside HMAC scope by design, so before this was
// reconciled the file reported "OK (2 tags verified)" with four entries
// gone. That is the shape of systemd's CVE-2023-31439.
func TestVerifyDetectsEntriesHiddenFromTheArray(t *testing.T) {
	dir := t.TempDir()
	path, key := buildSealedJournal(t, dir, 10, 5)

	before := reachableEntries(t, path)
	res, err := Verify(path, key)
	if err != nil {
		t.Fatalf("baseline verify: %v", err)
	}
	if !res.OK() || res.HiddenEntries != 0 {
		t.Fatalf("baseline is not clean: OK=%v hidden=%d", res.OK(), res.HiddenEntries)
	}
	if res.EntriesSealed == 0 {
		t.Fatal("no entries were counted as sealed — the reconciliation has nothing to check")
	}

	eaOff := findObject(t, path, ObjectEntryArray, false)
	if eaOff == 0 {
		t.Skip("no ENTRY_ARRAY in file")
	}
	// Layout: [object header][next:8][item0:8]... — a zero item ends the
	// array walk, so zeroing item0 drops this array's whole run.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	var zero [8]byte
	if _, err := f.WriteAt(zero[:], int64(eaOff)+int64(ObjectHeaderSize)+8); err != nil {
		t.Fatal(err)
	}
	f.Close()

	after := reachableEntries(t, path)
	if after >= before {
		t.Fatalf("the tamper did not hide anything (%d -> %d); test proves nothing",
			before, after)
	}

	res2, err := Verify(path, key)
	if err != nil {
		t.Fatalf("verify after tamper: %v", err)
	}
	if res2.OK() {
		t.Errorf("verify reports clean after %d entries were hidden from the array "+
			"(%d -> %d reachable)", before-after, before, after)
	}
	if res2.HiddenEntries != before-after {
		t.Errorf("HiddenEntries = %d, want %d", res2.HiddenEntries, before-after)
	}
	if res2.TagsChecked == 0 {
		t.Error("every TAG should still verify — the entry bytes were untouched")
	}
}

// A normally-closed file has a closing TAG, so nothing is left unsealed.
// This guards the new field against reporting phantom tails on healthy
// journals, which would train operators to ignore it.
func TestVerifyCleanFileHasNoUnsealedTail(t *testing.T) {
	for _, n := range []int{1, 7, 10, 23} {
		dir := t.TempDir()
		path, key := buildSealedJournal(t, dir, n, 5)
		res, err := Verify(path, key)
		if err != nil {
			t.Fatalf("%d entries: %v", n, err)
		}
		if !res.OK() {
			t.Errorf("%d entries: clean file reports not-OK (hidden=%d badTag=%d)",
				n, res.HiddenEntries, res.FirstBadTagOffset)
		}
		if res.UnsealedTailBytes != 0 {
			t.Errorf("%d entries: UnsealedTailBytes = %d on a cleanly closed file",
				n, res.UnsealedTailBytes)
		}
		if res.EntriesSealed != n {
			t.Errorf("%d entries: EntriesSealed = %d, want %d", n, res.EntriesSealed, n)
		}
	}
}

// Removing the closing TAG is what a killed journald leaves behind. The
// tail it leaves is sealed by nothing; verify must not present that as a
// verified file. Here the truncation also breaks the object walk, so the
// signal is an error — the point is that it is not a silent pass.
func TestVerifyDoesNotPassOffAnUnsealedTailAsVerified(t *testing.T) {
	dir := t.TempDir()
	path, key := buildSealedJournal(t, dir, 12, 5)

	lastTag := findObject(t, path, ObjectTag, true)
	if lastTag == 0 {
		t.Skip("no TAG found")
	}
	if err := os.Truncate(path, int64(lastTag)); err != nil {
		t.Fatal(err)
	}

	res, err := Verify(path, key)
	if err == nil && res.OK() && res.UnsealedTailBytes == 0 {
		t.Error("a journal whose closing TAG was removed verified clean with no " +
			"unsealed tail reported — the tail after the last TAG is sealed by nothing")
	}
}

// patchHeader rewrites the header of the journal at path with edit.
func patchHeader(t *testing.T, path string, edit func(*Header)) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buf := make([]byte, HeaderSize)
	if _, err := f.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	h, err := DecodeHeader(buf)
	if err != nil {
		t.Fatal(err)
	}
	edit(h)
	if err := h.EncodeInto(buf); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(buf, 0); err != nil {
		t.Fatal(err)
	}
}

// verifyFails reports whether Verify refuses the file.
func verifyFails(t *testing.T, path string, key *FSSKey) bool {
	t.Helper()
	res, err := Verify(path, key)
	return err != nil || !res.OK()
}

// Verify walked to the unsealed header field TailObjectOffset while the
// readers walk to the end of the file. Pointing it at the header made
// Verify check nothing and report OK, and the readers still returned
// every entry, a tampered one included.
func TestVerifyWalksToTheFileEnd(t *testing.T) {
	path, key := buildSealedJournal(t, t.TempDir(), 10, 5)
	entry := findObject(t, path, ObjectEntry, false)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{0xff}, int64(entry)+ObjectHeaderSize+8+3); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if !verifyFails(t, path, key) {
		t.Fatal("baseline: a tampered entry verified")
	}
	patchHeader(t, path, func(h *Header) { h.TailObjectOffset = HeaderSize })
	if !verifyFails(t, path, key) {
		t.Fatal("a tampered entry verified once TailObjectOffset pointed at the header")
	}
}

// The header's tail and counts are unsealed; like systemd's
// journal-verify, a file whose header disagrees with its objects fails.
func TestVerifyChecksHeaderAgainstObjects(t *testing.T) {
	for name, edit := range map[string]func(*Header){
		"tail at header": func(h *Header) { h.TailObjectOffset = HeaderSize },
		"tail short":     func(h *Header) { h.TailObjectOffset -= 8 },
		"n_objects":      func(h *Header) { h.NObjects++ },
		"n_entries":      func(h *Header) { h.NEntries-- },
	} {
		path, key := buildSealedJournal(t, t.TempDir(), 10, 5)
		if verifyFails(t, path, key) {
			t.Fatalf("%s: clean file fails", name)
		}
		patchHeader(t, path, edit)
		if !verifyFails(t, path, key) {
			t.Errorf("%s: header disagreeing with the objects verified", name)
		}
	}
}

// With a key in hand, a file that is not sealed proves nothing: clearing
// the sealed flag used to turn verify into "nothing to verify" and exit 0.
func TestVerifyUnsealedFileIsNotOK(t *testing.T) {
	path, key := buildSealedJournal(t, t.TempDir(), 10, 5)
	patchHeader(t, path, func(h *Header) { h.CompatFlags &^= CompatFlagSealed })
	res, err := Verify(path, key)
	if err == nil && res.OK() {
		t.Fatal("a file whose sealed flag was cleared verified OK")
	}
}

// The entry array is unsealed, so it may name an offset that no walk
// sees as an ENTRY: here a forged ENTRY carried inside a sealed MESSAGE
// payload, linked into a spare slot. EntryOffsets, and so MultiReader
// and SeekRealtime, returned it while every TAG verified.
func TestVerifyDetectsForeignEntryInTheArray(t *testing.T) {
	key, err := NewFSSKey(0, 60*1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "forged.journal")
	w, err := NewWriterWithOptions(path, WriterOptions{
		BootID: testBootID, MachineID: testMachineID, FSSKey: key, TagEvery: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	forged := make([]byte, minEntryObjectSize)
	forged[0] = byte(ObjectEntry)
	binary.LittleEndian.PutUint64(forged[8:], minEntryObjectSize)
	binary.LittleEndian.PutUint64(forged[ObjectHeaderSize+8:], 4242)
	if _, err := w.Append(&journal.Event{Ts: 1000, Msg: string(forged), Prio: journal.PriorityInfo}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(&journal.Event{Ts: 2000, Msg: "second", Prio: journal.PriorityInfo}); err != nil {
		t.Fatal(err)
	}
	arr := w.header.EntryArrayOffset
	w.Close()
	if verifyFails(t, path, key) {
		t.Fatal("clean file fails")
	}

	// The forged bytes follow the DATA fixed part and "MESSAGE=".
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := f.Stat()
	var at uint64
	for off := uint64(HeaderSize); off < uint64(st.Size()); {
		var hdr [ObjectHeaderSize]byte
		f.ReadAt(hdr[:], int64(off))
		oh, _ := DecodeObjectHeader(hdr[:])
		if oh.Type == ObjectData {
			p := make([]byte, 8+len(forged))
			f.ReadAt(p, int64(off+dataFixedPart))
			if string(p) == "MESSAGE="+string(forged) {
				at = off + dataFixedPart + 8
			}
		}
		off += AlignUp(oh.Size)
	}
	if at == 0 {
		t.Fatal("forged payload not found")
	}
	var slot [8]byte
	binary.LittleEndian.PutUint64(slot[:], at)
	if _, err := f.WriteAt(slot[:], int64(arr)+ObjectHeaderSize+8+2*8); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if n := reachableEntries(t, path); n != 3 {
		t.Fatalf("forged entry not reachable (%d entries)", n)
	}
	if !verifyFails(t, path, key) {
		t.Fatal("an entry array naming a forged ENTRY verified")
	}
}

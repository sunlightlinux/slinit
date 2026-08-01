package journalbin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// helper: build a sealed journal file with `nEntries` events all in
// the same epoch (Ts steps of 1us so epoch stays 0 for any sane
// interval). Returns path + key.
func buildSealedJournal(t *testing.T, dir string, nEntries int, tagEvery int) (string, *FSSKey) {
	t.Helper()
	key, err := NewFSSKey(0, 60*1_000_000) // 60s epoch — well beyond our test times
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sealed.journal")
	w, err := NewWriterWithOptions(path, WriterOptions{
		BootID:    testBootID,
		MachineID: testMachineID,
		FSSKey:    key,
		TagEvery:  tagEvery,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= int64(nEntries); i++ {
		if _, err := w.Append(&journal.Event{
			Ts:   i * 1000, // us
			Msg:  "e" + itoa(int(i)),
			Prio: journal.PriorityInfo,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path, key
}

func TestVerifyCleanFileNoTampering(t *testing.T) {
	path, key := buildSealedJournal(t, t.TempDir(), 20, 5) // TagEvery=5 → 4 mid-run tags + 1 close tag = 5

	res, err := Verify(path, key)
	if err != nil {
		t.Fatal(err)
	}
	if !res.SealingEnabled {
		t.Fatal("expected SealingEnabled true (file was written with FSS)")
	}
	if !res.OK() {
		t.Fatalf("expected OK verify, got first-bad at offset %d (seqnum %d)",
			res.FirstBadTagOffset, res.FirstBadTagSeqnum)
	}
	if res.TagsChecked < 4 {
		t.Errorf("TagsChecked = %d, want ≥4 (20 entries / TagEvery=5)", res.TagsChecked)
	}
}

func TestVerifyUnsealedFileShortcircuits(t *testing.T) {
	// A regular (unsealed) file — Verify should report
	// SealingEnabled=false and not fail.
	path := filepath.Join(t.TempDir(), "plain.journal")
	w, err := NewWriter(path, testBootID, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	w.Append(&journal.Event{Ts: 1000, Msg: "plain"})
	w.Close()

	key, _ := NewFSSKey(0, 60_000_000)
	res, err := Verify(path, key)
	if err != nil {
		t.Fatal(err)
	}
	if res.SealingEnabled {
		t.Fatal("unsealed file must not report SealingEnabled")
	}
	if !res.OK() {
		t.Fatal("unsealed file must trivially pass Verify")
	}
	if res.TagsChecked != 0 {
		t.Errorf("TagsChecked = %d, want 0", res.TagsChecked)
	}
}

func TestVerifyDetectsBitFlipInEntry(t *testing.T) {
	dir := t.TempDir()
	path, key := buildSealedJournal(t, dir, 10, 5)

	// Find the first ENTRY object and flip a byte inside its body.
	// HMAC covers the entire ENTRY body, so any bit-flip there
	// invalidates the tag that sealed this span. Bytes inside a
	// DATA object's mutable prefix (hash/next_hash/…) are NOT in
	// HMAC scope by design — they get rewritten by later Appends and
	// sealing them would require a compact-on-rotate pass we don't
	// have. See collectHMACInput doc.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	st, _ := f.Stat()
	fileSz := uint64(st.Size())
	off := uint64(HeaderSize)
	var entryOff uint64
	for off < fileSz {
		var ohBuf [ObjectHeaderSize]byte
		if _, err := f.ReadAt(ohBuf[:], int64(off)); err != nil {
			t.Fatal(err)
		}
		oh, err := DecodeObjectHeader(ohBuf[:])
		if err != nil {
			t.Fatal(err)
		}
		if oh.Type == ObjectEntry {
			entryOff = off
			break
		}
		off += AlignUp(oh.Size)
	}
	if entryOff == 0 {
		t.Fatal("no ENTRY found in sealed file")
	}
	// Flip a byte inside the entry body — pick offset entryOff +
	// ObjectHeaderSize + 8 + 8 (into the realtime_usec field).
	target := int64(entryOff) + int64(ObjectHeaderSize) + 16
	var b [1]byte
	if _, err := f.ReadAt(b[:], target); err != nil {
		t.Fatal(err)
	}
	b[0] ^= 0x01
	if _, err := f.WriteAt(b[:], target); err != nil {
		t.Fatal(err)
	}

	res, err := Verify(path, key)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK() {
		t.Fatal("expected verify to catch bit-flip inside a sealed ENTRY body")
	}
	if res.FirstBadTagOffset == 0 || res.FirstBadTagSeqnum == 0 {
		t.Fatalf("bad-tag metadata not populated: %+v", res)
	}
}

func TestVerifyDetectsBitFlipInTag(t *testing.T) {
	// Flip a byte in the stored HMAC itself. Verify recomputes and
	// mismatches immediately.
	dir := t.TempDir()
	path, key := buildSealedJournal(t, dir, 5, 100) // TagEvery=100 → only close-tag

	// Find the tag: walk objects looking for one of type TAG. Simpler
	// than parsing header — just linear scan.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	fileSz := uint64(st.Size())
	off := uint64(HeaderSize)
	var tagOff uint64
	for off < fileSz {
		var oh [ObjectHeaderSize]byte
		if _, err := f.ReadAt(oh[:], int64(off)); err != nil {
			t.Fatal(err)
		}
		if oh[0] == uint8(ObjectTag) {
			tagOff = off
			break
		}
		// Decode size to advance.
		h, err := DecodeObjectHeader(oh[:])
		if err != nil {
			t.Fatal(err)
		}
		off += AlignUp(h.Size)
	}
	if tagOff == 0 {
		t.Fatal("no TAG found in sealed file")
	}
	// Flip a byte inside the HMAC.
	var b [1]byte
	if _, err := f.ReadAt(b[:], int64(tagOff)+int64(tagHmacOffset)); err != nil {
		t.Fatal(err)
	}
	b[0] ^= 0x80
	if _, err := f.WriteAt(b[:], int64(tagOff)+int64(tagHmacOffset)); err != nil {
		t.Fatal(err)
	}

	res, err := Verify(path, key)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK() {
		t.Fatal("expected verify to catch tampered HMAC")
	}
	if res.FirstBadTagOffset != tagOff {
		t.Errorf("bad tag offset = %d, want %d", res.FirstBadTagOffset, tagOff)
	}
}

func TestVerifyReopenFSSResumes(t *testing.T) {
	// Write 5 events with FSS, close (flushes close-tag). Reopen
	// with FSS, write 5 more, close (flushes another close-tag).
	// Verify all 5+5 pass.
	dir := t.TempDir()
	key, _ := NewFSSKey(0, 60_000_000)
	path := filepath.Join(dir, "resume.journal")

	w1, err := NewWriterWithOptions(path, WriterOptions{
		BootID: testBootID, MachineID: testMachineID, FSSKey: key, TagEvery: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 5; i++ {
		w1.Append(&journal.Event{Ts: i * 1000, Msg: "p1"})
	}
	w1.Close()

	w2, err := NewWriterWithOptions(path, WriterOptions{
		BootID: testBootID, MachineID: testMachineID, FSSKey: key, TagEvery: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if w2.fssLastTagEnd <= HeaderSize {
		t.Fatal("FSS state not recovered on reopen — fssLastTagEnd stuck at header")
	}
	for i := int64(6); i <= 10; i++ {
		w2.Append(&journal.Event{Ts: i * 1000, Msg: "p2"})
	}
	w2.Close()

	res, err := Verify(path, key)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() {
		t.Fatalf("verify failed after reopen+append: bad tag at %d seq %d",
			res.FirstBadTagOffset, res.FirstBadTagSeqnum)
	}
	if res.TagsChecked < 2 {
		t.Errorf("TagsChecked = %d, want ≥2 (one per close)", res.TagsChecked)
	}
}

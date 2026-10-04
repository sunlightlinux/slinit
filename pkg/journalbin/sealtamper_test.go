package journalbin

import (
	"os"
	"testing"
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

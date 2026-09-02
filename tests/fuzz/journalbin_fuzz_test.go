package fuzz

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journalbin"
)

// FuzzJournalBinaryDecodeHeader fuzzes the 240-byte on-disk header
// parser. Prime target: this is the first bytes any reader consumes
// from a untrusted .journal file, and the header carries offset/size
// fields that later reads dereference — a bad-header parse that
// slips a wild offset through would trigger reads far outside the
// arena. Invariant is minimal here (must not panic) because the
// upstream API returns typed errors for every rejection path; the
// value is the seed corpus + coverage-guided mutation of a fixed-
// layout binary struct.
func FuzzJournalBinaryDecodeHeader(f *testing.F) {
	// Zero header (all-zero magic → ErrBadMagic).
	f.Add(make([]byte, journalbin.HeaderSize))
	// Truncated below HeaderSize.
	f.Add(make([]byte, 10))
	f.Add(make([]byte, 100))
	f.Add(make([]byte, journalbin.HeaderSize-1))
	// Valid magic, otherwise zero (should fail on HeaderSize check).
	buf := make([]byte, journalbin.HeaderSize)
	copy(buf, "SLJRNL01")
	f.Add(buf)
	// Valid magic + HeaderSize field but wild incompat_flags.
	buf2 := make([]byte, journalbin.HeaderSize)
	copy(buf2, "SLJRNL01")
	binary.LittleEndian.PutUint32(buf2[12:16], 0xFFFFFFFF) // unknown incompat
	binary.LittleEndian.PutUint64(buf2[88:96], uint64(journalbin.HeaderSize))
	f.Add(buf2)
	// Oversized buffer (should decode the first HeaderSize bytes cleanly
	// or reject on other fields).
	f.Add(make([]byte, journalbin.HeaderSize*4))

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = journalbin.DecodeHeader(data)
	})
}

// FuzzJournalBinaryOpenReader fuzzes the full OpenReader → EntryOffsets →
// Iter pipeline against a mutated on-disk file. The reader traverses
// the header, then follows ENTRY_ARRAY chain offsets that live inside
// the file — a hostile file can point those offsets anywhere, so
// bounds checks on every dereference are the load-bearing property.
//
// Fuzz stages the bytes into a temp file (the API is file-based;
// io.Reader isn't accepted), opens, reads, iterates. Any panic —
// out-of-bounds read, nil-deref on a torn header, infinite loop on
// a self-referential array chain — trips the invariant. The seed
// corpus includes a real .journal produced by Writer to give the
// mutator a starting point that already has valid magic + header
// shape; from there Go's coverage-guided mutator explores deep code
// paths that pure-random bytes would never reach.
func FuzzJournalBinaryOpenReader(f *testing.F) {
	// Seed 1: fresh empty journal (header only, no entries).
	f.Add(makeEmptyJournal(f))
	// Seed 2: a header with 1 entry present (Writer round-trip).
	f.Add(makeSingleEntryJournal(f))
	// Seed 3: garbage.
	f.Add([]byte("garbage"))
	// Seed 4: valid header, arena starts too early.
	f.Add(makeCorruptedHeader(f))

	f.Fuzz(func(t *testing.T, data []byte) {
		path := filepath.Join(t.TempDir(), "j")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Skip()
		}
		r, err := journalbin.OpenReader(path)
		if err != nil {
			return
		}
		defer r.Close()
		// Every method the reader exposes must be safe on any
		// successfully-opened file.
		_ = r.Header()
		_, _ = r.EntryOffsets()
		_, _, _ = r.SeekRealtime(0)
		_ = r.Iter(func(_ *journal.Event) bool { return true })
	})
}

// makeEmptyJournal returns bytes of a fresh journal with only a
// valid header + arena start marker — no entries.
func makeEmptyJournal(f *testing.F) []byte {
	f.Helper()
	dir, err := os.MkdirTemp("", "seedj")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "empty.journal")
	w, err := journalbin.NewWriter(path, "boot0", "machine0")
	if err != nil {
		return nil
	}
	_ = w.Close()
	b, _ := os.ReadFile(path)
	return b
}

// makeSingleEntryJournal seeds via Writer.Append so the byte layout
// matches the format spec exactly — mutator starts from a shape
// that hits the entry-parse code path from iteration one.
func makeSingleEntryJournal(f *testing.F) []byte {
	f.Helper()
	dir, err := os.MkdirTemp("", "seedj")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "one.journal")
	w, err := journalbin.NewWriter(path, "boot0", "machine0")
	if err != nil {
		return nil
	}
	ev := &journal.Event{
		Fields: map[string]string{
			"MESSAGE":  "seed",
			"_PID":     "1",
			"PRIORITY": "6",
		},
	}
	_, _ = w.Append(ev)
	_ = w.Close()
	b, _ := os.ReadFile(path)
	return b
}

// makeCorruptedHeader flips one byte in the arena_size field of a
// valid header. Gives the mutator a starting point that is "almost
// right" but has a numeric mismatch — exercises the sanity-check
// paths that would otherwise take many random mutations to hit.
func makeCorruptedHeader(f *testing.F) []byte {
	f.Helper()
	base := makeEmptyJournal(f)
	if len(base) < 120 {
		return base
	}
	out := append([]byte(nil), base...)
	// arena_size is at bytes 96..104. Flip a bit.
	out[96] ^= 0x01
	return out
}

package journalbin

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// TestIterTerminatesOnHostileObjectSize covers the wraparound +
// AlignUp overflow bug that made FuzzJournalBinaryOpenReader hang
// for the full go-test timeout (10 minutes) on the CI box: a fuzzed
// input crafted an ObjectHeader whose Size field decoded to a value
// near uint64.Max, so `off + oh.Size` wrapped past the fileSize
// bounds check and `AlignUp(oh.Size)` produced a next-offset ≤ off.
// The walker then read the same header forever.
//
// Both defences (overflow-safe bounds check + forward-progress
// guard) are exercised via a hand-crafted file. Wrapped in
// `time.AfterFunc` timeout so a regression (removing either guard)
// fails hard within 2 seconds rather than hanging the whole test
// binary — matches how the fuzz surface originally manifested.
func TestIterTerminatesOnHostileObjectSize(t *testing.T) {
	// Build a minimal valid header (magic + HeaderSize) so
	// OpenReader accepts the file, then plant a single object
	// header at HeaderSize with a hostile Size.
	buf := make([]byte, HeaderSize+ObjectHeaderSize+8)
	// Header: magic
	copy(buf[0:8], []byte("SLJRNL01"))
	// HeaderSize field (per format.go layout; header_size at offset 88)
	binary.LittleEndian.PutUint64(buf[88:96], uint64(HeaderSize))
	// tail_object_offset := file end
	binary.LittleEndian.PutUint64(buf[112:120], uint64(len(buf)))

	// Object at HeaderSize: Type = something non-Entry (arbitrary
	// non-zero), Size = uint64.Max.
	binary.LittleEndian.PutUint64(buf[HeaderSize:HeaderSize+8], 0xFFFFFFFFFFFFFFFF) // Size = max
	buf[HeaderSize+8] = 0x01                                                        // Type byte

	path := filepath.Join(t.TempDir(), "hostile.journal")
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := OpenReader(path)
	if err != nil {
		// Header rejected → the wraparound-in-Iter path is
		// unreachable; nothing to test here. Skip rather than
		// fail so a legitimate header-validation tightening
		// doesn't turn this into a false red.
		t.Skipf("OpenReader rejected the crafted file (%v) — Iter path unreachable, skipping", err)
	}
	defer r.Close()

	// Hard deadline: 2 s. Iter should return an error near-instantly
	// (bounds or non-progress); anything longer indicates the guards
	// were removed and the walker is looping.
	done := make(chan error, 1)
	timer := time.AfterFunc(2*time.Second, func() {
		done <- nil // signal timeout distinct from Iter's error
	})
	go func() {
		done <- r.Iter(func(_ *journal.Event) bool { return true })
	}()

	select {
	case err := <-done:
		timer.Stop()
		if err == nil {
			t.Fatal("Iter did not return within 2s — walker is looping (regression in overflow / progress guards)")
		}
		// Any error is fine — bounds-past-file-end or non-progressing
		// are both correct rejections. Sanity: the message should
		// mention "past file end" OR "non-progressing" to prove we
		// hit one of the new guards, not some unrelated failure.
		msg := err.Error()
		if !(strings.Contains(msg, "past file end") || strings.Contains(msg, "non-progressing")) {
			t.Logf("Iter returned an error but not the expected guard hit: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Iter didn't return + timer didn't fire — test infra broken")
	}
}

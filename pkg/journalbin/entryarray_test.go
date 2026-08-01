package journalbin

import (
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

func TestEntryArrayChainGrows(t *testing.T) {
	// Write more entries than the initial array capacity to force at
	// least one chain link (initial 4 → 8 → 16 → ...). 20 entries
	// exercises 4 + 8 + 8-more-in-a-16-cap-array = 3 arrays.
	path := filepath.Join(t.TempDir(), "chain.journal")
	w, err := NewWriter(path, testBootID, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 20; i++ {
		if _, err := w.Append(&journal.Event{
			Ts:   i * 1_000_000_000,
			Msg:  "entry " + itoa(int(i)),
			Prio: journal.PriorityInfo,
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	w.Close()

	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	offsets, err := r.EntryOffsets()
	if err != nil {
		t.Fatal(err)
	}
	if len(offsets) != 20 {
		t.Fatalf("EntryOffsets len = %d, want 20", len(offsets))
	}
	// Offsets must be strictly increasing (append-only file).
	for i := 1; i < len(offsets); i++ {
		if offsets[i] <= offsets[i-1] {
			t.Fatalf("offsets not monotonic at %d: %d <= %d", i, offsets[i], offsets[i-1])
		}
	}
	// Header's NEntryArrays should reflect the chain length.
	// 20 entries: array1(cap=4)=4, array2(cap=8)=8, array3(cap=16)=8 → 3 arrays.
	if r.Header().NEntryArrays != 3 {
		t.Errorf("NEntryArrays = %d, want 3", r.Header().NEntryArrays)
	}
}

func TestSeekRealtime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seek.journal")
	w, err := NewWriter(path, testBootID, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	// Ts in ns; writer stores /1000 = us. Write 5 entries at
	// realtimes 1000, 2000, 3000, 4000, 5000 (us).
	for i := int64(1); i <= 5; i++ {
		w.Append(&journal.Event{
			Ts:   i * 1_000_000, // us * 1000 = ns
			Msg:  "e" + itoa(int(i)),
			Prio: journal.PriorityInfo,
		})
	}
	w.Close()

	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	cases := []struct {
		target  uint64
		wantOK  bool
		wantMsg string
	}{
		{500, true, "e1"},   // before all → first
		{1000, true, "e1"},  // exact hit on first
		{1500, true, "e2"},  // between 1 and 2 → second
		{3000, true, "e3"},  // exact middle
		{5000, true, "e5"},  // exact last
		{5001, false, ""},   // past tail → not found
	}
	for _, c := range cases {
		got, ok, err := r.SeekRealtime(c.target)
		if err != nil {
			t.Errorf("SeekRealtime(%d): %v", c.target, err)
			continue
		}
		if ok != c.wantOK {
			t.Errorf("SeekRealtime(%d): ok=%v, want %v", c.target, ok, c.wantOK)
			continue
		}
		if !c.wantOK {
			continue
		}
		evt, err := r.ReadEntryAt(got)
		if err != nil {
			t.Errorf("ReadEntryAt(%d) after Seek: %v", got, err)
			continue
		}
		if evt.Msg != c.wantMsg {
			t.Errorf("SeekRealtime(%d): got msg %q, want %q", c.target, evt.Msg, c.wantMsg)
		}
	}
}

func TestEntryArrayRecoveryOnReopen(t *testing.T) {
	// Write 6 entries (fills first array 4 + starts second 2), close.
	// Reopen writer, append 4 more. Reader must see 10 in order.
	path := filepath.Join(t.TempDir(), "resume.journal")

	w1, err := NewWriter(path, testBootID, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 6; i++ {
		w1.Append(&journal.Event{Ts: i * 1_000_000, Msg: "phase1-" + itoa(int(i))})
	}
	w1.Close()

	w2, err := NewWriter(path, testBootID, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	// After reopen, the tail array should be recovered — this next
	// batch of appends must reuse existing capacity before allocating
	// a new array.
	if w2.entryArrayTail == 0 {
		t.Fatal("expected entryArrayTail to be recovered on reopen")
	}
	for i := int64(7); i <= 10; i++ {
		w2.Append(&journal.Event{Ts: i * 1_000_000, Msg: "phase2-" + itoa(int(i))})
	}
	w2.Close()

	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	offsets, err := r.EntryOffsets()
	if err != nil {
		t.Fatal(err)
	}
	if len(offsets) != 10 {
		t.Fatalf("offsets after reopen: got %d, want 10", len(offsets))
	}
}

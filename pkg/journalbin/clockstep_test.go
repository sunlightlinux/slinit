package journalbin

import (
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// A boot whose wall clock steps back two hours and forward again: its
// entries must still come out after the previous boot and in the order
// they were written. Merged by wall-clock time alone, the stepped-back
// entries came out first, ahead of the boot before them.
func TestMultiReaderOrdersSteppedBootByMonotonic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-11-21.journal")
	base := int64(1795260900) * 1e9
	type ev struct {
		boot     string
		ts, mono int64
		msg      string
	}
	var want []ev
	write := func(boot, bootHex string, n int, ts func(i int) int64) {
		w, err := NewWriter(path, bootHex, "m")
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < n; i++ {
			e := ev{bootHex, ts(i), int64(i+1) * 1e9, boot + "-" + itoa(i)}
			want = append(want, e)
			if _, err := w.Append(&journal.Event{Ts: e.ts, Mts: e.mono, BootID: bootHex, Msg: e.msg, Unit: "u"}); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	b1 := "11111111111111111111111111111111"
	b2 := "22222222222222222222222222222222"
	b3 := "33333333333333333333333333333333"
	write("one", b1, 5, func(i int) int64 { return base + int64(i)*1e9 })
	write("two", b2, 6, func(i int) int64 {
		ts := base + 600e9 + int64(i)*1e9
		if i == 2 || i == 3 {
			ts -= 7200e9 // stepped back, earlier than boot one
		}
		return ts
	})
	write("three", b3, 3, func(i int) int64 { return base + 1200e9 + int64(i)*1e9 })

	mr, err := OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	var got []string
	if err := mr.Iter(func(e *journal.Event) bool { got = append(got, e.Msg); return true }); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i].msg {
			t.Fatalf("entry %d = %s, want %s (order %v)", i, got[i], want[i].msg, got)
		}
	}
}

package journalbin

import (
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// EntryKey gives the times and boot of an entry without decoding it, and
// they agree with what ReadEntryAt decodes.
func TestEntryKeyMatchesReadEntryAt(t *testing.T) {
	const boot = "0123456789abcdef0123456789abcdef"
	path := filepath.Join(t.TempDir(), "k.journal")
	w, err := NewWriter(path, boot, "fedcba9876543210fedcba9876543210")
	if err != nil {
		t.Fatal(err)
	}
	evt := &journal.Event{Ts: 1795770000123456000, Mts: 987654000, Msg: "m", BootID: boot,
		MachineID: "fedcba9876543210fedcba9876543210", Prio: journal.PriorityInfo}
	if _, err := w.Append(evt); err != nil {
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
	offs, err := r.EntryOffsets()
	if err != nil || len(offs) != 1 {
		t.Fatalf("offsets %v, %v", offs, err)
	}
	rt, mono, b, err := r.EntryKey(offs[0])
	if err != nil {
		t.Fatal(err)
	}
	e, err := r.ReadEntryAt(offs[0])
	if err != nil {
		t.Fatal(err)
	}
	if int64(rt) != e.Ts/1000 || int64(mono) != e.Mts/1000 || b != e.BootID || b != boot {
		t.Fatalf("EntryKey %d %d %s, ReadEntryAt %d %d %s", rt, mono, b, e.Ts, e.Mts, e.BootID)
	}
	if _, _, _, err := r.EntryKey(1 << 40); err == nil {
		t.Fatal("EntryKey past the file returned no error")
	}
}

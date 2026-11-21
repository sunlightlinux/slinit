package journald

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journalbin"
)

func TestPersistedMarkerRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persisted")
	want := PersistedMarker{BootID: "abcd", Mts: 12345, Dir: "/var/log/slinit-journal", Format: "binary"}
	if err := WritePersistedMarker(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPersistedMarker(path)
	if err != nil || got != want {
		t.Fatalf("read %+v, %v; want %+v", got, err, want)
	}
	if _, err := ReadPersistedMarker(filepath.Join(t.TempDir(), "missing")); !os.IsNotExist(err) {
		t.Errorf("missing marker: err = %v, want not-exist", err)
	}
}

func tailEvents(boot string) []*journal.Event {
	var evs []*journal.Event
	for i := int64(1); i <= 6; i++ {
		evs = append(evs, &journal.Event{BootID: boot, Mts: i * 1e9, Ts: 1795260900e9 + i*1e9,
			Msg: "m" + string(rune('0'+i)), Unit: "u"})
	}
	evs = append(evs, &journal.Event{BootID: "otherboot", Mts: 9e9, Ts: 1, Msg: "other", Unit: "u"})
	return evs
}

// Only this boot's events after the marker go to disk, into a file of
// PID 1's own that the journal readers pick up beside journald's.
func TestWriteShutdownTailBinary(t *testing.T) {
	dir := t.TempDir()
	m := PersistedMarker{BootID: "b1", Mts: 4e9, Dir: dir, Format: "binary"}
	n, err := WriteShutdownTail(m, tailEvents("b1"), "b1", "mach", time.Unix(1795261000, 0))
	if err != nil || n != 2 {
		t.Fatalf("wrote %d, %v; want 2 (m5, m6)", n, err)
	}
	mr, err := journalbin.OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	var msgs []string
	mr.Iter(func(e *journal.Event) bool { msgs = append(msgs, e.Msg); return true })
	if strings.Join(msgs, ",") != "m5,m6" {
		t.Errorf("persisted %v, want m5, m6", msgs)
	}
}

func TestWriteShutdownTailJSONL(t *testing.T) {
	dir := t.TempDir()
	m := PersistedMarker{BootID: "b1", Mts: 5e9, Dir: dir, Format: "jsonl"}
	if n, err := WriteShutdownTail(m, tailEvents("b1"), "b1", "mach", time.Now()); err != nil || n != 1 {
		t.Fatalf("wrote %d, %v; want 1", n, err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "shutdown-*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("files %v", files)
	}
	data, _ := os.ReadFile(files[0])
	if !strings.Contains(string(data), `"m6"`) || strings.Contains(string(data), `"m5"`) {
		t.Errorf("jsonl tail = %s", data)
	}
}

// A marker from another boot, or nothing newer than it, writes nothing:
// journald never persisted this boot, or persisted all of it.
func TestWriteShutdownTailNothing(t *testing.T) {
	dir := t.TempDir()
	for _, m := range []PersistedMarker{
		{BootID: "oldboot", Mts: 0, Dir: dir, Format: "binary"},
		{BootID: "b1", Mts: 6e9, Dir: dir, Format: "binary"},
	} {
		if n, err := WriteShutdownTail(m, tailEvents("b1"), "b1", "mach", time.Now()); err != nil || n != 0 {
			t.Errorf("marker %+v: wrote %d, %v; want nothing", m, n, err)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("files created: %v", entries)
	}
}

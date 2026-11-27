package main

import (
	"net"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journald"
)

type collectSink struct{ got []*journal.Event }

func (c *collectSink) Handle(e *journal.Event) error { c.got = append(c.got, e); return nil }
func (c *collectSink) Close() error                  { return nil }

// fakeRing answers one CmdJournalQuery on a unix socket with evts, as PID 1
// answers it with its event ring.
func fakeRing(t *testing.T, evts []*journal.Event) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "ctl.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		if _, _, err := control.ReadPacket(c); err != nil {
			return
		}
		for _, e := range evts {
			b, _ := e.MarshalJSONL()
			control.WritePacket(c, control.RplyJournalEntry, b)
		}
		control.WritePacket(c, control.RplyJournalDone, nil)
	}()
	return sock
}

// A restarted daemon replayed PID 1's whole ring: a clean restart doubled
// the journal (measured in the VM, 442 entries became 884). With a marker
// saying an earlier run persisted this boot up to Mts 300, only what is
// not strictly older is written again; an event at exactly the marker's
// time is kept, since several can share it.
func TestReplaySkipsWhatAnEarlierRunPersisted(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "persisted")
	if err := journald.WritePersistedMarker(marker, journald.PersistedMarker{
		BootID: "b1", Mts: 300, Dir: "/var/log/journal", Format: "binary",
	}); err != nil {
		t.Fatal(err)
	}
	ring := []*journal.Event{
		{BootID: "b1", Mts: 100, Msg: "old"},
		{BootID: "b1", Mts: 299, Msg: "old too"},
		{BootID: "b1", Mts: 300, Msg: "at the marker"},
		{BootID: "b1", Mts: 301, Msg: "new"},
		{BootID: "b0", Mts: 50, Msg: "another boot"},
	}

	var sink collectSink
	n, err := replayBacklog(fakeRing(t, ring), &sink, replaySkip(marker, "/var/log/journal", "binary"))
	if err != nil {
		t.Fatal(err)
	}
	var msgs []string
	for _, e := range sink.got {
		msgs = append(msgs, e.Msg)
	}
	want := []string{"at the marker", "new", "another boot"}
	if n != len(want) || len(msgs) != len(want) {
		t.Fatalf("replayed %d %q, want %q", n, msgs, want)
	}
	for i := range want {
		if msgs[i] != want[i] {
			t.Fatalf("replayed %q, want %q", msgs, want)
		}
	}
}

// The marker only says what is in the journal it describes: another
// directory or format, an unreadable marker, or none at all, and the whole
// backlog is replayed as before.
func TestReplaySkipOnlyForTheSameJournal(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "persisted")
	journald.WritePersistedMarker(marker, journald.PersistedMarker{
		BootID: "b1", Mts: 300, Dir: "/var/log/journal", Format: "binary",
	})
	for _, c := range []struct {
		name, path, dir, format string
	}{
		{"other directory", marker, "/run/log/journal", "binary"},
		{"other format", marker, "/var/log/journal", "jsonl"},
		{"no marker", filepath.Join(t.TempDir(), "none"), "/var/log/journal", "binary"},
		{"marker disabled", "", "/var/log/journal", "binary"},
	} {
		if f := replaySkip(c.path, c.dir, c.format); f != nil {
			t.Errorf("%s: got a filter, want the whole backlog replayed", c.name)
		}
	}
}

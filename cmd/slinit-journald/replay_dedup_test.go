package main

import (
	"net"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journalbin"
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

const (
	testBoot = "0123456789abcdef0123456789abcdef"
	testMach = "fedcba9876543210fedcba9876543210"
)

func ev(mts int64, msg string) *journal.Event {
	return &journal.Event{Ts: 1795770000000000000 + mts, Mts: mts, Msg: msg, Unit: "u",
		BootID: testBoot, MachineID: testMach, Prio: journal.PriorityInfo}
}

func writeJournal(t *testing.T, dir string, evts ...*journal.Event) {
	t.Helper()
	w, err := journalbin.NewWriter(filepath.Join(dir, "day.journal"), testBoot, testMach)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evts {
		if _, err := w.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func msgs(evts []*journal.Event) []string {
	var out []string
	for _, e := range evts {
		out = append(out, e.Msg)
	}
	return out
}

func sameMsgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A restarted daemon replayed PID 1's whole ring: a clean restart doubled
// the journal (measured in the VM, 442 entries became 884). Only the ring
// events already in the journal are skipped now. "lost" never reached
// the journal (as an event emitted while the listener was down would
// not), so it is replayed although events after it were persisted; a
// marker-style high-water mark would have dropped it for good. Two
// identical events with one copy on disk replay once.
func TestReplaySkipsOnlyWhatIsOnDisk(t *testing.T) {
	dir := t.TempDir()
	writeJournal(t, dir, ev(100, "a"), ev(200, "b"), ev(400, "d"), ev(500, "twin"))
	ring := []*journal.Event{
		ev(100, "a"), ev(200, "b"), ev(300, "lost"), ev(400, "d"),
		ev(500, "twin"), ev(500, "twin"), ev(600, "new"),
	}
	other := ev(50, "another boot")
	other.BootID = "11111111111111111111111111111111"
	ring = append(ring, other)

	var sink collectSink
	n, err := replayBacklog(fakeRing(t, ring), &sink, dedupAgainstDisk(dir, testBoot))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"lost", "twin", "new", "another boot"}
	if got := msgs(sink.got); n != len(want) || !sameMsgs(got, want) {
		t.Fatalf("replayed %d %q, want %q", n, got, want)
	}
}

// Without a journal to compare against (none yet, another directory, an
// unreadable file) the whole backlog is replayed as before.
func TestReplayWholeBacklogWhenDiskSaysNothing(t *testing.T) {
	ring := []*journal.Event{ev(100, "a"), ev(200, "b")}
	for _, dir := range []string{t.TempDir(), filepath.Join(t.TempDir(), "missing")} {
		var sink collectSink
		if _, err := replayBacklog(fakeRing(t, ring), &sink, dedupAgainstDisk(dir, testBoot)); err != nil {
			t.Fatal(err)
		}
		if got := msgs(sink.got); !sameMsgs(got, []string{"a", "b"}) {
			t.Errorf("dir %s: replayed %q, want the whole backlog", dir, got)
		}
	}
	var sink collectSink
	if _, err := replayBacklog(fakeRing(t, ring), &sink, nil); err != nil {
		t.Fatal(err)
	}
	if len(sink.got) != 2 {
		t.Errorf("no filter: replayed %d, want 2", len(sink.got))
	}
}

// Two backlog events in the same microsecond, with the same unit and
// message but from different streams, are different events: only the
// one that reached the journal is skipped (the second review found the
// first key, time/unit/message only, skipped the wrong one).
func TestReplayKeyTellsStreamsApart(t *testing.T) {
	dir := t.TempDir()
	out := ev(100, "same line")
	out.Prio = journal.PriorityInfo
	out.Fields = map[string]string{"STREAM": "stdout"}
	errl := ev(100, "same line")
	errl.Prio = journal.PriorityWarning
	errl.Fields = map[string]string{"STREAM": "stderr"}
	writeJournal(t, dir, out)

	var sink collectSink
	if _, err := replayBacklog(fakeRing(t, []*journal.Event{errl, out}), &sink, dedupAgainstDisk(dir, testBoot)); err != nil {
		t.Fatal(err)
	}
	if len(sink.got) != 1 || sink.got[0].Fields["STREAM"] != "stderr" {
		var got []string
		for _, e := range sink.got {
			got = append(got, e.Fields["STREAM"])
		}
		t.Fatalf("replayed streams %q, want only the stderr line, which is not on disk", got)
	}
}

// Files whose last entry is older than the whole backlog are not read,
// newer ones are.
func TestReplaySkipsOlderFiles(t *testing.T) {
	dir := t.TempDir()
	w, err := journalbin.NewWriter(filepath.Join(dir, "old.journal"), testBoot, testMach)
	if err != nil {
		t.Fatal(err)
	}
	w.Append(ev(10, "long ago"))
	w.Close()
	writeJournal(t, dir, ev(100, "a"))
	var sink collectSink
	if _, err := replayBacklog(fakeRing(t, []*journal.Event{ev(100, "a"), ev(200, "b")}), &sink, dedupAgainstDisk(dir, testBoot)); err != nil {
		t.Fatal(err)
	}
	if got := msgs(sink.got); !sameMsgs(got, []string{"b"}) {
		t.Fatalf("replayed %q, want [b]", got)
	}
}

package journald

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

func TestFileSinkWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileSink(dir, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	for i := 0; i < 3; i++ {
		if err := fs.Handle(&journal.Event{Ts: int64(i + 1), Unit: "svc", Msg: "line"}); err != nil {
			t.Fatalf("handle: %v", err)
		}
	}
	if err := fs.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	got := readAllEvents(t, fs.CurrentPath())
	if len(got) != 3 {
		t.Fatalf("expected 3 events on disk, got %d", len(got))
	}
	for i, e := range got {
		if e.Ts != int64(i+1) || e.Unit != "svc" {
			t.Fatalf("event %d: got %+v", i, e)
		}
	}
	written, errs := fs.Stats()
	if written != 3 || errs != 0 {
		t.Fatalf("stats: written=%d errs=%d, want 3/0", written, errs)
	}
}

func TestFileSinkFsyncCadence(t *testing.T) {
	// fsyncEvery=2 → after 2 writes the buffer must have flushed to
	// disk (readable without Flush). After 1 write the buffer holds
	// the line (readable only after Flush).
	dir := t.TempDir()
	fs, err := NewFileSink(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	if err := fs.Handle(&journal.Event{Ts: 1, Msg: "one"}); err != nil {
		t.Fatal(err)
	}
	// Not flushed yet.
	if lines := countLines(t, fs.CurrentPath()); lines != 0 {
		t.Fatalf("expected 0 lines on disk before fsync, got %d", lines)
	}
	if err := fs.Handle(&journal.Event{Ts: 2, Msg: "two"}); err != nil {
		t.Fatal(err)
	}
	// Now the second Handle should have triggered fsync.
	if lines := countLines(t, fs.CurrentPath()); lines != 2 {
		t.Fatalf("expected 2 lines on disk after fsync, got %d", lines)
	}
}

func TestFileSinkAppendMode(t *testing.T) {
	// Two sequential Sink lifecycles against the same dir should
	// append, not truncate — the ability to resume across daemon
	// restarts is what makes the file useful for post-mortem.
	dir := t.TempDir()

	fs, err := NewFileSink(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = fs.Handle(&journal.Event{Ts: 1, Msg: "first"})
	path := fs.CurrentPath()
	fs.Close()

	fs2, err := NewFileSink(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = fs2.Handle(&journal.Event{Ts: 2, Msg: "second"})
	fs2.Close()

	got := readAllEvents(t, path)
	if len(got) != 2 || got[0].Msg != "first" || got[1].Msg != "second" {
		t.Fatalf("append: got %d events %+v", len(got), got)
	}
}

func TestFileSinkFileName(t *testing.T) {
	// Deterministic name check via the exposed helper.
	got := currentFileName(time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC))
	if got != "2026-07-31.jsonl" {
		t.Fatalf("got %q", got)
	}
}

func TestFileSinkAfterCloseErrors(t *testing.T) {
	fs, err := NewFileSink(t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	fs.Close()
	if err := fs.Handle(&journal.Event{Msg: "post-close"}); err == nil {
		t.Fatal("expected error writing after Close")
	}
}

func TestFileSinkCreatesDir(t *testing.T) {
	nested := filepath.Join(t.TempDir(), "a", "b", "c")
	fs, err := NewFileSink(nested, 1)
	if err != nil {
		t.Fatalf("expected NewFileSink to mkdir -p: %v", err)
	}
	defer fs.Close()
	if _, err := os.Stat(nested); err != nil {
		t.Fatalf("dir not created: %v", err)
	}
}

// ---- helpers ---------------------------------------------------------

func readAllEvents(t *testing.T, path string) []*journal.Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []*journal.Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), journal.MaxEventSize+1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e journal.Event
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("bad JSONL: %v", err)
		}
		out = append(out, &e)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		return 0
	}
	return strings.Count(string(b), "\n")
}

// After a crash mid-line the reopened sink appended straight onto the
// fragment: the first new event became part of an unparseable line and
// was lost to readers.
func TestFileSinkReopenAfterTornTail(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileSink(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	s.Handle(journalEvent(1, "b"))
	s.Handle(journalEvent(2, "b"))
	s.Close()
	p := s.CurrentPath()
	b, _ := os.ReadFile(p)
	if err := os.WriteFile(p, b[:len(b)-50], 0o640); err != nil { // torn tail
		t.Fatal(err)
	}
	s2, err := NewFileSink(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	s2.Handle(&journal.Event{Ts: time.Now().UnixNano(), Msg: "FIRST AFTER RESTART"})
	s2.Handle(&journal.Event{Ts: time.Now().UnixNano(), Msg: "SECOND AFTER RESTART"})
	s2.Close()

	data, _ := os.ReadFile(p)
	var msgs []string
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if ev, err := journal.UnmarshalEvent([]byte(line)); err == nil {
			msgs = append(msgs, ev.Msg)
		}
	}
	want := []string{journalEvent(1, "b").Msg, "FIRST AFTER RESTART", "SECOND AFTER RESTART"}
	if strings.Join(msgs, "|") != strings.Join(want, "|") {
		t.Errorf("readable events = %q, want %q", msgs, want)
	}
	// The index points at the new events' real line starts.
	r, err := OpenIdx(idxPath(p))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for i := int64(0); i < r.Len(); i++ {
		rec, _ := r.At(i)
		if rec.Offset != 0 && data[rec.Offset-1] != '\n' {
			t.Errorf("idx record %d points mid-line at %d", i, rec.Offset)
		}
	}
}

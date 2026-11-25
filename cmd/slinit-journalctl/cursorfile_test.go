package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journald"
)

// TestCursorFileResumesAfterLastEntry — the cursor file holds the last
// entry already shown; the next run starts after it. It used to start
// at it, so every run printed that entry again (measured: a second run
// with nothing new printed "msg 19").
func TestCursorFileResumesAfterLastEntry(t *testing.T) {
	sock := startFakeJournalServer(t)
	cf := filepath.Join(t.TempDir(), "cursor")
	out, err := queryLive(t, sock, "--cursor-file", cf, "-n", "2")
	if err != nil || out != "msg 18\nmsg 19\n" {
		t.Fatalf("first run: %q, %v", out, err)
	}
	out, err = queryLive(t, sock, "--cursor-file", cf)
	if err != nil || out != "" {
		t.Fatalf("second run with nothing new: %q, %v; want nothing", out, err)
	}
}

// TestCursorFileKeepsSameTimeEntries — --cursor-file resumed strictly
// after the cursor's time, so entries written in the same tick as the
// last one shown were never printed (a binary journal keeps
// microseconds): run 1 printed A, B and C followed in the same
// microsecond, run 2 printed nothing.
func TestCursorFileKeepsSameTimeEntries(t *testing.T) {
	ts := time.Now().Add(-time.Minute).UnixNano()
	ev := func(msg string) *journal.Event {
		return &journal.Event{Ts: ts, Mts: 1e9, BootID: testBoot1, Unit: "a.service",
			SyslogIdentifier: "a", Msg: msg, Prio: 6}
	}
	buf := journal.NewEventBuffer(100)
	sock := serveJournalBuffer(t, buf)
	dir := t.TempDir()
	cf, cfFollow := filepath.Join(dir, "cursor"), filepath.Join(dir, "cursor-f")

	buf.Push(ev("A"))
	for _, f := range []string{cf, cfFollow} {
		if out, err := queryLive(t, sock, "--cursor-file", f); err != nil || out != "A\n" {
			t.Fatalf("first run: %q, %v", out, err)
		}
	}
	buf.Push(ev("B"))
	buf.Push(ev("C"))
	if out, err := queryLive(t, sock, "-f", "--cursor-file", cfFollow); err != nil || out != "B\nC\n" {
		t.Errorf("-f after A: %q, %v; want B and C", out, err)
	}
	// -n is applied after the cursor's entry is dropped.
	if out, err := queryLive(t, sock, "--cursor-file", cf, "-n", "2"); err != nil || out != "B\nC\n" {
		t.Errorf("second run: %q, %v; want B and C", out, err)
	}
	// Run 2 ended on C, the third entry of its time; a cursor names
	// only a time, so the next run repeats B and C (the residual
	// documented at cursorMode; 4.0.2 repeated all three), and the one
	// after it, past a new time, nothing.
	ev2 := ev("D")
	ev2.Ts++
	buf.Push(ev2)
	if out, err := queryLive(t, sock, "--cursor-file", cf); err != nil || out != "B\nC\nD\n" {
		t.Errorf("third run: %q, %v; want B, C and D", out, err)
	}
	if out, err := queryLive(t, sock, "--cursor-file", cf); err != nil || out != "" {
		t.Errorf("fourth run with nothing new: %q, %v; want nothing", out, err)
	}
}

// The same through -D, over the binary journal (which keeps only
// microseconds of the three nanosecond times) and the JSONL one.
func TestCursorFileKeepsSameTimeEntriesInFiles(t *testing.T) {
	for _, format := range []string{"binary", "jsonl"} {
		t.Run(format, func(t *testing.T) {
			dir := t.TempDir()
			ts := time.Now().Add(-time.Minute).Truncate(time.Microsecond).UnixNano()
			write := func(msgs ...string) {
				var s interface {
					Handle(*journal.Event) error
					Close() error
				}
				var err error
				if format == "binary" {
					s, err = journald.NewBinarySink(journald.BinarySinkOptions{Dir: dir})
				} else {
					s, err = journald.NewFileSink(dir, 1)
				}
				if err != nil {
					t.Fatal(err)
				}
				for i, m := range msgs {
					e := &journal.Event{Ts: ts + int64(100*i), Mts: 1e9, BootID: testBoot1,
						Unit: "a.service", SyslogIdentifier: "a", Msg: m, Prio: 6}
					if format == "jsonl" {
						e.Ts = ts
					}
					if err := s.Handle(e); err != nil {
						t.Fatal(err)
					}
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			cf := filepath.Join(t.TempDir(), "cursor")
			run := func() string {
				t.Helper()
				files, err := journalFilesUnder(dir)
				if err != nil {
					t.Fatal(err)
				}
				out, err := queryFiles(t, files, options{cursorFile: cf})
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			write("A")
			if out := run(); out != "A\n" {
				t.Fatalf("first run: %q", out)
			}
			write("B", "C")
			if out := run(); out != "B\nC\n" {
				t.Errorf("second run: %q; want B and C", out)
			}
			// The residual repeat of B (see the live test), then nothing.
			ts += int64(time.Microsecond)
			write("D")
			if out := run(); out != "B\nC\nD\n" {
				t.Errorf("third run: %q; want B, C and D", out)
			}
			if out := run(); out != "" {
				t.Errorf("fourth run: %q; want nothing", out)
			}
		})
	}
}

package journald

import (
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

func newTestBinarySink(t *testing.T, every int) *BinarySink {
	t.Helper()
	s, err := NewBinarySink(BinarySinkOptions{Dir: t.TempDir(), FsyncEvery: every})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// -fsync-every was stored and never read, which lost a crashed boot's journal.
func TestBinarySinkSyncsEveryN(t *testing.T) {
	s := newTestBinarySink(t, 3)
	for i := 1; i <= 7; i++ {
		if err := s.Handle(&journal.Event{Msg: "m", Ts: int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.flushes.Load(); got != 2 {
		t.Errorf("%d fsyncs after 7 events at every 3, want 2", got)
	}
}

func TestBinarySinkExplicitFlushRestartsTheCount(t *testing.T) {
	s := newTestBinarySink(t, 3)
	s.Handle(&journal.Event{Msg: "m"})
	s.Handle(&journal.Event{Msg: "m"})
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	s.Handle(&journal.Event{Msg: "m"})
	s.Handle(&journal.Event{Msg: "m"})
	if got := s.flushes.Load(); got != 1 {
		t.Errorf("%d fsyncs: the count survived a flush", got)
	}
	if err := s.Rotate(); err != nil {
		t.Fatal(err)
	}
	s.Handle(&journal.Event{Msg: "m"})
	s.Handle(&journal.Event{Msg: "m"})
	if got := s.flushes.Load(); got != 1 {
		t.Errorf("%d fsyncs: the count survived a rotation", got)
	}
}

func TestDelayedFlush(t *testing.T) {
	runs := make(chan struct{}, 8)
	d := NewDelayedFlush(10*time.Millisecond, func() { runs <- struct{}{} })
	wait := func() {
		t.Helper()
		select {
		case <-runs:
		case <-time.After(5 * time.Second):
			t.Fatal("no flush")
		}
	}
	for range 5 {
		d.Note()
	}
	wait()
	time.Sleep(30 * time.Millisecond)
	if len(runs) != 0 {
		t.Error("several Notes gave several flushes")
	}
	d.Note() // after a run: needs its own
	wait()
	time.Sleep(30 * time.Millisecond)
	if len(runs) != 0 {
		t.Error("flushed with nothing noted")
	}
	d.Note()
	d.Stop()
	d.Note()
	time.Sleep(30 * time.Millisecond)
	if len(runs) != 0 {
		t.Error("flushed after Stop")
	}
}

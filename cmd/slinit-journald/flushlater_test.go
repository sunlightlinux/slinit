package main

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

type countingSink struct{ flushes atomic.Int32 }

func (c *countingSink) Handle(*journal.Event) error { return nil }
func (c *countingSink) Close() error                { return nil }
func (c *countingSink) Flush() error                { c.flushes.Add(1); return nil }

// One event on a quiet system reaches the disk without waiting for the count.
func TestGuardedSinkFlushesAQuietTail(t *testing.T) {
	inner := &countingSink{}
	g := &guardedSink{inner: inner, flushDelay: 5 * time.Millisecond}
	if err := g.Handle(&journal.Event{Msg: "last words"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for inner.flushes.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the event was never flushed")
		}
		time.Sleep(time.Millisecond)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	n := inner.flushes.Load()
	time.Sleep(20 * time.Millisecond)
	if got := inner.flushes.Load(); got != n {
		t.Errorf("flushed after Close: %d then %d", n, got)
	}
}

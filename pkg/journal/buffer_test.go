package journal

import (
	"testing"
	"time"
)

func makeEvent(ts int64, unit string, prio Priority, msg string) *Event {
	return &Event{
		Ts:   ts,
		Unit: unit,
		Prio: prio,
		Msg:  msg,
	}
}

func TestBufferPushAndSnapshot(t *testing.T) {
	b := NewEventBuffer(4)

	// Empty ring
	if got := b.Len(); got != 0 {
		t.Errorf("empty Len = %d, want 0", got)
	}
	snap, seq := b.Snapshot()
	if len(snap) != 0 || seq != 0 {
		t.Errorf("empty Snapshot = (%d events, seq=%d)", len(snap), seq)
	}

	// Push a few, less than capacity
	b.Push(makeEvent(1, "a", PriorityInfo, "one"))
	b.Push(makeEvent(2, "a", PriorityInfo, "two"))
	b.Push(makeEvent(3, "b", PriorityInfo, "three"))

	if got := b.Len(); got != 3 {
		t.Errorf("after 3 pushes, Len = %d, want 3", got)
	}
	snap, seq = b.Snapshot()
	if len(snap) != 3 || seq != 3 {
		t.Fatalf("Snapshot after 3 pushes = (%d events, seq=%d)", len(snap), seq)
	}
	if snap[0].Msg != "one" || snap[2].Msg != "three" {
		t.Errorf("chronological order broken: %+v", snap)
	}
}

func TestBufferWrapsAndEvicts(t *testing.T) {
	b := NewEventBuffer(MinBufferCap) // 32
	// Push more than capacity — oldest should evict.
	for i := 0; i < MinBufferCap+10; i++ {
		b.Push(makeEvent(int64(i+1), "a", PriorityInfo, "x"))
	}

	if got := b.Len(); got != MinBufferCap {
		t.Errorf("after overrun, Len = %d, want %d", got, MinBufferCap)
	}
	if got := b.Seq(); got != uint64(MinBufferCap+10) {
		t.Errorf("Seq = %d, want %d", got, MinBufferCap+10)
	}

	snap, _ := b.Snapshot()
	// Oldest survivor should be event with Ts = 11 (evicted 1..10).
	if snap[0].Ts != 11 {
		t.Errorf("after eviction, oldest Ts = %d, want 11", snap[0].Ts)
	}
	// Newest should be Ts = MinBufferCap + 10.
	if snap[len(snap)-1].Ts != int64(MinBufferCap+10) {
		t.Errorf("newest Ts = %d, want %d", snap[len(snap)-1].Ts, MinBufferCap+10)
	}
}

func TestBufferCapacityFloor(t *testing.T) {
	b := NewEventBuffer(4)
	if b.Capacity() != MinBufferCap {
		t.Errorf("small capacity should be promoted to MinBufferCap; got %d", b.Capacity())
	}
	b2 := NewEventBuffer(0)
	if b2.Capacity() != DefaultBufferCap {
		t.Errorf("zero capacity should be promoted to DefaultBufferCap; got %d", b2.Capacity())
	}
}

func TestBufferPushIgnoresNil(t *testing.T) {
	b := NewEventBuffer(0)
	b.Push(nil)
	if b.Len() != 0 || b.Seq() != 0 {
		t.Errorf("Push(nil) should be a no-op; got Len=%d Seq=%d", b.Len(), b.Seq())
	}
}

func TestQueryFilterUnit(t *testing.T) {
	b := NewEventBuffer(0)
	b.Push(makeEvent(1, "nginx", PriorityInfo, "a"))
	b.Push(makeEvent(2, "sshd", PriorityInfo, "b"))
	b.Push(makeEvent(3, "nginx", PriorityInfo, "c"))
	b.Push(makeEvent(4, "docker", PriorityInfo, "d"))

	// Single-unit filter.
	events, _ := b.Query(QueryFilter{Units: []string{"nginx"}, MinPriority: -1}, 0)
	if len(events) != 2 || events[0].Msg != "a" || events[1].Msg != "c" {
		t.Errorf("single-unit filter: got %d events %v", len(events), events)
	}

	// Multi-unit filter (OR within dimension).
	events, _ = b.Query(QueryFilter{Units: []string{"nginx", "sshd"}, MinPriority: -1}, 0)
	if len(events) != 3 {
		t.Errorf("multi-unit filter: got %d events, want 3", len(events))
	}
}

func TestQueryFilterPriority(t *testing.T) {
	b := NewEventBuffer(0)
	b.Push(makeEvent(1, "a", PriorityEmergency, "e1"))
	b.Push(makeEvent(2, "a", PriorityError, "e2"))
	b.Push(makeEvent(3, "a", PriorityInfo, "e3"))
	b.Push(makeEvent(4, "a", PriorityDebug, "e4"))

	// Keep err and above (Prio <= 3).
	events, _ := b.Query(QueryFilter{MinPriority: PriorityError}, 0)
	if len(events) != 2 {
		t.Fatalf("priority filter: got %d events, want 2", len(events))
	}
	if events[0].Msg != "e1" || events[1].Msg != "e2" {
		t.Errorf("priority filter result: %+v", events)
	}
}

func TestQueryFilterTimeRange(t *testing.T) {
	b := NewEventBuffer(0)
	b.Push(makeEvent(10, "a", PriorityInfo, "x"))
	b.Push(makeEvent(20, "a", PriorityInfo, "y"))
	b.Push(makeEvent(30, "a", PriorityInfo, "z"))

	events, _ := b.Query(QueryFilter{Since: 15, Until: 25, MinPriority: -1}, 0)
	if len(events) != 1 || events[0].Msg != "y" {
		t.Errorf("time range filter: got %v", events)
	}
}

func TestQueryLimit(t *testing.T) {
	b := NewEventBuffer(0)
	for i := 0; i < 100; i++ {
		b.Push(makeEvent(int64(i+1), "a", PriorityInfo, "x"))
	}

	// Last 10 only.
	events, _ := b.Query(QueryFilter{MinPriority: -1}, 10)
	if len(events) != 10 {
		t.Fatalf("limit=10: got %d events", len(events))
	}
	// Must be the most recent 10, not the oldest.
	if events[0].Ts != 91 || events[9].Ts != 100 {
		t.Errorf("limit tail: got Ts %d..%d, want 91..100", events[0].Ts, events[9].Ts)
	}
}

func TestSeqOverrunDetection(t *testing.T) {
	b := NewEventBuffer(MinBufferCap)

	// Take a snapshot at Seq=5.
	for i := 0; i < 5; i++ {
		b.Push(makeEvent(int64(i+1), "a", PriorityInfo, "x"))
	}
	_, seq1 := b.Snapshot()
	if seq1 != 5 {
		t.Fatalf("seq1 = %d, want 5", seq1)
	}

	// Push enough to wrap the ring twice (MinBufferCap*2 pushes).
	for i := 0; i < MinBufferCap*2; i++ {
		b.Push(makeEvent(int64(100+i), "a", PriorityInfo, "y"))
	}
	_, seq2 := b.Snapshot()

	// The delta is bigger than capacity, so the follower missed events.
	if seq2-seq1 <= uint64(MinBufferCap) {
		t.Errorf("seq delta should indicate overrun: seq1=%d seq2=%d cap=%d",
			seq1, seq2, MinBufferCap)
	}
}

func TestPruneOlderThan(t *testing.T) {
	b := NewEventBuffer(0)
	now := time.Now().UnixNano()

	// Populate with events spanning 3 hours ago, 1 hour ago, and now.
	b.Push(makeEvent(now-3*int64(time.Hour), "a", PriorityInfo, "old"))
	b.Push(makeEvent(now-1*int64(time.Hour), "a", PriorityInfo, "recent"))
	b.Push(makeEvent(now, "a", PriorityInfo, "now"))

	// Prune anything older than 2 hours ago.
	pruned := b.PruneOlderThan(time.Now().Add(-2 * time.Hour))
	if pruned != 1 {
		t.Errorf("pruned = %d, want 1", pruned)
	}

	snap, _ := b.Snapshot()
	if len(snap) != 2 || snap[0].Msg != "recent" {
		t.Errorf("post-prune snapshot: got %d events, first=%q", len(snap), snap[0].Msg)
	}
}

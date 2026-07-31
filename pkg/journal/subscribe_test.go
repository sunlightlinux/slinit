package journal

import (
	"os"
	"testing"
	"time"
)

// TestSubscribeReceivesEmit checks the happy-path fanout: an emitted
// event lands on the subscribed channel intact.
func TestSubscribeReceivesEmit(t *testing.T) {
	InitIDs("testhost")
	e := NewEmitter(NewEventBuffer(0), "/dev/null-nope")
	defer e.Close()

	ch, cancel := e.Subscribe(4)
	defer cancel()

	go func() {
		_ = e.Emit(&Event{Msg: "hi", Unit: "u1", Prio: PriorityInfo})
	}()

	select {
	case evt := <-ch:
		if evt.Msg != "hi" || evt.Unit != "u1" {
			t.Fatalf("wrong event: %+v", evt)
		}
		if evt.Pid != os.Getpid() {
			t.Fatalf("expected _pid to be stamped, got %d", evt.Pid)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for subscriber event")
	}
}

// TestSubscribeMultipleReceivers verifies fanout to N subscribers.
func TestSubscribeMultipleReceivers(t *testing.T) {
	InitIDs("testhost")
	e := NewEmitter(NewEventBuffer(0), "/dev/null-nope")
	defer e.Close()

	ch1, cancel1 := e.Subscribe(4)
	ch2, cancel2 := e.Subscribe(4)
	defer cancel1()
	defer cancel2()

	if got := e.SubscriberCount(); got != 2 {
		t.Fatalf("SubscriberCount: got %d want 2", got)
	}

	_ = e.Emit(&Event{Msg: "fanout"})

	for name, ch := range map[string]<-chan *Event{"ch1": ch1, "ch2": ch2} {
		select {
		case evt := <-ch:
			if evt.Msg != "fanout" {
				t.Fatalf("%s: wrong msg %q", name, evt.Msg)
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatalf("%s: timed out", name)
		}
	}
}

// TestSubscribeSlowConsumerDrop verifies the emit path does NOT block
// when a subscriber channel is full.
func TestSubscribeSlowConsumerDrop(t *testing.T) {
	InitIDs("testhost")
	e := NewEmitter(NewEventBuffer(0), "/dev/null-nope")
	defer e.Close()

	// Buffer size 2 — we'll emit 10 without reading.
	ch, cancel := e.Subscribe(2)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10; i++ {
			_ = e.Emit(&Event{Msg: "spam"})
		}
	}()

	select {
	case <-done:
		// Emitter did not block despite full subscriber. Good.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("emit blocked on full subscriber")
	}

	// Drain what we can — should be exactly bufferSize (2) events.
	got := 0
drainLoop:
	for {
		select {
		case <-ch:
			got++
		default:
			break drainLoop
		}
	}
	if got != 2 {
		t.Fatalf("expected 2 events buffered, got %d", got)
	}
}

// TestSubscribeCancelClosesChannel verifies the returned cancel func
// closes the channel and removes the subscription from the emitter.
func TestSubscribeCancelClosesChannel(t *testing.T) {
	InitIDs("testhost")
	e := NewEmitter(NewEventBuffer(0), "/dev/null-nope")
	defer e.Close()

	ch, cancel := e.Subscribe(4)
	if e.SubscriberCount() != 1 {
		t.Fatalf("expected 1 subscriber, got %d", e.SubscriberCount())
	}

	cancel()
	if e.SubscriberCount() != 0 {
		t.Fatalf("cancel didn't remove subscriber, count=%d", e.SubscriberCount())
	}

	// Channel is closed — reading from it should return zero value
	// with ok=false.
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after cancel")
	}

	// Second cancel is a no-op (sync.Once).
	cancel()
}

// TestSubscribeBufferSizeFloor verifies the 64-slot floor kicks in.
func TestSubscribeBufferSizeFloor(t *testing.T) {
	e := NewEmitter(NewEventBuffer(0), "/dev/null-nope")
	defer e.Close()

	ch, cancel := e.Subscribe(0)
	defer cancel()

	if cap(ch) != 64 {
		t.Fatalf("expected cap 64, got %d", cap(ch))
	}
}

func TestGlobalSubscribeNil(t *testing.T) {
	SetGlobal(nil)
	ch, cancel := GlobalSubscribe(4)
	if ch != nil {
		t.Fatal("expected nil channel with no global emitter")
	}
	// cancel must be safe to call even in the nil path.
	cancel()
}

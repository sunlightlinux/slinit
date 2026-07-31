package journald

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// captureSink is a Sink that stashes every received event so tests can
// assert on the delivered payload. Safe for concurrent Handle calls
// because the read loop is single-threaded, but the mutex guards
// against surprise if that changes.
type captureSink struct {
	mu     sync.Mutex
	events []*journal.Event
	closed atomic.Bool
	// fail forces Handle to return an error, exercising the dropped-
	// counter path.
	fail bool
}

func (c *captureSink) Handle(evt *journal.Event) error {
	c.mu.Lock()
	c.events = append(c.events, evt)
	c.mu.Unlock()
	if c.fail {
		return os.ErrClosed
	}
	return nil
}

func (c *captureSink) Close() error {
	c.closed.Store(true)
	return nil
}

func (c *captureSink) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

// tmpSocketPath returns a path inside a per-test directory so parallel
// runs don't collide on /run/slinit/events.sock (which we obviously
// can't touch during CI).
func tmpSocketPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "events.sock")
}

func TestReceiverBindAndReceive(t *testing.T) {
	journal.InitIDs("testhost")

	sink := &captureSink{}
	recv, err := NewReceiver(tmpSocketPath(t), sink)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recv.Run(ctx)

	// Emit via the same code path the real slinit uses.
	buf := journal.NewEventBuffer(4)
	e := journal.NewEmitter(buf, recv.Path())
	defer e.Close()

	if err := e.Emit(&journal.Event{Msg: "hello daemon", Unit: "svc", Prio: journal.PriorityInfo}); err != nil {
		t.Fatalf("emit: %v", err)
	}

	// Poll the sink briefly — recv is async. 250ms is plenty on a
	// local Unix socket; failing after that means real breakage.
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		if sink.len() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := recv.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !sink.closed.Load() {
		t.Fatal("sink.Close not called on Stop")
	}
	if got := sink.len(); got != 1 {
		t.Fatalf("received %d events, want 1", got)
	}
	if sink.events[0].Msg != "hello daemon" {
		t.Fatalf("wrong msg: %q", sink.events[0].Msg)
	}
	// SO_PASSCRED should have stamped OUR PID over what the emitter
	// wrote. (Since emitter runs in the same process, both are the
	// same PID — this test just proves the ancillary path fired
	// without erroring.)
	if sink.events[0].Pid != os.Getpid() {
		t.Fatalf("_pid: got %d, want %d", sink.events[0].Pid, os.Getpid())
	}
}

func TestReceiverStopIsIdempotent(t *testing.T) {
	sink := &captureSink{}
	recv, err := NewReceiver(tmpSocketPath(t), sink)
	if err != nil {
		t.Fatal(err)
	}
	recv.Run(context.Background())

	if err := recv.Stop(); err != nil {
		t.Fatal(err)
	}
	// Second Stop must not panic or error hard.
	if err := recv.Stop(); err != nil {
		t.Fatalf("second stop returned error: %v", err)
	}
}

func TestReceiverRemovesStaleSocket(t *testing.T) {
	path := tmpSocketPath(t)
	// Create a fake stale file — daemon should nuke it before bind.
	if err := os.WriteFile(path, []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	recv, err := NewReceiver(path, &captureSink{})
	if err != nil {
		t.Fatalf("bind failed despite stale file: %v", err)
	}
	if err := recv.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestReceiverNilSinkRejected(t *testing.T) {
	if _, err := NewReceiver(tmpSocketPath(t), nil); err == nil {
		t.Fatal("expected error for nil sink")
	}
}

func TestReceiverStatsCountDrops(t *testing.T) {
	journal.InitIDs("testhost")

	sink := &captureSink{fail: true}
	recv, err := NewReceiver(tmpSocketPath(t), sink)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recv.Run(ctx)

	buf := journal.NewEventBuffer(4)
	e := journal.NewEmitter(buf, recv.Path())
	defer e.Close()

	// 3 emits — all should be delivered but the sink rejects each,
	// so dropped counter climbs to 3 and received stays 0.
	for i := 0; i < 3; i++ {
		_ = e.Emit(&journal.Event{Msg: "drop me"})
	}

	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, dropped := recv.Stats(); dropped >= 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := recv.Stop(); err != nil {
		t.Fatal(err)
	}
	got, dropped := recv.Stats()
	if got != 0 {
		t.Fatalf("received=%d, want 0 (sink failing)", got)
	}
	if dropped < 3 {
		t.Fatalf("dropped=%d, want ≥3", dropped)
	}
}

func TestStdoutSinkClose(t *testing.T) {
	if err := (StdoutSink{}).Close(); err != nil {
		t.Fatal(err)
	}
}

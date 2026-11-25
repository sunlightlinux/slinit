package journal

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func initIDsForStallTest(t *testing.T) {
	t.Helper()
	resetIDsForTest()
	if err := InitIDs("test-host"); err != nil {
		t.Skipf("InitIDs failed: %v", err)
	}
	t.Cleanup(resetIDsForTest)
}

// listenUnixgram binds a datagram socket at path that nothing reads
// unless the test does: slinit-journald stopped, or its queue full.
func listenUnixgram(t *testing.T, path string) *net.UnixConn {
	t.Helper()
	ln, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatalf("listen unixgram: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

// Every state transition emits an event with the scheduling lock held.
// Once slinit-journald stopped reading, each send waited out its 10 ms
// deadline, so every transition cost 10 ms of the lock for as long as
// journald stayed stalled. Once the listener has taken nothing for
// emitStallWindow, sends do not wait until one goes through again.
func TestEmitStalledListenerDoesNotSlowEmit(t *testing.T) {
	initIDsForStallTest(t)
	p := filepath.Join(t.TempDir(), "events.sock")
	ln := listenUnixgram(t, p)
	e := NewEmitter(NewEventBuffer(MinBufferCap), p)
	defer e.Close()

	t0 := time.Now()
	for i := 0; i < 1000; i++ {
		e.Emit(&Event{Msg: fmt.Sprintf("event %d", i)})
	}
	took := time.Since(t0)
	sent, dropped := e.Stats()
	t.Logf("1000 emits to a stalled listener: %v (sent=%d dropped=%d)", took.Round(time.Millisecond), sent, dropped)
	if took > time.Second {
		t.Fatalf("1000 emits to a stalled listener took %v", took.Round(time.Millisecond))
	}

	// Once journald reads again, events reach it again.
	buf := make([]byte, 64*1024)
	for {
		ln.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		if _, err := ln.Read(buf); err != nil {
			break
		}
	}
	if err := e.Emit(&Event{Msg: "after the stall"}); err != nil {
		t.Fatalf("emit after the listener drained: %v", err)
	}
	ln.SetReadDeadline(time.Now().Add(time.Second))
	n, err := ln.Read(buf)
	if err != nil {
		t.Fatalf("the listener got nothing after it drained: %v", err)
	}
	if ev := string(buf[:n]); !strings.Contains(ev, "after the stall") {
		t.Errorf("the listener got %q", ev)
	}
}

// A listener that keeps reading but pauses now and then - slinit-journald
// fsyncing or rotating its file - is not a stalled one. After one send
// timed out during such a pause, every later send made one non-waiting
// attempt until one got through, so a burst lost most of its events, and
// journald replays the ring only when it starts. A send now waits as it
// always did until the listener has taken nothing for emitStallWindow.
func TestEmitBrieflyPausingListenerLosesNothingNew(t *testing.T) {
	initIDsForStallTest(t)
	p := filepath.Join(t.TempDir(), "events.sock")
	ln := listenUnixgram(t, p)
	e := NewEmitter(NewEventBuffer(MinBufferCap), p)
	defer e.Close()
	var got atomic.Int64
	go func() {
		buf := make([]byte, 65536)
		for {
			if _, err := ln.Read(buf); err != nil {
				return
			}
			if got.Add(1)%1000 == 0 {
				time.Sleep(30 * time.Millisecond)
			}
		}
	}()
	const events = 5000
	t0 := time.Now()
	for i := 0; i < events; i++ {
		e.Emit(&Event{Msg: fmt.Sprintf("event %d", i)})
	}
	took := time.Since(t0)
	sent, dropped := e.Stats()
	for deadline := time.Now().Add(2 * time.Second); got.Load() < int64(sent) && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("%d emits took %v; the listener got %d (sent=%d dropped=%d)",
		events, took.Round(time.Millisecond), got.Load(), sent, dropped)
	// Each pause can cost a few events, as it always could: a send still
	// gives up after 10 ms.
	if n := got.Load(); n < events-50 {
		t.Errorf("a listener pausing 30 ms every 1000 events got %d of %d", n, events)
	}
}

// The emitter keeps its connected socket, and nothing dropped it when the
// listener went away: after slinit-journald restarted, every send went to
// the old, closed socket and failed with ECONNREFUSED, and no live event
// reached the new daemon until PID 1 itself restarted.
func TestEmitReconnectsAfterListenerRestart(t *testing.T) {
	initIDsForStallTest(t)
	p := filepath.Join(t.TempDir(), "events.sock")
	ln := listenUnixgram(t, p)
	e := NewEmitter(nil, p)
	defer e.Close()
	if err := e.Emit(&Event{Msg: "before the restart"}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	buf := make([]byte, 64*1024)
	ln.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := ln.Read(buf); err != nil {
		t.Fatalf("the first listener got nothing: %v", err)
	}

	// journald restarts: the old socket goes, a new one binds the path.
	ln.Close()
	os.Remove(p)
	// An event while no daemon is there is lost, as it always was.
	e.Emit(&Event{Msg: "while it is down"})
	ln2 := listenUnixgram(t, p)

	var firstErr error
	for i := 0; i < 100; i++ {
		if err := e.Emit(&Event{Msg: fmt.Sprintf("after the restart %d", i)}); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	ln2.SetReadDeadline(time.Now().Add(time.Second))
	n, err := ln2.Read(buf)
	sent, dropped := e.Stats()
	if err != nil {
		t.Fatalf("the restarted listener got nothing (read: %v); sent=%d dropped=%d; first emit error: %v",
			err, sent, dropped, firstErr)
	}
	if ev := string(buf[:n]); !strings.Contains(ev, "after the restart 0") {
		t.Errorf("the restarted listener's first event is %q", ev)
	}
}

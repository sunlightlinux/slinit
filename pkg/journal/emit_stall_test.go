package journal

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
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
// journald stayed stalled. After one timeout the socket counts as
// congested and sends do not wait until one goes through again.
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

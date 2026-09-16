package control

import (
	"net"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// TestWritePacketSlowReaderTimesOut confirms writePacket doesn't wedge
// the caller when the client is a slow reader — the SetWriteDeadline
// arms per-write, on timeout the connection is marked closed under
// writeMu so every subsequent writePacket short-circuits instead of
// stacking another timeout window.
//
// Uses net.Pipe(): writes block until the other side reads, which is
// exactly the wedged-state-machine scenario an event listener would
// hit when a slow slinit-journalctl client stops draining.
func TestWritePacketSlowReaderTimesOut(t *testing.T) {
	// Shorten the deadline for the test — 5s is the production value,
	// but we don't need the goroutine wedge realism to prove the
	// mechanism.
	saved := writeDeadline
	writeDeadline = 50 * time.Millisecond
	defer func() { writeDeadline = saved }()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	c := &Connection{
		conn:       serverConn,
		handles:    make(map[uint32]service.Service),
		revHandles: make(map[service.Service]uint32),
		nextHandle: 1,
	}

	// Client never reads. First write should hit the write deadline
	// and return a timeout error, and the connection should be marked
	// closed as a side-effect.
	start := time.Now()
	err := c.writePacket(RplyACK, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("writePacket succeeded with a non-reading client; expected timeout")
	}
	netErr, ok := err.(net.Error)
	if !ok || !netErr.Timeout() {
		t.Fatalf("expected net.Error Timeout(), got %T: %v", err, err)
	}
	// Sanity: budget should have been honoured within a few multiples
	// (net.Pipe deadlines are precise; 500ms upper bound is generous).
	if elapsed > 500*time.Millisecond {
		t.Errorf("write blocked %v, expected ~50ms deadline", elapsed)
	}
	if !c.closed {
		t.Error("writePacket did not mark connection closed after timeout")
	}

	// Second writePacket must short-circuit on c.closed rather than
	// re-arm another 50ms deadline — otherwise a wave of listener
	// callbacks would each pay the full budget before giving up.
	start = time.Now()
	err = c.writePacket(RplyACK, nil)
	elapsed = time.Since(start)
	if err != errConnClosed {
		t.Fatalf("second writePacket: got %v, want errConnClosed", err)
	}
	if elapsed > 5*time.Millisecond {
		t.Errorf("second writePacket blocked %v; short-circuit failed", elapsed)
	}
}

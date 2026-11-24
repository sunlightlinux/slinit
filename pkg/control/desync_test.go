package control

import (
	"io"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// A client that stalls past the read deadline between a packet's header
// and its payload: serve() used to treat the timeout as idleness and
// loop, but the header was already consumed, so the payload bytes were
// parsed as fresh packets — here a wall-notice whose payload holds a
// reboot. WritePacket itself sends header and payload as two writes
// above 256 bytes, so a stopped or swapped-out client can do this.
func TestStalledPayloadDoesNotDesyncFraming(t *testing.T) {
	orig := readIdleTimeout
	readIdleTimeout = 200 * time.Millisecond
	t.Cleanup(func() { readIdleTimeout = orig })

	server, sockPath := setupTestServer(t)
	defer server.Stop()
	got := make(chan service.ShutdownType, 1)
	server.ShutdownFunc = func(st service.ShutdownType, _ uint8) { got <- st }

	conn := connectTest(t, sockPath)
	defer conn.Close()

	inner := []byte{CmdShutdown, 2, 0, uint8(service.ShutdownReboot), 0}
	if _, err := conn.Write([]byte{CmdWallNotice, byte(len(inner)), 0}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * readIdleTimeout)
	_, _ = conn.Write(inner)

	select {
	case st := <-got:
		t.Fatalf("payload bytes of a wall-notice executed as CmdShutdown(%d)", st)
	case <-time.After(time.Second):
	}

	// The server must have dropped the connection rather than keep a
	// stream it can no longer frame.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 64)); err != io.EOF {
		t.Errorf("read after a mid-packet stall: err = %v, want EOF", err)
	}
}

// Idleness between packets must not end the connection.
func TestIdleConnectionSurvivesReadDeadline(t *testing.T) {
	orig := readIdleTimeout
	readIdleTimeout = 100 * time.Millisecond
	t.Cleanup(func() { readIdleTimeout = orig })

	server, sockPath := setupTestServer(t)
	defer server.Stop()
	server.services.AddService(service.NewInternalService(server.services, "x"))
	conn := connectTest(t, sockPath)
	defer conn.Close()

	time.Sleep(5 * readIdleTimeout)
	findHandle(t, conn, "x")
}

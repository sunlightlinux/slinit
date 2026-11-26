package control

import (
	"bytes"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// lateConn is a net.Conn whose reads behave like a socket's under Go's
// poller: a read after the read deadline fails with a timeout even
// when the bytes are already buffered (internal/poll checks the
// deadline before it calls read(2)). Its first read returns and then
// lets the deadline lapse, the way a goroutine descheduled between the
// header and payload reads of one packet sees it.
type lateConn struct {
	mu       sync.Mutex
	in       *bytes.Reader
	out      bytes.Buffer
	deadline time.Time
	reads    int
	closed   bool
}

func (l *lateConn) Read(p []byte) (int, error) {
	l.mu.Lock()
	dl := l.deadline
	l.mu.Unlock()
	if !dl.IsZero() && !time.Now().Before(dl) {
		return 0, os.ErrDeadlineExceeded
	}
	l.mu.Lock()
	n, err := l.in.Read(p)
	l.reads++
	first := l.reads == 1
	l.mu.Unlock()
	if first && !dl.IsZero() {
		// The packet's remaining bytes are already here; the deadline
		// simply runs out before the next read is issued.
		time.Sleep(time.Until(dl) + 5*time.Millisecond)
	}
	return n, err
}

func (l *lateConn) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.out.Write(p)
}

func (l *lateConn) Close() error {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	return nil
}

func (l *lateConn) SetReadDeadline(t time.Time) error {
	l.mu.Lock()
	l.deadline = t
	l.mu.Unlock()
	return nil
}

func (l *lateConn) SetDeadline(t time.Time) error    { return l.SetReadDeadline(t) }
func (l *lateConn) SetWriteDeadline(time.Time) error { return nil }
func (l *lateConn) LocalAddr() net.Addr              { return &net.UnixAddr{Net: "unix"} }
func (l *lateConn) RemoteAddr() net.Addr             { return &net.UnixAddr{Net: "unix"} }

// A packet that arrived whole must be served even when the idle
// deadline runs out between reading its header and its payload. serve
// counted that as a stall inside the packet and dropped the client:
// slinitctl saw "connection reset by peer" for a request it had sent
// in full (TestIdleConnectionSurvivesReadDeadline hit it now and then).
func TestPacketStraddlingIdleDeadlineIsServed(t *testing.T) {
	orig := readIdleTimeout
	readIdleTimeout = 50 * time.Millisecond
	t.Cleanup(func() { readIdleTimeout = orig })

	server, _ := setupTestServer(t)
	defer server.Stop()
	server.services.AddService(service.NewInternalService(server.services, "x"))

	var req bytes.Buffer
	if err := WritePacket(&req, CmdFindService, EncodeServiceName("x")); err != nil {
		t.Fatal(err)
	}
	lc := &lateConn{in: bytes.NewReader(req.Bytes())}
	c := &Connection{
		server:         server,
		conn:           lc,
		handles:        make(map[uint32]service.Service),
		revHandles:     make(map[service.Service]uint32),
		nextHandle:     1,
		peerAuthorized: true,
	}

	done := make(chan struct{})
	go func() { c.serve(); close(done) }()
	select {
	case <-done: // the input ends in EOF, which ends serve
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return")
	}

	lc.mu.Lock()
	out := append([]byte(nil), lc.out.Bytes()...)
	lc.mu.Unlock()
	rply, payload, err := ReadPacket(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("no reply to a whole packet (connection dropped): %v", err)
	}
	if rply != RplyServiceRecord || len(payload) < 5 {
		t.Fatalf("reply %d %v, want RplyServiceRecord", rply, payload)
	}
}

package control

import (
	"net"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// TestUnauthorizedPeerRejected confirms that dispatch refuses any command
// when peerAuthorized is false. This is the defense-in-depth check that
// protects against perm/race mistakes on the control socket file.
func TestUnauthorizedPeerRejected(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	c := &Connection{
		conn:           serverConn,
		handles:        make(map[uint32]service.Service),
		revHandles:     make(map[service.Service]uint32),
		nextHandle:     1,
		peerAuthorized: false,
	}

	// Run dispatch in a goroutine since writePacket will block until the
	// reply is read on the other end.
	dispatchDone := make(chan error, 1)
	go func() {
		dispatchDone <- c.dispatch(CmdQueryVersion, nil)
	}()

	// Read the reply on the client side.
	clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	pktType, _, err := ReadPacket(clientConn)
	if err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}
	if pktType != RplyBadReq {
		t.Errorf("got pktType=%d, want RplyBadReq=%d", pktType, RplyBadReq)
	}

	if err := <-dispatchDone; err != nil {
		t.Errorf("dispatch returned error: %v", err)
	}
}

// TestAuthorizedPeerPasses confirms that an authorized peer's request
// progresses past the gate (QueryVersion returns the version reply, not
// BadReq).
func TestAuthorizedPeerPasses(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	c := &Connection{
		conn:           serverConn,
		handles:        make(map[uint32]service.Service),
		revHandles:     make(map[service.Service]uint32),
		nextHandle:     1,
		peerAuthorized: true,
	}

	dispatchDone := make(chan error, 1)
	go func() {
		dispatchDone <- c.dispatch(CmdQueryVersion, nil)
	}()

	clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	pktType, _, err := ReadPacket(clientConn)
	if err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}
	if pktType != RplyCPVersion {
		t.Errorf("got pktType=%d, want RplyCPVersion=%d", pktType, RplyCPVersion)
	}

	if err := <-dispatchDone; err != nil {
		t.Errorf("dispatch: %v", err)
	}
}

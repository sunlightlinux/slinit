package control

import (
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// slinitctl restart stops (with StopFlagRestarting), waits, then starts.
// In between nothing may be active (the service, and a boot target that
// depends on it), and a container's slinit, which ends when nothing is
// active, ended there: `docker exec ... slinitctl restart app` killed
// the container about half the time (tests/container case 09). The
// stop half now marks a restart pending until the start comes or the
// client goes away.
func TestClientRestartPendingBetweenStopAndStart(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	svc := service.NewInternalService(server.services, "app")
	server.services.AddService(svc)

	handleOf := func(conn net.Conn) uint32 {
		t.Helper()
		if err := WritePacket(conn, CmdFindService, EncodeServiceName("app")); err != nil {
			t.Fatal(err)
		}
		rply, payload := readReply(t, conn)
		if rply != RplyServiceRecord || len(payload) < 5 {
			t.Fatalf("find: reply %d", rply)
		}
		return binary.LittleEndian.Uint32(payload[1:5])
	}
	send := func(conn net.Conn, cmd uint8, payload []byte) {
		t.Helper()
		if err := WritePacket(conn, cmd, payload); err != nil {
			t.Fatal(err)
		}
		readReply(t, conn)
	}
	waitPending := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for server.services.ClientRestartPending() != want {
			if time.Now().After(deadline) {
				t.Fatalf("ClientRestartPending = %v, want %v", !want, want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	conn := connectTest(t, sockPath)
	h := handleOf(conn)
	send(conn, CmdStartService, EncodeHandle(h))
	if server.services.ClientRestartPending() {
		t.Fatal("pending before any restart")
	}

	// Stop half of a restart, then the start half.
	send(conn, CmdStopService, append(EncodeHandle(h), StopFlagRestarting))
	waitPending(true)
	send(conn, CmdStartService, EncodeHandle(h))
	waitPending(false)

	// A plain stop holds nothing.
	send(conn, CmdStopService, append(EncodeHandle(h), 0))
	if server.services.ClientRestartPending() {
		t.Fatal("a plain stop left a restart pending")
	}

	// A client that goes away between the halves releases its restart.
	send(conn, CmdStartService, EncodeHandle(h))
	send(conn, CmdStopService, append(EncodeHandle(h), StopFlagRestarting))
	waitPending(true)
	conn.Close()
	waitPending(false)
}

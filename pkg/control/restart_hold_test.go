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
	// A restart is not a stop on request: a crash after it must still
	// read as a failure in a container.
	if server.services.OperatorStopRequested() {
		t.Error("the stop half of a restart counted as a stop on request")
	}

	// A plain stop holds nothing.
	send(conn, CmdStopService, append(EncodeHandle(h), 0))
	if server.services.ClientRestartPending() {
		t.Fatal("a plain stop left a restart pending")
	}

	// The start that ends a restart need not name the same service, and
	// a doubled restarting stop holds once (the second review found a
	// hold per service could be left open by either).
	other := service.NewInternalService(server.services, "other")
	server.services.AddService(other)
	if err := WritePacket(conn, CmdFindService, EncodeServiceName("other")); err != nil {
		t.Fatal(err)
	}
	_, op := readReply(t, conn)
	ho := binary.LittleEndian.Uint32(op[1:5])
	send(conn, CmdStartService, EncodeHandle(h))
	send(conn, CmdStopService, append(EncodeHandle(h), StopFlagRestarting))
	send(conn, CmdStopService, append(EncodeHandle(h), StopFlagRestarting))
	waitPending(true)
	send(conn, CmdStartService, EncodeHandle(ho))
	waitPending(false)

	// A client that goes away between the halves releases its restart.
	send(conn, CmdStartService, EncodeHandle(h))
	send(conn, CmdStopService, append(EncodeHandle(h), StopFlagRestarting))
	waitPending(true)
	conn.Close()
	waitPending(false)
}

// A service waiting out its restart delay is STOPPED but counted active;
// the stop half of a restart sent then drops that, and was answered
// "already stopped" without a hold, so a container exited before the
// start came (the holistic review's F1). It holds now too.
func TestRestartDuringRestartDelayHolds(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	svc := service.NewProcessService(server.services, "app")
	svc.SetCommand([]string{"/bin/sh", "-c", "sleep 0.2; exit 1"})
	svc.SetAutoRestart(service.RestartAlways)
	svc.SetRestartDelay(5 * time.Second)
	server.services.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()
	if err := WritePacket(conn, CmdFindService, EncodeServiceName("app")); err != nil {
		t.Fatal(err)
	}
	_, payload := readReply(t, conn)
	h := binary.LittleEndian.Uint32(payload[1:5])
	if err := WritePacket(conn, CmdStartService, EncodeHandle(h)); err != nil {
		t.Fatal(err)
	}
	readReply(t, conn)

	// Wait for the crash: STOPPED, inside the 5 s delay, still active.
	deadline := time.Now().Add(3 * time.Second)
	for svc.State() != service.StateStopped || server.services.CountActiveServices() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("never in the restart delay: state %v active %d", svc.State(), server.services.CountActiveServices())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := WritePacket(conn, CmdStopService, append(EncodeHandle(h), StopFlagRestarting)); err != nil {
		t.Fatal(err)
	}
	readReply(t, conn)
	if !server.services.ClientRestartPending() {
		t.Fatalf("no restart hold for a restart in the restart delay (active %d)", server.services.CountActiveServices())
	}
	if err := WritePacket(conn, CmdStartService, EncodeHandle(h)); err != nil {
		t.Fatal(err)
	}
	readReply(t, conn)
	// The hold ends in a deferred call after the reply is written (so
	// that the wake-up it causes sees the service active), so the client
	// can read the reply a moment before the hold is gone: wait for it,
	// bounded. Checked at once, this failed about one run in four.
	for deadline := time.Now().Add(2 * time.Second); server.services.ClientRestartPending(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the start did not end the hold")
		}
	}
}

// A plain stop of a service that is simply down changes nothing and is
// not a stop on request.
func TestStopOfStoppedServiceDoesNotMark(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	svc := service.NewInternalService(server.services, "idle")
	server.services.AddService(svc)
	conn := connectTest(t, sockPath)
	defer conn.Close()
	if err := WritePacket(conn, CmdFindService, EncodeServiceName("idle")); err != nil {
		t.Fatal(err)
	}
	_, payload := readReply(t, conn)
	h := binary.LittleEndian.Uint32(payload[1:5])
	if err := WritePacket(conn, CmdStopService, append(EncodeHandle(h), 0)); err != nil {
		t.Fatal(err)
	}
	readReply(t, conn)
	if server.services.OperatorStopRequested() {
		t.Fatal("stopping a service that was already down counted as a stop on request")
	}
}

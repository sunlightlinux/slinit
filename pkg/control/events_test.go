package control

import (
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// readInfoPacket reads packets until it gets an info packet (code >= 100),
// with a timeout to prevent hangs.
func readInfoPacket(t *testing.T, conn net.Conn, timeout time.Duration) (uint8, []byte) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(timeout))
	defer conn.SetReadDeadline(time.Time{})
	for {
		rply, payload, err := ReadPacket(conn)
		if err != nil {
			t.Fatalf("Read error waiting for info packet: %v", err)
		}
		if rply >= 100 {
			return rply, payload
		}
		// Skip reply packets (shouldn't happen, but be safe)
	}
}

func TestServiceEventStarted(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewInternalService(server.services, "evt-svc")
	server.services.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Load service to get handle (auto-subscribes to events)
	if err := WritePacket(conn, CmdLoadService, EncodeServiceName("evt-svc")); err != nil {
		t.Fatal(err)
	}
	rply, payload, err := ReadPacket(conn)
	if err != nil {
		t.Fatal(err)
	}
	if rply != RplyServiceRecord {
		t.Fatalf("expected ServiceRecord, got %d", rply)
	}
	handle := binary.LittleEndian.Uint32(payload[1:5])

	// Start the service — should trigger EventStarted notification
	if err := WritePacket(conn, CmdStartService, EncodeHandle(handle)); err != nil {
		t.Fatal(err)
	}

	// Read the info packet (SERVICEEVENT)
	infoType, infoPayload := readInfoPacket(t, conn, 2*time.Second)
	if infoType != InfoServiceEvent {
		t.Fatalf("expected InfoServiceEvent (%d), got %d", InfoServiceEvent, infoType)
	}

	evtHandle, evtCode, status, err := DecodeServiceEvent(infoPayload)
	if err != nil {
		t.Fatal(err)
	}
	if evtHandle != handle {
		t.Fatalf("event handle: got %d, want %d", evtHandle, handle)
	}
	if evtCode != SvcEventStarted {
		t.Fatalf("event code: got %d, want %d (STARTED)", evtCode, SvcEventStarted)
	}
	if status.State != service.StateStarted {
		t.Fatalf("status state: got %d, want STARTED", status.State)
	}
}

func TestServiceEventStopped(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewInternalService(server.services, "stop-svc")
	server.services.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Load + start
	if err := WritePacket(conn, CmdLoadService, EncodeServiceName("stop-svc")); err != nil {
		t.Fatal(err)
	}
	rply, payload := readReply(t, conn)
	if rply != RplyServiceRecord {
		t.Fatalf("expected ServiceRecord, got %d", rply)
	}
	handle := binary.LittleEndian.Uint32(payload[1:5])

	if err := WritePacket(conn, CmdStartService, EncodeHandle(handle)); err != nil {
		t.Fatal(err)
	}
	// Drain STARTED event + ACK
	readInfoPacket(t, conn, 2*time.Second)
	readReply(t, conn)

	// Stop the service — should trigger EventStopped
	if err := WritePacket(conn, CmdStopService, EncodeHandle(handle)); err != nil {
		t.Fatal(err)
	}

	infoType, infoPayload := readInfoPacket(t, conn, 2*time.Second)
	if infoType != InfoServiceEvent {
		t.Fatalf("expected InfoServiceEvent, got %d", infoType)
	}

	_, evtCode, status, err := DecodeServiceEvent(infoPayload)
	if err != nil {
		t.Fatal(err)
	}
	if evtCode != SvcEventStopped {
		t.Fatalf("event code: got %d, want %d (STOPPED)", evtCode, SvcEventStopped)
	}
	if status.State != service.StateStopped {
		t.Fatalf("status state: got %d, want STOPPED", status.State)
	}
}

func TestServiceEventNoEventsAfterCloseHandle(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewInternalService(server.services, "close-svc")
	server.services.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Load + get handle
	if err := WritePacket(conn, CmdLoadService, EncodeServiceName("close-svc")); err != nil {
		t.Fatal(err)
	}
	rply, payload := readReply(t, conn)
	handle := binary.LittleEndian.Uint32(payload[1:5])

	// Close handle — unsubscribes from events
	if err := WritePacket(conn, CmdCloseHandle, EncodeHandle(handle)); err != nil {
		t.Fatal(err)
	}
	readReply(t, conn) // ACK

	// Start the service directly (not via protocol)
	server.services.StartService(svc)

	// Try to read — should timeout (no event expected)
	conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	_, _, err := ReadPacket(conn)
	if err == nil {
		t.Fatal("expected timeout (no event after close handle), but got a packet")
	}
	_ = rply
}

func TestListenEnvEvent(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Subscribe to env events
	if err := WritePacket(conn, CmdListenEnv, nil); err != nil {
		t.Fatal(err)
	}
	rply, _ := readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("expected ACK for ListenEnv, got %d", rply)
	}

	// Set a global env var
	server.services.GlobalSetEnv("TEST_KEY", "test_value")

	// Read the env event
	infoType, infoPayload := readInfoPacket(t, conn, 2*time.Second)
	if infoType != InfoEnvEvent {
		t.Fatalf("expected InfoEnvEvent (%d), got %d", InfoEnvEvent, infoType)
	}

	flags, varStr, err := DecodeEnvEvent(infoPayload)
	if err != nil {
		t.Fatal(err)
	}
	if varStr != "TEST_KEY=test_value" {
		t.Fatalf("env var string: got %q, want %q", varStr, "TEST_KEY=test_value")
	}
	if flags != 0 {
		t.Fatalf("flags: got %d, want 0 (fresh)", flags)
	}
}

func TestListenEnvEventOverride(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Subscribe
	WritePacket(conn, CmdListenEnv, nil)
	readReply(t, conn) // ACK

	// Set initial value
	server.services.GlobalSetEnv("OVER_KEY", "v1")
	readInfoPacket(t, conn, 2*time.Second) // drain first event

	// Override
	server.services.GlobalSetEnv("OVER_KEY", "v2")
	infoType, infoPayload := readInfoPacket(t, conn, 2*time.Second)
	if infoType != InfoEnvEvent {
		t.Fatalf("expected InfoEnvEvent, got %d", infoType)
	}

	flags, varStr, err := DecodeEnvEvent(infoPayload)
	if err != nil {
		t.Fatal(err)
	}
	if varStr != "OVER_KEY=v2" {
		t.Fatalf("env var: got %q, want %q", varStr, "OVER_KEY=v2")
	}
	if flags != EnvEventFlagOverride {
		t.Fatalf("flags: got %d, want %d (override)", flags, EnvEventFlagOverride)
	}
}

func TestListenEnvEventUnset(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Subscribe
	WritePacket(conn, CmdListenEnv, nil)
	readReply(t, conn)

	// Set then unset
	server.services.GlobalSetEnv("DEL_KEY", "val")
	readInfoPacket(t, conn, 2*time.Second) // drain set event

	server.services.GlobalUnsetEnv("DEL_KEY")
	infoType, infoPayload := readInfoPacket(t, conn, 2*time.Second)
	if infoType != InfoEnvEvent {
		t.Fatalf("expected InfoEnvEvent, got %d", infoType)
	}

	flags, varStr, err := DecodeEnvEvent(infoPayload)
	if err != nil {
		t.Fatal(err)
	}
	if varStr != "DEL_KEY" {
		t.Fatalf("unset var: got %q, want %q", varStr, "DEL_KEY")
	}
	if flags != EnvEventFlagOverride {
		t.Fatalf("flags: got %d, want %d (override for unset)", flags, EnvEventFlagOverride)
	}
}

func TestNoEnvEventsWithoutSubscription(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Don't subscribe — just set env
	server.services.GlobalSetEnv("NOSUB_KEY", "val")

	// Try to read — should timeout
	conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	_, _, err := ReadPacket(conn)
	if err == nil {
		t.Fatal("expected timeout (no event without subscription), but got a packet")
	}
}

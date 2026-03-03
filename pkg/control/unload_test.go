package control

import (
	"encoding/binary"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestControlUnloadService(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	// Add a stopped service
	svc := service.NewInternalService(server.services, "unload-me")
	server.services.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Find the service first
	nameData := EncodeServiceName("unload-me")
	if err := WritePacket(conn, CmdFindService, nameData); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, payload, err := ReadPacket(conn)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if rply != RplyServiceRecord {
		t.Fatalf("Expected ServiceRecord, got %d", rply)
	}
	handle := binary.LittleEndian.Uint32(payload[1:5])

	// Unload the stopped service
	if err := WritePacket(conn, CmdUnloadService, EncodeHandle(handle)); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, _ = readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("Expected ACK, got %d", rply)
	}

	// Verify service is gone
	if server.services.FindService("unload-me", false) != nil {
		t.Error("service should be removed after unload")
	}
}

func TestControlUnloadNotStopped(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	// Add and start a service
	svc := service.NewInternalService(server.services, "running-svc")
	server.services.AddService(svc)
	server.services.StartService(svc)

	if svc.State() != service.StateStarted {
		t.Fatalf("expected STARTED, got %v", svc.State())
	}

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Find the service
	nameData := EncodeServiceName("running-svc")
	if err := WritePacket(conn, CmdFindService, nameData); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, payload, err := ReadPacket(conn)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if rply != RplyServiceRecord {
		t.Fatalf("Expected ServiceRecord, got %d", rply)
	}
	handle := binary.LittleEndian.Uint32(payload[1:5])

	// Try to unload running service → should get NotStopped
	if err := WritePacket(conn, CmdUnloadService, EncodeHandle(handle)); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, _ = readReply(t, conn)
	if rply != RplyNotStopped {
		t.Fatalf("Expected NotStopped reply, got %d", rply)
	}

	// Service should still exist
	if server.services.FindService("running-svc", false) == nil {
		t.Error("service should still exist after failed unload")
	}
}

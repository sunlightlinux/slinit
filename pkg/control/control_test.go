package control

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// testLogger implements service.ServiceLogger for tests.
type testLogger struct{}

func (l *testLogger) ServiceStarted(name string)              {}
func (l *testLogger) ServiceStopped(name string)              {}
func (l *testLogger) ServiceFailed(name string, dep bool)     {}
func (l *testLogger) Error(format string, args ...interface{}) {}
func (l *testLogger) Info(format string, args ...interface{})  {}

func setupTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "test.socket")

	ss := service.NewServiceSet(&testLogger{})
	logger := logging.New(logging.LevelError)
	server := NewServer(ss, sockPath, logger)

	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}

	return server, sockPath
}

func connectTest(t *testing.T, sockPath string) net.Conn {
	t.Helper()
	// Wait briefly for server to be ready
	var conn net.Conn
	var err error
	for i := 0; i < 10; i++ {
		conn, err = net.Dial("unix", sockPath)
		if err == nil {
			return conn
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Failed to connect: %v", err)
	return nil
}

func TestQueryVersion(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Send query version
	if err := WritePacket(conn, CmdQueryVersion, nil); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	rply, payload, err := ReadPacket(conn)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}

	if rply != RplyCPVersion {
		t.Fatalf("Expected CPVersion reply, got %d", rply)
	}
	if len(payload) < 2 {
		t.Fatal("Payload too short")
	}
	ver := binary.LittleEndian.Uint16(payload)
	if ver != ProtocolVersion {
		t.Fatalf("Expected version %d, got %d", ProtocolVersion, ver)
	}
}

func TestFindServiceMissing(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	nameData := EncodeServiceName("nonexistent")
	if err := WritePacket(conn, CmdFindService, nameData); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	rply, _, err := ReadPacket(conn)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}

	if rply != RplyNoService {
		t.Fatalf("Expected NoService reply, got %d", rply)
	}
}

func TestFindServiceExists(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	// Add a test service
	svc := service.NewInternalService(server.services, "test-svc")
	server.services.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	nameData := EncodeServiceName("test-svc")
	if err := WritePacket(conn, CmdFindService, nameData); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	rply, payload, err := ReadPacket(conn)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}

	if rply != RplyServiceRecord {
		t.Fatalf("Expected ServiceRecord reply, got %d", rply)
	}
	if len(payload) < 6 {
		t.Fatal("Payload too short")
	}

	state := payload[0]
	if service.ServiceState(state) != service.StateStopped {
		t.Fatalf("Expected STOPPED state, got %d", state)
	}
}

func TestStartStopService(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewInternalService(server.services, "test-svc")
	server.services.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Load service to get handle
	nameData := EncodeServiceName("test-svc")
	if err := WritePacket(conn, CmdLoadService, nameData); err != nil {
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

	// Start the service
	if err := WritePacket(conn, CmdStartService, EncodeHandle(handle)); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, _, err = ReadPacket(conn)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if rply != RplyACK {
		t.Fatalf("Expected ACK, got %d", rply)
	}

	// Verify state
	if svc.State() != service.StateStarted {
		t.Fatalf("Expected STARTED, got %s", svc.State())
	}

	// Start again - should get AlreadySS
	if err := WritePacket(conn, CmdStartService, EncodeHandle(handle)); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, _, err = ReadPacket(conn)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if rply != RplyAlreadySS {
		t.Fatalf("Expected AlreadySS, got %d", rply)
	}

	// Stop the service
	if err := WritePacket(conn, CmdStopService, EncodeHandle(handle)); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, _, err = ReadPacket(conn)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if rply != RplyACK {
		t.Fatalf("Expected ACK, got %d", rply)
	}

	if svc.State() != service.StateStopped {
		t.Fatalf("Expected STOPPED, got %s", svc.State())
	}
}

func TestListServices(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	// Add multiple services
	svc1 := service.NewInternalService(server.services, "svc-alpha")
	svc2 := service.NewInternalService(server.services, "svc-beta")
	server.services.AddService(svc1)
	server.services.AddService(svc2)
	server.services.StartService(svc1)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	if err := WritePacket(conn, CmdListServices, nil); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	names := make(map[string]service.ServiceState)
	for {
		rply, payload, err := ReadPacket(conn)
		if err != nil {
			t.Fatalf("Read error: %v", err)
		}
		if rply == RplyListDone {
			break
		}
		if rply != RplySvcInfo {
			t.Fatalf("Expected SvcInfo, got %d", rply)
		}
		entry, _, err := DecodeSvcInfo(payload)
		if err != nil {
			t.Fatalf("Decode error: %v", err)
		}
		names[entry.Name] = entry.State
	}

	if len(names) != 2 {
		t.Fatalf("Expected 2 services, got %d", len(names))
	}
	if names["svc-alpha"] != service.StateStarted {
		t.Fatalf("Expected svc-alpha STARTED, got %s", names["svc-alpha"])
	}
	if names["svc-beta"] != service.StateStopped {
		t.Fatalf("Expected svc-beta STOPPED, got %s", names["svc-beta"])
	}
}

func TestServiceStatus(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewInternalService(server.services, "status-svc")
	server.services.AddService(svc)
	server.services.StartService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Load to get handle
	nameData := EncodeServiceName("status-svc")
	if err := WritePacket(conn, CmdLoadService, nameData); err != nil {
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

	// Query status
	if err := WritePacket(conn, CmdServiceStatus, EncodeHandle(handle)); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, payload, err = ReadPacket(conn)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if rply != RplyServiceStatus {
		t.Fatalf("Expected ServiceStatus, got %d", rply)
	}

	status, err := DecodeServiceStatus(payload)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}

	if status.State != service.StateStarted {
		t.Fatalf("Expected STARTED, got %s", status.State)
	}
	if status.SvcType != service.TypeInternal {
		t.Fatalf("Expected TypeInternal, got %s", status.SvcType)
	}
}

func TestShutdown(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	var receivedShutdown service.ShutdownType
	server.ShutdownFunc = func(st service.ShutdownType) {
		receivedShutdown = st
	}

	conn := connectTest(t, sockPath)
	defer conn.Close()

	payload := []byte{uint8(service.ShutdownPoweroff)}
	if err := WritePacket(conn, CmdShutdown, payload); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	rply, _, err := ReadPacket(conn)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if rply != RplyACK {
		t.Fatalf("Expected ACK, got %d", rply)
	}

	if receivedShutdown != service.ShutdownPoweroff {
		t.Fatalf("Expected ShutdownPoweroff, got %s", receivedShutdown)
	}
}

func TestSetTrigger(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewTriggeredService(server.services, "trigger-svc")
	server.services.AddService(svc)
	server.services.StartService(svc)

	// Service should be in STARTING (waiting for trigger)
	if svc.State() != service.StateStarting {
		t.Fatalf("Expected STARTING, got %s", svc.State())
	}

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Find the service to get handle
	nameData := EncodeServiceName("trigger-svc")
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

	// Set trigger
	trigPayload := make([]byte, 5)
	binary.LittleEndian.PutUint32(trigPayload, handle)
	trigPayload[4] = 1

	if err := WritePacket(conn, CmdSetTrigger, trigPayload); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, _, err = ReadPacket(conn)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if rply != RplyACK {
		t.Fatalf("Expected ACK, got %d", rply)
	}

	if svc.State() != service.StateStarted {
		t.Fatalf("Expected STARTED after trigger, got %s", svc.State())
	}
}

func TestCloseHandle(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewInternalService(server.services, "handle-svc")
	server.services.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Load to get handle
	nameData := EncodeServiceName("handle-svc")
	if err := WritePacket(conn, CmdLoadService, nameData); err != nil {
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

	// Close handle
	if err := WritePacket(conn, CmdCloseHandle, EncodeHandle(handle)); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, _, err = ReadPacket(conn)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if rply != RplyACK {
		t.Fatalf("Expected ACK, got %d", rply)
	}

	// Using closed handle should fail
	if err := WritePacket(conn, CmdStartService, EncodeHandle(handle)); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, _, err = ReadPacket(conn)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if rply != RplyBadReq {
		t.Fatalf("Expected BadReq for closed handle, got %d", rply)
	}
}

func TestMultipleConnections(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewInternalService(server.services, "multi-svc")
	server.services.AddService(svc)

	// Two concurrent connections
	conn1 := connectTest(t, sockPath)
	defer conn1.Close()
	conn2 := connectTest(t, sockPath)
	defer conn2.Close()

	// Both should be able to find the service
	nameData := EncodeServiceName("multi-svc")

	if err := WritePacket(conn1, CmdFindService, nameData); err != nil {
		t.Fatalf("Write error conn1: %v", err)
	}
	if err := WritePacket(conn2, CmdFindService, nameData); err != nil {
		t.Fatalf("Write error conn2: %v", err)
	}

	rply1, _, err := ReadPacket(conn1)
	if err != nil {
		t.Fatalf("Read error conn1: %v", err)
	}
	rply2, _, err := ReadPacket(conn2)
	if err != nil {
		t.Fatalf("Read error conn2: %v", err)
	}

	if rply1 != RplyServiceRecord {
		t.Fatalf("Conn1: expected ServiceRecord, got %d", rply1)
	}
	if rply2 != RplyServiceRecord {
		t.Fatalf("Conn2: expected ServiceRecord, got %d", rply2)
	}
}

func TestSocketCleanup(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "cleanup.socket")

	ss := service.NewServiceSet(&testLogger{})
	logger := logging.New(logging.LevelError)
	server := NewServer(ss, sockPath, logger)

	ctx := context.Background()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("Start error: %v", err)
	}

	// Socket file should exist
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		t.Fatal("Socket file should exist after start")
	}

	server.Stop()

	// Socket file should be cleaned up
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Fatal("Socket file should be removed after stop")
	}
}

func TestBadCommand(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Send an invalid command code
	if err := WritePacket(conn, 255, nil); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	rply, _, err := ReadPacket(conn)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}

	if rply != RplyBadReq {
		t.Fatalf("Expected BadReq for invalid command, got %d", rply)
	}
}

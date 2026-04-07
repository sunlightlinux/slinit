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

// readReply reads packets from conn, skipping any unsolicited info packets
// (InfoServiceEvent, InfoEnvEvent), and returns the first reply packet.
func readReply(t *testing.T, conn net.Conn) (uint8, []byte) {
	t.Helper()
	for {
		rply, payload, err := ReadPacket(conn)
		if err != nil {
			t.Fatalf("Read error: %v", err)
		}
		// Skip unsolicited info packets
		if rply >= 100 {
			continue
		}
		return rply, payload
	}
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
	if len(payload) < 4 {
		t.Fatalf("Payload too short: expected 4 bytes, got %d", len(payload))
	}
	minCompat := binary.LittleEndian.Uint16(payload[0:])
	actualVer := binary.LittleEndian.Uint16(payload[2:])
	if minCompat != MinCompatVersion {
		t.Fatalf("Expected min_compat %d, got %d", MinCompatVersion, minCompat)
	}
	if actualVer != CPVersion {
		t.Fatalf("Expected version %d, got %d", CPVersion, actualVer)
	}
	// Verify bidirectional compat: our version >= server's min compat
	if CPVersion < minCompat {
		t.Fatal("Client version is below server's min compat")
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
	rply, _ = readReply(t, conn)
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
	rply, _ = readReply(t, conn)
	if rply != RplyAlreadySS {
		t.Fatalf("Expected AlreadySS, got %d", rply)
	}

	// Stop the service
	if err := WritePacket(conn, CmdStopService, EncodeHandle(handle)); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, _ = readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("Expected ACK, got %d", rply)
	}

	if svc.State() != service.StateStopped {
		t.Fatalf("Expected STOPPED, got %s", svc.State())
	}
}

func TestWakeService(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	// parent waits-for child (soft dep — parent stays active if child stops)
	parent := service.NewInternalService(server.services, "parent")
	child := service.NewInternalService(server.services, "child")
	server.services.AddService(parent)
	server.services.AddService(child)
	parent.Record().AddDep(child, service.DepWaitsFor)

	// Start parent (child starts too)
	server.services.StartService(parent)
	if child.State() != service.StateStarted {
		t.Fatalf("child expected STARTED, got %s", child.State())
	}

	// Stop child — parent stays STARTED (soft dep)
	child.Stop(true)
	server.services.ProcessQueues()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Load child handle
	nameData := EncodeServiceName("child")
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

	// Wake child — parent is still active
	if err := WritePacket(conn, CmdWakeService, EncodeHandle(handle)); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, _ = readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("Expected ACK, got %d", rply)
	}

	if child.State() != service.StateStarted {
		t.Fatalf("child expected STARTED after wake, got %s", child.State())
	}
	if child.Record().IsMarkedActive() {
		t.Fatal("child should NOT be marked active after wake")
	}
}

func TestWakeServiceNoDependents(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewInternalService(server.services, "lonely")
	server.services.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	nameData := EncodeServiceName("lonely")
	if err := WritePacket(conn, CmdLoadService, nameData); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, payload, err := ReadPacket(conn)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	handle := binary.LittleEndian.Uint32(payload[1:5])

	// Wake with no dependents — should get NAK
	if err := WritePacket(conn, CmdWakeService, EncodeHandle(handle)); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, _ = readReply(t, conn)
	if rply != RplyNAK {
		t.Fatalf("Expected NAK for no dependents, got %d", rply)
	}
}

func TestReleaseServiceStops(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	// Start a service explicitly (marked active, no dependents)
	svc := service.NewInternalService(server.services, "release-svc")
	server.services.AddService(svc)
	server.services.StartService(svc)

	if svc.State() != service.StateStarted {
		t.Fatalf("Expected STARTED, got %s", svc.State())
	}
	if !svc.Record().IsMarkedActive() {
		t.Fatal("Expected marked active after start")
	}

	conn := connectTest(t, sockPath)
	defer conn.Close()

	nameData := EncodeServiceName("release-svc")
	if err := WritePacket(conn, CmdLoadService, nameData); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, payload, err := ReadPacket(conn)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	handle := binary.LittleEndian.Uint32(payload[1:5])

	// Release — no other dependents, so service should stop
	if err := WritePacket(conn, CmdReleaseService, EncodeHandle(handle)); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, _ = readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("Expected ACK, got %d", rply)
	}

	if svc.Record().IsMarkedActive() {
		t.Error("Expected not marked active after release")
	}
	if svc.State() != service.StateStopped {
		t.Errorf("Expected STOPPED (no dependents), got %s", svc.State())
	}
}

func TestReleaseServiceStaysRunning(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	// parent depends-on child (hard dep)
	parent := service.NewInternalService(server.services, "parent")
	child := service.NewInternalService(server.services, "child")
	server.services.AddService(parent)
	server.services.AddService(child)
	parent.Record().AddDep(child, service.DepRegular)

	// Start parent → child starts as dependency
	server.services.StartService(parent)

	// Also explicitly start child (mark it active)
	server.services.StartService(child)

	if !child.Record().IsMarkedActive() {
		t.Fatal("child should be marked active after explicit start")
	}

	conn := connectTest(t, sockPath)
	defer conn.Close()

	nameData := EncodeServiceName("child")
	if err := WritePacket(conn, CmdLoadService, nameData); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, payload, err := ReadPacket(conn)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	handle := binary.LittleEndian.Uint32(payload[1:5])

	// Release child — parent still requires it, so it should stay running
	if err := WritePacket(conn, CmdReleaseService, EncodeHandle(handle)); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, _ = readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("Expected ACK, got %d", rply)
	}

	if child.Record().IsMarkedActive() {
		t.Error("child should NOT be marked active after release")
	}
	if child.State() != service.StateStarted {
		t.Errorf("child should remain STARTED (parent still requires it), got %s", child.State())
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
		rply, payload := readReply(t, conn)
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

	rply, _ := readReply(t, conn)
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
	rply, _ = readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("Expected ACK, got %d", rply)
	}

	if svc.State() != service.StateStarted {
		t.Fatalf("Expected STARTED after trigger, got %s", svc.State())
	}
}

func TestUntrigger(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewTriggeredService(server.services, "trigger-svc")
	server.services.AddService(svc)

	// Trigger and start
	svc.SetTrigger(true)
	server.services.StartService(svc)

	if svc.State() != service.StateStarted {
		t.Fatalf("Expected STARTED, got %s", svc.State())
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

	// Untrigger (set trigger = false)
	trigPayload := make([]byte, 5)
	binary.LittleEndian.PutUint32(trigPayload, handle)
	trigPayload[4] = 0

	if err := WritePacket(conn, CmdSetTrigger, trigPayload); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, _ = readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("Expected ACK, got %d", rply)
	}

	// Service should remain STARTED but trigger flag cleared
	if svc.State() != service.StateStarted {
		t.Fatalf("Expected STARTED after untrigger, got %s", svc.State())
	}
	if svc.IsTriggered() {
		t.Fatal("Expected IsTriggered() = false after untrigger")
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
	rply, _ = readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("Expected ACK, got %d", rply)
	}

	// Using closed handle should fail
	if err := WritePacket(conn, CmdStartService, EncodeHandle(handle)); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, _ = readReply(t, conn)
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

// --- setenv / unsetenv / getallenv tests ---

func TestSetEnvAndGetAllEnv(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewInternalService(server.services, "env-svc")
	server.services.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	handle := findHandle(t, conn, "env-svc")

	// Set two env vars
	payload := EncodeSetEnv(handle, "FOO", "bar", false)
	if err := WritePacket(conn, CmdSetEnv, payload); err != nil {
		t.Fatal(err)
	}
	rply, _ := readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("setenv FOO: expected ACK, got %d", rply)
	}

	payload = EncodeSetEnv(handle, "BAZ", "qux", false)
	WritePacket(conn, CmdSetEnv, payload)
	rply, _ = readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("setenv BAZ: expected ACK, got %d", rply)
	}

	// GetAllEnv
	WritePacket(conn, CmdGetAllEnv, EncodeHandle(handle))
	var data []byte
	rply, data = readReply(t, conn)
	if rply != RplyEnvList {
		t.Fatalf("getallenv: expected EnvList, got %d", rply)
	}
	env, err := DecodeEnvList(data)
	if err != nil {
		t.Fatal(err)
	}
	if env["FOO"] != "bar" || env["BAZ"] != "qux" {
		t.Fatalf("unexpected env: %v", env)
	}
	if len(env) != 2 {
		t.Fatalf("expected 2 env vars, got %d", len(env))
	}
}

func TestUnsetEnv(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewInternalService(server.services, "unset-svc")
	server.services.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	handle := findHandle(t, conn, "unset-svc")

	// Set then unset
	WritePacket(conn, CmdSetEnv, EncodeSetEnv(handle, "KEY", "val", false))
	readReply(t, conn)

	WritePacket(conn, CmdSetEnv, EncodeSetEnv(handle, "KEY", "", true))
	rply, _ := readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("unset: expected ACK, got %d", rply)
	}

	// Verify empty
	WritePacket(conn, CmdGetAllEnv, EncodeHandle(handle))
	var data []byte
	rply, data = readReply(t, conn)
	if rply != RplyEnvList {
		t.Fatalf("getallenv: expected EnvList, got %d", rply)
	}
	env, _ := DecodeEnvList(data)
	if len(env) != 0 {
		t.Fatalf("expected 0 env vars after unset, got %d", len(env))
	}
}

// --- global setenv/unsetenv tests ---

func TestGlobalSetEnvAndGetAllEnv(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Set global env with handle=0
	payload := EncodeSetEnv(0, "GLOBAL_FOO", "gbar", false)
	if err := WritePacket(conn, CmdSetEnv, payload); err != nil {
		t.Fatal(err)
	}
	rply, _ := readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("global setenv: expected ACK, got %d", rply)
	}

	payload = EncodeSetEnv(0, "GLOBAL_BAZ", "gqux", false)
	WritePacket(conn, CmdSetEnv, payload)
	rply, _ = readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("global setenv BAZ: expected ACK, got %d", rply)
	}

	// GetAllEnv with handle=0 returns global env
	WritePacket(conn, CmdGetAllEnv, EncodeHandle(0))
	var data []byte
	rply, data = readReply(t, conn)
	if rply != RplyEnvList {
		t.Fatalf("global getallenv: expected EnvList, got %d", rply)
	}
	env, err := DecodeEnvList(data)
	if err != nil {
		t.Fatal(err)
	}
	if env["GLOBAL_FOO"] != "gbar" || env["GLOBAL_BAZ"] != "gqux" {
		t.Fatalf("unexpected global env: %v", env)
	}
}

func TestGlobalUnsetEnv(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Set then unset global
	WritePacket(conn, CmdSetEnv, EncodeSetEnv(0, "GKEY", "val", false))
	readReply(t, conn)

	WritePacket(conn, CmdSetEnv, EncodeSetEnv(0, "GKEY", "", true))
	rply, _ := readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("global unset: expected ACK, got %d", rply)
	}

	// Verify empty
	WritePacket(conn, CmdGetAllEnv, EncodeHandle(0))
	var data []byte
	rply, data = readReply(t, conn)
	if rply != RplyEnvList {
		t.Fatalf("global getallenv: expected EnvList, got %d", rply)
	}
	env, _ := DecodeEnvList(data)
	if _, found := env["GKEY"]; found {
		t.Fatalf("expected GKEY to be unset, but got: %v", env)
	}
}

// --- protocol v5 tests ---

func TestListServices5(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc1 := service.NewInternalService(server.services, "v5-alpha")
	svc2 := service.NewInternalService(server.services, "v5-beta")
	server.services.AddService(svc1)
	server.services.AddService(svc2)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	if err := WritePacket(conn, CmdListServices5, nil); err != nil {
		t.Fatal(err)
	}

	var names []string
	for {
		rply, payload, err := ReadPacket(conn)
		if err != nil {
			t.Fatal(err)
		}
		if rply == RplyListDone {
			break
		}
		if rply != RplySvcInfo {
			t.Fatalf("expected SvcInfo, got %d", rply)
		}
		entry, _, err := DecodeSvcInfo5(payload)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, entry.Name)
	}

	if len(names) < 2 {
		t.Fatalf("expected at least 2 services, got %d", len(names))
	}

	found := 0
	for _, n := range names {
		if n == "v5-alpha" || n == "v5-beta" {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("expected both v5-alpha and v5-beta, got names: %v", names)
	}
}

func TestServiceStatus5(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewInternalService(server.services, "v5-svc")
	server.services.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	handle := findHandle(t, conn, "v5-svc")

	if err := WritePacket(conn, CmdServiceStatus5, EncodeHandle(handle)); err != nil {
		t.Fatal(err)
	}

	rply, payload := readReply(t, conn)
	if rply != RplyServiceStatus {
		t.Fatalf("expected ServiceStatus, got %d", rply)
	}

	status, err := DecodeServiceStatus5(payload)
	if err != nil {
		t.Fatal(err)
	}

	if status.State != service.ServiceState(service.StateStopped) {
		t.Fatalf("expected stopped state, got %d", status.State)
	}
	if status.StopReason != uint8(service.ReasonNormal) {
		t.Fatalf("expected normal stop reason, got %d", status.StopReason)
	}
}

func TestServiceEvent5Notification(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewInternalService(server.services, "event5-svc")
	server.services.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	handle := findHandle(t, conn, "event5-svc")

	// Start service — triggers events + ACK
	WritePacket(conn, CmdStartService, EncodeHandle(handle))

	// Read all packets — expect v5 event, v4 event, and ACK in some order
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	var gotV5, gotV4, gotACK bool
	var v5Payload []byte
	for i := 0; i < 3; i++ {
		rply, payload, err := ReadPacket(conn)
		if err != nil {
			t.Fatalf("Read error on packet %d: %v", i, err)
		}
		switch rply {
		case InfoServiceEvent5:
			gotV5 = true
			v5Payload = payload
		case InfoServiceEvent:
			gotV4 = true
		case RplyACK:
			gotACK = true
		default:
			t.Fatalf("unexpected packet type %d", rply)
		}
	}

	if !gotV5 {
		t.Fatal("did not receive InfoServiceEvent5")
	}
	if !gotV4 {
		t.Fatal("did not receive InfoServiceEvent")
	}
	if !gotACK {
		t.Fatal("did not receive ACK")
	}

	// Validate v5 payload
	if len(v5Payload) < 19 {
		t.Fatalf("v5 event payload too short: %d", len(v5Payload))
	}
	h5, evt5, _, err := DecodeServiceEvent5(v5Payload)
	if err != nil {
		t.Fatal(err)
	}
	if h5 != handle {
		t.Fatalf("wrong handle in v5 event: %d", h5)
	}
	if evt5 != SvcEventStarted {
		t.Fatalf("expected STARTED event, got %d", evt5)
	}
}

// --- add-dep / rm-dep tests ---

func TestAddDepAndRmDep(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	parent := service.NewInternalService(server.services, "dep-parent")
	child := service.NewInternalService(server.services, "dep-child")
	server.services.AddService(parent)
	server.services.AddService(child)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	hParent := findHandle(t, conn, "dep-parent")
	hChild := findHandle(t, conn, "dep-child")

	// Add waits-for dep
	payload := EncodeDepRequest(hParent, hChild, uint8(service.DepWaitsFor))
	WritePacket(conn, CmdAddDep, payload)
	rply, _ := readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("add-dep: expected ACK, got %d", rply)
	}

	// Verify dep exists
	deps := parent.Record().Dependencies()
	found := false
	for _, d := range deps {
		if d.To == child && d.DepType == service.DepWaitsFor {
			found = true
		}
	}
	if !found {
		t.Fatal("waits-for dependency not found after add-dep")
	}

	// Remove dep
	WritePacket(conn, CmdRmDep, payload)
	rply, _ = readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("rm-dep: expected ACK, got %d", rply)
	}

	// Verify dep removed
	deps = parent.Record().Dependencies()
	for _, d := range deps {
		if d.To == child && d.DepType == service.DepWaitsFor {
			t.Fatal("waits-for dependency should be removed")
		}
	}
}

func TestRmDepNotFound(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc1 := service.NewInternalService(server.services, "rm-a")
	svc2 := service.NewInternalService(server.services, "rm-b")
	server.services.AddService(svc1)
	server.services.AddService(svc2)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	h1 := findHandle(t, conn, "rm-a")
	h2 := findHandle(t, conn, "rm-b")

	// Try to remove non-existent dep
	payload := EncodeDepRequest(h1, h2, uint8(service.DepRegular))
	WritePacket(conn, CmdRmDep, payload)
	rply, _ := readReply(t, conn)
	if rply != RplyNAK {
		t.Fatalf("rm-dep non-existent: expected NAK, got %d", rply)
	}
}

// --- enable / disable tests ---

func TestEnableService(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	boot := service.NewInternalService(server.services, "boot")
	server.services.AddService(boot)
	server.services.SetBootServiceName("boot")
	server.services.StartService(boot)

	target := service.NewInternalService(server.services, "target-svc")
	server.services.AddService(target)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	handle := findHandle(t, conn, "target-svc")

	// Enable
	WritePacket(conn, CmdEnableService, EncodeHandle(handle))
	rply, _ := readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("enable: expected ACK, got %d", rply)
	}

	// Verify: boot should have waits-for dep to target
	deps := boot.Record().Dependencies()
	found := false
	for _, d := range deps {
		if d.To == target && d.DepType == service.DepWaitsFor {
			found = true
		}
	}
	if !found {
		t.Fatal("boot should have waits-for dep to target after enable")
	}

	// Target should be started
	if target.State() != service.StateStarted {
		t.Fatalf("target should be STARTED after enable, got %v", target.State())
	}
}

func TestDisableService(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	boot := service.NewInternalService(server.services, "boot")
	server.services.AddService(boot)
	server.services.SetBootServiceName("boot")
	server.services.StartService(boot)

	target := service.NewInternalService(server.services, "target-svc")
	server.services.AddService(target)
	server.services.StartService(target)

	// Add waits-for first (simulating enabled state)
	boot.Record().AddDep(target, service.DepWaitsFor)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	handle := findHandle(t, conn, "target-svc")

	// Disable
	WritePacket(conn, CmdDisableService, EncodeHandle(handle))
	rply, _ := readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("disable: expected ACK, got %d", rply)
	}

	// Verify: boot should NOT have waits-for dep to target
	deps := boot.Record().Dependencies()
	for _, d := range deps {
		if d.To == target && d.DepType == service.DepWaitsFor {
			t.Fatal("boot should not have waits-for dep to target after disable")
		}
	}
}

func TestEnableNoBootService(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewInternalService(server.services, "no-boot-svc")
	server.services.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	handle := findHandle(t, conn, "no-boot-svc")

	// Enable without boot service → NAK
	WritePacket(conn, CmdEnableService, EncodeHandle(handle))
	rply, _ := readReply(t, conn)
	if rply != RplyNAK {
		t.Fatalf("enable without boot: expected NAK, got %d", rply)
	}
}

// findHandle is a test helper that resolves a service name to a handle.
func findHandle(t *testing.T, conn net.Conn, name string) uint32 {
	t.Helper()
	if err := WritePacket(conn, CmdFindService, EncodeServiceName(name)); err != nil {
		t.Fatalf("find %s: write error: %v", name, err)
	}
	rply, payload, err := ReadPacket(conn)
	if err != nil {
		t.Fatalf("find %s: read error: %v", name, err)
	}
	if rply != RplyServiceRecord {
		t.Fatalf("find %s: expected ServiceRecord, got %d", name, rply)
	}
	h, err := DecodeHandle(payload[1:]) // skip state byte
	if err != nil {
		t.Fatalf("find %s: decode handle: %v", name, err)
	}
	return h
}

// --- PREACK tests ---

func TestPreACKOnStop(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewInternalService(server.services, "preack-svc")
	server.services.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Load and start service
	nameData := EncodeServiceName("preack-svc")
	WritePacket(conn, CmdLoadService, nameData)
	_, payload, _ := ReadPacket(conn)
	handle := binary.LittleEndian.Uint32(payload[1:5])

	WritePacket(conn, CmdStartService, EncodeHandle(handle))
	readReply(t, conn)

	// Stop with PREACK flag (bit 7 = 0x80) + restart (bit 2 = 0x04)
	stopPayload := make([]byte, 5)
	binary.LittleEndian.PutUint32(stopPayload, handle)
	stopPayload[4] = 0x80 | 0x04 // preack + restart
	WritePacket(conn, CmdStopService, stopPayload)

	// First reply should be PREACK
	rply, _, err := ReadPacket(conn)
	if err != nil {
		t.Fatal(err)
	}
	if rply != RplyPreACK {
		t.Fatalf("expected PREACK (%d), got %d", RplyPreACK, rply)
	}

	// Then the main ACK (skip any info packets)
	rply2, _ := readReply(t, conn)
	if rply2 != RplyACK {
		t.Fatalf("expected ACK after PREACK, got %d", rply2)
	}
}

func TestNoPreACKWithoutFlag(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewInternalService(server.services, "no-preack-svc")
	server.services.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	nameData := EncodeServiceName("no-preack-svc")
	WritePacket(conn, CmdLoadService, nameData)
	_, payload, _ := ReadPacket(conn)
	handle := binary.LittleEndian.Uint32(payload[1:5])

	WritePacket(conn, CmdStartService, EncodeHandle(handle))
	readReply(t, conn)

	// Stop without PREACK flag
	WritePacket(conn, CmdStopService, EncodeHandle(handle))

	// Should get ACK directly (no PREACK)
	rply, _ := readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("expected ACK, got %d", rply)
	}
}

func TestPinnedStoppedReply(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewInternalService(server.services, "pinned-svc")
	server.services.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	nameData := EncodeServiceName("pinned-svc")
	WritePacket(conn, CmdLoadService, nameData)
	_, payload, _ := ReadPacket(conn)
	handle := binary.LittleEndian.Uint32(payload[1:5])

	// Stop with pin
	stopPayload := make([]byte, 5)
	binary.LittleEndian.PutUint32(stopPayload, handle)
	stopPayload[4] = 0x01 // pin
	WritePacket(conn, CmdStopService, stopPayload)
	readReply(t, conn) // ACK (already stopped)

	// Pin stop the service
	svc.PinStop()

	// Try to start — should get PinnedStopped
	WritePacket(conn, CmdStartService, EncodeHandle(handle))
	rply, _ := readReply(t, conn)
	if rply != RplyPinnedStopped {
		t.Fatalf("expected PinnedStopped (%d), got %d", RplyPinnedStopped, rply)
	}
}

func TestPinnedStartedReply(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewInternalService(server.services, "pinstart-svc")
	server.services.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	nameData := EncodeServiceName("pinstart-svc")
	WritePacket(conn, CmdLoadService, nameData)
	_, payload, _ := ReadPacket(conn)
	handle := binary.LittleEndian.Uint32(payload[1:5])

	// Start with pin
	startPayload := make([]byte, 5)
	binary.LittleEndian.PutUint32(startPayload, handle)
	startPayload[4] = 0x01 // pin
	WritePacket(conn, CmdStartService, startPayload)
	readReply(t, conn) // ACK

	// Try to stop (non-force) — should get PinnedStarted
	WritePacket(conn, CmdStopService, EncodeHandle(handle))
	rply, _ := readReply(t, conn)
	if rply != RplyPinnedStarted {
		t.Fatalf("expected PinnedStarted (%d), got %d", RplyPinnedStarted, rply)
	}
}

func TestQueryDependencies(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	parent := service.NewInternalService(server.services, "graph-parent")
	child1 := service.NewInternalService(server.services, "graph-child1")
	child2 := service.NewInternalService(server.services, "graph-child2")
	server.services.AddService(parent)
	server.services.AddService(child1)
	server.services.AddService(child2)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	hParent := findHandle(t, conn, "graph-parent")
	hChild1 := findHandle(t, conn, "graph-child1")
	hChild2 := findHandle(t, conn, "graph-child2")

	// Add two deps: parent → child1 (regular), parent → child2 (soft)
	WritePacket(conn, CmdAddDep, EncodeDepRequest(hParent, hChild1, uint8(service.DepRegular)))
	rply, _ := readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("add-dep regular: expected ACK, got %d", rply)
	}
	WritePacket(conn, CmdAddDep, EncodeDepRequest(hParent, hChild2, uint8(service.DepSoft)))
	rply, _ = readReply(t, conn)
	if rply != RplyACK {
		t.Fatalf("add-dep soft: expected ACK, got %d", rply)
	}

	// Query forward dependencies
	WritePacket(conn, CmdQueryDependencies, EncodeHandle(hParent))
	rply, payload := readReply(t, conn)
	if rply != RplyDependencies {
		t.Fatalf("expected RplyDependencies (%d), got %d", RplyDependencies, rply)
	}

	if len(payload) < 4 {
		t.Fatalf("payload too short: %d", len(payload))
	}
	count := int(binary.LittleEndian.Uint32(payload))
	if count != 2 {
		t.Fatalf("expected 2 dependencies, got %d", count)
	}

	// Parse deps: each is handle(4) + depType(1)
	off := 4
	type depInfo struct {
		handle  uint32
		depType service.DependencyType
	}
	var deps []depInfo
	for i := 0; i < count; i++ {
		if len(payload) < off+5 {
			t.Fatalf("truncated at dep %d", i)
		}
		h := binary.LittleEndian.Uint32(payload[off:])
		dt := service.DependencyType(payload[off+4])
		deps = append(deps, depInfo{handle: h, depType: dt})
		off += 5
	}

	// Resolve handle names
	foundRegular := false
	foundSoft := false
	for _, d := range deps {
		WritePacket(conn, CmdQueryServiceName, EncodeHandle(d.handle))
		rply, namePayload := readReply(t, conn)
		if rply != RplyServiceName {
			continue
		}
		name, _, _ := DecodeServiceName(namePayload)
		if name == "graph-child1" && d.depType == service.DepRegular {
			foundRegular = true
		}
		if name == "graph-child2" && d.depType == service.DepSoft {
			foundSoft = true
		}
	}

	if !foundRegular {
		t.Error("regular dependency on graph-child1 not found")
	}
	if !foundSoft {
		t.Error("soft dependency on graph-child2 not found")
	}
}

func TestQueryDependenciesEmpty(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewInternalService(server.services, "no-deps")
	server.services.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	handle := findHandle(t, conn, "no-deps")
	WritePacket(conn, CmdQueryDependencies, EncodeHandle(handle))
	rply, payload := readReply(t, conn)
	if rply != RplyDependencies {
		t.Fatalf("expected RplyDependencies, got %d", rply)
	}
	if len(payload) < 4 {
		t.Fatalf("payload too short")
	}
	count := binary.LittleEndian.Uint32(payload)
	if count != 0 {
		t.Errorf("expected 0 dependencies, got %d", count)
	}
}

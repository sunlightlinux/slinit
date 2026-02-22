package control

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/config"
	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestReloadStoppedService(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	// Set up a DirLoader with a temp service dir
	svcDir := t.TempDir()
	loader := config.NewDirLoader(server.services, []string{svcDir})
	server.services.SetLoader(loader)

	// Write initial service file
	if err := os.WriteFile(filepath.Join(svcDir, "reload-svc"), []byte("type = internal\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Load the service
	svc, err := loader.LoadService("reload-svc")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	_ = svc

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Get handle
	nameData := EncodeServiceName("reload-svc")
	if err := WritePacket(conn, CmdLoadService, nameData); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	rply, payload, err := ReadPacket(conn)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if rply != RplyServiceRecord {
		t.Fatalf("expected RplyServiceRecord, got %d", rply)
	}
	handle := binary.LittleEndian.Uint32(payload[1:5])

	// Modify the file
	if err := os.WriteFile(filepath.Join(svcDir, "reload-svc"), []byte("type = internal\nrestart = true\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Send reload command
	if err := WritePacket(conn, CmdReloadService, EncodeHandle(handle)); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	rply, _, err = ReadPacket(conn)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if rply != RplyACK {
		t.Fatalf("expected ACK, got %d", rply)
	}
}

func TestReloadStartedService(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svcDir := t.TempDir()
	loader := config.NewDirLoader(server.services, []string{svcDir})
	server.services.SetLoader(loader)

	if err := os.WriteFile(filepath.Join(svcDir, "started-svc"), []byte("type = internal\n"), 0644); err != nil {
		t.Fatal(err)
	}

	svc, err := loader.LoadService("started-svc")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	// Start the service
	server.services.StartService(svc)
	if svc.State() != service.StateStarted {
		t.Fatalf("expected STARTED, got %d", svc.State())
	}

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Get handle
	nameData := EncodeServiceName("started-svc")
	if err := WritePacket(conn, CmdLoadService, nameData); err != nil {
		t.Fatal(err)
	}
	rply, payload, err := ReadPacket(conn)
	if err != nil {
		t.Fatal(err)
	}
	if rply != RplyServiceRecord {
		t.Fatalf("expected RplyServiceRecord, got %d", rply)
	}
	handle := binary.LittleEndian.Uint32(payload[1:5])

	// Modify file (same type, allowed change)
	if err := os.WriteFile(filepath.Join(svcDir, "started-svc"), []byte("type = internal\nrestart = true\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Reload
	if err := WritePacket(conn, CmdReloadService, EncodeHandle(handle)); err != nil {
		t.Fatal(err)
	}
	rply, _, err = ReadPacket(conn)
	if err != nil {
		t.Fatal(err)
	}
	if rply != RplyACK {
		t.Fatalf("expected ACK for started reload, got %d", rply)
	}
}

func TestReloadWrongState(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	// Use an InternalService with unresolved dependency to keep it in STARTING
	svcDep := service.NewInternalService(server.services, "dep-svc")
	server.services.AddService(svcDep)

	svc := service.NewInternalService(server.services, "starting-svc")
	server.services.AddService(svc)
	svc.Record().AddDep(svcDep, service.DepRegular)

	// Start without processing queues - service stays in STARTING
	// because dependency is not started yet
	svc.Start()

	if svc.State() != service.StateStarting {
		t.Fatalf("expected STARTING, got %d", svc.State())
	}

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Get handle
	nameData := EncodeServiceName("starting-svc")
	if err := WritePacket(conn, CmdFindService, nameData); err != nil {
		t.Fatal(err)
	}
	rply, payload, err := ReadPacket(conn)
	if err != nil {
		t.Fatal(err)
	}
	if rply != RplyServiceRecord {
		t.Fatalf("expected RplyServiceRecord, got %d", rply)
	}
	handle := binary.LittleEndian.Uint32(payload[1:5])

	// Reload should fail (wrong state)
	if err := WritePacket(conn, CmdReloadService, EncodeHandle(handle)); err != nil {
		t.Fatal(err)
	}
	rply, _, err = ReadPacket(conn)
	if err != nil {
		t.Fatal(err)
	}
	if rply != RplyNAK {
		t.Fatalf("expected NAK for STARTING state, got %d", rply)
	}
}

func TestReloadInvalidHandle(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Send reload with an invalid handle
	if err := WritePacket(conn, CmdReloadService, EncodeHandle(999)); err != nil {
		t.Fatal(err)
	}
	rply, _, err := ReadPacket(conn)
	if err != nil {
		t.Fatal(err)
	}
	if rply != RplyBadReq {
		t.Fatalf("expected BadReq for invalid handle, got %d", rply)
	}
}

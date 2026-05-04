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

	rply, _ = readReply(t, conn)
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
	rply, _ = readReply(t, conn)
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
	rply, _ = readReply(t, conn)
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
	rply, _ := readReply(t, conn)
	if rply != RplyBadReq {
		t.Fatalf("expected BadReq for invalid handle, got %d", rply)
	}
}

func TestReloadAllNoLoader(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	// No loader set: reload-all must NAK.

	conn := connectTest(t, sockPath)
	defer conn.Close()

	if err := WritePacket(conn, CmdReloadAll, nil); err != nil {
		t.Fatal(err)
	}
	rply, _ := readReply(t, conn)
	if rply != RplyNAK {
		t.Fatalf("expected NAK without loader, got %d", rply)
	}
}

func TestReloadAllEmpty(t *testing.T) {
	// Loader set but no services loaded — must succeed with 0/0.
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svcDir := t.TempDir()
	loader := config.NewDirLoader(server.services, []string{svcDir})
	server.services.SetLoader(loader)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	if err := WritePacket(conn, CmdReloadAll, nil); err != nil {
		t.Fatal(err)
	}
	rply, payload := readReply(t, conn)
	if rply != RplyReloadAllResult {
		t.Fatalf("expected RplyReloadAllResult, got %d", rply)
	}
	if len(payload) < 4 {
		t.Fatalf("short payload: %d bytes", len(payload))
	}
	ok := binary.LittleEndian.Uint16(payload[0:2])
	failed := binary.LittleEndian.Uint16(payload[2:4])
	if ok != 0 || failed != 0 {
		t.Errorf("expected 0/0, got ok=%d failed=%d", ok, failed)
	}
}

func TestReloadAllMultiple(t *testing.T) {
	// Three loaded services, one stopped + one started + one in transitional
	// state (we can't easily force STARTING in a unit test, so we cover
	// stopped+started which is the common case). All succeed.
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svcDir := t.TempDir()
	loader := config.NewDirLoader(server.services, []string{svcDir})
	server.services.SetLoader(loader)

	for _, name := range []string{"alpha", "beta", "gamma"} {
		if err := os.WriteFile(filepath.Join(svcDir, name), []byte("type = internal\n"), 0644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		if _, err := loader.LoadService(name); err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
	}
	server.services.StartService(server.services.FindService("beta", false))
	if server.services.FindService("beta", false).State() != service.StateStarted {
		t.Fatal("beta should be STARTED")
	}

	// Modify all three on disk so reload has something to pick up.
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if err := os.WriteFile(filepath.Join(svcDir, name), []byte("type = internal\nrestart = true\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	conn := connectTest(t, sockPath)
	defer conn.Close()

	if err := WritePacket(conn, CmdReloadAll, nil); err != nil {
		t.Fatal(err)
	}
	rply, payload := readReply(t, conn)
	if rply != RplyReloadAllResult {
		t.Fatalf("expected RplyReloadAllResult, got %d", rply)
	}
	ok := binary.LittleEndian.Uint16(payload[0:2])
	failed := binary.LittleEndian.Uint16(payload[2:4])
	if ok != 3 || failed != 0 {
		t.Errorf("expected 3/0, got ok=%d failed=%d", ok, failed)
	}
}

func TestReloadAllPartialFailure(t *testing.T) {
	// Two services loaded; delete one's file from disk before reload-all.
	// The deleted one fails (loader cannot read it), the surviving one
	// succeeds.
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svcDir := t.TempDir()
	loader := config.NewDirLoader(server.services, []string{svcDir})
	server.services.SetLoader(loader)

	for _, name := range []string{"keep", "doomed"} {
		if err := os.WriteFile(filepath.Join(svcDir, name), []byte("type = internal\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := loader.LoadService(name); err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
	}

	if err := os.Remove(filepath.Join(svcDir, "doomed")); err != nil {
		t.Fatal(err)
	}

	conn := connectTest(t, sockPath)
	defer conn.Close()

	if err := WritePacket(conn, CmdReloadAll, nil); err != nil {
		t.Fatal(err)
	}
	rply, payload := readReply(t, conn)
	if rply != RplyReloadAllResult {
		t.Fatalf("expected RplyReloadAllResult, got %d", rply)
	}
	ok := binary.LittleEndian.Uint16(payload[0:2])
	failed := binary.LittleEndian.Uint16(payload[2:4])
	if ok != 1 || failed != 1 {
		t.Errorf("expected 1 ok / 1 failed, got ok=%d failed=%d", ok, failed)
	}
}

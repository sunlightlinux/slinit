package control

import (
	"encoding/binary"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/config"
	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestQueryServiceName(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewInternalService(server.services, "my-service")
	server.services.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Load service to get handle
	if err := WritePacket(conn, CmdLoadService, EncodeServiceName("my-service")); err != nil {
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

	// Query service name
	if err := WritePacket(conn, CmdQueryServiceName, EncodeHandle(handle)); err != nil {
		t.Fatal(err)
	}
	rply, payload, err = ReadPacket(conn)
	if err != nil {
		t.Fatal(err)
	}
	if rply != RplyServiceName {
		t.Fatalf("expected RplyServiceName, got %d", rply)
	}
	name, _, err := DecodeServiceName(payload)
	if err != nil {
		t.Fatal(err)
	}
	if name != "my-service" {
		t.Fatalf("expected 'my-service', got %q", name)
	}
}

func TestQueryServiceNameBadHandle(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Query with invalid handle
	if err := WritePacket(conn, CmdQueryServiceName, EncodeHandle(999)); err != nil {
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

func TestQueryServiceDscDir(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	// Set up loader with known dirs
	dirs := []string{"/etc/slinit.d", "/run/slinit.d", "/lib/slinit.d"}
	loader := config.NewDirLoader(server.services, dirs)
	server.services.SetLoader(loader)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	if err := WritePacket(conn, CmdQueryServiceDscDir, nil); err != nil {
		t.Fatal(err)
	}
	rply, payload, err := ReadPacket(conn)
	if err != nil {
		t.Fatal(err)
	}
	if rply != RplyServiceDscDir {
		t.Fatalf("expected RplyServiceDscDir, got %d", rply)
	}

	// Decode
	if len(payload) < 2 {
		t.Fatal("payload too short")
	}
	count := int(binary.LittleEndian.Uint16(payload))
	if count != len(dirs) {
		t.Fatalf("expected %d dirs, got %d", len(dirs), count)
	}
	off := 2
	for i, want := range dirs {
		dirLen := int(binary.LittleEndian.Uint16(payload[off:]))
		off += 2
		got := string(payload[off : off+dirLen])
		off += dirLen
		if got != want {
			t.Errorf("dir[%d]: got %q, want %q", i, got, want)
		}
	}
}

func TestQueryServiceDscDirNoLoader(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	// No loader set — should return empty list
	conn := connectTest(t, sockPath)
	defer conn.Close()

	if err := WritePacket(conn, CmdQueryServiceDscDir, nil); err != nil {
		t.Fatal(err)
	}
	rply, payload, err := ReadPacket(conn)
	if err != nil {
		t.Fatal(err)
	}
	if rply != RplyServiceDscDir {
		t.Fatalf("expected RplyServiceDscDir, got %d", rply)
	}
	if len(payload) < 2 {
		t.Fatal("payload too short")
	}
	count := int(binary.LittleEndian.Uint16(payload))
	if count != 0 {
		t.Fatalf("expected 0 dirs with no loader, got %d", count)
	}
}

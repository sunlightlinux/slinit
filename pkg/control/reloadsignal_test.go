package control

import (
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// TestReloadSignalNoConfig: a service without reload-signal stanza
// must NAK the request rather than silently sending nothing.
func TestReloadSignalNoConfig(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewInternalService(server.services, "no-reload-svc")
	server.services.AddService(svc)
	// No SetReloadSignal call → reloadSignal == 0

	conn := connectTest(t, sockPath)
	defer conn.Close()

	handle := loadHandle(t, conn, "no-reload-svc")

	if err := WritePacket(conn, CmdReloadSignal, EncodeHandle(handle)); err != nil {
		t.Fatal(err)
	}
	rply, _ := readReply(t, conn)
	if rply != RplyNAK {
		t.Errorf("rply=%d, want RplyNAK (no reload-signal configured)", rply)
	}
}

// TestReloadSignalNoPID: service with reload-signal configured but
// not running (no PID) returns RplySignalNoPID.
func TestReloadSignalNoPID(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewInternalService(server.services, "stopped-svc")
	svc.Record().SetReloadSignal(syscall.SIGHUP)
	server.services.AddService(svc)
	// Internal service: PID() always returns -1.

	conn := connectTest(t, sockPath)
	defer conn.Close()

	handle := loadHandle(t, conn, "stopped-svc")

	if err := WritePacket(conn, CmdReloadSignal, EncodeHandle(handle)); err != nil {
		t.Fatal(err)
	}
	rply, _ := readReply(t, conn)
	if rply != RplySignalNoPID {
		t.Errorf("rply=%d, want RplySignalNoPID", rply)
	}
}

// TestReloadSignalBadHandle: invalid handle returns BadReq.
func TestReloadSignalBadHandle(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	if err := WritePacket(conn, CmdReloadSignal, EncodeHandle(999)); err != nil {
		t.Fatal(err)
	}
	rply, _ := readReply(t, conn)
	if rply != RplyBadReq {
		t.Errorf("rply=%d, want RplyBadReq", rply)
	}
}

// loadHandle is a small helper used by the reload-signal tests to
// resolve a name to a handle on a test connection.
func loadHandle(t *testing.T, conn interface {
	Write(p []byte) (int, error)
	Read(p []byte) (int, error)
}, name string) uint32 {
	t.Helper()
	if err := WritePacket(conn, CmdLoadService, EncodeServiceName(name)); err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	rply, payload, err := ReadPacket(conn)
	if err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}
	if rply != RplyServiceRecord {
		t.Fatalf("expected RplyServiceRecord, got %d", rply)
	}
	if len(payload) < 5 {
		t.Fatalf("ServiceRecord payload too short: %d", len(payload))
	}
	return uint32(payload[1]) |
		uint32(payload[2])<<8 |
		uint32(payload[3])<<16 |
		uint32(payload[4])<<24
}

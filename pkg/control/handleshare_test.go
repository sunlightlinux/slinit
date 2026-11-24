package control

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/config"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// Unload must be refused while another client holds a handle to the
// service, as dinit's has_lone_ref refuses it. It used to succeed, and
// the other client's handle then drove the detached object: a start
// through it ran a service that was in no list and could not be found.
func TestUnloadRefusedWhileAnotherClientHoldsAHandle(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	x := service.NewInternalService(server.services, "x")
	server.services.AddService(x)

	a := connectTest(t, sockPath)
	defer a.Close()
	b := connectTest(t, sockPath)
	defer b.Close()
	ha := findHandle(t, a, "x")
	hb := findHandle(t, b, "x")

	if err := WritePacket(b, CmdUnloadService, EncodeHandle(hb)); err != nil {
		t.Fatal(err)
	}
	if r, _ := readReply(t, b); r != RplyNAK {
		t.Fatalf("unload while another client holds x: rply=%d, want NAK", r)
	}
	if server.services.FindService("x", false) == nil {
		t.Fatal("x left the set")
	}

	// Once the other client lets go, unload goes through.
	if err := WritePacket(a, CmdCloseHandle, EncodeHandle(ha)); err != nil {
		t.Fatal(err)
	}
	readReply(t, a)
	if err := WritePacket(b, CmdUnloadService, EncodeHandle(hb)); err != nil {
		t.Fatal(err)
	}
	if r, _ := readReply(t, b); r != RplyACK {
		t.Fatalf("unload after the other handle closed: rply=%d, want ACK", r)
	}
}

// waitEvent reads until an InfoServiceEvent for handle h arrives.
func waitEvent(t *testing.T, conn net.Conn, h uint32) bool {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer conn.SetReadDeadline(time.Time{})
	for {
		typ, ev, err := ReadPacket(conn)
		if err != nil {
			return false
		}
		if typ == InfoServiceEvent {
			if eh, err := DecodeHandle(ev); err == nil && eh == h {
				return true
			}
		}
	}
}

// A reload that changes a service's type replaces the object in the
// set. Every client's handle — the reloading one's and any other's —
// must follow it, or that handle acts on the detached object and never
// sees another event.
func TestReloadTypeChangeMovesEveryClientsHandle(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	svcDir := t.TempDir()
	server.services.SetLoader(config.NewDirLoader(server.services, []string{svcDir}))
	p := filepath.Join(svcDir, "x")
	if err := os.WriteFile(p, []byte("type = internal\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	watcher := connectTest(t, sockPath)
	defer watcher.Close()
	reloader := connectTest(t, sockPath)
	defer reloader.Close()

	if err := WritePacket(reloader, CmdLoadService, EncodeServiceName("x")); err != nil {
		t.Fatal(err)
	}
	_, pl := readReply(t, reloader)
	hr, _ := DecodeHandle(pl[1:])
	hw := findHandle(t, watcher, "x")

	if err := os.WriteFile(p, []byte("type = triggered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WritePacket(reloader, CmdReloadService, EncodeHandle(hr)); err != nil {
		t.Fatal(err)
	}
	if r, _ := readReply(t, reloader); r != RplyACK {
		t.Fatalf("reload: rply=%d, want ACK", r)
	}
	newObj := server.services.FindService("x", false)
	if _, ok := newObj.(*service.TriggeredService); !ok {
		t.Fatalf("set holds %T after reload, want *service.TriggeredService", newObj)
	}

	// Start and trigger through the watcher's handle, taken before the
	// reload: it must reach the object in the set, and both clients
	// must hear about it on their original handles.
	if err := WritePacket(watcher, CmdStartService, EncodeHandle(hw)); err != nil {
		t.Fatal(err)
	}
	readReply(t, watcher)
	if err := WritePacket(watcher, CmdSetTrigger, append(EncodeHandle(hw), 1)); err != nil {
		t.Fatal(err)
	}
	if !waitEvent(t, watcher, hw) {
		t.Error("the watcher's handle got no event from the reloaded service")
	}
	if !waitEvent(t, reloader, hr) {
		t.Error("the reloader's handle got no event from the reloaded service")
	}
	if st := newObj.State(); st != service.StateStarted {
		t.Errorf("reloaded service state = %v, want STARTED", st)
	}
}

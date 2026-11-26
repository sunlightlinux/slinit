package main

import (
	"encoding/binary"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/control"
)

// These tests drive the dinit adapter the way the rest of slinitctl does:
// slinit-framed requests in, slinit-framed replies out, with a real dinit
// daemon underneath. They need a dinit binary; set SLINIT_TEST_DINIT or put
// one on PATH.
func startTestDinit(t *testing.T) (net.Conn, string) {
	t.Helper()
	bin := os.Getenv("SLINIT_TEST_DINIT")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("dinit"); err != nil {
			t.Skip("no dinit binary: set SLINIT_TEST_DINIT (make in a dinit checkout builds src/dinit, no install needed) or put dinit on PATH")
		}
	}
	dir, err := os.MkdirTemp("", "dc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	if len(sock) > 100 {
		t.Skipf("temp dir too long for a unix socket: %s", sock)
	}
	svc := filepath.Join(dir, "svc")
	bootD := filepath.Join(svc, "boot.d")
	if err := os.MkdirAll(bootD, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"boot":    "type = internal\nwaits-for.d: boot.d\n",
		"runner":  "type = process\ncommand = /bin/sleep 600\n",
		"oneshot": "type = scripted\ncommand = /bin/true\n",
	} {
		if err := os.WriteFile(filepath.Join(svc, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(bin, "-u", "-d", svc, "-p", sock, "-l", filepath.Join(dir, "log"))
	if err := cmd.Start(); err != nil {
		t.Fatalf("start dinit: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	var nc net.Conn
	deadline := time.Now().Add(5 * time.Second)
	for {
		if nc, err = net.Dial("unix", sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dinit socket never came up: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { nc.Close() })
	_ = nc.SetDeadline(time.Now().Add(20 * time.Second))
	return nc, svc
}

// ask sends a slinit-framed request and reads the next slinit-framed reply,
// skipping information packets. It is what slinitctl's own request helpers
// do, boiled down.
func ask(t *testing.T, conn net.Conn, cmd uint8, payload []byte) (uint8, []byte) {
	t.Helper()
	if err := control.WritePacket(conn, cmd, payload); err != nil {
		t.Fatalf("write cmd %d: %v", cmd, err)
	}
	for {
		rply, p, err := control.ReadPacket(conn)
		if err != nil {
			t.Fatalf("read reply to cmd %d: %v", cmd, err)
		}
		switch rply {
		case control.InfoServiceEvent, control.InfoServiceEvent5, control.InfoEnvEvent:
			continue
		default:
			return rply, p
		}
	}
}

func loadHandle(t *testing.T, conn net.Conn, name string) uint32 {
	t.Helper()
	rply, p := ask(t, conn, control.CmdLoadService, control.EncodeServiceName(name))
	if rply != control.RplyServiceRecord {
		t.Fatalf("load %s: reply %d, want service record", name, rply)
	}
	return binary.LittleEndian.Uint32(p[1:5])
}

// connectAdapter wraps a dinit connection the way main() does: the handshake
// detects dinit and returns the adapter.
func connectAdapter(t *testing.T, nc net.Conn) net.Conn {
	t.Helper()
	peerIsDinit = false
	conn, err := versionHandshake(nc)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if !peerIsDinit {
		t.Fatal("handshake did not recognise the daemon as dinit")
	}
	if _, ok := conn.(*dinitConn); !ok {
		t.Fatalf("handshake returned %T, want *dinitConn", conn)
	}
	return conn
}

func TestAdapterDetectsDinitAndLists(t *testing.T) {
	nc, _ := startTestDinit(t)
	conn := connectAdapter(t, nc)
	if peerCPVersion < 7 {
		t.Errorf("peerCPVersion = %d, want >= 7", peerCPVersion)
	}

	// dinit lists only loaded services; load one so the list has more than
	// the boot service the daemon brings up on its own.
	loadHandle(t, conn, "runner")

	// A list is one request, then a run of SVCINFO replies ending in
	// LISTDONE: write once, read until done.
	if err := control.WritePacket(conn, control.CmdListServices5, nil); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for {
		rply, p, err := control.ReadPacket(conn)
		if err != nil {
			t.Fatal(err)
		}
		if rply == control.RplyListDone {
			break
		}
		if rply != control.RplySvcInfo {
			continue
		}
		e, _, err := control.DecodeSvcInfo5(p)
		if err != nil {
			t.Fatalf("decode list entry: %v", err)
		}
		names[e.Name] = true
	}
	if !names["boot"] || !names["runner"] {
		t.Errorf("list is missing expected services: %v", names)
	}
}

func TestAdapterStartStatusStop(t *testing.T) {
	nc, _ := startTestDinit(t)
	conn := connectAdapter(t, nc)
	h := loadHandle(t, conn, "runner")

	if rply, _ := ask(t, conn, control.CmdStartService, control.EncodeHandle(h)); rply != control.RplyACK {
		t.Fatalf("start: reply %d, want ACK", rply)
	}

	// Status should settle on STARTED with a pid.
	deadline := time.Now().Add(5 * time.Second)
	var st control.ServiceStatusInfo5
	for {
		rply, p := ask(t, conn, control.CmdServiceStatus5, control.EncodeHandle(h))
		if rply != control.RplyServiceStatus {
			t.Fatalf("status: reply %d", rply)
		}
		var err error
		if st, err = control.DecodeServiceStatus5(p); err != nil {
			t.Fatal(err)
		}
		if st.State == 2 /* STARTED */ {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runner never started: state %d", st.State)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st.Flags&control.StatusFlagHasPID == 0 {
		t.Errorf("started runner has no pid flag: %#x", st.Flags)
	}

	// v1 status carries the pid where v5 does not; the adapter fills it.
	rply, p := ask(t, conn, control.CmdServiceStatus, control.EncodeHandle(h))
	if rply != control.RplyServiceStatus {
		t.Fatalf("v1 status: reply %d", rply)
	}
	v1, err := control.DecodeServiceStatus(p)
	if err != nil {
		t.Fatal(err)
	}
	if v1.PID <= 0 {
		t.Errorf("v1 status pid = %d, want > 0", v1.PID)
	}

	if rply, _ := ask(t, conn, control.CmdStopService, control.EncodeHandle(h)); rply != control.RplyACK {
		t.Fatalf("stop: reply %d, want ACK", rply)
	}
}

func TestAdapterEnableDisableWithSymlink(t *testing.T) {
	nc, svcDir := startTestDinit(t)
	conn := connectAdapter(t, nc)

	link := filepath.Join(svcDir, "boot.d", "oneshot")
	if _, err := os.Lstat(link); err == nil {
		t.Fatal("the symlink exists before enable")
	}

	if err := cmdEnable(conn, "oneshot", "", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("enable left no symlink: %v", err)
	}
	if !strings.HasSuffix(target, "oneshot") {
		t.Errorf("symlink points at %q, want it to end in oneshot", target)
	}

	if err := cmdDisable(conn, "oneshot", "", false, true); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("disable left the symlink behind (err=%v)", err)
	}
}

func TestAdapterRejectsSlinitExtensions(t *testing.T) {
	nc, _ := startTestDinit(t)
	conn := connectAdapter(t, nc)
	// boot-time is a slinit-only command; the adapter must refuse it rather
	// than send dinit something it will misread.
	err := control.WritePacket(conn, control.CmdBootTime, nil)
	if err == nil {
		t.Fatal("writing a slinit-only command to a dinit adapter did not error")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("error %q does not explain the command is unsupported", err)
	}
}

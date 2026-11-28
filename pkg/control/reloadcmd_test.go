package control

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// slinitctl reload-signal runs a reload-command with $MAINPID set, waits
// for it, reports its failure, refuses a service that is not running,
// and sends a reload-signal set as well after the command succeeded.
func TestReloadCommand(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	ss := server.services
	dir := t.TempDir()
	out := filepath.Join(dir, "mainpid")
	hup := filepath.Join(dir, "hup")

	svc := service.NewProcessService(ss, "rc")
	svc.SetCommand([]string{"/bin/sh", "-c", "trap 'echo x >> " + hup + "' HUP; while :; do sleep 0.05; done"})
	svc.SetReloadCommand([]string{"/bin/sh", "-c", "echo $MAINPID > " + out + "; exit ${RC_EXIT:-0}"})
	ss.AddService(svc)
	conn := connectTest(t, sockPath)
	defer conn.Close()
	handle := loadHandle(t, conn, "rc")
	reload := func() (uint8, string) {
		t.Helper()
		if err := WritePacket(conn, CmdReloadSignal, EncodeHandle(handle)); err != nil {
			t.Fatal(err)
		}
		r, b := readReply(t, conn)
		return r, string(b)
	}

	if r, _ := reload(); r != RplySignalNoPID {
		t.Errorf("a stopped service: reply %d, want SignalNoPID", r)
	}
	ss.StartService(svc)
	deadline := time.Now().Add(3 * time.Second)
	for svc.State() != service.StateStarted && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // the trap is set
	defer ss.StopService(svc)

	if r, b := reload(); r != RplyACK {
		t.Fatalf("reply %d %q, want ACK", r, b)
	}
	b, _ := os.ReadFile(out)
	if got := strings.TrimSpace(string(b)); got != strconv.Itoa(svc.PID()) {
		t.Errorf("$MAINPID = %q, want %d", got, svc.PID())
	}

	// A signal set as well follows the command.
	ss.WithGraphLock(func() { svc.Record().SetReloadSignal(1) }) // SIGHUP
	if r, b := reload(); r != RplyACK {
		t.Fatalf("with a signal: reply %d %q", r, b)
	}
	time.Sleep(300 * time.Millisecond)
	if h, _ := os.ReadFile(hup); strings.Count(string(h), "\n") != 1 {
		t.Errorf("the reload-signal after the command reached the service %d times, want 1", strings.Count(string(h), "\n"))
	}

	// A failing command fails the reload, and sends no signal.
	ss.WithGraphLock(func() {
		svc.SetReloadCommand([]string{"/bin/sh", "-c", "exit 3"})
	})
	if r, b := reload(); r != RplySignalErr || !strings.Contains(b, "exited 3") {
		t.Errorf("a failing command: reply %d %q, want SignalErr naming exit 3", r, b)
	}
	time.Sleep(300 * time.Millisecond)
	if h, _ := os.ReadFile(hup); strings.Count(string(h), "\n") != 1 {
		t.Error("a signal followed a failed reload-command")
	}
}

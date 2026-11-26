package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// A shutdown remain stops every service and keeps PID 1 running. It
// parked PID 1 in InfiniteHold instead: no event loop, so orphans went
// unreaped, signals went to a channel nobody read, and a later shutdown
// was held by the gate for good. The machine could only be reset. As
// dinit does, PID 1 now runs an event loop again, and a poweroff after
// the remain is carried out.
func TestShutdownAfterRemain(t *testing.T) {
	dir := t.TempDir()
	writeService(t, dir, "boot", `
type = process
command = /bin/sleep 100
`)
	r := startNS(t, dir, `
mount -t tmpfs none /run || exit 1
exec "$B/slinit" -d "$D/sd" -p "$D/sock" --syslog-socket "" --no-watchdog --catch-all-log="$D/cal"`)
	sock := filepath.Join(dir, "sock")
	cal := filepath.Join(dir, "cal")

	send := func(st service.ShutdownType) {
		t.Helper()
		var conn net.Conn
		var err error
		deadline := time.Now().Add(15 * time.Second)
		for {
			if conn, err = net.Dial("unix", sock); err == nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("dial control socket: %v", err)
		}
		defer conn.Close()
		if err := control.WritePacket(conn, control.CmdShutdown, []byte{uint8(st)}); err != nil {
			t.Fatalf("send shutdown %s: %v", st, err)
		}
		if rply, _, err := control.ReadPacket(conn); err != nil || rply != control.RplyACK {
			t.Fatalf("shutdown %s: reply %d, %v", st, rply, err)
		}
	}
	waitLog := func(s string) bool {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if b, _ := os.ReadFile(cal); strings.Contains(string(b), s) {
				return true
			}
			time.Sleep(50 * time.Millisecond)
		}
		return false
	}

	if !waitLog("[ OK ] boot") {
		log, _ := os.ReadFile(cal)
		t.Fatalf("boot service never started\ncatch-all log:\n%s", log)
	}
	send(service.ShutdownRemain)
	if !waitLog("[STOPPD] boot") {
		log, _ := os.ReadFile(cal)
		t.Fatalf("remain did not stop the boot service\ncatch-all log:\n%s", log)
	}
	// Past the event loop's return, into whatever PID 1 does next.
	if r.wait(time.Second) {
		t.Fatalf("PID 1 ended after a remain: %v", r.cmd.ProcessState)
	}

	send(service.ShutdownPoweroff)
	ended := r.wait(10 * time.Second)
	log, _ := os.ReadFile(cal)
	if !ended {
		t.Fatalf("PID 1 still up 10s after a poweroff that followed a remain\ncatch-all log:\n%s", log)
	}
	// reboot(2) in a PID namespace ends its init with SIGINT for a
	// poweroff; unshare passes it on.
	ws, _ := r.cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ws.Signaled() || ws.Signal() != syscall.SIGINT {
		t.Errorf("namespace ended with %v, want SIGINT (poweroff)\ncatch-all log:\n%s", r.cmd.ProcessState, log)
	}
	if strings.Contains(string(log), "boot failure") || strings.Contains(string(log), "Boot failure") {
		t.Errorf("the empty service set after a remain was taken for a boot failure\ncatch-all log:\n%s", log)
	}
}

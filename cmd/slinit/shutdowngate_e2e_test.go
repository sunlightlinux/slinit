package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The control server listens before the boot services are started, so
// requests can come in while main is still wiring the server's hooks.
// Main assigned ShutdownFunc, WallFunc and NologinPath after Start, which
// connection goroutines read unlocked: a data race, a shutdown that
// could be held after the hand-over had drained the slot (ACKed, never
// carried out; see TestShutdownGateHandOverRace), and a `shutdown +N`
// scheduled during boot got neither its wall message nor /run/nologin.
//
// The boot service is a FIFO, so loading it blocks until the script has
// made its requests.
func TestShutdownRequestsDuringBootLoad(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sd"), 0755); err != nil {
		t.Fatal(err)
	}
	r := startNS(t, dir, `
mount -t tmpfs none /run && mount -t tmpfs none /etc || exit 1
mkfifo "$D/sd/boot"
"$B/slinit" -m -d "$D/sd" -p "$D/sock" --syslog-socket "" >"$D/log" 2>&1 &
i=0; while [ ! -S "$D/sock" ] && [ $i -lt 400 ]; do sleep 0.05; i=$((i+1)); done
"$B/slinitctl" -p "$D/sock" shutdown poweroff +3 >>"$D/trace" 2>&1; echo "schedule rc=$?" >>"$D/trace"
if [ -e /run/nologin ]; then echo nologin=yes; else echo nologin=no; fi >>"$D/trace"
"$B/slinitctl" -p "$D/sock" shutdown -c >>"$D/trace" 2>&1; echo "cancel rc=$?" >>"$D/trace"
"$B/slinitctl" -p "$D/sock" shutdown >>"$D/trace" 2>&1; echo "shutdown rc=$?" >>"$D/trace"
printf 'type = process\ncommand = /bin/sleep 100\n' > "$D/sd/boot"
wait
echo "slinit exited $?" >>"$D/trace"`)
	ended := r.wait(25 * time.Second)
	trace, _ := os.ReadFile(filepath.Join(dir, "trace"))
	log, _ := os.ReadFile(filepath.Join(dir, "log"))
	if !ended || !strings.Contains(string(trace), "slinit exited 0") {
		t.Errorf("the shutdown requested during boot was not carried out (ended=%v)", ended)
	}
	if !strings.Contains(string(trace), "nologin=yes") {
		t.Errorf("a shutdown scheduled during boot did not create /run/nologin")
	}
	if t.Failed() {
		t.Logf("trace:\n%s\nslinit log:\n%s", trace, log)
	}
}

// With no boot service to load, PID 1 waits in the rescue menu. A
// poweroff sent over the control socket then was ACKed and dropped: the
// menu stayed up and its timeout rebooted a minute later. It must be
// carried out at once, as a poweroff.
func TestShutdownDuringRescueMenu(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sd"), 0755); err != nil {
		t.Fatal(err)
	}
	// The menu needs a console it can open; give it a pty.
	ptm, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	defer ptm.Close()
	if err := unix.IoctlSetPointerInt(int(ptm.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Skipf("unlock pty: %v", err)
	}
	n, err := unix.IoctlGetInt(int(ptm.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Skipf("pty number: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pts"), []byte(fmt.Sprintf("/dev/pts/%d", n)), 0644); err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, ptm)
	r := startNS(t, dir, `
mount --bind "$(cat "$D/pts")" /dev/console && mount -t tmpfs none /run || exit 1
exec "$B/slinit" -d "$D/sd" -p "$D/sock" --syslog-socket "" --no-watchdog --catch-all-log="$D/cal"
`)
	sock := filepath.Join(dir, "sock")
	cal := filepath.Join(dir, "cal")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(cal); strings.Contains(string(b), "entering rescue menu") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond) // into the menu's wait
	out, err := exec.Command(filepath.Join(nsBinDir(t), "slinitctl"), "-p", sock, "poweroff").CombinedOutput()
	if err != nil {
		t.Fatalf("slinitctl poweroff: %v\n%s", err, out)
	}
	ended := r.wait(10 * time.Second)
	log, _ := os.ReadFile(cal)
	if !ended {
		t.Fatalf("PID 1 still up 10s after an ACKed poweroff (%q)\ncatch-all log:\n%s", out, log)
	}
	// reboot(2) in a PID namespace ends its init with SIGINT for a
	// halt or poweroff and SIGHUP for a reboot; unshare passes it on.
	ws, _ := r.cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ws.Signaled() || ws.Signal() != syscall.SIGINT {
		t.Errorf("namespace ended with %v, want SIGINT (poweroff)\ncatch-all log:\n%s", r.cmd.ProcessState, log)
	}
}

// When the boot service starts and then everything stops on its own,
// the event loop returns and PID 1 waits in the boot-collapse menu. A
// poweroff sent then went to the loop that had already returned: ACKed,
// dropped, and the menu's timeout rebooted. It must be held by the gate
// and carried out at once, as a poweroff.
func TestShutdownDuringCollapseMenu(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sd"), 0755); err != nil {
		t.Fatal(err)
	}
	// Starts, exits, is not restarted: nothing is left running.
	if err := os.WriteFile(filepath.Join(dir, "sd", "boot"),
		[]byte("type = process\ncommand = /bin/sleep 1\nrestart = no\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// The menu needs a console it can open; give it a pty.
	ptm, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	defer ptm.Close()
	if err := unix.IoctlSetPointerInt(int(ptm.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Skipf("unlock pty: %v", err)
	}
	n, err := unix.IoctlGetInt(int(ptm.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Skipf("pty number: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pts"), []byte(fmt.Sprintf("/dev/pts/%d", n)), 0644); err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, ptm)
	r := startNS(t, dir, `
mount --bind "$(cat "$D/pts")" /dev/console && mount -t tmpfs none /run || exit 1
exec "$B/slinit" -d "$D/sd" -p "$D/sock" --syslog-socket "" --no-watchdog --catch-all-log="$D/cal"
`)
	sock := filepath.Join(dir, "sock")
	cal := filepath.Join(dir, "cal")
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(cal); strings.Contains(string(b), "Boot failure detected") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond) // into the menu's wait
	out, err := exec.Command(filepath.Join(nsBinDir(t), "slinitctl"), "-p", sock, "poweroff").CombinedOutput()
	if err != nil {
		t.Fatalf("slinitctl poweroff: %v\n%s", err, out)
	}
	ended := r.wait(10 * time.Second)
	log, _ := os.ReadFile(cal)
	if !ended {
		t.Fatalf("PID 1 still up 10s after an ACKed poweroff (%q)\ncatch-all log:\n%s", out, log)
	}
	ws, _ := r.cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ws.Signaled() || ws.Signal() != syscall.SIGINT {
		t.Errorf("namespace ended with %v, want SIGINT (poweroff)\ncatch-all log:\n%s", r.cmd.ProcessState, log)
	}
}

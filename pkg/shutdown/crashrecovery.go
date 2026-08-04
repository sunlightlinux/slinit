package shutdown

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/bootmode"
)

// CrashPauseFn, when non-nil, is called by spawnCrashShell with `true`
// before SIGHUP + sulogin drop, and left set — the reboot syscall
// terminates the whole process, so unpausing after the shell exits
// is moot. Wired by main.go to serviceSet.SetCrashPause so a
// restart=yes tty svc cannot respawn its shell while sulogin holds
// /dev/console (would deadlock the operator with two readers
// competing for keystrokes — seen live in QEMU).
//
// Package-level variable rather than a param on CrashRecovery
// because CrashRecovery is registered via `defer` at main() entry,
// long before ServiceSet exists; a variable let main.go poke the
// wiring later. Nil-safe: production builds that don't wire it
// simply skip the pause step (with the two-shell race still
// possible in the post-boot panic path — the goroutine-panic
// wrappers cover most cases before this ever runs).
var CrashPauseFn func(paused bool)

// CrashRecovery is a deferred function that catches panics in the main
// goroutine and performs emergency cleanup. When running as PID 1, a panic
// would leave the system without an init process and hang forever. This
// safety net (inspired by s6-linux-init's "crash script") ensures that:
//   - Bare metal / PID 1: log to /dev/console → kill all → sync → reboot
//   - Container mode: log to stderr → exit(111)
//
// Usage: defer shutdown.CrashRecovery(isPID1, containerMode)
func CrashRecovery(isPID1, containerMode bool) {
	r := recover()
	if r == nil {
		return
	}

	msg := fmt.Sprintf("slinit: FATAL PANIC: %v\n", r)

	if containerMode {
		// Container mode: write to stderr and exit with failure code.
		os.Stderr.WriteString(msg)
		os.Stderr.WriteString("slinit: container init crashed, exiting\n")
		os.Exit(111)
	}

	if !isPID1 {
		// Non-PID1 system manager: just write to stderr and exit.
		os.Stderr.WriteString(msg)
		os.Exit(111)
	}

	// PID 1 crash recovery: last-resort emergency reboot.
	// Write directly to /dev/console since stdout/stderr may be broken.
	writeConsole(msg)

	// systemd.crash_shell parity: if slinit.crash-shell is on the
	// kernel cmdline, drop into a sulogin on /dev/console FIRST so the
	// operator can inspect the state. Best-effort: broken /proc or
	// missing sulogin fall through to the emergency reboot path.
	if opts, err := bootmode.ParseFromProc(); err == nil && opts.CrashShell {
		writeConsole("slinit: crash-shell enabled — spawning shell on /dev/console (exit to reboot)\n")
		spawnCrashShell()
		writeConsole("slinit: crash-shell exited; proceeding with emergency reboot\n")
	}

	writeConsole("slinit: PID 1 crashed — killing all processes and rebooting\n")

	// Kill every process except ourselves (PID 1).
	// kill(-1, SIGKILL) sends to all processes except the caller.
	syscall.Kill(-1, syscall.SIGKILL)

	// Sync filesystems to minimize data loss.
	syscall.Sync()

	// Force immediate reboot. This does not return.
	err := syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART)
	if err != nil {
		writeConsole(fmt.Sprintf("slinit: reboot syscall failed: %v\n", err))
	}

	// If reboot failed, halt instead.
	syscall.Reboot(syscall.LINUX_REBOOT_CMD_HALT)

	// Absolute last resort: block forever (PID 1 must never exit).
	select {}
}

// writeConsole writes a message directly to /dev/console.
// Errors are silently ignored — this is a last-resort path.
func writeConsole(msg string) {
	f, err := os.OpenFile("/dev/console", os.O_WRONLY, 0)
	if err != nil {
		// Fall back to stderr
		os.Stderr.WriteString(msg)
		return
	}
	f.WriteString(msg)
	f.Close()
}

// spawnCrashShell forks sulogin (or /bin/sh) on /dev/console and blocks
// until the operator exits it. Used from the CrashRecovery path when
// slinit.crash-shell is on the kernel cmdline (systemd.crash_shell
// parity). Best-effort: no shell binary → silent return → emergency
// reboot proceeds. Never returns an error since we're already on the
// last-resort crash path.
func spawnCrashShell() {
	candidates := []string{"/sbin/sulogin", "/bin/sulogin", "/bin/sh", "/usr/bin/sh"}
	var shell string
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			shell = p
			break
		}
	}
	if shell == "" {
		writeConsole("slinit: crash-shell: no sulogin or /bin/sh found; skipping\n")
		return
	}
	// Freeze the service state machine so restart=yes services (tty
	// svc in particular) do NOT respawn while sulogin holds the tty.
	// Without this the sighup below kicks bash off /dev/console, the
	// event loop sees the exit and calls callBringUp → new bash on
	// same tty → two-shell race. CrashPauseFn is nil-safe (production
	// builds may not wire it).
	if CrashPauseFn != nil {
		CrashPauseFn(true)
	}

	// Kill every existing process so nothing competes for /dev/console
	// with our sulogin. SIGHUP was tried first (send terminals a
	// hangup, let shells exit cleanly) but on Alpine bash --login
	// somewhere between kernel signal delivery and Go runtime signal
	// masking failed to actually die — leaving two shells racing on
	// the tty. SIGKILL is bulletproof: caught / ignored / masked all
	// bypassed. We're already on the crash path headed for a hard
	// reboot; grace for daemons isn't the priority here, a working
	// sulogin is.
	//
	// kill(-1, SIGKILL) sends SIGKILL to every process the caller
	// can signal — everything except us (PID 1). Small sleep lets
	// the kernel actually deliver + reap before we grab the tty fd,
	// preventing a race where /dev/console still shows the outgoing
	// controlling-tty owner.
	syscall.Kill(-1, syscall.SIGKILL)
	time.Sleep(200 * time.Millisecond)

	tty, err := os.OpenFile("/dev/console", os.O_RDWR, 0)
	if err != nil {
		writeConsole(fmt.Sprintf("slinit: crash-shell: open /dev/console: %v\n", err))
		return
	}
	defer tty.Close()
	cmd := exec.Command(shell)
	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	_ = cmd.Run() // operator exit is expected; ignore error
}

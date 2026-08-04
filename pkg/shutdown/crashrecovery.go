package shutdown

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/sunlightlinux/slinit/pkg/bootmode"
)

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

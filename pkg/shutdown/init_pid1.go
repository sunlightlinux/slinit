// Package shutdown implements PID 1 initialization and system shutdown
// operations for slinit, including reboot, halt, poweroff, and soft-reboot.
package shutdown

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"unsafe"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"golang.org/x/sys/unix"
)

// PR_SET_CHILD_SUBREAPER is the prctl constant for setting a process as
// a child subreaper. Orphaned descendant processes will be reparented to
// this process instead of init (PID 1).
const prSetChildSubreaper = 36

// Boot housekeeping defaults (configurable before calling InitPID1).
var (
	// bootBanner is printed to the console at the start of InitPID1.
	// Set via SetBootBanner(). Empty string disables the banner.
	bootBanner = "slinit booting..."

	// initUmask is the initial umask set in InitPID1. Default 0022
	// (matches s6-linux-init's default).
	initUmask uint32 = 0022
)

// SetBootBanner overrides the boot banner shown on the console.
// Pass empty string to disable.
func SetBootBanner(s string) { bootBanner = s }

// SetInitUmask overrides the initial umask set during PID 1 init.
func SetInitUmask(mask uint32) { initUmask = mask }

// InitPID1 performs early initialization required when running as PID 1.
// This includes boot housekeeping (chdir, umask, setsid, banner), setting
// up /dev/console, disabling Ctrl+Alt+Del, setting the child subreaper
// flag, and ignoring terminal job control signals.
func InitPID1(logger *logging.Logger) error {
	// chdir to / — ensures a sane working directory regardless of how
	// the kernel invoked init (s6-linux-init does this as its first step).
	if err := syscall.Chdir("/"); err != nil {
		logger.Debug("chdir /: %v (non-fatal)", err)
	}

	// Set initial umask. Configurable via SetInitUmask() before calling InitPID1.
	old := syscall.Umask(int(initUmask))
	if old != int(initUmask) {
		logger.Debug("umask set to %04o (was %04o)", initUmask, old)
	}

	// Become session leader so PID 1 owns its own session/TTY.
	// This may fail if already a session leader (typical for PID 1), which is fine.
	if _, err := syscall.Setsid(); err != nil {
		logger.Debug("setsid: %v (non-fatal, likely already session leader)", err)
	} else {
		logger.Debug("Session leader established")
	}

	// Mount essential filesystems early (needed before any service starts)
	mountEarlyFS(logger)

	// Set up /dev/console for stdin/stdout/stderr
	if err := setupConsole(); err != nil {
		logger.Debug("Console setup: %v (non-fatal)", err)
	} else {
		logger.Debug("Console redirected to /dev/console")
	}

	// Print boot banner on console (s6-linux-init prints a banner too).
	if bootBanner != "" {
		fmt.Fprintln(os.Stdout, bootBanner)
	}

	// Suppress non-critical kernel messages on console (like dmesg -n 1).
	// This prevents kernel log noise from interfering with service output.
	if _, err := unix.Klogctl(6 /* SYSLOG_ACTION_CONSOLE_OFF */, nil); err != nil {
		logger.Debug("klogctl(6): %v (non-fatal)", err)
	} else {
		logger.Debug("Kernel console messages suppressed")
	}

	// Disable Ctrl+Alt+Del reboot
	if err := disableCAD(); err != nil {
		logger.Debug("Disable CAD: %v (non-fatal)", err)
	} else {
		logger.Debug("Ctrl+Alt+Del disabled")
	}

	// Set child subreaper so orphaned processes reparent to us
	if err := SetChildSubreaper(); err != nil {
		logger.Debug("Set child subreaper: %v (non-fatal)", err)
	} else {
		logger.Debug("Child subreaper set")
	}

	// Ignore terminal job control signals
	ignoreTerminalSignals()
	logger.Debug("Terminal signals ignored (SIGTSTP, SIGTTIN, SIGTTOU, SIGPIPE)")

	// Clock guard: advance system clock if it's in the past
	// (protects against dead CMOS battery / missing RTC resetting to epoch)
	ClockGuard(logger)

	return nil
}

// mountEarlyFS mounts devtmpfs and proc if not already mounted.
// This provides /dev/null, /dev/zero, etc. needed by os/exec before
// any service starts. Also mounts /proc for kernel info access.
func mountEarlyFS(logger *logging.Logger) {
	// Mount devtmpfs on /dev (provides /dev/null, /dev/zero, etc.)
	if err := syscall.Mount("devtmpfs", "/dev", "devtmpfs", 0, ""); err != nil {
		logger.Debug("Mount devtmpfs: %v (non-fatal)", err)
	} else {
		logger.Debug("Mounted devtmpfs on /dev")
	}

	// Mount proc on /proc
	os.MkdirAll("/proc", 0555)
	if err := syscall.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		logger.Debug("Mount proc: %v (non-fatal)", err)
	} else {
		logger.Debug("Mounted proc on /proc")
	}
}

// setupConsole opens /dev/console and redirects stdin, stdout, and stderr to it.
// This ensures that log output goes to the system console when running as PID 1.
func setupConsole() error {
	// Open /dev/console for reading (stdin)
	consR, err := os.OpenFile("/dev/console", os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	if err := syscall.Dup2(int(consR.Fd()), 0); err != nil {
		consR.Close()
		return err
	}
	if int(consR.Fd()) > 2 {
		consR.Close()
	}

	// Open /dev/console for writing (stdout + stderr)
	consW, err := os.OpenFile("/dev/console", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := syscall.Dup2(int(consW.Fd()), 1); err != nil {
		consW.Close()
		return err
	}
	if err := syscall.Dup2(int(consW.Fd()), 2); err != nil {
		consW.Close()
		return err
	}
	if int(consW.Fd()) > 2 {
		consW.Close()
	}

	return nil
}

// disableCAD disables the Ctrl+Alt+Del reboot key combination.
// On Linux, this prevents the kernel from immediately rebooting
// when that key combination is pressed, giving slinit time to
// perform an orderly shutdown instead.
func disableCAD() error {
	return syscall.Reboot(syscall.LINUX_REBOOT_CMD_CAD_OFF)
}

// SetChildSubreaper sets the current process as a child subreaper.
// Descendant processes that are orphaned (their parent exits) will
// be reparented to this process rather than to PID 1.
// This is exported for use in tests.
func SetChildSubreaper() error {
	_, _, errno := syscall.RawSyscall(
		syscall.SYS_PRCTL,
		uintptr(prSetChildSubreaper),
		uintptr(1),
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

// isChildSubreaper checks if the current process is a child subreaper.
// Used in tests to verify SetChildSubreaper worked.
func isChildSubreaper() (bool, error) {
	const prGetChildSubreaper = 37
	var result int32
	_, _, errno := syscall.RawSyscall(
		syscall.SYS_PRCTL,
		uintptr(prGetChildSubreaper),
		uintptr(unsafe.Pointer(&result)),
		0,
	)
	if errno != 0 {
		return false, errno
	}
	return result != 0, nil
}

// ignoreTerminalSignals ignores signals related to terminal job control.
// These signals are not meaningful for an init system and would otherwise
// cause it to stop or interfere with process management.
func ignoreTerminalSignals() {
	signal.Ignore(
		syscall.SIGTSTP,  // Terminal stop (Ctrl+Z)
		syscall.SIGTTIN,  // Background process attempting read
		syscall.SIGTTOU,  // Background process attempting write
		syscall.SIGPIPE,  // Broken pipe
	)
}

// InitContainer performs initialization for container mode (-o / --container).
// Unlike InitPID1, it skips console setup, devtmpfs/proc mounts, and CAD
// disabling since the container runtime handles those. It only sets the
// subreaper flag and ignores terminal signals.
func InitContainer(logger *logging.Logger) error {
	// Set child subreaper so orphaned processes reparent to us
	if err := SetChildSubreaper(); err != nil {
		logger.Debug("Set child subreaper: %v (non-fatal)", err)
	} else {
		logger.Debug("Child subreaper set")
	}

	// Ignore terminal job control signals
	ignoreTerminalSignals()
	logger.Debug("Terminal signals ignored (SIGTSTP, SIGTTIN, SIGTTOU, SIGPIPE)")

	// Clock guard: advance system clock if it's in the past
	// (containers can inherit a stale clock from the host)
	ClockGuard(logger)

	return nil
}

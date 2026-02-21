// Package shutdown implements PID 1 initialization and system shutdown
// operations for slinit, including reboot, halt, poweroff, and soft-reboot.
package shutdown

import (
	"os"
	"os/signal"
	"syscall"
	"unsafe"

	"github.com/sunlightlinux/slinit/pkg/logging"
)

// PR_SET_CHILD_SUBREAPER is the prctl constant for setting a process as
// a child subreaper. Orphaned descendant processes will be reparented to
// this process instead of init (PID 1).
const prSetChildSubreaper = 36

// InitPID1 performs early initialization required when running as PID 1.
// This includes setting up /dev/console, disabling Ctrl+Alt+Del, setting
// the child subreaper flag, and ignoring terminal job control signals.
func InitPID1(logger *logging.Logger) error {
	// Set up /dev/console for stdin/stdout/stderr
	if err := setupConsole(); err != nil {
		logger.Debug("Console setup: %v (non-fatal)", err)
	} else {
		logger.Debug("Console redirected to /dev/console")
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

	return nil
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

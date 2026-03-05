package shutdown

import (
	"os"
	"syscall"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// Mockable exec function for testing.
var execFunc = syscall.Exec

// SoftReboot performs a soft reboot by re-executing slinit with the same
// arguments. This restarts the init system without rebooting the kernel.
//
// The sequence is:
// 1. Kill all remaining processes
// 2. Run shutdown hook, swapoff/umount
// 3. Sync filesystems
// 4. Re-exec slinit with original arguments
//
// If the exec fails, an error is returned and the caller should fall back
// to a hard reboot.
func SoftReboot(logger *logging.Logger) error {
	logger.Notice("Performing soft reboot...")

	// Resolve the executable path NOW, before we kill processes and
	// unmount filesystems. As PID 1, /proc may not be mounted at
	// package init time, so we resolve here while it's still available.
	execPath, err := os.Executable()
	if err != nil {
		// Fallback: os.Args[0] — the kernel always passes the absolute
		// path when launching PID 1.
		execPath = os.Args[0]
		logger.Debug("os.Executable() failed (%v), using os.Args[0]=%s", err, execPath)
	}

	// Kill remaining processes
	KillAllProcesses(logger)

	// Run shutdown hook (same as other shutdown types)
	hookHandledCleanup := runHookFunc(service.ShutdownSoftReboot, logger)
	if !hookHandledCleanup {
		swapOff(logger)
		unmountAll(logger)
	}

	// Sync filesystems
	syncFunc()

	logger.Notice("Re-executing %s", execPath)

	// Re-exec slinit with the same arguments and environment.
	// syscall.Exec replaces the current process image entirely.
	// If successful, this function never returns.
	return execFunc(execPath, os.Args, os.Environ())
}

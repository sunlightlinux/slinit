package shutdown

import (
	"os"
	"syscall"

	"github.com/sunlightlinux/slinit/pkg/logging"
)

// Mockable exec function for testing.
var execFunc = syscall.Exec

// SoftReboot performs a soft reboot by re-executing slinit with the same
// arguments. This restarts the init system without rebooting the kernel.
//
// The sequence is:
// 1. Sync filesystems to minimize data loss
// 2. Kill all remaining processes
// 3. Re-exec slinit with original arguments
//
// If the exec fails, an error is returned and the caller should fall back
// to a hard reboot.
func SoftReboot(logger *logging.Logger) error {
	logger.Notice("Performing soft reboot...")

	// Sync before cleanup
	syncFunc()

	// Kill remaining processes
	KillAllProcesses(logger)

	// Sync again after killing processes
	syncFunc()

	// Get the current executable path
	execPath, err := os.Executable()
	if err != nil {
		return err
	}

	logger.Notice("Re-executing %s", execPath)

	// Re-exec slinit with the same arguments and environment.
	// syscall.Exec replaces the current process image entirely.
	// If successful, this function never returns.
	return execFunc(execPath, os.Args, os.Environ())
}

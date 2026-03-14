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
// 1. Resolve exec path while /proc is still mounted
// 2. Run shutdown hook (if any) — hook may do its own cleanup
// 3. Sync filesystems
// 4. Re-exec slinit with original arguments
//
// Unlike a hard reboot/halt, soft reboot does NOT unmount filesystems or kill
// all processes. Filesystems must remain mounted and writable so the new slinit
// instance can create its control socket and load services normally.
//
// If the exec fails, an error is returned and the caller should fall back
// to a hard reboot.
func SoftReboot(logger *logging.Logger) error {
	logger.Notice("Performing soft reboot...")

	// Resolve the executable path NOW, before services stop and /proc
	// may become unavailable. As PID 1, /proc may not be mounted at
	// package init time, so we resolve here while it's still available.
	execPath, err := os.Executable()
	if err != nil {
		// Fallback: os.Args[0] — the kernel always passes the absolute
		// path when launching PID 1.
		execPath = os.Args[0]
		logger.Debug("os.Executable() failed (%v), using os.Args[0]=%s", err, execPath)
	}

	// Run shutdown hook if configured. For soft reboot we do NOT unmount
	// filesystems ourselves — keeping them mounted and writable is required
	// so the re-exec'd slinit can create its control socket.
	runHookFunc(service.ShutdownSoftReboot, logger)

	// Sync filesystems to flush any pending writes before re-exec.
	syncFunc()

	logger.Notice("Re-executing %s", execPath)

	// Re-exec slinit with the same arguments and environment.
	// syscall.Exec replaces the current process image entirely.
	// If successful, this function never returns.
	return execFunc(execPath, os.Args, os.Environ())
}

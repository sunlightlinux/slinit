package shutdown

import (
	"os"
	"strings"
	"syscall"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
	"github.com/sunlightlinux/slinit/pkg/snapshot"
)

// Mockable exec function for testing.
var execFunc = syscall.Exec

// statFunc is a mockable os.Stat for testing the snapshot-detection branch.
var statFunc = os.Stat

// SoftReboot performs a soft reboot by re-executing slinit with the same
// arguments. This restarts the init system without rebooting the kernel.
//
// The sequence is:
// 1. Resolve exec path while /proc is still mounted
// 2. Run shutdown hook (if any) — hook may do its own cleanup
// 3. Sync filesystems
// 4. Re-exec slinit with original arguments (plus a --restore-from-snapshot
//    pointer if the event loop dropped a snapshot)
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

	// If a soft-reboot snapshot was written (by eventloop.OnPreShutdown),
	// hand it to the new slinit binary via --restore-from-snapshot. This
	// is what makes manual activations / pins / triggers / global env
	// survive the in-place exec.
	argv := softRebootArgv(os.Args, snapshot.SoftRebootPath)

	logger.Notice("Re-executing %s", execPath)

	// Re-exec slinit with the same arguments and environment.
	// syscall.Exec replaces the current process image entirely.
	// If successful, this function never returns.
	return execFunc(execPath, argv, os.Environ())
}

// softRebootArgv returns args with --restore-from-snapshot=path appended
// when the snapshot file exists, or args unchanged otherwise. If args
// already contains a --restore-from-snapshot flag (e.g. the operator
// chained two soft-reboots in a row), the path is rewritten in place
// rather than duplicated — multiple values would confuse flag parsing.
func softRebootArgv(args []string, path string) []string {
	if path == "" {
		return args
	}
	if _, err := statFunc(path); err != nil {
		return args
	}
	flag := "--restore-from-snapshot=" + path
	out := make([]string, 0, len(args)+1)
	replaced := false
	for _, a := range args {
		if strings.HasPrefix(a, "--restore-from-snapshot=") {
			out = append(out, flag)
			replaced = true
			continue
		}
		out = append(out, a)
	}
	if !replaced {
		out = append(out, flag)
	}
	return out
}

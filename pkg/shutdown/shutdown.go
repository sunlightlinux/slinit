package shutdown

import (
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

const (
	// ProcessKillGracePeriod is the time to wait between SIGTERM and SIGKILL
	// when killing all remaining processes during shutdown.
	ProcessKillGracePeriod = 1 * time.Second

	// EmergencyShutdownTimeout is the maximum time to wait for services to
	// stop before forcing a shutdown.
	EmergencyShutdownTimeout = 90 * time.Second
)

// Mockable syscall functions for testing.
var (
	killFunc   = syscall.Kill
	syncFunc   = syscall.Sync
	rebootFunc = syscall.Reboot
)

// Execute performs the full shutdown sequence after all services have stopped.
// It kills remaining processes, syncs filesystems, and issues the appropriate
// reboot syscall. This function should only be called when running as PID 1.
// It does not return under normal circumstances.
func Execute(shutdownType service.ShutdownType, logger *logging.Logger) {
	logger.Notice("Executing shutdown: %s", shutdownType)

	// Kill all remaining processes
	KillAllProcesses(logger)

	// Sync filesystems to minimize data loss
	logger.Info("Syncing filesystems...")
	syncFunc()

	// Issue the appropriate reboot command
	if err := rebootSystem(shutdownType); err != nil {
		logger.Error("Reboot syscall failed: %v", err)
	}

	// If we get here, the reboot syscall failed.
	// PID 1 must never exit, so hold indefinitely.
	logger.Error("Shutdown failed, holding indefinitely")
	InfiniteHold()
}

// KillAllProcesses sends SIGTERM to all processes, waits for a grace period,
// then sends SIGKILL. This mirrors dinit's process cleanup in shutdown.cc.
// kill(-1, sig) sends the signal to every process except PID 1 itself.
func KillAllProcesses(logger *logging.Logger) {
	logger.Info("Sending SIGTERM to all processes...")
	if err := killFunc(-1, syscall.SIGTERM); err != nil {
		// ESRCH means no processes to signal - that's fine
		if err != syscall.ESRCH {
			logger.Debug("kill(-1, SIGTERM): %v", err)
		}
	}

	time.Sleep(ProcessKillGracePeriod)

	logger.Info("Sending SIGKILL to remaining processes...")
	if err := killFunc(-1, syscall.SIGKILL); err != nil {
		if err != syscall.ESRCH {
			logger.Debug("kill(-1, SIGKILL): %v", err)
		}
	}
}

// rebootSystem maps a ShutdownType to the appropriate Linux reboot command
// and issues the syscall.
func rebootSystem(shutdownType service.ShutdownType) error {
	var cmd int
	switch shutdownType {
	case service.ShutdownHalt:
		cmd = syscall.LINUX_REBOOT_CMD_HALT
	case service.ShutdownPoweroff:
		cmd = syscall.LINUX_REBOOT_CMD_POWER_OFF
	case service.ShutdownReboot:
		cmd = syscall.LINUX_REBOOT_CMD_RESTART
	default:
		// For unknown types, default to halt
		cmd = syscall.LINUX_REBOOT_CMD_HALT
	}
	return rebootFunc(cmd)
}

// InfiniteHold blocks the calling goroutine forever.
// PID 1 must never exit; this is used as a last resort when the
// reboot syscall fails or when ShutdownRemain is requested.
func InfiniteHold() {
	select {}
}

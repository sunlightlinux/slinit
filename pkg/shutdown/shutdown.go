package shutdown

import (
	"bytes"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

const (
	// DefaultKillGracePeriod is the default time to wait between SIGTERM and
	// SIGKILL when killing all remaining processes during shutdown.
	DefaultKillGracePeriod = 3 * time.Second

	// EmergencyShutdownTimeout is the maximum time to wait for services to
	// stop before forcing a shutdown.
	EmergencyShutdownTimeout = 90 * time.Second
)

// killGracePeriod is the configured time between SIGTERM and SIGKILL.
// Settable via SetKillGracePeriod (--shutdown-grace flag).
var killGracePeriod = DefaultKillGracePeriod

// SetKillGracePeriod overrides the default SIGTERM→SIGKILL grace period.
func SetKillGracePeriod(d time.Duration) {
	if d < 0 {
		d = 0
	}
	killGracePeriod = d
}

// KillGracePeriod returns the current SIGTERM→SIGKILL grace period.
func KillGracePeriod() time.Duration {
	return killGracePeriod
}

// shutdownHookPaths is the list of paths to search for a shutdown hook script.
// The first executable hook found is used; the rest are ignored.
var shutdownHookPaths = []string{
	"/etc/slinit/shutdown-hook",
	"/lib/slinit/shutdown-hook",
}

// Mockable syscall functions for testing.
var (
	killFunc   = syscall.Kill
	syncFunc   = syscall.Sync
	rebootFunc = syscall.Reboot
	runHookFunc = runShutdownHook
)

// Execute performs the full shutdown sequence after all services have stopped.
// It kills remaining processes, runs the shutdown hook (if present), performs
// filesystem cleanup, syncs, and issues the appropriate reboot syscall.
// This function should only be called when running as PID 1.
// It does not return under normal circumstances.
func Execute(shutdownType service.ShutdownType, logger *logging.Logger) {
	logger.Notice("Executing shutdown: %s", shutdownType)

	// Broadcast a final wall notice to any logged-in users.
	WallShutdownNotice(shutdownType, 0, logger)

	// Kill all remaining processes
	KillAllProcesses(logger)

	// Run shutdown hook; if it exits 0, it handled umount/swapoff itself
	hookHandledCleanup := runHookFunc(shutdownType, logger)

	// If hook didn't handle cleanup (or wasn't found), do it ourselves
	if !hookHandledCleanup {
		swapOff(logger)
		unmountAll(logger)
	}

	// Persist current time for next boot's clock guard
	if err := WriteClockTimestamp(); err != nil {
		logger.Debug("Failed to save clock timestamp: %v", err)
	} else {
		logger.Debug("Clock timestamp saved for next boot")
	}

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

// KillAllProcesses sends SIGTERM to all processes, waits for the configured
// grace period, then sends SIGKILL. This mirrors dinit's process cleanup in
// shutdown.cc. kill(-1, sig) sends the signal to every process except PID 1.
func KillAllProcesses(logger *logging.Logger) {
	grace := killGracePeriod

	logger.Info("Sending SIGTERM to all processes (grace %v)...", grace)
	if err := killFunc(-1, syscall.SIGTERM); err != nil {
		// ESRCH means no processes to signal - that's fine
		if err != syscall.ESRCH {
			logger.Debug("kill(-1, SIGTERM): %v", err)
		}
	}

	if grace > 0 {
		time.Sleep(grace)
	}

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
	case service.ShutdownKexec:
		// LINUX_REBOOT_CMD_KEXEC: reboot using a previously loaded kexec kernel.
		// The constant 0x45584543 is defined in linux/reboot.h but not in Go's syscall package.
		cmd = 0x45584543
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

// shutdownTypeArg returns the string argument passed to the shutdown hook
// script, matching dinit's convention.
func shutdownTypeArg(st service.ShutdownType) string {
	switch st {
	case service.ShutdownReboot:
		return "reboot"
	case service.ShutdownHalt:
		return "halt"
	case service.ShutdownPoweroff:
		return "poweroff"
	case service.ShutdownSoftReboot:
		return "soft"
	case service.ShutdownKexec:
		return "kexec"
	default:
		return "halt"
	}
}

// runShutdownHook searches for and executes a shutdown hook script.
// The hook receives the shutdown type as its first argument.
// Returns true if the hook was found, executed, and exited with status 0
// (meaning the hook handled umount/swapoff itself).
// Returns false if no hook was found, it failed to execute, or exited non-zero.
func runShutdownHook(shutdownType service.ShutdownType, logger *logging.Logger) bool {
	// Find the first executable hook
	var hookPath string
	for _, path := range shutdownHookPaths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		// Check if it's a regular file and executable (any execute bit)
		if !info.Mode().IsRegular() {
			continue
		}
		if info.Mode().Perm()&0111 == 0 {
			logger.Debug("Shutdown hook %s exists but is not executable", path)
			continue
		}
		hookPath = path
		break
	}

	if hookPath == "" {
		logger.Debug("No shutdown hook found")
		return false
	}

	arg := shutdownTypeArg(shutdownType)
	logger.Notice("Running shutdown hook: %s %s", hookPath, arg)

	cmd := exec.Command(hookPath, arg)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	err := cmd.Run()

	// Log any output from the hook
	if output.Len() > 0 {
		for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
			if len(line) > 0 {
				logger.Info("shutdown-hook: %s", string(line))
			}
		}
	}

	if err != nil {
		logger.Error("Shutdown hook failed: %v", err)
		return false
	}

	logger.Notice("Shutdown hook completed successfully (cleanup handled by hook)")
	return true
}

// swapOff disables all swap devices. Called during shutdown when no
// shutdown hook handled the cleanup.
func swapOff(logger *logging.Logger) {
	logger.Info("Disabling swap...")
	cmd := exec.Command("/sbin/swapoff", "-a")
	if output, err := cmd.CombinedOutput(); err != nil {
		logger.Debug("swapoff -a: %v (output: %s)", err, bytes.TrimSpace(output))
	}
}

// unmountAll unmounts all filesystems. Called during shutdown when no
// shutdown hook handled the cleanup.
func unmountAll(logger *logging.Logger) {
	logger.Info("Unmounting filesystems...")
	cmd := exec.Command("/bin/umount", "-a", "-r")
	if output, err := cmd.CombinedOutput(); err != nil {
		logger.Debug("umount -a -r: %v (output: %s)", err, bytes.TrimSpace(output))
	}
}

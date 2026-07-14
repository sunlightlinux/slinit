package shutdown

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
	"github.com/sunlightlinux/slinit/pkg/utmp"
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

// syncEnabled and wtmpEnabled gate the corresponding steps in Execute so
// slinit-shutdown can honour systemd-style --no-sync / --no-wtmp flags.
// Default: enabled (traditional shutdown behaviour).
var (
	syncEnabled = true
	wtmpEnabled = true
)

// finalSleep is the s6-linux-init-maker `-q` analogue: an optional
// pause after the SIGKILL wave and before umountall so pending fs
// writes (journal, flash-backed rewrites) have a beat to flush before
// we tear the filesystem down. Default 0 = disabled, matching the
// existing zero-cost path.
var finalSleep time.Duration

// minimumUptime is systemd v261's MinimumUptimeSec= anti-boot-loop
// floor. When set (non-zero) and Execute() fires before the system has
// been up for at least this long, the delta is slept off before the
// destructive sequence starts. Prevents tight reboot loops from
// burning flash / CI harnesses when a broken start-up unit trips into
// an immediate reboot. Default 0 = disabled (no floor).
var minimumUptime time.Duration

// uptimeFunc reads current system uptime (/proc/uptime) at check
// time. Overridable for tests so the boot-loop guard can be exercised
// without waiting real seconds.
var uptimeFunc = readUptime

// SetSyncEnabled toggles the pre-reboot syscall.Sync() call in Execute.
// Disable with `slinit-shutdown -n` / `--no-sync` for a fast unclean exit.
func SetSyncEnabled(v bool) { syncEnabled = v }

// SetWtmpEnabled toggles the utmp/wtmp write in Execute.
// Disable with `slinit-shutdown -d` / `--no-wtmp` to skip the shutdown record.
func SetWtmpEnabled(v bool) { wtmpEnabled = v }

// SetFinalSleep configures the post-SIGKILL / pre-umount settle
// pause. 0 (default) preserves the current zero-cost fast path.
func SetFinalSleep(d time.Duration) { finalSleep = d }

// SetMinimumUptime configures the anti-boot-loop floor. Passed by the
// daemon from --minimum-uptime-sec (or system.conf equivalent). Zero
// disables the check.
func SetMinimumUptime(d time.Duration) { minimumUptime = d }

// sleepFunc is the sleep primitive used between SIGKILL and umount.
// Overridable for tests so unit tests don't have to sleep.
var sleepFunc = time.Sleep

// shutdownHookPaths is the list of paths to search for a shutdown hook script.
// The first executable hook found is used; the rest are ignored.
var shutdownHookPaths = []string{
	"/etc/slinit/shutdown-hook",
	"/lib/slinit/shutdown-hook",
}

// Mockable syscall functions for testing.
var (
	killFunc           = syscall.Kill
	syncFunc           = syscall.Sync
	rebootFunc         = syscall.Reboot
	runHookFunc        = runShutdownHook
	logoutAllUsersFunc = utmp.LogoutAllUsers
	logShutdownFunc    = utmp.LogShutdown
)

// Execute performs the full shutdown sequence after all services have stopped.
// It kills remaining processes, runs the shutdown hook (if present), performs
// filesystem cleanup, syncs, and issues the appropriate reboot syscall.
// This function should only be called when running as PID 1.
// It does not return under normal circumstances.
func Execute(shutdownType service.ShutdownType, logger *logging.Logger) {
	// KillAllProcesses below will kill(-1, SIGKILL) every non-init process,
	// including any shell that fork/exec'd us. When the session leader dies
	// the kernel delivers SIGHUP to the rest of the session — us included.
	// Go's default action for SIGHUP terminates the process, so without
	// this an interactive `reboot -f` from a user shell dies silently at
	// the SIGKILL sweep and never reaches the reboot syscall. PID 1 has no
	// controlling tty so this is a no-op there; running it unconditionally
	// keeps the two entry paths symmetric.
	signal.Ignore(syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT, syscall.SIGPIPE)

	logger.Notice("Executing shutdown: %s", shutdownType)

	// Anti-boot-loop floor (systemd v261 MinimumUptimeSec=). When a
	// shutdown fires earlier than the configured floor, sleep the
	// delta before touching processes/filesystems. Keeps a broken
	// start-up unit from immediately re-tripping into the reboot
	// syscall in a tight loop that would burn flash / CI harnesses.
	if minimumUptime > 0 {
		if up, err := uptimeFunc(); err == nil && up < minimumUptime {
			wait := minimumUptime - up
			logger.Notice("Boot-loop guard: uptime %v < minimum %v, delaying shutdown by %v",
				up, minimumUptime, wait)
			sleepFunc(wait)
		}
	}

	// Broadcast a final wall notice to any logged-in users.
	WallShutdownNotice(shutdownType, 0, logger)

	// Mark every active login session as closed in utmp+wtmp so that
	// last(1) shows clean logout boundaries across the reboot. Doing
	// this before KillAllProcesses means the utmp/wtmp files are still
	// on a writable filesystem and no logger has been torn down.
	// Gated by wtmpEnabled (systemd's -d / --no-wtmp on reboot(8)).
	if wtmpEnabled {
		if n := logoutAllUsersFunc(); n > 0 {
			logger.Info("Logged out %d active session(s)", n)
		}
		logShutdownFunc()
	}

	// Kill all remaining processes
	KillAllProcesses(logger)

	// Settle pause (s6-linux-init-maker -q): give journals and other
	// flash-backed writers a beat to finish before umountall unwinds
	// the fs. Zero (default) skips the sleep entirely.
	if finalSleep > 0 {
		logger.Info("Settle pause: sleeping %v before umount", finalSleep)
		sleepFunc(finalSleep)
	}

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

	// Sync filesystems to minimize data loss. Skipped when the caller
	// asks for a fast exit via systemd's -n / --no-sync.
	if syncEnabled {
		logger.Info("Syncing filesystems...")
		syncFunc()
	} else {
		logger.Info("Skipping filesystem sync (--no-sync)")
	}

	// Issue the appropriate reboot command. Kexec has a well-known
	// gotcha: LINUX_REBOOT_CMD_KEXEC returns EINVAL when no kernel
	// has been pre-loaded via kexec_load / kexec_file_load (which
	// most operators do out-of-band via `kexec -l`). Rather than
	// leave the system in the "shutdown failed, holding indefinitely"
	// dead end — an operator who asked to reboot did not ask to stop
	// forever — we detect that specific EINVAL and fall back to a
	// normal reboot. This mirrors systemctl kexec's behavior.
	rebootType := shutdownType
	if err := rebootSystem(rebootType); err != nil {
		if rebootType == service.ShutdownKexec && errors.Is(err, syscall.EINVAL) {
			logger.Error("kexec reboot: no kernel pre-loaded (use `kexec -l <kernel>` before `slinitctl shutdown kexec`); falling back to normal reboot")
			rebootType = service.ShutdownReboot
			if err := rebootSystem(rebootType); err != nil {
				logger.Error("Fallback reboot syscall failed: %v", err)
			}
		} else {
			logger.Error("Reboot syscall failed: %v", err)
		}
	}

	// If we get here, the reboot syscall failed.
	// PID 1 must never exit, so hold indefinitely.
	logger.Error("Shutdown failed, holding indefinitely")
	InfiniteHold()
}

// ExecuteForce performs the minimal "get out now" shutdown path used by
// systemd's reboot -f: utmp shutdown record, filesystem sync, and the
// kernel reboot syscall. Services are NOT stopped and filesystems are
// NOT unmounted — this matches reboot(8)'s documented contract:
// "In most cases, filesystems are not properly unmounted before shutdown."
//
// Deliberate omissions vs. Execute (verified against systemd's
// systemctl-compat-halt.c halt_main → halt_now):
//   - no wall broadcast (systemd only walls via the logind path, which
//     -f skips entirely);
//   - no kill(-1); the reboot syscall is what stops everything;
//   - no umount / swapoff / shutdown-hook.
//
// Callers: `/sbin/reboot -f`, `slinit-shutdown -f`. PID 1's own shutdown
// flow uses Execute instead, which stops services and unmounts before
// the reboot syscall.
func ExecuteForce(shutdownType service.ShutdownType, logger *logging.Logger) {
	// Insurance against a session-leader SIGHUP if the caller ever ends
	// up in a killed pgroup — cheap, harmless on PID 1.
	signal.Ignore(syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT, syscall.SIGPIPE)

	logger.Notice("Forced shutdown: %s (filesystems not unmounted)", shutdownType)

	// utmp/wtmp shutdown record (respects --no-wtmp via SetWtmpEnabled).
	// systemd only writes when its own systemd-update-utmp is absent;
	// slinit has no such helper, so we always write unless -d.
	if wtmpEnabled {
		if n := logoutAllUsersFunc(); n > 0 {
			logger.Info("Logged out %d active session(s)", n)
		}
		logShutdownFunc()
	}

	// Persist the clock timestamp — the file is small and the write is
	// cheap; skipping it would cost the next boot a false-positive on
	// the clock regression guard.
	if err := WriteClockTimestamp(); err != nil {
		logger.Debug("Failed to save clock timestamp: %v", err)
	}

	// Sync unless -n / --no-sync. systemd's `reboot -ff` (double force)
	// skips sync — same effect here via `reboot -f -n`.
	if syncEnabled {
		logger.Info("Syncing filesystems...")
		syncFunc()
	} else {
		logger.Info("Skipping filesystem sync (--no-sync)")
	}

	// Re-enable Ctrl+Alt+Del so if the reboot syscall itself somehow
	// fails, the operator still has a kernel-level escape hatch. systemd
	// does the same in halt_now(). Errors are non-fatal.
	_ = rebootFunc(syscall.LINUX_REBOOT_CMD_CAD_ON)

	// See Execute() for the same fallback rationale on kexec EINVAL.
	rebootType := shutdownType
	if err := rebootSystem(rebootType); err != nil {
		if rebootType == service.ShutdownKexec && errors.Is(err, syscall.EINVAL) {
			logger.Error("kexec reboot: no kernel pre-loaded; falling back to normal reboot")
			rebootType = service.ShutdownReboot
			if err := rebootSystem(rebootType); err != nil {
				logger.Error("Fallback reboot syscall failed: %v", err)
			}
		} else {
			logger.Error("Reboot syscall failed: %v", err)
		}
	}
	logger.Error("Forced shutdown syscall returned unexpectedly")
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

// readUptime returns the system uptime by reading /proc/uptime (first
// field, seconds since boot as a float). Returns an error on any read
// or parse failure — the caller treats that as "unknown, skip guard".
func readUptime() (time.Duration, error) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	first, _, ok := strings.Cut(strings.TrimSpace(string(data)), " ")
	if !ok {
		first = strings.TrimSpace(string(data))
	}
	secs, err := strconv.ParseFloat(first, 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(secs * float64(time.Second)), nil
}

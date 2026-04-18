// Package shutdown implements PID 1 initialization and system shutdown
// operations for slinit, including reboot, halt, poweroff, and soft-reboot.
package shutdown

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"unsafe"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"golang.org/x/sys/unix"
)

// PR_SET_CHILD_SUBREAPER is the prctl constant for setting a process as
// a child subreaper. Orphaned descendant processes will be reparented to
// this process instead of init (PID 1).
const prSetChildSubreaper = 36

// RunMode selects how InitPID1 treats /run at boot.
type RunMode int

const (
	// RunModeMount (default) mounts a fresh tmpfs on /run. If /run is
	// already a mountpoint this is a no-op so we don't shadow an
	// existing tmpfs the initramfs or a previous boot left behind.
	RunModeMount RunMode = iota

	// RunModeRemount unmounts /run if it's already a tmpfs and mounts
	// a fresh one, guaranteeing an empty /run at boot even if a prior
	// boot or initramfs populated it.
	RunModeRemount

	// RunModeKeep leaves /run completely alone. Use when the
	// initramfs or host system has already staged exactly the layout
	// you want.
	RunModeKeep
)

// Boot housekeeping defaults (configurable before calling InitPID1).
var (
	// bootBanner is printed to the console at the start of InitPID1.
	// Set via SetBootBanner(). Empty string disables the banner.
	bootBanner = "slinit booting..."

	// initUmask is the initial umask set in InitPID1. Default 0022
	// (matches s6-linux-init's default).
	initUmask uint32 = 0022

	// devtmpfsPath is the mount point for devtmpfs. Default /dev.
	// Set via SetDevtmpfsPath() to mount devtmpfs somewhere else
	// (e.g. a chroot preparing /mnt/dev). Empty string skips the
	// mount entirely, matching s6-linux-init's --no-devtmpfs.
	devtmpfsPath = "/dev"

	// runMode selects whether /run is mounted fresh, remounted, or
	// left untouched by InitPID1.
	runMode = RunModeMount

	// kcmdlineDest is where /proc/cmdline is snapshotted during
	// PID 1 init. Empty disables the snapshot. The default path is
	// chosen so services can read the kernel command line without
	// depending on /proc being mounted at service start.
	kcmdlineDest = "/run/slinit/kcmdline"
)

// SetBootBanner overrides the boot banner shown on the console.
// Pass empty string to disable.
func SetBootBanner(s string) { bootBanner = s }

// SetInitUmask overrides the initial umask set during PID 1 init.
func SetInitUmask(mask uint32) { initUmask = mask }

// SetDevtmpfsPath overrides the devtmpfs mount point. Pass an empty
// string to skip the devtmpfs mount entirely (useful when the
// initramfs has already staged /dev and remounting would lose binds).
func SetDevtmpfsPath(p string) { devtmpfsPath = p }

// SetRunMode selects how /run is staged during InitPID1. Invalid
// values default to RunModeMount.
func SetRunMode(m RunMode) {
	if m < RunModeMount || m > RunModeKeep {
		m = RunModeMount
	}
	runMode = m
}

// SetKcmdlineDest overrides the /proc/cmdline snapshot destination.
// Empty string disables the snapshot.
func SetKcmdlineDest(p string) { kcmdlineDest = p }

// ParseRunMode accepts the CLI spelling of a RunMode.
func ParseRunMode(s string) (RunMode, error) {
	switch s {
	case "", "mount", "fresh":
		return RunModeMount, nil
	case "remount":
		return RunModeRemount, nil
	case "keep", "hands-off", "none":
		return RunModeKeep, nil
	default:
		return RunModeMount, fmt.Errorf("invalid run mode %q (want mount|remount|keep)", s)
	}
}

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

// mountEarlyFS mounts devtmpfs, proc, and /run if not already mounted.
// This provides /dev/null, /dev/zero, etc. needed by os/exec before
// any service starts. Also mounts /proc for kernel info access and
// stages /run per the configured RunMode so services can rely on a
// writable tmpfs from the very first service invocation.
func mountEarlyFS(logger *logging.Logger) {
	// Mount devtmpfs on the configured path. Empty path skips the
	// mount entirely, matching s6-linux-init's --no-devtmpfs.
	if devtmpfsPath != "" {
		if err := syscall.Mount("devtmpfs", devtmpfsPath, "devtmpfs", 0, ""); err != nil {
			logger.Debug("Mount devtmpfs on %s: %v (non-fatal)", devtmpfsPath, err)
		} else {
			logger.Debug("Mounted devtmpfs on %s", devtmpfsPath)
		}
	} else {
		logger.Debug("Skipping devtmpfs mount (path disabled)")
	}

	// Mount proc on /proc
	os.MkdirAll("/proc", 0555)
	if err := syscall.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		logger.Debug("Mount proc: %v (non-fatal)", err)
	} else {
		logger.Debug("Mounted proc on /proc")
	}

	// Stage /run according to the configured mode. StageRun is
	// idempotent so this is a no-op if the caller already staged /run
	// before StartCatchAll (to keep the catch-all log from being
	// hidden by a later tmpfs mount).
	StageRun(logger)

	// Snapshot kernel cmdline so services can read it without depending
	// on /proc being mounted in their mount namespace.
	if kcmdlineDest != "" {
		if err := snapshotKernelCmdline(kcmdlineDest); err != nil {
			logger.Debug("Snapshot kernel cmdline: %v (non-fatal)", err)
		} else {
			logger.Debug("Kernel cmdline snapshot written to %s", kcmdlineDest)
		}
	}
}

// runStaged ensures mountRunTmpfs runs exactly once per process. This
// lets callers stage /run early (e.g. before StartCatchAll, so the
// catch-all log file lives on the final tmpfs and isn't buried under
// a later mount) without breaking InitPID1, which also wants to do it.
var runStaged sync.Once

// StageRun stages /run per the configured RunMode. It is idempotent:
// the second and subsequent calls are no-ops.
//
// Call this from the caller (cmd/slinit) before StartCatchAll when you
// want the catch-all log to survive — otherwise InitPID1 mounts a
// fresh tmpfs over /run and hides the log file the catch-all logger
// opened on the initramfs's /run.
func StageRun(logger *logging.Logger) {
	runStaged.Do(func() { mountRunTmpfs(logger) })
}

// mountRunTmpfs stages /run according to the runMode global. See
// RunMode for the individual modes.
func mountRunTmpfs(logger *logging.Logger) {
	const runPath = "/run"
	switch runMode {
	case RunModeKeep:
		logger.Debug("Run mode: keep — leaving /run untouched")
		return
	case RunModeRemount:
		// Best-effort unmount first. MNT_DETACH so we don't block on a
		// busy tmpfs; a stale mount behind the new one is acceptable.
		if err := unix.Unmount(runPath, unix.MNT_DETACH); err != nil && !os.IsNotExist(err) {
			logger.Debug("Unmount %s before remount: %v (non-fatal)", runPath, err)
		}
		fallthrough
	case RunModeMount:
		os.MkdirAll(runPath, 0755)
		// mode=0755,nosuid,nodev matches systemd and s6-linux-init.
		const flags = syscall.MS_NOSUID | syscall.MS_NODEV
		if err := syscall.Mount("tmpfs", runPath, "tmpfs", flags, "mode=0755"); err != nil {
			logger.Debug("Mount tmpfs on %s: %v (non-fatal)", runPath, err)
		} else {
			logger.Debug("Mounted tmpfs on %s", runPath)
		}
	}
}

// snapshotKernelCmdline copies /proc/cmdline into dest so services can
// access the kernel command line without requiring /proc in their own
// mount namespace. Creates dest's parent directory if missing.
func snapshotKernelCmdline(dest string) error {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return fmt.Errorf("read /proc/cmdline: %w", err)
	}
	if err := os.MkdirAll(parentDir(dest), 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", parentDir(dest), err)
	}
	// 0444 — world-readable, not writable. Services may read freely;
	// no one should rewrite the snapshot after boot.
	if err := os.WriteFile(dest, data, 0444); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return nil
}

// parentDir is filepath.Dir(p) inlined to avoid pulling in path/filepath
// just for this one call (keeps the PID-1 binary minimal).
func parentDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			if i == 0 {
				return "/"
			}
			return p[:i]
		}
	}
	return "."
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

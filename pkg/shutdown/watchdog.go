package shutdown

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"golang.org/x/sys/unix"
)

// WDIOC_SETTIMEOUT: Linux watchdog driver ioctl to set the reset
// countdown, defined in linux/watchdog.h as _IOWR('W', 6, int).
// Hard-coded here rather than pulled from x/sys/unix so the build
// doesn't tie to a particular version's constant name (kernels have
// carried this since forever, and the number is stable).
const wdiocSetTimeout = 0xC0045706

// finit-parity: arm the hardware watchdog with a short timeout and
// leave it armed at shutdown so the WDT peripheral resets the board
// instead of relying on the reboot(2) syscall. Only meaningful for
// ShutdownReboot — WDT can't power off, halt, or exec another init.
var (
	// watchdogRebootEnabled gates the arm-and-wait detour before the
	// reboot syscall in Execute. Off by default; toggled by
	// SetWatchdogReboot from cmd/slinit main once the kernel-cmdline
	// slinit.reboot-watchdog flag is parsed.
	watchdogRebootEnabled bool
	// watchdogDevice is the WDT character device the driver exposes.
	// Overridable for tests + for boards with multiple WDTs.
	watchdogDevice = "/dev/watchdog"
	// watchdogRebootTimeout is how long the kernel WDT will wait
	// before firing the reset once armed. Short enough that shutdown
	// doesn't visibly stall, long enough that kernel + firmware can
	// log the impending reset. 10s matches Finit's default expectation
	// for the same knob.
	watchdogRebootTimeout = 10 * time.Second
	// watchdogOpenFunc + watchdogIoctlFunc are hookable for tests so
	// the arm path is exercisable without a real /dev/watchdog. In
	// production these resolve to syscall.Open + unix.Syscall(IOCTL).
	watchdogOpenFunc  = defaultWatchdogOpen
	watchdogIoctlFunc = defaultWatchdogIoctl
)

// SetWatchdogReboot toggles WDT-driven reboot for the ShutdownReboot
// path. Callers: cmd/slinit main (kernel cmdline slinit.reboot-
// watchdog=yes) and future explicit CLI flag. Idempotent.
func SetWatchdogReboot(v bool) { watchdogRebootEnabled = v }

// WatchdogRebootEnabled reports the current flag value. Used by
// tests + by the Execute path's runtime check.
func WatchdogRebootEnabled() bool { return watchdogRebootEnabled }

// SetWatchdogDevice overrides the device path (default /dev/watchdog).
// Boards with multiple WDTs may point at a specific one; tests can
// point at a regular file backed by a stub ioctl.
func SetWatchdogDevice(path string) { watchdogDevice = path }

// SetWatchdogRebootTimeout tunes the timeout the kernel WDT counts
// down before firing the reset. Clamped to [1s, 300s] — a sub-second
// window races kernel scheduling; a five-minute-plus window defeats
// the point ("reset promptly"). Zero and negative fall back to the
// default (10s).
func SetWatchdogRebootTimeout(d time.Duration) {
	if d <= 0 {
		d = 10 * time.Second
	}
	if d < time.Second {
		d = time.Second
	}
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	watchdogRebootTimeout = d
}

// armWatchdogAndWait opens the WDT device, sets a short timeout via
// WDIOC_SETTIMEOUT, leaves the fd open so the driver stays armed,
// and sleeps well past the timeout window. Returns nil if the arm
// call succeeded (in which case the WDT usually fires before this
// function returns — we're then dead); returns an error if open or
// ioctl failed and the caller should fall back to the reboot(2)
// syscall as the safety net.
//
// Deliberately does NOT close the fd. Many Linux WDT drivers disarm
// on close unless CONFIG_WATCHDOG_NOWAYOUT is set OR the "V" magic-
// close character was written. Leaving the fd open sidesteps both
// contracts and keeps the WDT armed for every driver.
func armWatchdogAndWait(logger *logging.Logger) error {
	fd, err := watchdogOpenFunc(watchdogDevice)
	if err != nil {
		return fmt.Errorf("watchdog: open %s: %w", watchdogDevice, err)
	}
	timeoutSec := int32(watchdogRebootTimeout / time.Second)
	if timeoutSec < 1 {
		timeoutSec = 1
	}
	if err := watchdogIoctlFunc(fd, wdiocSetTimeout, uintptr(unsafe.Pointer(&timeoutSec))); err != nil {
		// fd deliberately NOT closed — even a failed WDIOC_SETTIMEOUT
		// may have partially armed the driver, and closing could then
		// disarm it. Let the caller fall through to reboot(2).
		return fmt.Errorf("watchdog: WDIOC_SETTIMEOUT: %w", err)
	}
	logger.Warn("reboot-watchdog: armed %s with %ds timeout, waiting for hardware reset",
		watchdogDevice, timeoutSec)
	// Sleep past 2x the timeout — if the WDT hasn't fired by then,
	// something is wrong with the kernel driver or the peripheral,
	// and the caller will fall through to reboot(2) as backup.
	sleepFunc(time.Duration(timeoutSec) * 2 * time.Second)
	return nil
}

// defaultWatchdogOpen is the production Open path (bypasses os.File
// so the returned fd has no Go finalizer to accidentally close it).
func defaultWatchdogOpen(path string) (int, error) {
	return syscall.Open(path, syscall.O_WRONLY, 0)
}

// defaultWatchdogIoctl wraps unix.Syscall(SYS_IOCTL, …) with an
// errno → error translation the test double can mimic.
func defaultWatchdogIoctl(fd int, req uintptr, arg uintptr) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, arg)
	if errno != 0 {
		return errno
	}
	return nil
}

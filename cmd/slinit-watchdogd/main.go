// slinit-watchdogd — runtime hardware watchdog petting daemon.
// finit-parity for finit's built-in watchdog.c: opens /dev/watchdog,
// sets a bounded timeout, and calls WDIOC_KEEPALIVE at half the
// timeout interval so the kernel WDT peripheral stays quiet under
// normal operation. On clean shutdown (SIGTERM / SIGINT) it writes
// the "V" magic-close byte + closes the device so the WDT disarms
// gracefully; on SIGPWR it hands the device off to an external
// watchdogd successor (close without "V", WDT stays armed for the
// next petter to inherit).
//
// Complements pkg/shutdown's `reboot-watchdog` (which arms the WDT
// at shutdown time to trigger a hardware reset). Together they
// cover the full runtime-plus-shutdown WDT lifecycle a real
// embedded deployment needs.
//
// Deliberate scope-outs (matching finit's src/watchdog.c):
//   - No external-watchdogd chaining beyond SIGPWR — operators
//     wanting a full watchdog framework use skarnet's / Debian's
//     `watchdogd`.
//   - No pretest hooks (fs-check / disk-monitor). Slinit's own
//     health-check-command directive is the right seam for that.
//   - No per-service watchdog supervision. That's a distinct
//     feature (systemd WatchdogSec=) and lives in pkg/service if
//     ever added; the daemon here only pets the kernel WDT.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	// WDIOC_SETTIMEOUT: Linux watchdog driver ioctl to set the
	// reset countdown. _IOWR('W', 6, int) — stable across every
	// kernel that ships the watchdog API (linux/watchdog.h).
	// Duplicated from pkg/shutdown/watchdog.go rather than
	// imported: cmd/ binaries stay independent of pkg/shutdown's
	// larger surface (fs sync, wtmp write, reboot syscall).
	wdiocSetTimeout uint = 0xC0045706
	// WDIOC_KEEPALIVE: pet the WDT. _IOR('W', 5, int). Any ioctl
	// call resets the driver's countdown. Some drivers also
	// accept a write() of any byte to the same effect; the ioctl
	// is portable across every mainline driver.
	wdiocKeepalive uint = 0x80045705

	// defaultTimeoutSec is a conservative default — 60 s gives
	// the reset countdown enough runway that a hiccup in the
	// petting loop (GC pause, temporary schedule starvation) does
	// not accidentally reset the machine, but short enough that a
	// real slinit-watchdogd freeze produces a reset within a
	// minute rather than after several minutes.
	defaultTimeoutSec = 60

	// minIntervalSec is the floor for the petting interval — 5 s
	// keeps syscall pressure trivial (twelve pets/minute worst
	// case) while still catching a wedge inside a 30 s window.
	minIntervalSec = 5

	// magicCloseByte is the sentinel a driver watches for to
	// grant graceful disarm on close. Any other byte pets; only
	// "V" disarms. Documented in Documentation/watchdog/
	// watchdog-api.rst.
	magicCloseByte = 'V'
)

func main() {
	var (
		device      = flag.String("d", "/dev/watchdog", "watchdog device path")
		timeoutSec  = flag.Int("t", defaultTimeoutSec, "timeout in seconds (kernel reset window)")
		intervalSec = flag.Int("i", 0, "petting interval in seconds (0 = timeout/2, min 5)")
		verbose     = flag.Bool("v", false, "log every pet at Info level (default: quiet)")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: slinit-watchdogd [-d DEVICE] [-t TIMEOUT_SEC] [-i INTERVAL_SEC] [-v]\n")
	}
	flag.Parse()

	if *timeoutSec < 1 {
		fmt.Fprintln(os.Stderr, "slinit-watchdogd: timeout must be >= 1s")
		os.Exit(2)
	}
	pettingInterval := *intervalSec
	if pettingInterval <= 0 {
		pettingInterval = *timeoutSec / 2
	}
	if pettingInterval < minIntervalSec {
		pettingInterval = minIntervalSec
	}
	if pettingInterval >= *timeoutSec {
		// Clamp to timeout-1 so the WDT never fires between two
		// consecutive pets — an interval >= timeout races the
		// hardware reset and defeats the entire point.
		pettingInterval = *timeoutSec - 1
	}

	if err := run(*device, *timeoutSec, pettingInterval, *verbose); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-watchdogd: %v\n", err)
		os.Exit(1)
	}
}

// run opens the WDT, sets the timeout, and enters the pet loop.
// Returns on SIGTERM / SIGINT (graceful disarm) or SIGPWR
// (handover to an external watchdogd — WDT stays armed).
func run(device string, timeoutSec, intervalSec int, verbose bool) error {
	// syscall.Open (not os.OpenFile) so no *os.File finalizer can
	// race the magic-close ceremony below.
	fd, err := syscall.Open(device, syscall.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", device, err)
	}

	// Set the timeout upfront. Kernel drivers accept an int
	// argument; the pointer must survive the ioctl call, which
	// it does because Go doesn't move stack values under ioctl.
	timeoutArg := int32(timeoutSec)
	if err := ioctl(fd, wdiocSetTimeout, unsafe.Pointer(&timeoutArg)); err != nil {
		_ = syscall.Close(fd)
		return fmt.Errorf("WDIOC_SETTIMEOUT(%d): %w", timeoutSec, err)
	}

	fmt.Fprintf(os.Stderr, "slinit-watchdogd: armed %s (timeout=%ds, interval=%ds)\n",
		device, timeoutSec, intervalSec)

	// Signal wiring.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGPWR, syscall.SIGHUP)

	ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
	defer ticker.Stop()

	// Prime the pump — one immediate pet so a slow first Ticker
	// tick doesn't sail past the initial timeout window when
	// timeoutSec is small and the daemon starts under load.
	if err := pet(fd); err != nil {
		_ = syscall.Close(fd)
		return fmt.Errorf("initial WDIOC_KEEPALIVE: %w", err)
	}
	if verbose {
		fmt.Fprintln(os.Stderr, "slinit-watchdogd: initial pet")
	}

	for {
		select {
		case <-ticker.C:
			if err := pet(fd); err != nil {
				// Best-effort: log and continue. The alternative
				// (exit) would let the WDT fire and reset the
				// system when the problem might just be a
				// transient ioctl failure.
				fmt.Fprintf(os.Stderr, "slinit-watchdogd: pet failed: %v\n", err)
				continue
			}
			if verbose {
				fmt.Fprintln(os.Stderr, "slinit-watchdogd: pet")
			}

		case sig := <-sigCh:
			switch sig {
			case syscall.SIGTERM, syscall.SIGINT:
				// Graceful shutdown: write "V" magic byte + close.
				// Driver disarms; system does NOT reset. Slinit
				// itself, if wanting a WDT-driven reset, calls
				// pkg/shutdown/armWatchdogAndWait separately.
				if _, werr := syscall.Write(fd, []byte{magicCloseByte}); werr != nil {
					fmt.Fprintf(os.Stderr, "slinit-watchdogd: magic-close write: %v\n", werr)
				}
				_ = syscall.Close(fd)
				fmt.Fprintf(os.Stderr, "slinit-watchdogd: exiting on %s (WDT disarmed)\n", sig)
				return nil

			case syscall.SIGPWR:
				// Hand-over to an external watchdogd: close
				// WITHOUT the "V" magic so the driver keeps the
				// WDT armed. The successor daemon must open
				// /dev/watchdog within the current timeout
				// window; if it doesn't, the WDT fires. This
				// matches finit's src/watchdog.c "handover" flag.
				_ = syscall.Close(fd)
				fmt.Fprintf(os.Stderr, "slinit-watchdogd: exiting on SIGPWR (WDT armed for successor)\n")
				return nil

			case syscall.SIGHUP:
				// Re-arm: kernel drivers accept a second
				// WDIOC_SETTIMEOUT to change the window at
				// runtime. Useful for operator scripts that
				// tighten / loosen the reset window without
				// restarting the daemon.
				if err := ioctl(fd, wdiocSetTimeout, unsafe.Pointer(&timeoutArg)); err != nil {
					fmt.Fprintf(os.Stderr, "slinit-watchdogd: SIGHUP re-arm: %v\n", err)
				} else {
					fmt.Fprintf(os.Stderr, "slinit-watchdogd: SIGHUP: timeout re-armed at %ds\n", timeoutSec)
				}
			}
		}
	}
}

// pet issues one WDIOC_KEEPALIVE ioctl. The dummy int argument is
// unused by the driver but the syscall interface still needs a
// pointer of the right shape.
func pet(fd int) error {
	var dummy int32
	return ioctl(fd, wdiocKeepalive, unsafe.Pointer(&dummy))
}

// ioctl wraps the SYS_IOCTL syscall with an errno → error
// translation. Kept as a local helper so the call sites stay
// short; unix.Syscall would also work but explicit wrapping
// makes the error path clearer.
func ioctl(fd int, req uint, arg unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(req), uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

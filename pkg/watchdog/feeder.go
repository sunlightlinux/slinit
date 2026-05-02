// Package watchdog implements a hardware watchdog feeder for slinit.
//
// On telco / 5G / digital-call gear, a daemon-level watchdog is mandatory:
// if slinit (or the kernel) hangs, the kernel watchdog timer expires and
// the system resets without operator intervention. The feeder opens
// /dev/watchdog{0}, sets the kernel-side timeout, then pings the device
// at a sub-timeout interval until it is closed.
//
// The Linux watchdog API (Documentation/watchdog/watchdog-api.rst) lets a
// userspace process keep the timer alive by writing any non-'V' byte to
// the device fd, and disarm it cleanly by writing the byte 'V' before
// close ("magic close"). We use byte writes for the keep-alive — that
// keeps Ping() trivially testable against a regular file or pipe and
// avoids a second ioctl on every tick.
package watchdog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// DefaultDevice is the canonical watchdog character device.
	DefaultDevice = "/dev/watchdog0"

	// FallbackDevice is the legacy single-watchdog name used on systems
	// that only expose one timer. We try DefaultDevice first and fall
	// back here only when the caller did not pin a path explicitly.
	FallbackDevice = "/dev/watchdog"

	// DefaultTimeout is the kernel-side timer used when none is supplied.
	// 60s is conservative enough to ride out a transient stall but tight
	// enough that a wedged kernel does not park the box for minutes.
	DefaultTimeout = 60 * time.Second

	// DefaultIntervalDivisor: ping at timeout/N. Three pings per timeout
	// window means a single dropped tick (e.g. brief CPU starvation)
	// still leaves two more chances before the watchdog expires.
	DefaultIntervalDivisor = 3

	// magicClose is the sentinel byte the kernel watches for on close
	// (CONFIG_WATCHDOG_NOWAYOUT=n). Writing 'V' immediately before
	// close() disarms the timer cleanly.
	magicClose = 'V'

	// keepAliveByte is any non-magic byte; the kernel resets the timer
	// on every byte write that is NOT 'V'. We use 0 for clarity.
	keepAliveByte = 0

	// wdiocSetTimeout is _IOWR('W', 6, int) — the WDIOC_SETTIMEOUT
	// ioctl number from <linux/watchdog.h>. It takes a pointer to an
	// int (seconds). We hard-code the constant rather than importing
	// from linux/watchdog so the package builds on non-Linux GOOS for
	// tests; the OS-conditional Open() never reaches the ioctl on
	// other platforms anyway.
	wdiocSetTimeout = 0xC0045706
)

// Config configures a Feeder. Zero values trigger sensible defaults; see
// Resolve.
type Config struct {
	// Device is the path to the watchdog character device. Empty means
	// "try DefaultDevice, fall back to FallbackDevice".
	Device string

	// Timeout is the kernel-side timer programmed via WDIOC_SETTIMEOUT.
	// Zero means DefaultTimeout.
	Timeout time.Duration

	// Interval is how often we ping. Zero means Timeout / DefaultIntervalDivisor.
	Interval time.Duration
}

// Resolve fills in defaults and validates the resulting configuration.
// It does not touch the filesystem; callers can use it ahead of Open()
// to surface flag errors before we commit to opening /dev/watchdog.
func (c Config) Resolve() (Config, error) {
	out := c
	if out.Timeout <= 0 {
		out.Timeout = DefaultTimeout
	}
	if out.Interval <= 0 {
		out.Interval = out.Timeout / DefaultIntervalDivisor
		if out.Interval <= 0 {
			out.Interval = time.Second
		}
	}
	if out.Interval >= out.Timeout {
		return out, fmt.Errorf("watchdog interval (%s) must be < timeout (%s)",
			out.Interval, out.Timeout)
	}
	return out, nil
}

// Feeder owns an open /dev/watchdog file descriptor and pings it on a
// timer until Close. All public methods are safe to call from any
// goroutine; an internal mutex serialises writes to the device.
type Feeder struct {
	cfg Config

	mu     sync.Mutex
	file   *os.File
	closed bool
}

// Open opens the watchdog device and programs the kernel-side timeout.
// The returned Feeder is armed: the kernel timer is running and will
// expire (and reset the box) unless Run is started or Ping is called
// inside the configured interval.
//
// On platforms without /dev/watchdog (containers, non-Linux, missing
// kernel support) Open returns an error and the caller is expected to
// continue without a feeder.
func Open(cfg Config) (*Feeder, error) {
	resolved, err := cfg.Resolve()
	if err != nil {
		return nil, err
	}

	device := resolved.Device
	if device == "" {
		// Auto-discover: prefer the canonical numbered device, fall back
		// to the legacy unnumbered alias only if the first does not exist.
		if _, err := os.Stat(DefaultDevice); err == nil {
			device = DefaultDevice
		} else if _, err := os.Stat(FallbackDevice); err == nil {
			device = FallbackDevice
		} else {
			return nil, fmt.Errorf("no watchdog device found (looked at %s, %s)",
				DefaultDevice, FallbackDevice)
		}
		resolved.Device = device
	}

	// O_WRONLY | O_CLOEXEC: write-only is enough for keepalives and the
	// magic-close byte; CLOEXEC keeps the fd from leaking into services
	// we fork later.
	file, err := os.OpenFile(device, os.O_WRONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", device, err)
	}

	f := &Feeder{cfg: resolved, file: file}

	if err := f.setKernelTimeout(resolved.Timeout); err != nil {
		// We could not program the timer; bail out cleanly so the kernel
		// timer is disarmed (via magic-close) before we return.
		_ = f.Close()
		return nil, fmt.Errorf("set watchdog timeout: %w", err)
	}

	// Initial ping so the just-programmed timer starts from "now"
	// rather than from whatever value the device had before we opened it.
	if err := f.Ping(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("initial watchdog ping: %w", err)
	}

	return f, nil
}

// setKernelTimeout asks the kernel to use the given timeout. The kernel
// rounds to the nearest value its driver supports and writes the
// effective value back through the int pointer; we accept whatever it
// chooses without complaint (the operator can read the effective value
// via Timeout()).
func (f *Feeder) setKernelTimeout(d time.Duration) error {
	secs := int(d.Round(time.Second).Seconds())
	if secs <= 0 {
		secs = 1
	}
	if err := unix.IoctlSetPointerInt(int(f.file.Fd()), wdiocSetTimeout, secs); err != nil {
		return err
	}
	return nil
}

// Run pings the device on a ticker until ctx is cancelled or the feeder
// is closed. It returns nil on clean cancellation and the underlying
// error on a ping failure (which is treated as non-fatal at the call
// site — a hung device is exactly what the watchdog itself protects
// against, but losing the fd should still surface).
func (f *Feeder) Run(ctx context.Context) error {
	t := time.NewTicker(f.cfg.Interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := f.Ping(); err != nil {
				// Don't treat a closed feeder as an error: Close is the
				// expected way to stop Run, and there is a short window
				// where the ticker can fire after Close acquires the
				// mutex but before the goroutine sees ctx cancelled.
				if errors.Is(err, errClosed) {
					return nil
				}
				return err
			}
		}
	}
}

var errClosed = errors.New("watchdog: feeder closed")

// Ping resets the kernel-side timer. Safe to call concurrently with Run
// (e.g. from a service-level health-check probe in a future phase).
func (f *Feeder) Ping() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errClosed
	}
	if _, err := f.file.Write([]byte{keepAliveByte}); err != nil {
		return fmt.Errorf("watchdog ping: %w", err)
	}
	return nil
}

// Close disarms the kernel watchdog (magic-close) and releases the fd.
// Idempotent: calling Close more than once is a no-op and returns nil.
//
// CRITICAL: Close MUST run before any normal shutdown / reboot path.
// If we exit without writing 'V' on a kernel built with NOWAYOUT=y the
// kernel will keep the timer armed and reset the box mid-shutdown,
// truncating the actual reboot sequence (filesystem sync, etc.).
func (f *Feeder) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true

	// Best-effort magic-close. If this fails (closed fd, EIO on a dead
	// device) we still want to close the underlying file so we don't
	// leak the descriptor.
	var writeErr error
	if _, err := f.file.Write([]byte{magicClose}); err != nil {
		writeErr = fmt.Errorf("watchdog magic-close: %w", err)
	}

	closeErr := f.file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return fmt.Errorf("watchdog close: %w", closeErr)
	}
	return nil
}

// Device returns the resolved device path the feeder is bound to.
func (f *Feeder) Device() string { return f.cfg.Device }

// Timeout returns the configured kernel-side timeout. The actual value
// the kernel programmed may differ — see WDIOC_GETTIMEOUT — but slinit
// does not rely on a specific value, only on "ping faster than this".
func (f *Feeder) Timeout() time.Duration { return f.cfg.Timeout }

// Interval returns the ping period. Run() uses this verbatim.
func (f *Feeder) Interval() time.Duration { return f.cfg.Interval }

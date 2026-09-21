// Package eventloop implements the central event coordination for slinit,
// replacing dinit's dasynq event loop with Go-idiomatic goroutines and channels.
package eventloop

import (
	"os"
	"os/signal"
	"syscall"
)

// shutdownSignalSet is every signal that asks slinit to bring the
// system down. All of them are fatal by default in the Go runtime,
// which is why they must be claimed before any slow work — see
// SetupEarlySignals.
func shutdownSignalSet() []os.Signal {
	sigs := []os.Signal{
		syscall.SIGTERM,
		syscall.SIGINT, // also the Ctrl+Alt+Del path once CAD is off
		syscall.SIGQUIT,
		syscall.SIGHUP,
		syscall.SIGUSR1, // SysV: halt/reboot (busybox reboot)
		syscall.SIGUSR2, // SysV: poweroff (busybox poweroff)
	}
	// Linux real-time signals (systemd-compatible shutdown triggers).
	// On non-Linux platforms extraShutdownSignals() returns nil.
	for _, s := range extraShutdownSignals() {
		sigs = append(sigs, s)
	}
	return sigs
}

// SetupEarlySignals claims the shutdown signals and returns the channel
// they will arrive on. Call it as the first thing in main, and hand the
// result to EventLoop.AdoptSignals.
//
// This has to happen before any boot work, because the kernel only
// drops a signal to PID 1 when PID 1 has no handler for it — and the Go
// runtime installs handlers for everything at startup regardless. Until
// signal.Notify claims a signal, the runtime's own disposition applies,
// and for this set that disposition is "die". PID 1 dying is
// `Kernel panic - not syncing: Attempted to kill init!`.
//
// Registering inside EventLoop.Run left roughly 1600 lines of boot —
// mounts, service loading, the whole start cascade — during which
// Ctrl+Alt+Del panicked the machine instead of rebooting it. Observed
// on a rescue-mode boot, panicking at 6.3s with exitcode=0x200, which
// is the Go runtime's exit(2) on a fatal signal.
//
// SIGCHLD is deliberately NOT claimed here. It is claimed by Run, once
// there is a reader. Adding it now would let orphan reaping fill the
// 32-slot buffer during boot, and a full buffer makes the signal
// package drop sends — including the Ctrl+Alt+Del this exists to
// catch.
func SetupEarlySignals() chan os.Signal {
	sigCh := make(chan os.Signal, 32)
	signal.Notify(sigCh, shutdownSignalSet()...)
	return sigCh
}

// SetupSignals registers OS signal handlers and returns a channel
// that receives intercepted signals. The buffer is sized generously
// to avoid dropping signals under heavy SIGCHLD load (PID 1 with
// many orphan processes exiting simultaneously).
func SetupSignals() chan os.Signal {
	sigCh := make(chan os.Signal, 32)
	signal.Notify(sigCh, append(shutdownSignalSet(), syscall.SIGCHLD)...)
	return sigCh
}

// StopSignals removes all signal handlers.
func StopSignals(sigCh chan os.Signal) {
	signal.Stop(sigCh)
	close(sigCh)
}

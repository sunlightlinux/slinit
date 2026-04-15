// Package eventloop implements the central event coordination for slinit,
// replacing dinit's dasynq event loop with Go-idiomatic goroutines and channels.
package eventloop

import (
	"os"
	"os/signal"
	"syscall"
)

// SetupSignals registers OS signal handlers and returns a channel
// that receives intercepted signals. The buffer is sized generously
// to avoid dropping signals under heavy SIGCHLD load (PID 1 with
// many orphan processes exiting simultaneously).
func SetupSignals() chan os.Signal {
	sigCh := make(chan os.Signal, 32)
	sigs := []os.Signal{
		syscall.SIGTERM,
		syscall.SIGINT,
		syscall.SIGQUIT,
		syscall.SIGHUP,
		syscall.SIGUSR1, // SysV: halt/reboot (busybox reboot)
		syscall.SIGUSR2, // SysV: poweroff (busybox poweroff)
		syscall.SIGCHLD, // For PID 1 orphan reaping
	}
	// Linux real-time signals (systemd-compatible shutdown triggers).
	// On non-Linux platforms extraShutdownSignals() returns nil.
	for _, s := range extraShutdownSignals() {
		sigs = append(sigs, s)
	}
	signal.Notify(sigCh, sigs...)
	return sigCh
}

// StopSignals removes all signal handlers.
func StopSignals(sigCh chan os.Signal) {
	signal.Stop(sigCh)
	close(sigCh)
}

// Package eventloop implements the central event coordination for slinit,
// replacing dinit's dasynq event loop with Go-idiomatic goroutines and channels.
package eventloop

import (
	"os"
	"os/signal"
	"syscall"
)

// SetupSignals registers OS signal handlers and returns a channel
// that receives intercepted signals.
func SetupSignals() chan os.Signal {
	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh,
		syscall.SIGTERM,
		syscall.SIGINT,
		syscall.SIGQUIT,
		syscall.SIGHUP,
	)
	return sigCh
}

// StopSignals removes all signal handlers.
func StopSignals(sigCh chan os.Signal) {
	signal.Stop(sigCh)
	close(sigCh)
}

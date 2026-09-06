//go:build pprof

// Diagnostic pprof endpoint. Only compiled when `-tags pprof` is on
// the go build line. Production builds use pprof_stub.go which
// compiles maybeStartPprof to an empty function — the entire
// net/http + net/http/pprof surface is excluded (saves ~4 MB text
// and closes the profiling attack surface).
//
// Diagnostic build:
//
//	go build -tags 'paniconce pprof' -ldflags='-s -w' -o slinit ./cmd/slinit
//
// After hot-patching + softreboot, the socket is live at the
// hardcoded path below. From a client:
//
//	curl --unix-socket /run/slinit/pprof.sock http://x/debug/pprof/heap -o heap.pprof
//	go tool pprof heap.pprof
//
// Two-heap diff for leak hunting:
//
//	curl --unix-socket /run/slinit/pprof.sock http://x/debug/pprof/heap -o before.pprof
//	# ... induce load ...
//	curl --unix-socket /run/slinit/pprof.sock http://x/debug/pprof/heap -o after.pprof
//	go tool pprof -diff_base before.pprof after.pprof
package main

import (
	"net"
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof/* on http.DefaultServeMux
	"os"

	"github.com/sunlightlinux/slinit/pkg/logging"
)

const pprofSockPath = "/run/slinit/pprof.sock"

// maybeStartPprof exposes the standard net/http/pprof handlers on a
// Unix socket. Chmod 0600 so only the owner (root for PID 1) can
// profile. Failure to bind is logged but never fatal — a diagnostic
// feature must not affect boot.
func maybeStartPprof(logger *logging.Logger) {
	// Remove any stale socket from a previous run; ignore failure.
	_ = os.Remove(pprofSockPath)
	l, err := net.Listen("unix", pprofSockPath)
	if err != nil {
		logger.Warn("pprof: listen %s: %v", pprofSockPath, err)
		return
	}
	if err := os.Chmod(pprofSockPath, 0o600); err != nil {
		logger.Warn("pprof: chmod %s: %v", pprofSockPath, err)
	}
	go func() {
		if err := http.Serve(l, nil); err != nil {
			logger.Warn("pprof: serve exited: %v", err)
		}
	}()
	logger.Notice("pprof: listening on %s (built with -tags pprof)", pprofSockPath)
}

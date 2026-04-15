//go:build !linux

package eventloop

import (
	"syscall"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// extraShutdownSignals is a no-op on non-Linux platforms. Real-time
// signal numbering is Linux-specific and the systemd convention does
// not apply elsewhere.
func extraShutdownSignals() []syscall.Signal { return nil }

// rtShutdownType always returns ok=false on non-Linux platforms.
func rtShutdownType(_ syscall.Signal) (service.ShutdownType, string, bool) {
	return 0, "", false
}

//go:build linux

package eventloop

import (
	"syscall"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// Linux glibc reserves signals 32 and 33 for NPTL's internal use,
// so the first real-time signal available to applications is 34.
// This matches the base used by systemd for its shutdown signal
// convention — see systemd(1), "Signals accepted by PID 1".
const sigRTMin = 34

// Systemd-compatible shutdown RT signals. These allow standard tooling
// (e.g. `systemctl poweroff` from inside a container) to trigger the
// appropriate shutdown action by sending an RT signal to PID 1, without
// requiring any slinit-specific client.
//
//	SIGRTMIN+3 → halt
//	SIGRTMIN+4 → poweroff
//	SIGRTMIN+5 → reboot
//	SIGRTMIN+6 → kexec
var (
	sigHalt     = syscall.Signal(sigRTMin + 3)
	sigPoweroff = syscall.Signal(sigRTMin + 4)
	sigReboot   = syscall.Signal(sigRTMin + 5)
	sigKexec    = syscall.Signal(sigRTMin + 6)
)

// extraShutdownSignals returns the RT signals that SetupSignals should
// register with signal.Notify in addition to the classic Unix signals.
func extraShutdownSignals() []syscall.Signal {
	return []syscall.Signal{sigHalt, sigPoweroff, sigReboot, sigKexec}
}

// rtShutdownType maps a received RT signal to the corresponding
// ShutdownType and a human-readable signal name. Returns ok=false if
// sig is not one of the recognised shutdown RT signals.
func rtShutdownType(sig syscall.Signal) (st service.ShutdownType, name string, ok bool) {
	switch sig {
	case sigHalt:
		return service.ShutdownHalt, "SIGRTMIN+3", true
	case sigPoweroff:
		return service.ShutdownPoweroff, "SIGRTMIN+4", true
	case sigReboot:
		return service.ShutdownReboot, "SIGRTMIN+5", true
	case sigKexec:
		return service.ShutdownKexec, "SIGRTMIN+6", true
	}
	return 0, "", false
}

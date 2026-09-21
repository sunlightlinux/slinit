package eventloop

import (
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// Rescue/emergency mode loads no boot services, so an empty service set
// is the steady state rather than a collapse. checkInactive used to
// call it a boot failure and return true, which exited Run the instant
// rescue mode started. Run's deferred StopSignals then closed the
// signal channel, so Ctrl+Alt+Del — which arrives as SIGINT once
// InitPID1 has disabled CAD — was delivered to a process that no longer
// read signals, and did nothing at all. The collapse prompt it printed
// also competed with the rescue shell for /dev/console.
func TestRescueModeEmptyServiceSetIsNotACollapse(t *testing.T) {
	logger := logging.New(logging.LevelError)

	el := New(service.NewServiceSet(logger), logger)
	el.SetPID1Mode(true)
	el.SetRescueMode(true)

	if el.checkInactive() {
		t.Error("rescue mode treated an empty service set as a boot failure; " +
			"Run would exit and stop handling signals")
	}
}

// The same emptiness outside rescue mode IS a collapse, or the guard
// above would have disabled boot-failure detection everywhere.
func TestEmptyServiceSetIsStillACollapseOutsideRescue(t *testing.T) {
	logger := logging.New(logging.LevelError)

	el := New(service.NewServiceSet(logger), logger)
	el.SetPID1Mode(true)

	if !el.checkInactive() {
		t.Error("a normal PID 1 boot with no active services should be " +
			"detected as a boot failure")
	}
}

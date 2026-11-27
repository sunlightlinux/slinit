package eventloop

import (
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// onStart runs for the request that starts the shutdown, and not for one
// arriving while it is under way: a failure-action's reboot-argument must
// not ride on an operator's reboot already in progress.
func TestInitiateShutdownWithRunsOnlyForTheStarter(t *testing.T) {
	logger := logging.New(logging.LevelError)
	set := service.NewServiceSet(logger)
	el := New(set, logger)
	first, second := 0, 0
	el.InitiateShutdownWith(service.ShutdownReboot, func() { first++ })
	el.InitiateShutdownWith(service.ShutdownReboot, func() { second++ })
	if first != 1 || second != 0 {
		t.Fatalf("onStart ran %d and %d times, want 1 and 0", first, second)
	}
}

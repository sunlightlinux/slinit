package eventloop

import (
	"context"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// A boot service that dies before the event loop starts must still be
// noticed. main starts the boot cascade and only then calls Run, so a
// command that exits at once — a typo, a missing binary, a container
// workload that finishes instantly — goes inactive in that gap.
//
// The notification used to be dropped: the service set only created its
// inactive channel when Run first asked for it, and ServiceInactive
// skipped the send while the channel was nil. Run then waited forever
// for an event that had already happened. As PID 1 that is a machine
// stuck with no recovery menu; in a container it is `slinit -o` that
// never exits, found by tests/container case 02 with `exit 7`.
func TestBootServiceDyingBeforeRunIsStillABootFailure(t *testing.T) {
	logger := logging.New(logging.LevelError)
	set := service.NewServiceSet(logger)

	// What the boot cascade does for a service that starts and exits
	// before Run: active, then inactive, with nobody listening yet.
	set.ServiceActive(nil)
	set.ServiceInactive(nil)

	el := New(set, logger)
	el.SetPID1Mode(true)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- el.Run(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v; want nil (boot failure detected)", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run never returned: the pre-Run inactive notification was lost")
	}
}

package eventloop

import (
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestEscalateShutdown_Levels(t *testing.T) {
	logger := logging.New(logging.LevelDebug)
	set := service.NewServiceSet(logger)
	el := New(set, logger)

	// Simulate that shutdown was already initiated
	el.shutdownSignals.Store(1)
	el.shutdownInitiated = true

	// First escalation (count becomes 2): reduces timer
	result := el.escalateShutdown("SIGINT")
	if !result {
		t.Error("expected escalateShutdown to return true")
	}
	if el.shutdownSignals.Load() != 2 {
		t.Errorf("expected signal count 2, got %d", el.shutdownSignals.Load())
	}

	// Second escalation (count becomes 3): should send to forceExitCh
	result = el.escalateShutdown("SIGINT")
	if !result {
		t.Error("expected escalateShutdown to return true")
	}
	// forceExitCh should have a message
	select {
	case <-el.forceExitCh:
		// expected
	default:
		t.Error("expected force exit signal on 3rd escalation")
	}
}

func TestShutdownReporter_StartStop(t *testing.T) {
	logger := logging.New(logging.LevelDebug)
	set := service.NewServiceSet(logger)
	el := New(set, logger)

	// Start and immediately stop - should not panic
	el.startShutdownReporter()
	el.stopShutdownReporter()

	// Double stop should be safe
	el.stopShutdownReporter()
}

func TestLogBlockingServices_NoServices(t *testing.T) {
	logger := logging.New(logging.LevelDebug)
	set := service.NewServiceSet(logger)
	el := New(set, logger)

	// Should not panic with empty set
	el.logBlockingServices()
}

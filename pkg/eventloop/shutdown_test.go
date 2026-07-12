package eventloop

import (
	"strings"
	"testing"
	"time"

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

// TestFormatBlockingServices ensures the emergency-force-exit suffix
// is empty when nothing is blocking (so the log line stays clean on
// the healthy shutdown-not-timing-out path).
func TestFormatBlockingServices_Empty(t *testing.T) {
	got := formatBlockingServices(nil)
	if got != "" {
		t.Errorf("empty active list: expected empty suffix, got %q", got)
	}
	got = formatBlockingServices([]service.ActiveServiceInfo{})
	if got != "" {
		t.Errorf("zero-length active list: expected empty suffix, got %q", got)
	}
}

// TestFormatBlockingServices_WithEntries verifies operator-visible
// wording: the suffix opens with "; still blocking:" and lists every
// active service with its state (and PID when > 0).
func TestFormatBlockingServices_WithEntries(t *testing.T) {
	active := []service.ActiveServiceInfo{
		{Name: "docker", State: service.StateStopping, PID: 1234},
		{Name: "elogind", State: service.StateStopping, PID: 5678},
		// PID=0 → suppressed (no ", pid 0" noise for scripted services
		// or services in transition without a live PID).
		{Name: "boot", State: service.StateStopped, PID: 0},
	}
	got := formatBlockingServices(active)
	if !strings.HasPrefix(got, "; still blocking: ") {
		t.Errorf("prefix mismatch: %q", got)
	}
	for _, want := range []string{
		"docker (STOPPING, pid 1234)",
		"elogind (STOPPING, pid 5678)",
		"boot (STOPPED)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	// PID=0 rendered without a ", pid 0" segment.
	if strings.Contains(got, "pid 0") {
		t.Errorf("PID=0 leaked as 'pid 0' in %q", got)
	}
}

// TestSetEmergencyTimeout_Positive: a positive override is used verbatim.
func TestSetEmergencyTimeout_Positive(t *testing.T) {
	logger := logging.New(logging.LevelDebug)
	set := service.NewServiceSet(logger)
	el := New(set, logger)

	el.SetEmergencyTimeout(30 * time.Second)
	if got := el.effectiveEmergencyTimeout(); got != 30*time.Second {
		t.Errorf("expected 30s, got %v", got)
	}
}

// TestSetEmergencyTimeout_ZeroFallsBack: zero (the flag's zero-value
// on daemon start) uses the compile-time default. Negatives are
// treated the same as zero rather than propagating a bogus timeout
// that would fire immediately.
func TestSetEmergencyTimeout_ZeroFallsBack(t *testing.T) {
	logger := logging.New(logging.LevelDebug)
	set := service.NewServiceSet(logger)
	el := New(set, logger)

	if got := el.effectiveEmergencyTimeout(); got != defaultEmergencyTimeout {
		t.Errorf("unset: expected default %v, got %v",
			defaultEmergencyTimeout, got)
	}

	el.SetEmergencyTimeout(-1 * time.Second)
	if got := el.effectiveEmergencyTimeout(); got != defaultEmergencyTimeout {
		t.Errorf("negative: expected default %v, got %v",
			defaultEmergencyTimeout, got)
	}
}

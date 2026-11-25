package eventloop

import (
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// The emergency timer used to be armed before OnPreShutdown, which runs
// the system-down hooks (30s per script, any number of them) and ifdown
// (90s). Slow ones spent the whole budget while every service was still
// STARTED, and the timer fired before StopAllServices had asked a single
// service to stop: the services got no graceful stop at all.
func TestSlowPreShutdownDoesNotEatTheStopBudget(t *testing.T) {
	logger := logging.New(logging.LevelError)
	set := service.NewServiceSet(logger)
	set.ServiceActive(nil) // one service still up, as on a real box
	el := New(set, logger)
	el.SetEmergencyTimeout(200 * time.Millisecond)
	el.OnPreShutdown = func(service.ShutdownType) {
		time.Sleep(400 * time.Millisecond)
	}

	el.InitiateShutdown(service.ShutdownReboot)
	defer el.cancelEmergencyTimer()

	select {
	case <-el.forceExitCh:
		t.Fatal("forceExitCh already signalled when StopAllServices began: " +
			"the pre-shutdown hooks spent the services' stop budget")
	default:
	}

	// The stop phase is still guarded.
	select {
	case <-el.forceExitCh:
	case <-time.After(2 * time.Second):
		t.Fatal("emergency timer never fired for the stop phase")
	}
}

// A system-down hook that never finishes must not hold the shutdown
// for ever: services are stopped once the pre-shutdown budget is spent.
func TestHungPreShutdownIsCapped(t *testing.T) {
	logger := logging.New(logging.LevelError)
	set := service.NewServiceSet(logger)
	set.ServiceActive(nil)
	el := New(set, logger)
	el.SetEmergencyTimeout(100 * time.Millisecond)
	release := make(chan struct{})
	defer close(release)
	el.OnPreShutdown = func(service.ShutdownType) { <-release }

	returned := make(chan struct{})
	go func() {
		el.InitiateShutdown(service.ShutdownReboot)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("InitiateShutdown waited for a hung pre-shutdown hook; " +
			"StopAllServices never ran")
	}
	el.cancelEmergencyTimer()
}

// A repeated shutdown signal while the hooks run arms the escalated,
// shorter timer. Arming the stop-phase timer afterwards must not put
// the full timeout back.
func TestEscalationDuringPreShutdownKeepsShorterTimer(t *testing.T) {
	logger := logging.New(logging.LevelError)
	set := service.NewServiceSet(logger)
	set.ServiceActive(nil)
	el := New(set, logger)
	el.SetEmergencyTimeout(2 * time.Second)
	el.OnPreShutdown = func(service.ShutdownType) {
		el.escalateShutdown("SIGTERM") // timeout/4 = 500ms
	}

	start := time.Now()
	el.InitiateShutdown(service.ShutdownReboot)
	defer el.cancelEmergencyTimer()
	select {
	case <-el.forceExitCh:
		if d := time.Since(start); d > 1500*time.Millisecond {
			t.Errorf("forced exit after %v; the escalated 500ms timer was replaced", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("emergency timer never fired")
	}
}

// A panicking hook is logged; the shutdown goes on to stop services.
func TestPanickingPreShutdownStillStopsServices(t *testing.T) {
	logger := logging.New(logging.LevelError)
	set := service.NewServiceSet(logger)
	set.ServiceActive(nil)
	el := New(set, logger)
	el.SetEmergencyTimeout(time.Second)
	el.OnPreShutdown = func(service.ShutdownType) { panic("boom") }

	el.InitiateShutdown(service.ShutdownReboot)
	defer el.cancelEmergencyTimer()
	if !set.IsShuttingDown() {
		t.Error("StopAllServices did not run after the hook panicked")
	}
}

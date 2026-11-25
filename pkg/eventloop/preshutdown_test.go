package eventloop

import (
	"context"
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

// After a remain PID 1 runs on with a new loop. The old one must not
// leave its emergency timer or blocker reporter behind: the timer logged
// a forced shutdown 90s into a system that was staying up. The shutdown
// below takes the empty-set path, which returns Run through forceExitCh.
func TestRunLeavesNoShutdownTimersBehind(t *testing.T) {
	logger := logging.New(logging.LevelError)
	el := New(service.NewServiceSet(logger), logger)
	el.SetPID1Mode(true)

	done := make(chan error, 1)
	go func() { done <- el.Run(context.Background()) }()
	el.InitiateShutdown(service.ShutdownRemain)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after a shutdown with nothing running")
	}

	el.mu.Lock()
	defer el.mu.Unlock()
	if el.emergencyTimer != nil {
		el.emergencyTimer.Stop()
		t.Error("the emergency timer outlived Run")
	}
	if el.shutdownReporterStop != nil {
		close(el.shutdownReporterStop)
		t.Error("the blocker reporter outlived Run")
	}
}

package service

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// HealthChecker runs periodic health checks on a STARTED service.
// Unlike ready-check-command (which polls only during startup), the health
// checker runs continuously while the service is STARTED.
//
// On failure:
//   - The unhealthy callback command is executed (if configured).
//   - After maxFailures consecutive failures, the service is restarted
//     (by signaling the parent to stop, letting auto-restart handle it).
type HealthChecker struct {
	command     []string      // health check command (exit 0 = healthy)
	interval    time.Duration // time between checks
	delay       time.Duration // initial delay before first check
	maxFailures int           // consecutive failures before restart (0 = never restart)
	unhealthyCmd []string     // command to run on each failure

	svc    Service
	logger ServiceLogger
	onFail func() // called when maxFailures reached (triggers service restart)

	mu         sync.Mutex
	failures   int           // consecutive failure count
	stopCh     chan struct{}
	doneCh     chan struct{}
}

// NewHealthChecker creates a new health checker.
func NewHealthChecker(svc Service, cmd []string, interval, delay time.Duration,
	maxFailures int, unhealthyCmd []string, logger ServiceLogger, onFail func()) *HealthChecker {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if maxFailures < 0 {
		maxFailures = 0
	}
	return &HealthChecker{
		command:      cmd,
		interval:     interval,
		delay:        delay,
		maxFailures:  maxFailures,
		unhealthyCmd: unhealthyCmd,
		svc:          svc,
		logger:       logger,
		onFail:       onFail,
	}
}

// Start launches the periodic health check goroutine.
func (hc *HealthChecker) Start() {
	hc.mu.Lock()
	if hc.stopCh != nil {
		hc.mu.Unlock()
		return
	}
	hc.stopCh = make(chan struct{})
	hc.doneCh = make(chan struct{})
	hc.failures = 0
	hc.mu.Unlock()

	go hc.loop()
}

// Stop signals the health check loop to exit and waits for completion.
func (hc *HealthChecker) Stop() {
	hc.mu.Lock()
	if hc.stopCh == nil {
		hc.mu.Unlock()
		return
	}
	select {
	case <-hc.stopCh:
	default:
		close(hc.stopCh)
	}
	doneCh := hc.doneCh
	hc.mu.Unlock()

	if doneCh != nil {
		<-doneCh
	}
}

// ConsecutiveFailures returns the current consecutive failure count.
func (hc *HealthChecker) ConsecutiveFailures() int {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	return hc.failures
}

func (hc *HealthChecker) loop() {
	defer close(hc.doneCh)

	// Initial delay
	if hc.delay > 0 {
		select {
		case <-time.After(hc.delay):
		case <-hc.stopCh:
			return
		}
	}

	// First check
	if !hc.checkOnce() {
		return
	}

	ticker := time.NewTicker(hc.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if !hc.checkOnce() {
				return
			}
		case <-hc.stopCh:
			return
		}
	}
}

// checkOnce runs the health check command once.
// Returns false if the loop should exit (stop requested or max failures reached).
func (hc *HealthChecker) checkOnce() bool {
	select {
	case <-hc.stopCh:
		return false
	default:
	}

	ctx, cancel := context.WithTimeout(context.Background(), hc.interval)
	defer cancel()

	cmd := exec.CommandContext(ctx, hc.command[0], hc.command[1:]...)
	err := cmd.Run()

	if err == nil {
		// Healthy — reset failure counter
		hc.mu.Lock()
		if hc.failures > 0 {
			hc.logger.Info("Service '%s': health check passed (recovered after %d failures)",
				hc.svc.Name(), hc.failures)
		}
		hc.failures = 0
		hc.mu.Unlock()
		return true
	}

	// Unhealthy
	hc.mu.Lock()
	hc.failures++
	failures := hc.failures
	hc.mu.Unlock()

	hc.logger.Info("Service '%s': health check failed (%d/%s): %v",
		hc.svc.Name(), failures, hc.maxFailuresStr(), err)

	// Run unhealthy callback (best-effort, don't block long)
	hc.runUnhealthyCmd()

	// Check if we've reached max failures
	if hc.maxFailures > 0 && failures >= hc.maxFailures {
		hc.logger.Error("Service '%s': health check failed %d consecutive times, triggering restart",
			hc.svc.Name(), failures)
		if hc.onFail != nil {
			hc.onFail()
		}
		return false // stop checking — service will restart
	}

	return true
}

func (hc *HealthChecker) maxFailuresStr() string {
	if hc.maxFailures <= 0 {
		return "∞"
	}
	return fmt.Sprintf("%d", hc.maxFailures)
}

// runUnhealthyCmd executes the unhealthy callback command.
func (hc *HealthChecker) runUnhealthyCmd() {
	if len(hc.unhealthyCmd) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, hc.unhealthyCmd[0], hc.unhealthyCmd[1:]...)
	if err := cmd.Run(); err != nil {
		hc.logger.Info("Service '%s': unhealthy-command failed: %v", hc.svc.Name(), err)
	}
}

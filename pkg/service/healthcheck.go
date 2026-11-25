package service

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
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
	command      []string      // health check command (exit 0 = healthy)
	interval     time.Duration // time between checks
	delay        time.Duration // initial delay before first check
	maxFailures  int           // consecutive failures before restart (0 = never restart)
	unhealthyCmd []string      // command to run on each failure

	svc    Service
	logger ServiceLogger
	onFail func() // called when maxFailures reached (triggers service restart)

	mu       sync.Mutex
	failures int // consecutive failure count
	// cancel ends the loop and kills a check under way; doneCh is
	// closed once the loop has exited. Both are nil while none runs.
	cancel context.CancelFunc
	doneCh chan struct{}
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
	if hc.cancel != nil {
		hc.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	hc.cancel = cancel
	hc.doneCh = done
	hc.failures = 0
	hc.mu.Unlock()

	go hc.loop(ctx, done)
}

// running reports whether the checker's loop is running.
func (hc *HealthChecker) running() bool {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	return hc.cancel != nil
}

// Stop ends the health check loop and waits for it to exit. A check
// under way is killed, not waited for: BringDown calls Stop with queueMu
// held, and a check may run for its whole interval.
func (hc *HealthChecker) Stop() {
	hc.mu.Lock()
	cancel, done := hc.cancel, hc.doneCh
	hc.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// ConsecutiveFailures returns the current consecutive failure count.
func (hc *HealthChecker) ConsecutiveFailures() int {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	return hc.failures
}

func (hc *HealthChecker) loop(ctx context.Context, done chan struct{}) {
	defer func() {
		// Reset, so the next Start (after a restart of the service)
		// runs again. Here rather than in Stop: a loop that ended at
		// its failure threshold had no Stop to do it, and every later
		// Start found it still marked running.
		hc.mu.Lock()
		if hc.doneCh == done {
			hc.cancel()
			hc.cancel = nil
			hc.doneCh = nil
		}
		hc.mu.Unlock()
		close(done)
	}()

	// Initial delay
	if hc.delay > 0 {
		select {
		case <-time.After(hc.delay):
		case <-ctx.Done():
			return
		}
	}

	// First check
	if !hc.checkOnce(ctx) {
		return
	}

	ticker := time.NewTicker(hc.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if !hc.checkOnce(ctx) {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// checkOnce runs the health check command once.
// Returns false if the loop should exit (stop requested or max failures reached).
func (hc *HealthChecker) checkOnce(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}

	checkCtx, cancel := context.WithTimeout(ctx, hc.interval)
	defer cancel()

	cmd := exec.CommandContext(checkCtx, hc.command[0], hc.command[1:]...)
	err := process.RunAdhoc(cmd)
	if ctx.Err() != nil {
		return false // stopped: the check was cut short, not failed
	}

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
	hc.runUnhealthyCmd(ctx)

	// Check if we've reached max failures
	if hc.maxFailures > 0 && failures >= hc.maxFailures {
		hc.logger.Error("Service '%s': health check failed %d consecutive times",
			hc.svc.Name(), failures)
		if hc.onFail != nil {
			hc.onFail()
		}
		return false // stop checking — the service is stopped, and restarted per its policy
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
func (hc *HealthChecker) runUnhealthyCmd(ctx context.Context) {
	if len(hc.unhealthyCmd) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, hc.unhealthyCmd[0], hc.unhealthyCmd[1:]...)
	if err := process.RunAdhoc(cmd); err != nil {
		hc.logger.Info("Service '%s': unhealthy-command failed: %v", hc.svc.Name(), err)
	}
}

package service

import (
	"context"
	"math/rand"
	"os/exec"
	"sync"
	"time"
)

// CronRunner manages a periodic sub-task for a service.
// When the parent service reaches STARTED, Start() is called to begin
// the periodic execution. Stop() blocks until any in-progress execution
// completes, then returns.
//
// Two scheduling modes:
//
//   - Interval mode (default): runs every `interval` duration.
//   - Calendar mode: when `calendar != nil`, fire times are derived from
//     a systemd-style OnCalendar expression. `interval` is unused.
//
// Optional modifiers:
//
//   - randomizedDelay: jitter added to each fire time, uniform [0, d).
//   - persistent: on startup, if lastRun is set and a fire time was
//     missed (lastRun > 0 < now), run once immediately to catch up
//     before resuming the schedule. Currently the persistence store is
//     in-memory (per-process); a future on-disk store would survive
//     daemon restarts.
type CronRunner struct {
	command         []string
	interval        time.Duration
	delay           time.Duration
	onError         string // "continue" (default) or "stop"
	calendar        *CalendarSpec
	randomizedDelay time.Duration
	persistent      bool
	lastRun         time.Time

	svc    Service // parent service (for logging context)
	logger ServiceLogger

	mu      sync.Mutex
	running bool          // true while a cron-command execution is in progress
	stopCh  chan struct{} // closed to signal the cron loop to exit
	doneCh  chan struct{} // closed when the cron loop has fully exited
}

// NewCronRunner creates a new CronRunner in interval mode.
func NewCronRunner(svc Service, cmd []string, interval, delay time.Duration, onError string, logger ServiceLogger) *CronRunner {
	if onError == "" {
		onError = "continue"
	}
	if interval <= 0 {
		interval = 60 * time.Second
	}
	return &CronRunner{
		command:  cmd,
		interval: interval,
		delay:    delay,
		onError:  onError,
		svc:      svc,
		logger:   logger,
	}
}

// NewCalendarCronRunner creates a new CronRunner in calendar mode. The
// command is invoked at every instant matching `calendar`. Optional
// `randomizedDelay` (>=0) adds uniform jitter to each fire time;
// `persistent` enables catch-up on startup when a fire was missed.
func NewCalendarCronRunner(
	svc Service, cmd []string, calendar *CalendarSpec,
	randomizedDelay time.Duration, persistent bool,
	onError string, logger ServiceLogger,
) *CronRunner {
	if onError == "" {
		onError = "continue"
	}
	return &CronRunner{
		command:         cmd,
		calendar:        calendar,
		randomizedDelay: randomizedDelay,
		persistent:      persistent,
		onError:         onError,
		svc:             svc,
		logger:          logger,
	}
}

// Start launches the periodic execution goroutine.
// Must only be called once. Safe to call from any goroutine.
func (cr *CronRunner) Start() {
	cr.mu.Lock()
	if cr.stopCh != nil {
		cr.mu.Unlock()
		return // already running
	}
	cr.stopCh = make(chan struct{})
	cr.doneCh = make(chan struct{})
	cr.mu.Unlock()

	go cr.loop()
}

// Stop signals the cron loop to exit and waits for any in-progress
// execution to complete. Safe to call multiple times.
func (cr *CronRunner) Stop() {
	cr.mu.Lock()
	if cr.stopCh == nil {
		cr.mu.Unlock()
		return
	}
	select {
	case <-cr.stopCh:
		// already stopped
	default:
		close(cr.stopCh)
	}
	doneCh := cr.doneCh
	cr.mu.Unlock()

	if doneCh != nil {
		<-doneCh
	}
}

// IsRunning returns true if a cron-command is currently executing.
func (cr *CronRunner) IsRunning() bool {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	return cr.running
}

func (cr *CronRunner) loop() {
	defer close(cr.doneCh)

	if cr.calendar != nil {
		cr.loopCalendar()
		return
	}
	cr.loopInterval()
}

// loopInterval drives the original "every N seconds" schedule.
func (cr *CronRunner) loopInterval() {
	if cr.delay > 0 {
		select {
		case <-time.After(cr.delay):
		case <-cr.stopCh:
			return
		}
	}

	if !cr.runOnce() {
		return
	}

	ticker := time.NewTicker(cr.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if !cr.runOnce() {
				return
			}
		case <-cr.stopCh:
			return
		}
	}
}

// loopCalendar computes successive fire times from the CalendarSpec.
// On startup, if persistent and lastRun indicates a missed fire, runs
// once immediately to catch up; otherwise sleeps until the next match.
// Random jitter (if configured) is added between fire times.
func (cr *CronRunner) loopCalendar() {
	now := time.Now()
	// Catch-up: if persistent and the next scheduled fire after lastRun
	// is in the past, run now once before resuming.
	if cr.persistent && !cr.lastRun.IsZero() {
		nextMissed := cr.calendar.NextAfter(cr.lastRun)
		if !nextMissed.IsZero() && nextMissed.Before(now) {
			cr.logger.Info(
				"Service '%s': calendar catch-up (missed fire at %v)",
				cr.svc.Name(), nextMissed)
			if !cr.runOnce() {
				return
			}
		}
	}

	for {
		now = time.Now()
		next := cr.calendar.NextAfter(now)
		if next.IsZero() {
			// Spec has no future match — exit quietly.
			return
		}
		// Apply jitter so a fleet of machines doesn't herd onto the same
		// fire time. Uniform [0, randomizedDelay).
		if cr.randomizedDelay > 0 {
			next = next.Add(time.Duration(rand.Int63n(int64(cr.randomizedDelay))))
		}
		delay := time.Until(next)
		if delay < 0 {
			delay = 0
		}
		select {
		case <-time.After(delay):
		case <-cr.stopCh:
			return
		}
		cr.lastRun = next
		if !cr.runOnce() {
			return
		}
	}
}

// runOnce executes the cron command once. Returns false if the loop
// should exit (stop requested or on-error=stop and command failed).
func (cr *CronRunner) runOnce() bool {
	// Check stop before starting
	select {
	case <-cr.stopCh:
		return false
	default:
	}

	cr.mu.Lock()
	cr.running = true
	cr.mu.Unlock()

	err := cr.executeCommand()

	cr.mu.Lock()
	cr.running = false
	cr.mu.Unlock()

	if err != nil {
		cr.logger.Error("cron-command for '%s' failed: %v", cr.svc.Name(), err)
		if cr.onError == "stop" {
			return false
		}
	}

	return true
}

// executeCommand runs the cron command with a timeout matching the interval.
func (cr *CronRunner) executeCommand() error {
	if len(cr.command) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), cr.interval)
	defer cancel()

	cmd := exec.CommandContext(ctx, cr.command[0], cr.command[1:]...)
	return cmd.Run()
}

package service

import (
	"context"
	"os/exec"
	"sync"
	"time"
)

// CronRunner manages a periodic sub-task for a service.
// When the parent service reaches STARTED, Start() is called to begin
// the periodic execution. Stop() blocks until any in-progress execution
// completes, then returns.
type CronRunner struct {
	command  []string
	interval time.Duration
	delay    time.Duration
	onError  string // "continue" (default) or "stop"

	svc    Service // parent service (for logging context)
	logger ServiceLogger

	mu      sync.Mutex
	running bool       // true while a cron-command execution is in progress
	stopCh  chan struct{} // closed to signal the cron loop to exit
	doneCh  chan struct{} // closed when the cron loop has fully exited
}

// NewCronRunner creates a new CronRunner.
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

	// Initial delay
	if cr.delay > 0 {
		select {
		case <-time.After(cr.delay):
		case <-cr.stopCh:
			return
		}
	}

	// Run once immediately, then on interval
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

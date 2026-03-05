package eventloop

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// Default emergency shutdown timeout.
const defaultEmergencyTimeout = 90 * time.Second

// EventLoop is the central event coordinator for slinit.
// It replaces dasynq's epoll-based event loop with Go channels and select.
type EventLoop struct {
	services *service.ServiceSet
	logger   *logging.Logger
	sigCh    chan os.Signal

	// Shutdown state, protected by mu for concurrent access from
	// signal handler goroutine and control socket goroutines.
	mu                sync.Mutex
	shutdownInitiated bool
	shutdownType      service.ShutdownType
	emergencyTimer    *time.Timer

	// Atomic counter for repeated shutdown signals (escalation).
	shutdownSignals atomic.Int32

	// PID 1 mode enables boot failure detection and orphan reaping
	isPID1 bool

	// Container mode: SIGINT/SIGTERM trigger graceful halt instead of reboot
	isContainer bool

	// Channel for forcing event loop exit (emergency timeout)
	forceExitCh chan struct{}

	// Callback for when all services have stopped
	OnAllStopped func()

	// OnReopenSocket is called on SIGUSR1 to reopen the control socket
	OnReopenSocket func()
}

// New creates a new EventLoop.
func New(services *service.ServiceSet, logger *logging.Logger) *EventLoop {
	return &EventLoop{
		services:    services,
		logger:      logger,
		forceExitCh: make(chan struct{}, 1),
	}
}

// SetPID1Mode enables PID 1 specific behavior:
// - Boot failure detection when all services stop without explicit shutdown
// - Orphan process reaping on SIGCHLD
func (el *EventLoop) SetPID1Mode(v bool) {
	el.isPID1 = v
}

// SetContainerMode enables container-specific behavior:
// - SIGINT/SIGTERM trigger graceful halt instead of reboot
// - Boot failure detection (same as PID 1)
func (el *EventLoop) SetContainerMode(v bool) {
	el.isContainer = v
}

// GetShutdownType returns the shutdown type that was requested.
// The caller uses this to determine the appropriate system action
// (reboot, halt, poweroff, soft-reboot, etc.) after Run() returns.
func (el *EventLoop) GetShutdownType() service.ShutdownType {
	el.mu.Lock()
	defer el.mu.Unlock()
	return el.shutdownType
}

// Run starts the event loop. It blocks until the context is cancelled,
// a shutdown signal is received and all services stop, or an emergency
// timeout forces exit.
func (el *EventLoop) Run(ctx context.Context) error {
	el.sigCh = SetupSignals()
	defer StopSignals(el.sigCh)

	el.logger.Info("slinit event loop started (PID %d)", os.Getpid())

	inactiveCh := el.services.InactiveCh()

	for {
		select {
		case <-ctx.Done():
			el.logger.Info("Context cancelled, shutting down")
			el.cancelEmergencyTimer()
			return ctx.Err()

		case <-el.forceExitCh:
			el.logger.Error("Emergency shutdown timeout reached, forcing exit")
			return nil

		case sig := <-el.sigCh:
			if el.handleSignal(sig) {
				if el.services.CountActiveServices() == 0 {
					el.logger.Info("All services stopped, exiting")
					el.cancelEmergencyTimer()
					return nil
				}
			}

		case <-inactiveCh:
			if el.checkInactive() {
				el.cancelEmergencyTimer()
				return nil
			}
		}
	}
}

// checkInactive evaluates whether the event loop should exit after a service
// became inactive. Returns true if the loop should terminate.
func (el *EventLoop) checkInactive() bool {
	if el.services.CountActiveServices() != 0 {
		return false
	}

	el.mu.Lock()
	shutting := el.shutdownInitiated
	el.mu.Unlock()

	if shutting {
		el.logger.Info("All services stopped, exiting")
		if el.OnAllStopped != nil {
			el.OnAllStopped()
		}
		return true
	}

	// Boot failure: all services stopped without explicit shutdown (PID 1 only)
	if el.isPID1 {
		el.logger.Error("All services stopped without shutdown — boot failure")
		return true
	}

	return false
}

// handleSignal processes an OS signal. Returns true if shutdown was initiated.
func (el *EventLoop) handleSignal(sig os.Signal) bool {
	sysSignal, ok := sig.(syscall.Signal)
	if !ok {
		return false
	}

	switch sysSignal {
	case syscall.SIGTERM:
		if el.isShuttingDown() {
			return el.escalateShutdown("SIGTERM")
		}
		if el.isContainer {
			el.logger.Notice("Received SIGTERM, initiating graceful halt (container mode)")
			el.initiateShutdown(service.ShutdownHalt)
		} else if el.isPID1 {
			el.logger.Notice("Received SIGTERM, initiating reboot")
			el.initiateShutdown(service.ShutdownReboot)
		} else {
			el.logger.Notice("Received SIGTERM, initiating shutdown")
			el.initiateShutdown(service.ShutdownHalt)
		}
		return true

	case syscall.SIGINT:
		if el.isShuttingDown() {
			return el.escalateShutdown("SIGINT")
		}
		if el.isContainer {
			el.logger.Notice("Received SIGINT, initiating graceful halt (container mode)")
			el.initiateShutdown(service.ShutdownHalt)
		} else if el.isPID1 {
			el.logger.Notice("Received SIGINT, initiating reboot")
			el.initiateShutdown(service.ShutdownReboot)
		} else {
			el.logger.Notice("Received SIGINT, initiating shutdown")
			el.initiateShutdown(service.ShutdownHalt)
		}
		return true

	case syscall.SIGQUIT:
		el.logger.Notice("Received SIGQUIT, initiating poweroff")
		el.initiateShutdown(service.ShutdownPoweroff)
		return true

	case syscall.SIGUSR1:
		el.logger.Notice("Received SIGUSR1, reopening control socket")
		if el.OnReopenSocket != nil {
			el.OnReopenSocket()
		}
		return false

	case syscall.SIGUSR2:
		el.logger.Notice("Received SIGUSR2, initiating poweroff")
		el.initiateShutdown(service.ShutdownPoweroff)
		return true

	case syscall.SIGHUP:
		el.logger.Notice("Received SIGHUP")
		return false

	case syscall.SIGCHLD:
		el.reapOrphans()
		return false
	}

	return false
}

// reapOrphans collects zombie orphan processes (PID 1 only).
//
// Each managed service child gets its own process group (Setpgid), and
// group members are reaped directly in handleChildExit via
// KillProcessGroup(-pgid). This loop catches remaining orphans:
// double-forked daemons, setsid'd children, etc.
//
// There is a small race with os/exec's internal Wait4(pid): if we reap
// a managed child here, the goroutine gets ECHILD and reports status=0.
// In practice Go's runtime reaps managed children before this handler
// runs, so the race is extremely unlikely. The trade-off (no zombie
// accumulation) is worth it for PID 1.
func (el *EventLoop) reapOrphans() {
	if !el.isPID1 {
		return
	}
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			break
		}
		el.logger.Debug("Reaped orphan process %d (status: %v)", pid, status)
	}
}

// escalateShutdown handles repeated shutdown signals during an ongoing
// shutdown. The second signal halves the emergency timeout; the third
// forces immediate exit. Returns true (shutdown already in progress).
func (el *EventLoop) escalateShutdown(sigName string) bool {
	count := el.shutdownSignals.Add(1)
	switch {
	case count == 2:
		el.logger.Notice("Received %s again, halving emergency timeout", sigName)
		el.resetEmergencyTimer(defaultEmergencyTimeout / 4)
	case count >= 3:
		el.logger.Error("Received %s a third time, forcing immediate exit", sigName)
		select {
		case el.forceExitCh <- struct{}{}:
		default:
		}
	}
	return true
}

// isShuttingDown returns the current shutdown state (thread-safe).
func (el *EventLoop) isShuttingDown() bool {
	el.mu.Lock()
	defer el.mu.Unlock()
	return el.shutdownInitiated
}

// InitiateShutdown triggers a shutdown from outside the event loop
// (e.g., control socket). Safe for concurrent use.
func (el *EventLoop) InitiateShutdown(shutdownType service.ShutdownType) {
	el.initiateShutdown(shutdownType)
}

func (el *EventLoop) initiateShutdown(shutdownType service.ShutdownType) {
	el.mu.Lock()
	if el.shutdownInitiated {
		el.mu.Unlock()
		return
	}
	el.shutdownInitiated = true
	el.shutdownType = shutdownType
	el.shutdownSignals.Store(1)

	// Start emergency timeout with a cancellable timer
	el.emergencyTimer = time.AfterFunc(defaultEmergencyTimeout, func() {
		el.logger.Error("Services did not stop within %v, forcing shutdown", defaultEmergencyTimeout)
		select {
		case el.forceExitCh <- struct{}{}:
		default:
		}
	})
	el.mu.Unlock()

	el.services.StopAllServices(shutdownType)
}

// cancelEmergencyTimer stops the emergency timer if it's running.
func (el *EventLoop) cancelEmergencyTimer() {
	el.mu.Lock()
	defer el.mu.Unlock()
	if el.emergencyTimer != nil {
		el.emergencyTimer.Stop()
		el.emergencyTimer = nil
	}
}

// resetEmergencyTimer replaces the emergency timer with a shorter duration.
func (el *EventLoop) resetEmergencyTimer(d time.Duration) {
	el.mu.Lock()
	defer el.mu.Unlock()
	if el.emergencyTimer != nil {
		el.emergencyTimer.Stop()
	}
	el.emergencyTimer = time.AfterFunc(d, func() {
		el.logger.Error("Escalated emergency timeout reached, forcing shutdown")
		select {
		case el.forceExitCh <- struct{}{}:
		default:
		}
	})
}

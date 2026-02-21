package eventloop

import (
	"context"
	"os"
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

	// Set to true when shutdown is initiated
	shutdownInitiated bool

	// The type of shutdown requested
	shutdownType service.ShutdownType

	// PID 1 mode enables boot failure detection and orphan reaping
	isPID1 bool

	// Channel for forcing event loop exit (emergency timeout)
	forceExitCh chan struct{}

	// Callback for when all services have stopped
	OnAllStopped func()
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

// GetShutdownType returns the shutdown type that was requested.
// The caller uses this to determine the appropriate system action
// (reboot, halt, poweroff, soft-reboot, etc.) after Run() returns.
func (el *EventLoop) GetShutdownType() service.ShutdownType {
	return el.shutdownType
}

// Run starts the event loop. It blocks until the context is cancelled,
// a shutdown signal is received and all services stop, or an emergency
// timeout forces exit.
func (el *EventLoop) Run(ctx context.Context) error {
	el.sigCh = SetupSignals()
	defer StopSignals(el.sigCh)

	el.logger.Info("slinit event loop started (PID %d)", os.Getpid())

	for {
		select {
		case <-ctx.Done():
			el.logger.Info("Context cancelled, shutting down")
			return ctx.Err()

		case <-el.forceExitCh:
			el.logger.Error("Emergency shutdown timeout reached, forcing exit")
			return nil

		case sig := <-el.sigCh:
			if el.handleSignal(sig) {
				// Shutdown requested - check if already done
				if el.services.CountActiveServices() == 0 {
					el.logger.Info("All services stopped, exiting")
					return nil
				}
			}
		}

		// Check if all services have stopped
		if el.services.CountActiveServices() == 0 {
			if el.shutdownInitiated {
				el.logger.Info("All services stopped, exiting")
				if el.OnAllStopped != nil {
					el.OnAllStopped()
				}
				return nil
			}
			if el.isPID1 {
				// PID 1: all services stopped without explicit shutdown = boot failure
				el.logger.Warn("All services stopped without shutdown request (boot failure?)")
				return nil
			}
		}
	}
}

// handleSignal processes an OS signal. Returns true if shutdown was initiated.
func (el *EventLoop) handleSignal(sig os.Signal) bool {
	sysSignal, ok := sig.(syscall.Signal)
	if !ok {
		return false
	}

	switch sysSignal {
	case syscall.SIGTERM:
		el.logger.Notice("Received SIGTERM, initiating shutdown")
		el.initiateShutdown(service.ShutdownHalt)
		return true

	case syscall.SIGINT:
		if os.Getpid() == 1 {
			// PID 1: SIGINT means reboot (Ctrl+Alt+Del)
			el.logger.Notice("Received SIGINT (PID 1), initiating reboot")
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

	case syscall.SIGHUP:
		el.logger.Notice("Received SIGHUP")
		// Could trigger service reload in the future
		return false

	case syscall.SIGCHLD:
		// Reap orphaned child processes (PID 1 duty)
		if el.isPID1 {
			el.reapOrphans()
		}
		return false
	}

	return false
}

// InitiateShutdown triggers a shutdown from outside the event loop (e.g., control socket).
func (el *EventLoop) InitiateShutdown(shutdownType service.ShutdownType) {
	el.initiateShutdown(shutdownType)
}

func (el *EventLoop) initiateShutdown(shutdownType service.ShutdownType) {
	if el.shutdownInitiated {
		return
	}
	el.shutdownInitiated = true
	el.shutdownType = shutdownType
	el.services.StopAllServices(shutdownType)

	// Start emergency timeout goroutine
	go func() {
		time.Sleep(defaultEmergencyTimeout)
		el.logger.Error("Services did not stop within %v, forcing shutdown", defaultEmergencyTimeout)
		select {
		case el.forceExitCh <- struct{}{}:
		default:
		}
	}()
}

// reapOrphans collects exit status from orphaned child processes.
// When running as PID 1 (or as a child subreaper), orphaned processes
// reparent to this process. Without explicit reaping, they become zombies.
func (el *EventLoop) reapOrphans() {
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			break
		}
		el.logger.Debug("Reaped orphan process %d (status %v)", pid, status)
	}
}

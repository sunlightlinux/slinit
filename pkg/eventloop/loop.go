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

	// Container mode: SIGINT/SIGTERM trigger graceful halt instead of reboot
	isContainer bool

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
			return ctx.Err()

		case <-el.forceExitCh:
			el.logger.Error("Emergency shutdown timeout reached, forcing exit")
			return nil

		case sig := <-el.sigCh:
			if el.handleSignal(sig) {
				if el.services.CountActiveServices() == 0 {
					el.logger.Info("All services stopped, exiting")
					return nil
				}
			}

		case <-inactiveCh:
			// A service became inactive — check if shutdown is complete
			if el.shutdownInitiated && el.services.CountActiveServices() == 0 {
				el.logger.Info("All services stopped, exiting")
				if el.OnAllStopped != nil {
					el.OnAllStopped()
				}
				return nil
			}
			// Boot failure: all services stopped without explicit shutdown (PID 1 only)
			if !el.shutdownInitiated && el.isPID1 && el.services.CountActiveServices() == 0 {
				el.logger.Error("All services stopped without shutdown — boot failure")
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
		if el.isContainer {
			// Container mode: SIGTERM = graceful halt (Docker/Podman stop)
			el.logger.Notice("Received SIGTERM, initiating graceful halt (container mode)")
			el.initiateShutdown(service.ShutdownHalt)
		} else if el.isPID1 {
			// PID 1: SIGTERM = reboot (sent by busybox reboot)
			el.logger.Notice("Received SIGTERM, initiating reboot")
			el.initiateShutdown(service.ShutdownReboot)
		} else {
			el.logger.Notice("Received SIGTERM, initiating shutdown")
			el.initiateShutdown(service.ShutdownHalt)
		}
		return true

	case syscall.SIGINT:
		if el.isContainer {
			// Container mode: SIGINT = graceful halt
			el.logger.Notice("Received SIGINT, initiating graceful halt (container mode)")
			el.initiateShutdown(service.ShutdownHalt)
		} else if el.isPID1 {
			// PID 1: SIGINT = reboot (Ctrl+Alt+Del)
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
		// SysV init convention: SIGUSR1 = halt (sent by busybox halt)
		el.logger.Notice("Received SIGUSR1, initiating shutdown")
		el.initiateShutdown(service.ShutdownHalt)
		return true

	case syscall.SIGUSR2:
		// SysV init convention: SIGUSR2 = poweroff (sent by busybox poweroff)
		el.logger.Notice("Received SIGUSR2, initiating poweroff")
		el.initiateShutdown(service.ShutdownPoweroff)
		return true

	case syscall.SIGHUP:
		el.logger.Notice("Received SIGHUP")
		// Could trigger service reload in the future
		return false

	case syscall.SIGCHLD:
		// Reap orphaned processes. Each managed service child gets its own
		// process group (Setpgid), and group members are reaped directly in
		// handleChildExit via KillProcessGroup(-pgid). This loop catches
		// remaining orphans: double-forked daemons, setsid'd children, etc.
		//
		// There is a small race with os/exec's internal Wait4(pid): if we
		// reap a managed child here, the goroutine gets ECHILD and reports
		// status=0. In practice Go's runtime reaps managed children before
		// this handler runs, so the race is extremely unlikely. The
		// trade-off (no zombie accumulation) is worth it for PID 1.
		if el.isPID1 {
			for {
				var status syscall.WaitStatus
				pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
				if pid <= 0 || err != nil {
					break
				}
				el.logger.Debug("Reaped orphan process %d (status: %v)", pid, status)
			}
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

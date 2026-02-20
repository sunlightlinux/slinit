package eventloop

import (
	"context"
	"os"
	"syscall"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// EventLoop is the central event coordinator for slinit.
// It replaces dasynq's epoll-based event loop with Go channels and select.
type EventLoop struct {
	services *service.ServiceSet
	logger   *logging.Logger
	sigCh    chan os.Signal

	// Set to true when shutdown is initiated
	shutdownInitiated bool

	// Callback for when all services have stopped
	OnAllStopped func()
}

// New creates a new EventLoop.
func New(services *service.ServiceSet, logger *logging.Logger) *EventLoop {
	return &EventLoop{
		services: services,
		logger:   logger,
	}
}

// Run starts the event loop. It blocks until the context is cancelled
// or a shutdown signal is received.
func (el *EventLoop) Run(ctx context.Context) error {
	el.sigCh = SetupSignals()
	defer StopSignals(el.sigCh)

	el.logger.Info("slinit event loop started (PID %d)", os.Getpid())

	for {
		select {
		case <-ctx.Done():
			el.logger.Info("Context cancelled, shutting down")
			return ctx.Err()

		case sig := <-el.sigCh:
			if el.handleSignal(sig) {
				// Shutdown requested
				if el.services.CountActiveServices() == 0 {
					el.logger.Info("All services stopped, exiting")
					return nil
				}
			}
		}

		// Check if all services have stopped during shutdown
		if el.shutdownInitiated && el.services.CountActiveServices() == 0 {
			el.logger.Info("All services stopped, exiting")
			if el.OnAllStopped != nil {
				el.OnAllStopped()
			}
			return nil
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
			// PID 1: SIGINT means reboot
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
	el.services.StopAllServices(shutdownType)
}

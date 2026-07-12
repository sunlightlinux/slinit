package eventloop

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/process"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// Default emergency shutdown timeout. Configurable at daemon start
// via --emergency-timeout for workloads whose stop cascade legitimately
// runs longer than the built-in 90s guard (docker + complex services).
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

	// Emergency shutdown timeout. Zero means "use defaultEmergencyTimeout".
	// Set via SetEmergencyTimeout before Run(); reads are unlocked because
	// the field is only written at startup, before any goroutine reads it.
	emergencyTimeout time.Duration

	// Atomic counter for repeated shutdown signals (escalation).
	shutdownSignals atomic.Int32

	// PID 1 mode enables boot failure detection and orphan reaping
	isPID1 bool

	// Container mode: SIGINT/SIGTERM trigger graceful halt instead of reboot
	isContainer bool

	// Channel for forcing event loop exit (emergency timeout)
	forceExitCh chan struct{}

	// Shutdown reporter: periodically logs which services are blocking shutdown
	shutdownReporterStop chan struct{}

	// Callback for when all services have stopped
	OnAllStopped func()

	// OnReopenSocket is called on SIGUSR1 to reopen the control socket
	OnReopenSocket func()

	// SignalShutdownGate, when set, is consulted before every signal-driven
	// shutdown attempt (CAD, SIGTERM/SIGINT to PID 1, RT signals, etc.).
	// Returning false aborts the shutdown; the signal is logged and
	// otherwise ignored. It exists to implement /etc/slinit/shutdown.allow
	// style access control without coupling pkg/eventloop to pkg/shutdown.
	// reason is a human-readable signal name for logging.
	SignalShutdownGate func(reason string) bool

	// OnPreShutdown is called once per shutdown attempt, immediately
	// before StopAllServices runs and while every service still holds
	// its activation/pin/trigger state. main.go wires this to capture
	// a snapshot for soft-reboot — by the time StopAllServices returns
	// the operator-visible state is gone. Returning is best-effort:
	// errors are logged by the callback and shutdown continues.
	OnPreShutdown func(shutdownType service.ShutdownType)
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

// SetEmergencyTimeout overrides the shutdown emergency-timeout guard
// (default 90s). Values <= 0 fall back to the default. Must be called
// before Run(); once the event loop is running the value is captured
// into the timer callback and further changes have no effect on the
// in-flight shutdown.
func (el *EventLoop) SetEmergencyTimeout(d time.Duration) {
	el.emergencyTimeout = d
}

// effectiveEmergencyTimeout returns the configured emergency timeout,
// falling back to the compile-time default when unset or non-positive.
func (el *EventLoop) effectiveEmergencyTimeout() time.Duration {
	if el.emergencyTimeout > 0 {
		return el.emergencyTimeout
	}
	return defaultEmergencyTimeout
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

	// Read shutdown state once (lock-free atomic)
	shutting := el.isShuttingDown()

	// Real-time shutdown signals (systemd SIGRTMIN+3..+6 convention).
	// These are the standard way to trigger halt/poweroff/reboot/kexec
	// against a container's PID 1 — `systemctl poweroff` from inside a
	// container translates to `kill -s RTMIN+4 1`.
	if st, name, ok := rtShutdownType(sysSignal); ok {
		if shutting {
			return el.escalateShutdown(name)
		}
		if !el.gateAllows(name) {
			return false
		}
		el.logger.Notice("Received %s, initiating %s", name, st)
		el.initiateShutdown(st)
		return true
	}

	switch sysSignal {
	case syscall.SIGTERM:
		if shutting {
			return el.escalateShutdown("SIGTERM")
		}
		if !el.gateAllows("SIGTERM") {
			return false
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
		if shutting {
			return el.escalateShutdown("SIGINT")
		}
		if !el.gateAllows("SIGINT") {
			return false
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
		if shutting {
			return el.escalateShutdown("SIGQUIT")
		}
		if !el.gateAllows("SIGQUIT") {
			return false
		}
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
		if shutting {
			return el.escalateShutdown("SIGUSR2")
		}
		if !el.gateAllows("SIGUSR2") {
			return false
		}
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
// Race resolution: Wait4(-1, ...) also collects any managed child that
// exits at the same moment. When that happens process.DefaultExitRouter
// routes the real WaitStatus to the per-service wait goroutine; without
// the router, cmd.Wait() would observe ECHILD and silently report
// status=0, losing the real exit code in finish-command, is-failed,
// and chain-to gating.
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
		if process.DefaultExitRouter.Route(pid, status) {
			el.logger.Debug("Routed reaped pid %d to managed-child waiter (status: %v)", pid, status)
			continue
		}
		el.logger.Debug("Reaped orphan process %d (status: %v)", pid, status)
	}
}

// gateAllows consults el.SignalShutdownGate and returns true if the
// shutdown should proceed. With no gate installed it is a no-op that
// always allows. When the gate denies, it logs a notice so the operator
// can see *why* a signal was ignored and returns false.
func (el *EventLoop) gateAllows(sigName string) bool {
	if el.SignalShutdownGate == nil {
		return true
	}
	if el.SignalShutdownGate(sigName) {
		return true
	}
	el.logger.Notice("Shutdown gate denied %s — ignoring signal", sigName)
	return false
}

// escalateShutdown handles repeated shutdown signals during an ongoing
// shutdown. The second signal reduces the timeout and logs blocking services;
// the third sends SIGKILL to all and forces immediate exit.
func (el *EventLoop) escalateShutdown(sigName string) bool {
	count := el.shutdownSignals.Add(1)
	switch {
	case count == 2:
		el.logger.Notice("Received %s again, reducing emergency timeout to 25%%", sigName)
		el.resetEmergencyTimer(el.effectiveEmergencyTimeout() / 4)
		// Log which services are blocking shutdown
		el.logBlockingServices()
	case count >= 3:
		blocking := formatBlockingServices(el.services.GetActiveServiceInfo())
		el.logger.Error("Received %s a third time, killing all processes and forcing exit%s",
			sigName, blocking)
		el.services.KillActiveServices()
		el.stopShutdownReporter()
		select {
		case el.forceExitCh <- struct{}{}:
		default:
		}
	}
	return true
}

// logBlockingServices logs info about services that are not yet stopped.
func (el *EventLoop) logBlockingServices() {
	active := el.services.GetActiveServiceInfo()
	if len(active) == 0 {
		return
	}
	el.logger.Notice("Waiting for %d service(s) to stop: %s",
		len(active), joinActiveServiceInfo(active))
}

// joinActiveServiceInfo renders a comma-separated list of blocking
// services with state + PID. Shared between the periodic reporter and
// the emergency force-exit log so both produce the same operator-
// visible string.
func joinActiveServiceInfo(active []service.ActiveServiceInfo) string {
	parts := make([]string, 0, len(active))
	for _, info := range active {
		s := info.Name + " (" + info.State.String()
		if info.PID > 0 {
			s += fmt.Sprintf(", pid %d", info.PID)
		}
		s += ")"
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

// formatBlockingServices returns a "; still blocking: X, Y, Z" suffix
// for emergency-path log lines, or an empty string when nothing is
// active. Callers append it to the primary error message so the
// operator sees the blocker list in the same journal entry as the
// force-exit event — no need to correlate two separate log lines.
func formatBlockingServices(active []service.ActiveServiceInfo) string {
	if len(active) == 0 {
		return ""
	}
	return "; still blocking: " + joinActiveServiceInfo(active)
}

// startShutdownReporter launches a goroutine that periodically logs
// which services are blocking shutdown.
func (el *EventLoop) startShutdownReporter() {
	stop := make(chan struct{})
	el.shutdownReporterStop = stop
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				el.logBlockingServices()
			case <-stop:
				return
			}
		}
	}()
}

// stopShutdownReporter stops the periodic shutdown reporter.
func (el *EventLoop) stopShutdownReporter() {
	if el.shutdownReporterStop != nil {
		close(el.shutdownReporterStop)
		el.shutdownReporterStop = nil
	}
}

// isShuttingDown returns the current shutdown state (lock-free).
func (el *EventLoop) isShuttingDown() bool {
	return el.shutdownSignals.Load() > 0
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

	// Start emergency timeout with a cancellable timer.
	// Capture immutable refs to avoid racing on el fields after mutex release.
	// services is captured too so the callback can enumerate blockers
	// without touching el fields the timer isn't holding a lock on.
	logger := el.logger
	forceExitCh := el.forceExitCh
	services := el.services
	timeout := el.effectiveEmergencyTimeout()
	el.emergencyTimer = time.AfterFunc(timeout, func() {
		blocking := formatBlockingServices(services.GetActiveServiceInfo())
		logger.Error("Services did not stop within %v, forcing shutdown%s",
			timeout, blocking)
		select {
		case forceExitCh <- struct{}{}:
		default:
		}
	})
	// Release mutex before calling StopAllServices to avoid potential
	// deadlock if service state changes try to signal back to the event loop.
	el.mu.Unlock()

	// Capture operator state (snapshot) while services still hold it.
	// StopAllServices clears activation/pin/trigger as it tears down,
	// so this hook must run first.
	if el.OnPreShutdown != nil {
		el.OnPreShutdown(shutdownType)
	}

	el.services.StopAllServices(shutdownType)

	// Start periodic reporting of blocking services
	el.startShutdownReporter()
}

// cancelEmergencyTimer stops the emergency timer if it's running.
func (el *EventLoop) cancelEmergencyTimer() {
	el.stopShutdownReporter()
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
	logger := el.logger
	forceExitCh := el.forceExitCh
	services := el.services
	el.emergencyTimer = time.AfterFunc(d, func() {
		blocking := formatBlockingServices(services.GetActiveServiceInfo())
		logger.Error("Escalated emergency timeout reached, forcing shutdown%s",
			blocking)
		select {
		case forceExitCh <- struct{}{}:
		default:
		}
	})
}

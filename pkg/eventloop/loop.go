package eventloop

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/process"
	"github.com/sunlightlinux/slinit/pkg/service"
	"github.com/sunlightlinux/slinit/pkg/shutdown"
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

	// restartWaitUntil is when nothing being active stops waiting for a
	// client restart's start (checkInactive); zero while not waiting.
	// Used only on the loop's goroutine.
	restartWaitUntil time.Time

	// Shutdown state, protected by mu for concurrent access from
	// signal handler goroutine and control socket goroutines.
	mu                sync.Mutex
	shutdownInitiated bool
	shutdownType      service.ShutdownType
	emergencyTimer    *time.Timer
	// runExited is set when Run returns. A shutdown still in its
	// pre-shutdown work then (an escalated timer returned Run) must not
	// arm a timer or reporter that nothing will cancel.
	runExited bool

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

	// Rescue/emergency mode. There are no boot services by design, so
	// "nothing is running" is the expected steady state rather than a
	// collapse — see checkInactive.
	isRescue bool

	// SIGPWR work: at most one power hook run queued behind the one
	// running. See queuePowerHook.
	powerOnce    sync.Once
	powerPending chan struct{}
	// powerHook is shutdown.HandlePowerSignal; a field so tests can
	// stand in a slow hook.
	powerHook func(*logging.Logger) shutdown.PowerState

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
	// errors are logged by the callback and shutdown continues. It runs
	// on its own goroutine and is waited for at most the emergency
	// timeout (see runPreShutdown), so time-critical work belongs at
	// its start.
	OnPreShutdown func(shutdownType service.ShutdownType)

	// OnShutdownAnnounce fires at the very TOP of initiateShutdown,
	// before the "Shutting down slinit (...)" WARN log line. main.go
	// wires this to unmute the catch-all console tee so the WARN +
	// subsequent teardown output is visible on /dev/console again.
	// Distinct from OnPreShutdown, which fires later — after the
	// WARN and before the emergency timer is armed. Best-effort;
	// panics are recovered by the caller's outer defer.
	OnShutdownAnnounce func()
}

// New creates a new EventLoop.
func New(services *service.ServiceSet, logger *logging.Logger) *EventLoop {
	return &EventLoop{
		services:    services,
		logger:      logger,
		forceExitCh: make(chan struct{}, 1),
		powerHook:   shutdown.HandlePowerSignal,
	}
}

// releaseSignals ends the loop's claim on its signals. As PID 1 the
// claimed set must stay claimed: signal.Stop hands a signal back to the
// Go runtime, whose disposition for SIGINT, SIGTERM, SIGHUP and SIGQUIT
// is to exit — and Run returns well before slinit is done (the final
// shutdown sequence, a soft reboot's hooks and sweep, a ShutdownRemain
// hold that lasts for ever). A Ctrl+Alt+Del or a stray `kill -INT 1` in
// that window was `Attempted to kill init!`. So a fresh channel claims
// the set before the loop's channel lets go, leaving no gap; nothing
// reads it, and signal delivery to a full channel is dropped, not
// blocking. Children still get default dispositions, unlike with
// signal.Ignore, which exec would pass on. The fresh channel is kept in
// heldSigCh so the next loop can let it go once it has claimed the set:
// discarded, it stayed registered, one more per remain.
func (el *EventLoop) releaseSignals() {
	if el.isPID1 || os.Getpid() == 1 {
		ch := SetupEarlySignals()
		heldSigMu.Lock()
		prev := heldSigCh
		heldSigCh = ch
		heldSigMu.Unlock()
		if prev != nil {
			StopSignals(prev)
		}
	}
	StopSignals(el.sigCh)
}

// heldSigCh is the channel releaseSignals claims the signals on between
// two loops, guarded by heldSigMu. Package-level: each loop is a new
// EventLoop.
var (
	heldSigMu sync.Mutex
	heldSigCh chan os.Signal
)

// releaseHeldSignals lets go of the channel the previous loop left
// holding the signals. Call it once this loop has claimed them.
func releaseHeldSignals() {
	heldSigMu.Lock()
	ch := heldSigCh
	heldSigCh = nil
	heldSigMu.Unlock()
	if ch != nil {
		StopSignals(ch)
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

// SetRescueMode tells the loop it is supervising a rescue/emergency
// boot, where no boot services are loaded at all.
func (el *EventLoop) SetRescueMode(v bool) {
	el.isRescue = v
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

// AdoptSignals tells the loop to use a channel that has already been
// registered with SetupEarlySignals, instead of registering its own
// when Run starts. Everything that arrived during boot is still on it.
func (el *EventLoop) AdoptSignals(sigCh chan os.Signal) {
	el.sigCh = sigCh
}

// Run starts the event loop. It blocks until the context is cancelled,
// a shutdown signal is received and all services stop, or an emergency
// timeout forces exit.
func (el *EventLoop) Run(ctx context.Context) error {
	if el.sigCh == nil {
		el.sigCh = SetupSignals()
	} else {
		// Signals were claimed early (see SetupEarlySignals); anything
		// the operator sent during boot is already queued on this
		// channel and will be handled as soon as the loop turns. Only
		// SIGCHLD is still outstanding, because there was no reader
		// for it until now.
		signal.Notify(el.sigCh, syscall.SIGCHLD)
	}
	releaseHeldSignals()
	defer el.releaseSignals()
	defer el.stopPowerHooks()
	// An orphan that exited before SIGCHLD was claimed (during boot, or
	// between the previous loop and this one after a remain) sent a
	// signal nobody received: reap it now rather than at the next exit.
	el.reapOrphans()
	defer func() {
		el.mu.Lock()
		el.runExited = true
		el.mu.Unlock()
		el.cancelEmergencyTimer()
	}()

	el.logger.Info("slinit event loop started (PID %d)", os.Getpid())

	inactiveCh := el.services.InactiveCh()

	for {
		select {
		case <-ctx.Done():
			el.logger.Info("Context cancelled, shutting down")
			el.cancelEmergencyTimer()
			return ctx.Err()

		case <-el.forceExitCh:
			// forceExitCh has two writer paths: emergency timer
			// (real timeout — services blocked shutdown) and the
			// empty-services fast path in initiateShutdown (nothing
			// to stop). Distinguish so the operator log reflects
			// reality instead of blaming a phantom timeout.
			if el.services.CountActiveServices() == 0 {
				el.logger.Info("All services stopped, exiting")
			} else {
				el.logger.Error("Emergency shutdown timeout reached, forcing exit")
			}
			// The reporter and timer go with the loop: after a remain
			// PID 1 runs on, and they would report into it for ever.
			el.cancelEmergencyTimer()
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

// clientRestartWait is how long nothing being active may wait for the
// start half of a client's restart (see checkInactive).
var clientRestartWait = 60 * time.Second

// checkInactive evaluates whether the event loop should exit after a service
// became inactive. Returns true if the loop should terminate.
func (el *EventLoop) checkInactive() bool {
	if el.services.CountActiveServices() != 0 {
		el.restartWaitUntil = time.Time{}
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

	// Rescue/emergency: an empty service set is the whole point of the
	// mode, so this is not a collapse. Returning true here exited the
	// loop the moment rescue mode started, which closed the signal
	// channel via StopSignals — Ctrl+Alt+Del then did nothing at all,
	// and the collapse prompt fought the rescue shell for /dev/console.
	if el.isRescue {
		return false
	}

	// Boot failure: all services stopped without explicit shutdown (PID 1 only)
	if el.isPID1 {
		// A client between the stop and the start of a restart: what it
		// stopped is about to be started again, so nothing being active now
		// is not the end (a container's slinit used to exit here). The
		// start, or the client going away, brings the check back. The wait
		// is counted from here, when nothing is active, not from the stop
		// request (a stop can take longer than any fixed bound), and is
		// bounded, so a client that never sends the start does not keep a
		// finished container running.
		if el.services.ClientRestartPending() {
			now := time.Now()
			if el.restartWaitUntil.IsZero() {
				el.restartWaitUntil = now.Add(clientRestartWait)
				time.AfterFunc(clientRestartWait, el.services.PokeInactive)
			}
			if now.Before(el.restartWaitUntil) {
				return false
			}
			el.logger.Notice("A client restart left nothing active for %v without its start; exiting", clientRestartWait)
		}

		// In a container this is also how a finished workload looks —
		// the job ran, exited, and left nothing active. main decides
		// which it was and logs that; calling it a failure here
		// contradicted the "workload finished" line that followed.
		if el.isContainer {
			el.logger.Info("All services stopped, exiting")
		} else {
			el.logger.Error("All services stopped without shutdown — boot failure")
		}
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

	// SIGPWR: a UPS daemon reporting that mains power went away or came
	// back. Handled before the switch for the same reason as the RT
	// signals — loop.go cannot name a Linux-only signal in a case label.
	//
	// This reads why and runs the operator's power hook. It deliberately
	// does NOT shut anything down, not even on a low battery: what to do
	// about a draining UPS is policy, and an init system that guesses
	// will be wrong on some machine in the most expensive way there is.
	// `slinitctl poweroff` is one line inside the hook.
	if isPowerSignal(sysSignal) {
		if shutting {
			// Already going down. Reading the status would consume the
			// file the hook might still want, and there is nothing left
			// to decide.
			el.logger.Info("Received SIGPWR while shutting down, ignoring")
			return false
		}
		el.queuePowerHook()
		return false
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
	el.mu.Lock()
	if el.runExited {
		el.mu.Unlock()
		return
	}
	el.shutdownReporterStop = stop
	el.mu.Unlock()
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
	el.mu.Lock()
	defer el.mu.Unlock()
	if el.shutdownReporterStop != nil {
		close(el.shutdownReporterStop)
		el.shutdownReporterStop = nil
	}
}

// isShuttingDown returns the current shutdown state (lock-free).
func (el *EventLoop) isShuttingDown() bool {
	return el.shutdownSignals.Load() > 0
}

// queuePowerHook hands a SIGPWR to the power-hook goroutine. The hook is
// the operator's program and may run for minutes: on this goroutine it
// stalled orphan reaping, Ctrl+Alt+Del, SIGTERM and even the shutdown
// the hook itself asks for. Runs never overlap. A SIGPWR that arrives
// while one runs queues one more run, which reads the status file when
// it starts; further ones collapse into it, as the kernel collapses a
// repeated pending signal, so nothing the UPS daemon wrote is lost.
func (el *EventLoop) queuePowerHook() {
	el.powerOnce.Do(func() {
		el.powerPending = make(chan struct{}, 1)
		go el.runPowerHooks(el.powerPending)
	})
	select {
	case el.powerPending <- struct{}{}:
	default:
		el.logger.Info("Received SIGPWR while a power hook run is already queued, collapsing into it")
	}
}

func (el *EventLoop) runPowerHooks(pending <-chan struct{}) {
	for range pending {
		if el.isShuttingDown() {
			// Same rule as a SIGPWR received during shutdown: leave
			// the status file for a hook that may still be running.
			el.logger.Info("SIGPWR queued before shutdown began, ignoring")
			continue
		}
		state := el.powerHook(el.logger)
		el.logger.Notice("Received SIGPWR, power status: %s", state)
	}
}

// stopPowerHooks ends the SIGPWR worker with the loop, after any run it
// is in. Called on the Run goroutine, the only one that queues a run, so
// no send can race the close; one queued later finds no channel and is
// dropped.
func (el *EventLoop) stopPowerHooks() {
	el.powerOnce.Do(func() {})
	if el.powerPending != nil {
		close(el.powerPending)
		el.powerPending = nil
	}
}

// InitiateShutdown triggers a shutdown from outside the event loop
// (e.g., control socket). Safe for concurrent use.
func (el *EventLoop) InitiateShutdown(shutdownType service.ShutdownType) {
	el.initiateShutdown(shutdownType)
}

// InitiateShutdownKill shuts down without waiting services out: the
// teardown is started so every service transitions and gets its
// stop-command, and then each one is SIGKILLed instead of being given
// its stop-timeout. This is `slinitctl shutdown <type> now`, for an
// operator who wants the box down rather than the services happy.
func (el *EventLoop) InitiateShutdownKill(shutdownType service.ShutdownType) {
	el.initiateShutdown(shutdownType)
	// After initiateShutdown, so the kill lands on services that are
	// already in their stopping transition; their exits then drive the
	// loop out through the normal inactive path.
	el.logger.Warn("Killing services immediately (requested)")
	el.services.KillActiveServices()
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
	// Call the "shutdown announced" hook FIRST, before the WARN log
	// line — main.go uses this to unmute the catch-all console tee
	// so operators see the "Shutting down slinit (...)" announcement
	// AND the [STOPPD] cascade on /dev/console.
	if el.OnShutdownAnnounce != nil {
		el.OnShutdownAnnounce()
	}
	// Announce on the operational log (surfaces on /dev/console when
	// slinit is PID 1) so operators watching the serial line see that
	// PID 1 caught the request before the [STOPPD] cascade begins.
	// Matches dinitctl's own "Shutting down dinit..." announcement,
	// except emitted server-side so it's visible whether the trigger
	// was a control-socket client, a shutdown-binary invocation, or a
	// reboot syscall relay. Level is Warn (not Info/Notice) so it
	// clears the boot-console filter cmd/slinit sets to LevelWarn in
	// systemMode — Notice would be silently dropped there, defeating
	// the whole point of the announcement.
	el.logger.Warn("Shutting down slinit (%s)...", shutdownType)

	// Release mutex before calling OnPreShutdown and StopAllServices to
	// avoid potential deadlock if service state changes try to signal
	// back to the event loop.
	el.mu.Unlock()

	// Capture operator state (snapshot) and run the system-down work
	// while services still hold their state. StopAllServices clears
	// activation/pin/trigger as it tears down, so this hook must run
	// first. It is bounded: see runPreShutdown.
	el.runPreShutdown(shutdownType)

	// The emergency timer guards the stop phase only. Armed before the
	// pre-shutdown work, slow system-down hooks or ifdown spent the
	// whole budget while every service was still running, and the
	// timer fired before a single service had been asked to stop.
	el.armEmergencyTimer()

	// Start periodic reporting of blocking services. Before the stop,
	// whose end can return Run at once: started after, it outlived the
	// loop that should have stopped it.
	el.startShutdownReporter()

	el.services.StopAllServices(shutdownType)

	// Empty-set fast path: rescue/emergency boots have no services
	// running (bypass the service graph entirely), so there's nothing
	// for inactiveCh to notify about. Poke forceExitCh so Run() exits
	// immediately instead of waiting for the emergency timer. The
	// Run-side handler already logs "All services stopped, exiting"
	// when it sees count == 0, so no message duplication.
	if el.services.CountActiveServices() == 0 {
		select {
		case el.forceExitCh <- struct{}{}:
		default:
		}
	}
}

// runPreShutdown runs OnPreShutdown and waits for it at most as long as
// the emergency timeout, the budget the stop phase gets too. It runs
// operator hooks (30s each, any number of them) and ifdown (90s), so
// unbounded it could hold the shutdown for as long as they liked.
// When it overruns, services are stopped anyway and the work carries
// on beside the stop; anything it left running is swept with the rest
// at the end of the shutdown.
func (el *EventLoop) runPreShutdown(shutdownType service.ShutdownType) {
	if el.OnPreShutdown == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		// A panic on its own goroutine would take PID 1 down
		// mid-shutdown; the stop goes on without it instead.
		defer func() {
			if r := recover(); r != nil {
				el.logger.Error("Pre-shutdown hook panicked: %v", r)
			}
		}()
		el.OnPreShutdown(shutdownType)
	}()
	limit := el.effectiveEmergencyTimeout()
	t := time.NewTimer(limit)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		el.logger.Warn("Pre-shutdown hooks still running after %v, stopping services anyway", limit)
	}
}

// armEmergencyTimer starts the emergency timeout for the stop phase.
// A repeated shutdown signal during the pre-shutdown work has already
// armed the shorter escalated timer; that one stays. Once Run has
// returned there is no loop left to force out.
func (el *EventLoop) armEmergencyTimer() {
	el.mu.Lock()
	defer el.mu.Unlock()
	if el.emergencyTimer != nil || el.runExited {
		return
	}
	// Capture immutable refs to avoid racing on el fields from the
	// timer goroutine. services is captured too so the callback can
	// enumerate blockers without touching el fields the timer isn't
	// holding a lock on.
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

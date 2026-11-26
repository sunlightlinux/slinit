package service

import (
	"context"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// The hooks a transition waits on — exec-condition (the start's
// predicates), pre-start-command (and the start's env-generator run),
// pre-stop-hook, the control-command that stands in for a stop signal,
// and finish-command — ran synchronously inside
// BringUp, BringDown and the exit handler, all of which hold queueMu.
// For as long as one ran, up to hook-timeout, every other service's
// transitions and every control-socket request that takes the lock
// waited behind it: measured at 2.7 seconds behind a `sleep 3`
// finish-command.
//
// They now run on a goroutine of their own, as dinit runs everything
// that can take time: the transition stops where the hook starts,
// the service stays in the state it is in (STARTING for a pre-start,
// STOPPING for a pre-stop or control-command, whatever it was for a
// finish-command), and the hook's completion takes the lock and carries
// the transition on from that point. The order is the one it always
// was — pre-start before the fork, pre-stop before the stop signal,
// finish-command before the restart or the next start — only the lock
// is no longer held across it.
//
// One state hook runs at a time; s.stateHook names it. While it is set:
//
//   - BringDown does nothing (a pre-start is cancelled first): the
//     hook's completion moves the stop on. A stop that arrives during a
//     pre-start ends it, killing its process group, and the start ends
//     there; a finish-command or pre-stop-hook is allowed to finish.
//   - A main-process exit during a pre-stop-hook or control-command is
//     parked in pendingExit and handled when the hook is done, so its
//     finish-command still runs after the pre-stop-hook. Only that
//     process's exit, only for those two hooks, and never across runs:
//     startProcess and every new hook clear it.
//   - The shutdown teardown (KillActiveServices) cancels it, and no
//     hook runs after it (ServiceSet.finalKill): the completion of one
//     it ended goes straight on, without starting the next (a
//     pre-stop-hook's finish-command, say).
//   - No new run of the service begins: a start does not cancel a stop
//     still waiting for its dependents (CanInterruptStop), the service
//     cannot reach STOPPED, so it is neither restarted nor unloaded
//     under the hook, and BringUp, should it be reached anyway, waits
//     for the hook (bringUpBlockedLocked). A new run forked while a
//     finish-command still ran had that hook's exit dispatched against
//     it, which killed it.

// hookKind names what a state hook is for.
type hookKind uint8

const (
	hookPreStart      hookKind = iota + 1 // env-generator and pre-start-command
	hookPreStop                           // pre-stop-hook
	hookStopControl                       // control-command-<SIG> in place of the stop signal
	hookFinish                            // finish-command
	hookEnvGen                            // env-generator of a smooth recovery
	hookExecCondition                     // the start's predicates, with an exec-condition among them
)

// endsWithStop reports whether a stop ends the hook rather than waiting
// for it: one that prepares a launch the stop has called off.
func (k hookKind) endsWithStop() bool {
	return k == hookPreStart || k == hookEnvGen || k == hookExecCondition
}

// parksExit reports whether a main-process exit during the hook waits
// for its completion (pendingExit): a stop's hooks, which run while the
// process they stop for is still there.
func (k hookKind) parksExit() bool {
	return k == hookPreStop || k == hookStopControl
}

func (k hookKind) String() string {
	switch k {
	case hookPreStart:
		return "pre-start-command"
	case hookPreStop:
		return "pre-stop-hook"
	case hookStopControl:
		return "control-command"
	case hookFinish:
		return "finish-command"
	case hookEnvGen:
		return "env-generator"
	case hookExecCondition:
		return "exec-condition"
	}
	return "hook"
}

// stateHook is one run of a state hook.
type stateHook struct {
	kind hookKind
	// pid is the main process when the hook began; only its exit is
	// parked for the hook (parksExit).
	pid    int
	cancel context.CancelFunc
	// done is closed when the run is over and its completion has run.
	done chan struct{}
}

// startStateHookLocked runs a state hook off the lock. prepare is called
// here, under queueMu, with the run's context: it reads what it needs
// from the service and returns the function that runs the hook, which
// is called without the lock. then is called under queueMu once that
// has returned, with its error and whether the run was cancelled, and
// is followed by a queue pass.
//
// Caller must hold queueMu, and no other state hook may be running.
func (s *ProcessService) startStateHookLocked(kind hookKind,
	prepare func(ctx context.Context) func() error,
	then func(err error, cancelled bool)) {
	ctx, cancel := context.WithCancel(context.Background())
	h := &stateHook{kind: kind, pid: s.pid, cancel: cancel, done: make(chan struct{})}
	if prev := s.stateHook.Load(); prev != nil {
		// Not expected: every caller checks first. Superseding the
		// running hook lost its completion (a finish-command's exit was
		// never dispatched, a smooth recovery never ended), so this one
		// waits for it instead, and starts from its completion.
		cancel()
		s.services.logger.Error("Service '%s': %s started while %s still runs; it waits for it",
			s.serviceName, kind, prev.kind)
		s.queuedHooks = append(s.queuedHooks, func() {
			s.startStateHookLocked(kind, prepare, then)
		})
		return
	}
	var run func() error
	if s.services.finalKill.Load() {
		// The shutdown's final kill has been sent: a hook started now
		// would only hold the teardown up, for as long as hook-timeout,
		// after the kill meant to end it. Its completion still runs,
		// and sees the run as cancelled, as it would a hook the kill
		// ended.
		s.services.logger.Info("Service '%s': %s not run: processes are being killed",
			s.serviceName, kind)
		cancel()
		run = func() error { return ctx.Err() }
	} else {
		run = prepare(ctx)
	}
	// A parked exit belongs to the hook that parked it, whose
	// completion has run.
	s.pendingExit = nil
	s.stateHook.Store(h)
	go func() {
		defer close(h.done)
		err := run()
		cancelled := ctx.Err() != nil
		cancel()
		s.services.queueMu.Lock()
		defer s.services.queueMu.Unlock()
		s.stateHook.CompareAndSwap(h, nil)
		then(err, cancelled)
		if s.pendingExit != nil && !s.stateHookRunning() {
			// Every completion of a hook that parks takes it.
			s.services.logger.Error("Service '%s': exit of process %d left unhandled by its %s",
				s.serviceName, s.pendingExit.PID, kind)
			s.pendingExit = nil
		}
		s.afterStateHookLocked()
		s.services.processQueuesLocked()
	}()
}

// afterStateHookLocked is what follows every state hook's completion.
// A start that found the hook running (bringUpBlockedLocked) waits in
// STARTING as though for a dependency, and is looked at again now; a
// hook that found another running starts now. Caller holds queueMu.
func (s *ProcessService) afterStateHookLocked() {
	if s.stateHookRunning() {
		return
	}
	if s.state.Load() == StateStarting && s.waitingForDeps {
		s.services.AddTransitionQueue(s)
	}
	if len(s.queuedHooks) > 0 {
		next := s.queuedHooks[0]
		s.queuedHooks = s.queuedHooks[1:]
		next()
	}
}

// bringUpBlockedLocked reports whether BringUp must not begin a new run
// now, and if so leaves the service STARTING, waiting as it waits for a
// dependency, to be brought up when the hook is done
// (afterStateHookLocked). Nothing is expected to reach BringUp while a
// hook runs: a start does not interrupt a stop then, and the service
// cannot be STOPPED. Caller holds queueMu.
func (s *ProcessService) bringUpBlockedLocked() bool {
	h := s.stateHook.Load()
	if h == nil {
		return false
	}
	s.services.logger.Error("Service '%s': start while its %s still runs; it waits for it",
		s.serviceName, h.kind)
	s.waitingForDeps = true
	// The slot allDepsStarted took is taken again then.
	s.dropStartSlot()
	return true
}

// stateHookHolder is a service that runs state hooks (ProcessService).
type stateHookHolder interface {
	stateHookRunning() bool
}

// stateHookBusy reports whether svc has a state hook running. Caller
// holds queueMu.
func stateHookBusy(svc Service) bool {
	h, ok := svc.(stateHookHolder)
	return ok && h.stateHookRunning()
}

// stateHookRunning reports whether a state hook is running. Caller holds
// queueMu.
func (s *ProcessService) stateHookRunning() bool {
	return s.stateHook.Load() != nil
}

// RunningStateHook names the state hook running, or returns "" when
// none is. A reload, which holds queueMu, refuses to change a service's
// settings under a hook that is reading them. Caller holds queueMu.
func (s *ProcessService) RunningStateHook() string {
	if h := s.stateHook.Load(); h != nil {
		return h.kind.String()
	}
	return ""
}

// cancelStateHook ends a running state hook, killing its process group.
// Its completion still runs, and sees the run as cancelled. Safe without
// queueMu.
func (s *ProcessService) cancelStateHook() {
	if h := s.stateHook.Load(); h != nil {
		h.cancel()
	}
}

// takePendingExit returns and clears an exit parked while a stop hook
// ran, if it is the exit of pid, the process that stop was for. Caller
// holds queueMu.
func (s *ProcessService) takePendingExit(pid int) (process.ChildExit, bool) {
	e := s.pendingExit
	s.pendingExit = nil
	if e == nil || pid <= 0 || e.PID != pid {
		return process.ChildExit{}, false
	}
	return *e, true
}

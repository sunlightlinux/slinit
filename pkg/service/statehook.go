package service

import (
	"context"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// The hooks a transition waits on — pre-start-command (and the start's
// env-generator run), pre-stop-hook, the control-command that stands in
// for a stop signal, and finish-command — ran synchronously inside
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
//     finish-command still runs after the pre-stop-hook.
//   - The shutdown teardown (KillActiveServices) cancels it.

// hookKind names what a state hook is for.
type hookKind uint8

const (
	hookPreStart    hookKind = iota + 1 // env-generator and pre-start-command
	hookPreStop                         // pre-stop-hook
	hookStopControl                     // control-command-<SIG> in place of the stop signal
	hookFinish                          // finish-command
	hookEnvGen                          // env-generator of a smooth recovery
)

// endsWithStop reports whether a stop ends the hook rather than waiting
// for it: one that prepares a launch the stop has called off.
func (k hookKind) endsWithStop() bool {
	return k == hookPreStart || k == hookEnvGen
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
	}
	return "hook"
}

// stateHook is one run of a state hook.
type stateHook struct {
	kind   hookKind
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
	h := &stateHook{kind: kind, cancel: cancel, done: make(chan struct{})}
	run := prepare(ctx)
	if prev := s.stateHook.Swap(h); prev != nil {
		// Not expected: every caller checks first. Do not leave the
		// earlier run unbounded.
		s.services.logger.Error("Service '%s': %s started while %s still runs",
			s.serviceName, kind, prev.kind)
		prev.cancel()
	}
	go func() {
		defer close(h.done)
		err := run()
		cancelled := ctx.Err() != nil
		cancel()
		s.services.queueMu.Lock()
		defer s.services.queueMu.Unlock()
		if !s.stateHook.CompareAndSwap(h, nil) {
			return // superseded; see above
		}
		then(err, cancelled)
		s.services.processQueuesLocked()
	}()
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
// ran. Caller holds queueMu.
func (s *ProcessService) takePendingExit() (process.ChildExit, bool) {
	e := s.pendingExit
	s.pendingExit = nil
	if e == nil {
		return process.ChildExit{}, false
	}
	return *e, true
}

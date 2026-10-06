package service

import "time"

// A service that must not launch the instant its dependencies are up —
// a daemon that needs a moment after a mount appears, a device that
// settles — had no way to say so. `restart-delay` governs the gap before
// a RE-start and nothing governed the first one.
//
// The nearest workaround was `pre-start-command = /bin/sleep N`, which
// works and is the wrong shape: the synchronous hooks run inside the
// scheduling lock, so sleeping in one stalls every other service's
// transitions for the duration. Measured at the 5-second default, an
// unrelated `slinitctl start` waited 4.01 seconds behind one. A delay
// implemented as a timer holds nothing.
//
// The service waits in STARTING, which matters for more than looks: the
// active-service count has already been taken by the time BringUp runs,
// and every stop door is guarded by `state != STOPPED`, so a stop
// arriving mid-delay can still act on the service. Parking in STOPPED
// is what made a restart-delay leave the count unreleasable (v3.0.3);
// STARTING has neither problem.

// startDelayTimer arms the wait before this attempt's fork/exec and
// reports whether it did. False means "no delay configured, launch now".
//
// Caller must hold queueMu — this runs from BringUp.
func (s *ProcessService) startDelayTimer(launch func()) bool {
	if s.startDelay <= 0 {
		return false
	}
	// One wait per attempt: startProcess clears the flag, so a restart
	// waits again, and the timer's own callback does not re-arm.
	if s.startDelayDone {
		return false
	}
	s.startDelayDone = true

	s.services.logger.Info("Service '%s': waiting %v before start (start-delay)",
		s.serviceName, s.startDelay)

	if s.startDelayT != nil {
		s.startDelayT.Stop()
	}
	s.startDelayT = time.AfterFunc(s.startDelay, func() {
		s.services.queueMu.Lock()
		defer s.services.queueMu.Unlock()
		s.startDelayT = nil
		// A stop (or anything else) may have moved the service on while
		// we waited. Only a service still STARTING is ours to launch.
		if s.state.Load() != StateStarting {
			return
		}
		launch()
		s.services.processQueuesLocked()
	})
	return true
}

// cancelStartDelay disarms a pending start-delay. Idempotent; safe to
// call for a service that never had one.
func (s *ProcessService) cancelStartDelay() {
	if s.startDelayT != nil {
		s.startDelayT.Stop()
		s.startDelayT = nil
	}
	s.startDelayDone = false
}

// SetStartDelay sets the wait before each start attempt's fork/exec.
// Values <= 0 disable it.
func (s *ProcessService) SetStartDelay(d time.Duration) { s.startDelay = d }

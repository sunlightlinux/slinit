package service

import "time"

// start-when-idle is systemd's Type=idle: hold a service back until the
// others have finished starting, so its output is not interleaved with
// theirs. It exists for the login prompt: getty came up as soon as the
// system could run it, and the "[ OK ] network" of a DHCP still waiting
// for its lease was then printed straight over the prompt.
//
// Like systemd's, the wait is bounded: past idleMaxWait the service starts
// anyway, as a login prompt must not depend on a lease arriving. And like
// start-delay it is a timer in STARTING, which holds no lock and leaves the
// service stoppable (see startdelay.go for why STARTING).

var (
	idleMaxWait = 5 * time.Second
	idlePoll    = 50 * time.Millisecond
)

// idleWaitTimer holds launch back while another service is busy starting,
// and reports whether it did. False means "launch now".
//
// Caller must hold queueMu — this runs from BringUp.
func (s *ProcessService) idleWaitTimer(launch func()) bool {
	if !s.startWhenIdle || !s.services.othersStarting(s) {
		return false
	}
	s.services.logger.Info("Service '%s': waiting for other services to start (start-when-idle)",
		s.serviceName)

	deadline := time.Now().Add(idleMaxWait)
	var poll func()
	poll = func() {
		s.services.queueMu.Lock()
		defer s.services.queueMu.Unlock()
		// A stop may have moved the service on while we waited: only a
		// service still STARTING and still waiting is ours to launch.
		if s.idleT == nil || s.state.Load() != StateStarting {
			s.idleT = nil
			return
		}
		if s.services.othersStarting(s) && time.Now().Before(deadline) {
			s.idleT = time.AfterFunc(idlePoll, poll)
			return
		}
		s.idleT = nil
		launch()
		s.services.processQueuesLocked()
	}
	s.idleT = time.AfterFunc(idlePoll, poll)
	return true
}

// cancelIdleWait disarms a pending start-when-idle wait. Idempotent; safe
// to call for a service that never had one.
func (s *ProcessService) cancelIdleWait() {
	if s.idleT != nil {
		s.idleT.Stop()
		s.idleT = nil
	}
}

// othersStarting reports whether a service other than self is busy
// starting. One still waiting for a dependency is not: that dependency is
// counted itself, and boot, which waits for getty, would otherwise hold getty
// for the whole wait. Nor is one itself in a start-when-idle wait, or two
// gettys would each wait out the other.
//
// One whose dependencies have all started is busy, though its flag still says
// it is waiting: it is only further down the queue. When getty and chronyd
// both depend on the service that started last, getty took chronyd for idle
// and came up first, and chronyd's status line landed on the login prompt.
//
// Caller must hold queueMu; ListServices then takes ss.mu, and nothing
// takes queueMu while holding ss.mu.
func (ss *ServiceSet) othersStarting(self Service) bool {
	for _, svc := range ss.ListServices() {
		if svc == self || svc.State() != StateStarting {
			continue
		}
		if rec := svc.Record(); rec.waitingForDeps && !rec.checkDepsStarted() {
			continue
		}
		if p, ok := svc.(*ProcessService); ok && p.idleT != nil {
			continue
		}
		return true
	}
	return false
}

// SetStartWhenIdle sets whether each start attempt waits for the other
// services to finish starting.
func (s *ProcessService) SetStartWhenIdle(on bool) { s.startWhenIdle = on }

package service

import (
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// start-on-calendar: start the service at the times a systemd-style
// calendar expression names, as a systemd timer with OnCalendar= starts
// its unit. An activation directive on the service, like start-on-path-*
// and start-on-mount: the service runs as itself, with its own sandbox
// and settings, where cron-command runs a command beside a service.
// One already running is left alone, as a timer that elapses for an
// active unit does nothing. Pair it with manual = yes for a service the
// calendar alone should start, as for path activation.
//
// CalendarStarter keeps one timer per service, by name: armed when the
// service is loaded, re-armed after each firing for the next time the
// expression names, removed when it is unloaded. A reload that keeps
// the expression re-arms it; one that drops it removes the timer.

// CalendarStarter fires start-on-calendar. Its methods are safe for
// concurrent use; it takes no service lock while holding its own.
type CalendarStarter struct {
	set    *ServiceSet
	logger ServiceLogger

	mu     sync.Mutex
	timers map[string]*time.Timer
	now    func() time.Time // for tests
}

// NewCalendarStarter returns a starter for the services of ss.
func NewCalendarStarter(ss *ServiceSet, logger ServiceLogger) *CalendarStarter {
	return &CalendarStarter{set: ss, logger: logger, timers: map[string]*time.Timer{}, now: time.Now}
}

// Add arms svc's start-on-calendar, replacing an earlier timer of the
// same name; a service without one only has an earlier timer removed.
func (c *CalendarStarter) Add(svc Service) {
	name := svc.Name()
	spec := svc.Record().StartOnCalendar()
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := c.timers[name]; t != nil {
		t.Stop()
		delete(c.timers, name)
	}
	if spec == nil {
		return
	}
	if svc.Record().startOnCalendarPersistent {
		c.armCatchUpLocked(name, spec)
		return
	}
	c.armLocked(name, spec)
}

// armCatchUpLocked is start-on-calendar-persistent, systemd's
// Persistent=: a time the expression named since the last recorded
// start, while slinit was not running, starts the service now; then the
// calendar is armed as usual. The record is read when this timer fires,
// not at load, and the timer is the service's one, so a second Add
// (a reload) replaces it rather than catch up twice. With no record
// nothing is caught up, as in systemd.
func (c *CalendarStarter) armCatchUpLocked(name string, spec *CalendarSpec) {
	var t *time.Timer
	t = time.AfterFunc(0, func() {
		c.mu.Lock()
		if c.timers[name] != t {
			c.mu.Unlock()
			return
		}
		last, ok := readStamp(calendarStampPath(name))
		missed := time.Time{}
		if ok {
			missed = spec.NextAfter(last)
		}
		c.armLocked(name, spec)
		c.mu.Unlock()
		if missed.IsZero() || !missed.Before(c.now()) {
			return
		}
		c.logger.Info("Service '%s': start-on-calendar catch-up (missed %v)", name, missed)
		if s := c.set.FindService(name, false); s != nil && c.set.calendarFire(s) {
			c.stamp(s)
		}
	})
	c.timers[name] = t
}

// calendarStampPath is where the last start of a persistent
// start-on-calendar is kept: a directory of its own under
// cron-persistent's, whose records are named after services too.
func calendarStampPath(name string) string {
	return filepath.Join(cronPersistDir, "calendar", strings.ReplaceAll(name, "/", "_"))
}

// stamp records now as s's last calendar start, when it is persistent.
func (c *CalendarStarter) stamp(s Service) {
	var persistent bool
	c.set.WithGraphLock(func() { persistent = s.Record().startOnCalendarPersistent })
	if !persistent {
		return
	}
	if err := writeStamp(calendarStampPath(s.Name()), c.now()); err != nil {
		c.logger.Error("Service '%s': start-on-calendar-persistent: %v", s.Name(), err)
	}
}

// Remove disarms the service's timer.
func (c *CalendarStarter) Remove(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := c.timers[name]; t != nil {
		t.Stop()
		delete(c.timers, name)
	}
}

// Close disarms every timer.
func (c *CalendarStarter) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for n, t := range c.timers {
		t.Stop()
		delete(c.timers, n)
	}
}

func (c *CalendarStarter) armLocked(name string, spec *CalendarSpec) {
	c.armAfterLocked(name, spec, time.Time{})
}

// armAfterLocked arms the timer for the first time the expression names
// after now, and after fired, the time just fired for: a wall clock
// stepped back while the timer ran would otherwise name that same time
// again, and the job run twice.
func (c *CalendarStarter) armAfterLocked(name string, spec *CalendarSpec, fired time.Time) {
	now := c.now()
	next := nextCalendarFire(spec, now, fired)
	if next.IsZero() {
		c.logger.Error("Service '%s': start-on-calendar names no future time", name)
		return
	}
	var t *time.Timer
	t = time.AfterFunc(next.Sub(now), func() {
		c.mu.Lock()
		if c.timers[name] != t {
			c.mu.Unlock() // replaced or removed since
			return
		}
		c.armAfterLocked(name, spec, next)
		c.mu.Unlock()
		// By name: a reload that changed the type replaced the record.
		if s := c.set.FindService(name, false); s != nil && c.set.calendarFire(s) {
			c.stamp(s)
		}
	})
	c.timers[name] = t
}

// nextCalendarFire is the first time spec names after now and after
// fired (see armAfterLocked).
func nextCalendarFire(spec *CalendarSpec, now, fired time.Time) time.Time {
	from := now
	if fired.After(from) {
		from = fired
	}
	return spec.NextAfter(from)
}

// calendarFire starts s for its start-on-calendar: a stopped service is
// started; a scripted one that is STARTED has run its command and stays
// STARTED, as a oneshot with RemainAfterExit= would, so it is restarted
// to run the job again, as systemd runs a finished oneshot again; any
// other running service is left alone, as a timer that elapses for an
// active unit does nothing.
//
// The job's run again is not a restart: it is not counted as one, nor
// cascades to what is part of it (rerunJob); and a job a running
// service depends on (hard) is not run again, since its stop would
// wait for that service or take it down, where starting a finished
// oneshot leaves the units after it alone in systemd. Reports whether
// it started or ran anything.
func (ss *ServiceSet) calendarFire(s Service) bool {
	ss.queueMu.Lock()
	defer ss.queueMu.Unlock()
	r := s.Record()
	if r.startOnCalendar == nil || ss.IsShuttingDown() {
		// Cleared by a reload meanwhile; or a shutdown, checked under
		// queueMu as StartServiceUnlessShuttingDown does, so it cannot
		// begin between the check and the start.
		return false
	}
	_, scripted := s.(*ScriptedService)
	switch st := r.state.Load(); {
	case st == StateStopped:
		ss.logger.Info("Service '%s': start-on-calendar fired", s.Name())
		s.Start()
	case st == StateStarted && scripted:
		if r.IsStartPinned() {
			ss.logger.Info("Service '%s': start-on-calendar fired; pinned started, so the job is not run again", s.Name())
			return false
		}
		for _, d := range r.dependents {
			if d.IsHard() && d.From.State() != StateStopped {
				// Its stop would wait for that service to stop, or
				// take it down: neither is what a timer does to the
				// units after a oneshot.
				ss.logger.Info("Service '%s': start-on-calendar fired; '%s' depends on it and is up, so the job is not run again",
					s.Name(), d.From.Name())
				return false
			}
		}
		ss.logger.Info("Service '%s': start-on-calendar fired; running the job again", s.Name())
		r.rerunJob = true
		r.Restart()
	default:
		return false
	}
	ss.processQueuesLocked()
	return true
}

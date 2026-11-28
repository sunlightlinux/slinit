package service

import (
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
	if spec != nil {
		c.armLocked(name, spec)
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
		s := c.set.FindService(name, false)
		if s == nil || c.set.IsShuttingDown() {
			return
		}
		c.set.calendarFire(s)
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
func (ss *ServiceSet) calendarFire(s Service) {
	ss.queueMu.Lock()
	defer ss.queueMu.Unlock()
	r := s.Record()
	if r.startOnCalendar == nil { // cleared by a reload meanwhile
		return
	}
	_, scripted := s.(*ScriptedService)
	switch st := r.state.Load(); {
	case st == StateStopped:
		ss.logger.Info("Service '%s': start-on-calendar fired", s.Name())
		s.Start()
	case st == StateStarted && scripted:
		ss.logger.Info("Service '%s': start-on-calendar fired; running the job again", s.Name())
		r.Restart()
	default:
		return
	}
	ss.processQueuesLocked()
}

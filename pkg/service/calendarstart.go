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
	now := c.now()
	next := spec.NextAfter(now)
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
		c.armLocked(name, spec)
		c.mu.Unlock()
		// By name: a reload that changed the type replaced the record.
		s := c.set.FindService(name, false)
		if s == nil {
			return
		}
		// Under the graph lock: a reload rewrites the expression there.
		var armed bool
		c.set.WithGraphLock(func() { armed = s.Record().StartOnCalendar() != nil })
		if armed && s.State() == StateStopped && !c.set.IsShuttingDown() {
			c.logger.Info("Service '%s': start-on-calendar fired", name)
			c.set.StartService(s)
		}
	})
	c.timers[name] = t
}

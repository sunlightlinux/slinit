package service

import (
	"math/rand/v2"
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
	// nominal is the time the expression named for each armed timer,
	// before its randomized delay: a re-arm inside that delay must not
	// lose the run (Add).
	nominal map[string]time.Time
	// stopped is a time a Remove caught inside its randomized delay
	// (stopped before firing, the time named past), for the Add a
	// reload makes right after (Remove, then Add).
	stopped map[string]stoppedRun
	delays  map[string]time.Duration // each armed timer's randomized delay
	now     func() time.Time         // for tests
}

// NewCalendarStarter returns a starter for the services of ss.
func NewCalendarStarter(ss *ServiceSet, logger ServiceLogger) *CalendarStarter {
	return &CalendarStarter{set: ss, logger: logger, timers: map[string]*time.Timer{}, nominal: map[string]time.Time{}, stopped: map[string]stoppedRun{}, delays: map[string]time.Duration{}, now: time.Now}
}

type stoppedRun struct {
	missed time.Time
	delay  time.Duration
}

// Add arms svc's start-on-calendar, replacing an earlier timer of the
// same name; a service without one only has an earlier timer removed.
func (c *CalendarStarter) Add(svc Service) {
	name := svc.Name()
	spec := svc.Record().StartOnCalendar()
	c.mu.Lock()
	defer c.mu.Unlock()
	// A timer stopped before it fired, for a time already past: the
	// reload came inside the randomized delay. That run is due; systemd
	// reckons from the last trigger and so starts it at once.
	var missed time.Time
	if t := c.timers[name]; t != nil {
		if t.Stop() && !c.nominal[name].IsZero() && !c.nominal[name].After(c.now()) {
			missed = c.nominal[name]
		}
		delete(c.timers, name)
		delete(c.nominal, name)
	}
	// Or one the Remove of this reload stopped: due if still within the
	// delay it was waiting out, and stale (a real unload, long ago)
	// after.
	if st, ok := c.stopped[name]; ok {
		delete(c.stopped, name)
		if missed.IsZero() && !c.now().After(st.missed.Add(st.delay)) {
			missed = st.missed
		}
	}
	if spec == nil {
		return
	}
	delay := svc.Record().startOnCalendarDelay
	if !missed.IsZero() {
		c.armMissedLocked(name, spec, delay, missed)
		return
	}
	if svc.Record().startOnCalendarPersistent {
		c.armCatchUpLocked(name, spec, delay)
		return
	}
	c.armLocked(name, spec, delay)
}

// armCatchUpLocked is start-on-calendar-persistent, systemd's
// Persistent=: a time the expression named since the last recorded
// start, while slinit was not running, starts the service now; then the
// calendar is armed as usual. The record is read when this timer fires,
// not at load, and the timer is the service's one, so a second Add
// (a reload) replaces it rather than catch up twice. With no record
// nothing is caught up, as in systemd.
func (c *CalendarStarter) armCatchUpLocked(name string, spec *CalendarSpec, delay time.Duration) {
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
		c.armLocked(name, spec, delay)
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

// CalendarStampPath is calendarStampPath, for slinitctl list-timers.
func CalendarStampPath(name string) string { return calendarStampPath(name) }

// ReadCalendarStamp reads the last recorded calendar start of name.
func ReadCalendarStamp(name string) (time.Time, bool) { return readStamp(calendarStampPath(name)) }

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
		if t.Stop() && !c.nominal[name].IsZero() && !c.nominal[name].After(c.now()) {
			c.stopped[name] = stoppedRun{c.nominal[name], c.delays[name]}
		}
		delete(c.timers, name)
	}
	delete(c.nominal, name)
}

// armMissedLocked starts the service at once for missed, a time the
// expression named that a re-arm caught inside its randomized delay,
// then arms the calendar after it.
func (c *CalendarStarter) armMissedLocked(name string, spec *CalendarSpec, delay time.Duration, missed time.Time) {
	var t *time.Timer
	t = time.AfterFunc(0, func() {
		c.mu.Lock()
		if c.timers[name] != t {
			c.mu.Unlock()
			return
		}
		c.armAfterLocked(name, spec, missed, delay)
		c.mu.Unlock()
		if s := c.set.FindService(name, false); s != nil && c.set.calendarFire(s) {
			c.stamp(s)
		}
	})
	c.timers[name] = t
	c.nominal[name] = missed
	c.delays[name] = delay
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

func (c *CalendarStarter) armLocked(name string, spec *CalendarSpec, delay time.Duration) {
	c.armAfterLocked(name, spec, time.Time{}, delay)
}

// calendarJitter is the random part of start-on-calendar-randomized-delay, in
// [0, delay): systemd's RandomizedDelaySec=, drawn at each time so a
// fleet does not start the job at the same instant. A var for tests.
var calendarJitter = func(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(delay)))
}

// armAfterLocked arms the timer for the first time the expression names
// after now, and after fired, the time just fired for: a wall clock
// stepped back while the timer ran would otherwise name that same time
// again, and the job run twice.
func (c *CalendarStarter) armAfterLocked(name string, spec *CalendarSpec, fired time.Time, delay time.Duration) {
	now := c.now()
	next := nextCalendarFire(spec, now, fired)
	if next.IsZero() {
		c.logger.Error("Service '%s': start-on-calendar names no future time", name)
		return
	}
	// Up to delay later. The time after it is the expression's next
	// after the late start (nextCalendarFire takes the later of now and
	// next), as systemd reckons from the last trigger: a delay longer
	// than the interval skips a time, there as here.
	var t *time.Timer
	t = time.AfterFunc(next.Add(calendarJitter(delay)).Sub(now), func() {
		c.mu.Lock()
		if c.timers[name] != t {
			c.mu.Unlock() // replaced or removed since
			return
		}
		c.armAfterLocked(name, spec, next, delay)
		c.mu.Unlock()
		// By name: a reload that changed the type replaced the record.
		if s := c.set.FindService(name, false); s != nil && c.set.calendarFire(s) {
			c.stamp(s)
		}
	})
	c.timers[name] = t
	c.nominal[name] = next
	c.delays[name] = delay
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
	return ss.timerFire(s, "start-on-calendar", func(r *ServiceRecord) bool { return r.startOnCalendar != nil })
}

// timerFire is calendarFire for any timer: what names the timer in the
// log, and armed reports, under queueMu, whether the service still has
// it (a reload may have cleared it).
func (ss *ServiceSet) timerFire(s Service, what string, armed func(*ServiceRecord) bool) bool {
	ss.queueMu.Lock()
	defer ss.queueMu.Unlock()
	r := s.Record()
	if !armed(r) || ss.IsShuttingDown() {
		// Cleared by a reload meanwhile; or a shutdown, checked under
		// queueMu as StartServiceUnlessShuttingDown does, so it cannot
		// begin between the check and the start.
		return false
	}
	_, scripted := s.(*ScriptedService)
	switch st := r.state.Load(); {
	case st == StateStopped:
		ss.logger.Info("Service '%s': %s fired", s.Name(), what)
		s.Start()
	case st == StateStarted && scripted:
		if r.IsStartPinned() {
			ss.logger.Info("Service '%s': %s fired; pinned started, so the job is not run again", s.Name(), what)
			return false
		}
		for _, d := range r.dependents {
			if d.IsHard() && d.From.State() != StateStopped {
				// Its stop would wait for that service to stop, or
				// take it down: neither is what a timer does to the
				// units after a oneshot.
				ss.logger.Info("Service '%s': %s fired; '%s' depends on it and is up, so the job is not run again",
					s.Name(), what, d.From.Name())
				return false
			}
		}
		ss.logger.Info("Service '%s': %s fired; running the job again", s.Name(), what)
		r.rerunJob = true
		r.Restart()
	default:
		return false
	}
	ss.processQueuesLocked()
	return true
}

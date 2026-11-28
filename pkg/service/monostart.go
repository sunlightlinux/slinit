package service

import (
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// MonoStarter starts services at monotonic times, a .timer's
// OnBootSec= and OnUnitActiveSec=:
//
//   - start-on-boot-sec: once, that long after the kernel booted (at
//     once if that is past when the service loads, as a timer started
//     late elapses at once);
//   - start-on-unit-active-sec: that long after the service last
//     started, again and again. The last start is the later of the
//     service's own (an operator's counts: a start re-arms the timer)
//     and the last one this timer made, so a run the timer started,
//     still coming up, does not make it fire again at once.
//
// A start is timerFire's: a stopped service is started, a finished
// scripted job run again. start-on-calendar-randomized-delay applies
// on top, as RandomizedDelaySec= does to every elapse.
//
// Its state belongs to one goroutine, which runs what Add, Remove, a
// start of a service and a timer queue, in order. Add and Remove may be
// called with queueMu held (a load from inside the state machine, as
// chain-to's), and the goroutine takes queueMu to read a service; with
// no lock of its own there is no order to get wrong.
type MonoStarter struct {
	set    *ServiceSet
	logger ServiceLogger

	qmu  sync.Mutex
	q    []func()
	wake chan struct{}
	done chan struct{}

	// The goroutine's own.
	timers   map[string]*time.Timer
	bootDone map[string]bool      // start-on-boot-sec has fired
	lastFire map[string]time.Time // the last start this timer made
	heard    map[Service]bool     // services listened to for their starts
	now      func() time.Time
	bootTime time.Time
	fires    int // elapses acted on, for tests
}

// NewMonoStarter returns a running starter for the services of ss.
func NewMonoStarter(ss *ServiceSet, logger ServiceLogger) *MonoStarter {
	m := &MonoStarter{set: ss, logger: logger, wake: make(chan struct{}, 1), done: make(chan struct{}),
		timers: map[string]*time.Timer{}, bootDone: map[string]bool{}, lastFire: map[string]time.Time{},
		heard: map[Service]bool{}, now: time.Now, bootTime: kernelBootTime()}
	go m.run()
	return m
}

// KernelBootTime is kernelBootTime, for slinitctl list-timers.
func KernelBootTime() time.Time { return kernelBootTime() }

// kernelBootTime is when the kernel booted, on the wall clock: now less
// CLOCK_BOOTTIME (time since boot, suspend included, as systemd's
// OnBootSec= counts).
func kernelBootTime() time.Time {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
		return time.Now()
	}
	return time.Now().Add(-time.Duration(ts.Nano()))
}

func (m *MonoStarter) do(f func()) {
	m.qmu.Lock()
	m.q = append(m.q, f)
	m.qmu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *MonoStarter) run() {
	for {
		select {
		case <-m.done:
			for _, t := range m.timers {
				t.Stop()
			}
			return
		case <-m.wake:
		}
		for {
			m.qmu.Lock()
			if len(m.q) == 0 {
				m.qmu.Unlock()
				break
			}
			f := m.q[0]
			m.q = m.q[1:]
			m.qmu.Unlock()
			f()
		}
	}
}

// Add arms svc's monotonic timers, replacing earlier ones; a service
// with none only has an earlier timer removed.
func (m *MonoStarter) Add(svc Service) {
	m.do(func() {
		if _, active := m.read(svc); active > 0 && !m.heard[svc] {
			m.heard[svc] = true
			svc.Record().AddListener(m)
		}
		m.arm(svc.Name())
	})
}

// Remove disarms name's timer (unloaded).
func (m *MonoStarter) Remove(name string) {
	m.do(func() {
		if t := m.timers[name]; t != nil {
			t.Stop()
			delete(m.timers, name)
		}
		delete(m.bootDone, name)
		delete(m.lastFire, name)
		for svc := range m.heard {
			if svc.Name() == name {
				svc.Record().RemoveListener(m)
				delete(m.heard, svc)
			}
		}
	})
}

// ServiceEvent re-arms on a start, which moves
// start-on-unit-active-sec on.
func (m *MonoStarter) ServiceEvent(svc Service, event ServiceEvent) {
	if event == EventStarted {
		m.do(func() {
			if m.heard[svc] {
				m.arm(svc.Name())
			}
		})
	}
}

// Close disarms every timer and stops the goroutine.
func (m *MonoStarter) Close() { close(m.done) }

type monoConf struct {
	boot, active, delay time.Duration
	started             time.Time
}

// read is svc's settings and last start, under queueMu.
func (m *MonoStarter) read(svc Service) (boot, active time.Duration) {
	c := m.conf(svc)
	return c.boot, c.active
}

func (m *MonoStarter) conf(svc Service) monoConf {
	m.set.queueMu.Lock()
	defer m.set.queueMu.Unlock()
	r := svc.Record()
	return monoConf{r.startOnBootSec, r.startOnUnitActiveSec, r.startOnCalendarDelay, r.startedTime}
}

// target is the next time name's timers name, or zero.
func (m *MonoStarter) target(name string) (time.Time, monoConf) {
	s := m.set.FindService(name, false)
	if s == nil {
		return time.Time{}, monoConf{}
	}
	c := m.conf(s)
	var target time.Time
	if c.boot > 0 && !m.bootDone[name] {
		target = m.bootTime.Add(c.boot)
	}
	if c.active > 0 {
		base := c.started
		if f := m.lastFire[name]; f.After(base) {
			base = f
		}
		if !base.IsZero() {
			if t := base.Add(c.active); target.IsZero() || t.Before(target) {
				target = t
			}
		}
	}
	return target, c
}

func (m *MonoStarter) arm(name string) {
	if t := m.timers[name]; t != nil {
		t.Stop()
		delete(m.timers, name)
	}
	target, c := m.target(name)
	if target.IsZero() {
		return
	}
	wait := target.Add(calendarJitter(c.delay)).Sub(m.now())
	var t *time.Timer
	t = time.AfterFunc(max(wait, 0), func() { m.do(func() { m.fired(name, t, target) }) })
	m.timers[name] = t
}

// fired is a timer's elapse, on the goroutine.
func (m *MonoStarter) fired(name string, t *time.Timer, target time.Time) {
	if m.timers[name] != t {
		return // replaced or removed since
	}
	delete(m.timers, name)
	now := m.now()
	// Started since it was armed (an operator's start moved the
	// interval on): arm again for the new time instead.
	if t2, _ := m.target(name); t2.After(target) && t2.After(now) {
		m.arm(name)
		return
	}
	_, c := m.target(name)
	if c.boot > 0 && !m.bootTime.Add(c.boot).After(now) {
		m.bootDone[name] = true
	}
	m.lastFire[name] = now
	m.fires++
	m.arm(name)
	if s := m.set.FindService(name, false); s != nil {
		m.set.timerFire(s, "a monotonic timer", func(r *ServiceRecord) bool {
			return r.startOnBootSec > 0 || r.startOnUnitActiveSec > 0
		})
	}
}

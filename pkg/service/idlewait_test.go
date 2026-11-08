package service

import (
	"strings"
	"testing"
	"time"
)

func newIdleService(set *ServiceSet, name string) *ProcessService {
	svc := NewProcessService(set, name)
	svc.SetCommand([]string{"/bin/sh", "-c", "while :; do sleep 60; done"})
	svc.SetStartWhenIdle(true)
	set.AddService(svc)
	return svc
}

// shortenIdleWait makes the bound quick for a test.
func shortenIdleWait(t *testing.T, d time.Duration) {
	t.Helper()
	old := idleMaxWait
	idleMaxWait = d
	t.Cleanup(func() { idleMaxWait = old })
}

func stopAll(set *ServiceSet, svcs ...Service) {
	for _, s := range svcs {
		set.StopService(s)
	}
	time.Sleep(300 * time.Millisecond)
}

// With nothing else starting, there is nothing to wait for.
func TestIdleStartsAtOnceWhenNothingElseStarts(t *testing.T) {
	set, _ := newTestSet()
	svc := newIdleService(set, "getty")
	begin := time.Now()
	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("state = %v", got)
	}
	if d := time.Since(begin); d > time.Second {
		t.Errorf("took %v with nothing else starting", d)
	}
	stopAll(set, svc)
}

// The service waits while another is busy, and starts when that one is done.
func TestIdleWaitsForABusyService(t *testing.T) {
	set, _ := newTestSet()
	busy := newScripted(t, set, "busy", []string{"/bin/sleep", "1"}, nil)
	svc := newIdleService(set, "getty")

	set.StartService(busy)
	set.StartService(svc)

	time.Sleep(400 * time.Millisecond)
	if busy.State() != StateStarting {
		t.Fatalf("setup: busy is %v, want STARTING", busy.State())
	}
	if st := svc.State(); st != StateStarting || svc.PID() > 0 {
		t.Errorf("state %v pid %d while another service starts: the wait did nothing", st, svc.PID())
	}
	if got := waitState(t, svc, StateStarted, 3*time.Second); got != StateStarted {
		t.Fatalf("state = %v after the other finished", got)
	}
	if busy.State() != StateStarted {
		t.Errorf("started before the other finished: it is %v", busy.State())
	}
	stopAll(set, svc, busy)
}

// The wait is bounded: a service that never finishes starting cannot hold
// the login prompt back for ever.
func TestIdleWaitIsBounded(t *testing.T) {
	shortenIdleWait(t, 500*time.Millisecond)
	set, _ := newTestSet()
	busy := newScripted(t, set, "busy", []string{"/bin/sleep", "30"}, nil)
	svc := newIdleService(set, "getty")

	set.StartService(busy)
	begin := time.Now()
	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 3*time.Second); got != StateStarted {
		t.Fatalf("state = %v past the bound", got)
	}
	if d := time.Since(begin); d < 400*time.Millisecond {
		t.Errorf("started after %v, before the bound", d)
	}
	stopAll(set, svc, busy)
}

// Two services waiting do not wait for each other, or the two gettys of a
// real boot would always take the whole bound.
func TestIdleServicesDoNotWaitForEachOther(t *testing.T) {
	set, _ := newTestSet()
	busy := newScripted(t, set, "busy", []string{"/bin/sleep", "1"}, nil)
	a, b := newIdleService(set, "getty1"), newIdleService(set, "getty2")
	begin := time.Now()
	set.StartService(busy)
	set.StartService(a)
	set.StartService(b)
	time.Sleep(300 * time.Millisecond)
	if a.State() != StateStarting || b.State() != StateStarting {
		t.Fatalf("setup: %v and %v, want both waiting", a.State(), b.State())
	}
	for _, s := range []*ProcessService{a, b} {
		if got := waitState(t, s, StateStarted, 6*time.Second); got != StateStarted {
			t.Fatalf("%s = %v", s.Name(), got)
		}
	}
	if d := time.Since(begin); d > 3*time.Second {
		t.Errorf("took %v: they waited for each other", d)
	}
	stopAll(set, a, b, busy)
}

// A service that only waits for its dependencies is not busy itself. This
// is boot, which waits for getty: counted, it would hold getty the whole
// bound on every boot.
func TestIdleIgnoresAServiceWaitingForItsDependencies(t *testing.T) {
	set, _ := newTestSet()
	svc := newIdleService(set, "getty")
	boot := NewInternalService(set, "boot")
	set.AddService(boot)
	boot.Record().AddDep(svc, DepWaitsFor)

	begin := time.Now()
	set.StartService(boot)
	if got := waitState(t, svc, StateStarted, 6*time.Second); got != StateStarted {
		t.Fatalf("getty = %v", got)
	}
	if d := time.Since(begin); d > 2*time.Second {
		t.Errorf("took %v: its dependent held it back", d)
	}
	stopAll(set, boot, svc)
}

// A stop during the wait cancels it, and nothing is launched afterwards.
func TestIdleStopCancelsTheWait(t *testing.T) {
	set, _ := newTestSet()
	busy := newScripted(t, set, "busy", []string{"/bin/sleep", "30"}, nil)
	svc := newIdleService(set, "getty")

	set.StartService(busy)
	set.StartService(svc)
	time.Sleep(200 * time.Millisecond)
	if svc.State() != StateStarting {
		t.Fatalf("setup: state %v, want STARTING", svc.State())
	}
	set.StopService(svc)
	if got := waitState(t, svc, StateStopped, 3*time.Second); got != StateStopped {
		t.Fatalf("state = %v after the stop", got)
	}
	time.Sleep(300 * time.Millisecond)
	if svc.State() != StateStopped || svc.PID() > 0 {
		t.Errorf("launched after being stopped: state %v pid %d", svc.State(), svc.PID())
	}
	stopAll(set, busy)
}

// Two services depend on the one that starts last: the idle one must wait for
// the other, which it took for idle while that was still queued.
func TestIdleWaitsForAServiceQueuedBehindTheSameDependency(t *testing.T) {
	for _, gettyFirst := range []bool{true, false} {
		set, _ := newTestSet()
		misc := newScripted(t, set, "misc", []string{"/bin/sleep", "0.2"}, nil)
		getty := newIdleService(set, "getty")
		chronyd := newScripted(t, set, "chronyd", []string{"/bin/sleep", "0.8"}, nil)
		getty.Record().AddDep(misc, DepRegular)
		chronyd.Record().AddDep(misc, DepRegular)
		if gettyFirst {
			set.StartService(getty)
			set.StartService(chronyd)
		} else {
			set.StartService(chronyd)
			set.StartService(getty)
		}
		if got := waitState(t, misc, StateStarted, 3*time.Second); got != StateStarted {
			t.Fatalf("setup: misc = %v", got)
		}
		time.Sleep(200 * time.Millisecond)
		if chronyd.State() != StateStarting {
			t.Fatalf("setup: chronyd = %v, want STARTING", chronyd.State())
		}
		if st := getty.State(); st != StateStarting || getty.PID() > 0 {
			t.Errorf("gettyFirst=%v: getty %v pid %d while chronyd is starting", gettyFirst, st, getty.PID())
		}
		if got := waitState(t, getty, StateStarted, 3*time.Second); got != StateStarted {
			t.Errorf("getty = %v after chronyd finished", got)
		}
		stopAll(set, getty, chronyd, misc)
	}
}

// A calendar task says when it runs: its interval is unused, and "interval=0s"
// read as a task in a tight loop.
func TestCronScheduleDescription(t *testing.T) {
	spec, err := ParseCalendar("daily")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 8, 16, 0, 0, 0, time.UTC)
	got := (&CronRunner{calendar: spec}).Schedule(now)
	if !strings.HasPrefix(got, "calendar, next at 2026-11-") || strings.Contains(got, "interval") {
		t.Errorf("calendar: %q", got)
	}
	if got := (&CronRunner{interval: time.Minute}).Schedule(now); got != "interval=1m0s" {
		t.Errorf("interval: %q", got)
	}
}

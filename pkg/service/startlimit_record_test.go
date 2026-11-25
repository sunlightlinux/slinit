package service

import (
	"runtime"
	"testing"
	"time"
)

// startLimitedPair sets up a limiter with one slot, held by a (which
// stays STARTING), and an interruptible b registered alongside it.
func startLimitedPair(t *testing.T, threshold time.Duration) (*ServiceSet, *testSvc, *testSvc) {
	t.Helper()
	set, _ := newTestSet()
	set.SetStartLimiter(1, threshold)
	a := newTestSvc(t, set, "a")
	a.autoStart = false
	b := newTestSvc(t, set, "b")
	b.interruptible = true
	set.AddService(a)
	set.AddService(b)
	set.StartService(a)
	return set, a, b
}

// finishStart completes a's start under the queue lock, as a real
// readiness notification would, and gives a woken waiter time to run.
func finishStart(set *ServiceSet, a *testSvc) {
	set.queueMu.Lock()
	a.Started()
	set.processQueuesLocked()
	set.queueMu.Unlock()
	time.Sleep(50 * time.Millisecond)
}

// A service stopped while queued for a start slot must not be brought
// up when the slot arrives: it used to be, on a STOPPED record, and the
// second ServiceInactive left the active count at -1 — shutdown then
// waited out the emergency timeout for it to reach 0.
func TestStartLimiter_StopWhileWaitingForSlot(t *testing.T) {
	set, a, b := startLimitedPair(t, time.Hour)
	set.StartService(b)
	if !b.waitingForStartSlot {
		t.Fatal("b should be waiting for a start slot")
	}
	set.StopService(b)
	if b.State() != StateStopped {
		t.Fatalf("b = %v after stop, want STOPPED", b.State())
	}
	if n := set.GetStartLimiter().WaiterCount(); n != 0 {
		t.Errorf("limiter still holds %d waiter(s) for a stopped service", n)
	}

	finishStart(set, a)
	set.queueMu.Lock()
	up := b.upCount
	set.queueMu.Unlock()
	if up != 0 {
		t.Errorf("b brought up %d time(s) after it was stopped", up)
	}
	if n := set.CountActiveServices(); n != 1 {
		t.Errorf("active count %d, want 1 (a only)", n)
	}
	set.StopAllServices(ShutdownPoweroff)
	if n := set.CountActiveServices(); n != 0 {
		t.Errorf("active count %d after everything stopped, want 0", n)
	}
}

// Start/stop cycles while waiting must not park a goroutine per cycle,
// nor bring the service up once per cycle when the slot finally frees.
func TestStartLimiter_RestartWhileWaitingForSlot(t *testing.T) {
	set, a, b := startLimitedPair(t, time.Hour)
	g0 := runtime.NumGoroutine()
	for i := 0; i < 100; i++ {
		set.StartService(b)
		set.StopService(b)
	}
	time.Sleep(50 * time.Millisecond)
	if extra := runtime.NumGoroutine() - g0; extra > 5 {
		t.Errorf("%d goroutines left parked by 100 cancelled slot waits", extra)
	}
	set.StartService(b)
	finishStart(set, a)
	set.queueMu.Lock()
	up, st := b.upCount, b.State()
	set.queueMu.Unlock()
	if up != 1 || st != StateStarted {
		t.Errorf("b upCount=%d state=%v for one start, want 1 STARTED", up, st)
	}
}

// An interrupted start (STARTING -> STOPPING via a stop, not a failure)
// must give its slot back; it used to keep it, and every later start
// waited behind a STOPPED service.
func TestStartLimiter_InterruptedStartReleasesSlot(t *testing.T) {
	set, a, b := startLimitedPair(t, time.Hour)
	a.interruptible = true
	set.StopService(a)
	if a.State() != StateStopped {
		t.Fatalf("a = %v after stop, want STOPPED", a.State())
	}
	if n := set.GetStartLimiter().TotalStarting(); n != 0 {
		t.Errorf("limiter still counts %d starter(s) after a's start was interrupted", n)
	}
	set.StartService(b)
	if b.State() != StateStarted {
		t.Errorf("b = %v, want STARTED: the only slot is still held by stopped a", b.State())
	}
}

// A starter past the slow threshold stops counting, so a waiter queued
// behind it must be woken then, not only when some other starter ends.
func TestStartLimiter_SlowStarterWakesWaiter(t *testing.T) {
	set, _, b := startLimitedPair(t, 50*time.Millisecond)
	set.StartService(b)
	deadline := time.Now().Add(time.Second)
	for {
		set.queueMu.Lock()
		st := b.State()
		set.queueMu.Unlock()
		if st == StateStarted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("b = %v 1s after a passed the 50ms slow threshold", st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A trigger set while the triggered service waits for a start slot must
// not start it there and then: the start waits for the slot, as it waits
// for its dependencies. Started while still queued, the service was later
// handed the slot it no longer needed, and since Started had already run
// nothing gave it back — every later start waited for the slow threshold.
func TestStartLimiter_TriggerWhileWaitingForSlot(t *testing.T) {
	set, _ := newTestSet()
	set.SetStartLimiter(1, time.Hour)
	a := newTestSvc(t, set, "a")
	a.autoStart = false
	set.AddService(a)
	trig := NewTriggeredService(set, "trig")
	set.AddService(trig)
	c := newTestSvc(t, set, "c")
	set.AddService(c)

	set.StartService(a)
	set.StartService(trig)
	set.SetTriggerService(trig, true)
	set.queueMu.Lock()
	st, waiting := trig.State(), trig.waitingForStartSlot
	set.queueMu.Unlock()
	if st != StateStarting || !waiting {
		t.Errorf("triggered while queued for a slot: %v, waiting %v; want STARTING, still waiting", st, waiting)
	}

	finishStart(set, a)
	if got := waitForState(t, trig, StateStarted, time.Second); got != StateStarted {
		t.Fatalf("trig = %v once a's slot was free, want STARTED", got)
	}

	set.StartService(c)
	if got := waitForState(t, c, StateStarted, time.Second); got != StateStarted {
		set.queueMu.Lock()
		n := set.GetStartLimiter().TotalStarting()
		set.queueMu.Unlock()
		t.Errorf("c = %v with nothing else starting (limiter holds %d slot(s)), want STARTED", got, n)
	}
}

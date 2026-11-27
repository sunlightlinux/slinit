package service

import "testing"

// A stop asked of a start-pinned service is put off until the pin goes.
// Unpin carried it out only for a STARTED service, so a service pinned
// while STARTING and unpinned before it started kept a stop nobody would
// ever make: the shutdown's own unpin was enough, and the shutdown then
// sat out its emergency timeout.

func assertAllStopped(t *testing.T, set *ServiceSet, svcs ...Service) {
	t.Helper()
	for _, s := range svcs {
		if s.State() != StateStopped {
			t.Errorf("%s still %v at the end (requiredBy %d)", s.Name(), s.State(), s.Record().RequiredBy())
		}
	}
	if n := set.CountActiveServices(); n != 0 {
		t.Errorf("%d services still counted active", n)
	}
}

// (a) `slinitctl --no-wait --pin start trig` for a triggered service
// that is still waiting for its trigger, then a shutdown.
func TestUnpinStartingTriggeredStopsAtShutdown(t *testing.T) {
	set, _ := newTestSet()
	trig := NewTriggeredService(set, "trig")
	set.AddService(trig)

	set.StartServiceUnlessShuttingDown(trig, true)
	if trig.State() != StateStarting {
		t.Fatalf("trig=%v, want STARTING", trig.State())
	}

	set.StopAllServices(ShutdownPoweroff)
	assertAllStopped(t, set, trig)
}

// The same without a shutdown: `start --pin trig`, `stop trig` (put off
// by the pin), `unpin trig`.
func TestUnpinStartingCarriesOutDeferredStop(t *testing.T) {
	set, _ := newTestSet()
	trig := NewTriggeredService(set, "trig")
	set.AddService(trig)

	set.StartServiceUnlessShuttingDown(trig, true)
	set.StopService(trig)
	if trig.State() != StateStarting {
		t.Fatalf("trig=%v after a stop of the pinned service, want STARTING", trig.State())
	}
	set.UnpinService(trig)
	assertAllStopped(t, set, trig)
}

// (b) s1 depends-on s0 (s0 slow to start), s2 waits-for s1. start s2,
// release s2, start --pin s1, shutdown, then s0 completes its start.
func TestUnpinStartingWithSlowDependencyStopsAtShutdown(t *testing.T) {
	set, _ := newTestSet()
	svcs := addTestSvcs(t, set, "s0", "s1", "s2")
	s0, s1, s2 := svcs[0], svcs[1], svcs[2]
	s0.autoStart = false
	if err := set.AddDependency(s1, s0, DepRegular); err != nil {
		t.Fatal(err)
	}
	if err := set.AddDependency(s2, s1, DepWaitsFor); err != nil {
		t.Fatal(err)
	}

	set.StartServiceUnlessShuttingDown(s2, false)
	set.ReleaseService(s2)
	set.StartServiceUnlessShuttingDown(s1, true)
	set.StopAllServices(ShutdownPoweroff)
	set.WithGraphLock(func() {
		if s0.State() == StateStarting && s0.upReqd {
			s0.Started()
			set.processQueuesLocked()
		}
	})
	assertAllStopped(t, set, s0, s1, s2)
}

// (c) A start-pinned dependent that is STARTING but still waiting for
// another dependency counts as stopped to its hard dependencies, which
// may then stop underneath it while it still holds them. Here the pin
// cannot keep d up: the edge in -> d is added after the pin was made.
// d stops with in still holding it, so it stays counted active.
func cPinnedStartingSetup(t *testing.T) (*ServiceSet, []*testSvc) {
	set, _ := newTestSet()
	sv := addTestSvcs(t, set, "d", "slow", "in", "top")
	d, slow, in, top := sv[0], sv[1], sv[2], sv[3]
	slow.autoStart = false
	for _, e := range []struct {
		from, to *testSvc
		typ      DependencyType
	}{{slow, d, DepWaitsFor}, {in, slow, DepWaitsFor}, {top, in, DepRegular}} {
		if err := set.AddDependency(e.from, e.to, e.typ); err != nil {
			t.Fatal(err)
		}
	}
	set.StartServiceUnlessShuttingDown(top, true)
	if err := set.AddDependency(in, d, DepRegular); err != nil {
		t.Fatal(err)
	}
	if in.State() != StateStarting || !in.IsStartPinned() {
		t.Fatalf("in=%v pinned=%v, want STARTING and pinned", in.State(), in.IsStartPinned())
	}
	return set, sv
}

func completeStart(set *ServiceSet, s *testSvc) {
	set.WithGraphLock(func() {
		if s.upReqd && s.State() == StateStarting {
			s.Started()
			s.upReqd = false
			set.processQueuesLocked()
		}
	})
}

// d is released by in only after it has stopped.
func TestPinnedStartingDependentReleasesStoppedDependency(t *testing.T) {
	set, sv := cPinnedStartingSetup(t)
	d, slow := sv[0], sv[1]
	set.StopService(d)
	if d.State() != StateStopped || d.Record().RequiredBy() == 0 {
		t.Fatalf("d=%v requiredBy=%d, want STOPPED and still required", d.State(), d.Record().RequiredBy())
	}
	set.StopAllServices(ShutdownPoweroff)
	completeStart(set, slow)
	assertAllStopped(t, set, sv[0], sv[1], sv[2], sv[3])
}

// d, stopped under in, is started again before the shutdown (the restart
// a `slinitctl restart d` makes).
func TestPinnedStartingDependentRestartedDependency(t *testing.T) {
	set, sv := cPinnedStartingSetup(t)
	d, slow := sv[0], sv[1]
	set.MarkRestartingService(d)
	set.StopService(d)
	set.StartService(d)
	set.StopAllServices(ShutdownPoweroff)
	completeStart(set, slow)
	assertAllStopped(t, set, sv[0], sv[1], sv[2], sv[3])
}

// A service pinned only through a pinned dependent (dependency pin) is
// in the same position: top depends-on trig, a triggered service still
// waiting for its trigger. `start --pin top`, `stop --force trig` (put
// off: trig is pinned through top), `unpin top`: losing that pin while
// STARTING did not carry out the forced stop, and both stayed STARTING.
func TestDependencyUnpinStartingStopsService(t *testing.T) {
	set, _ := newTestSet()
	trig := NewTriggeredService(set, "trig")
	set.AddService(trig)
	top := newTestSvc(t, set, "top")
	set.AddService(top)
	if err := set.AddDependency(top, trig, DepRegular); err != nil {
		t.Fatal(err)
	}

	set.StartServiceUnlessShuttingDown(top, true)
	if trig.State() != StateStarting || !trig.IsStartPinned() {
		t.Fatalf("trig=%v pinned=%v, want STARTING and pinned through top", trig.State(), trig.IsStartPinned())
	}
	set.ForceStopService(trig)
	if trig.State() != StateStarting {
		t.Fatalf("trig=%v after a forced stop put off by the pin, want STARTING", trig.State())
	}
	set.UnpinService(top)
	assertAllStopped(t, set, top, trig)
}

// A service pinned while STOPPING, by a `start --pin` that cannot
// interrupt the stop, can stop with the pin up when a forced stop of
// its dependency reaches it: the release of its activation is put off
// by the pin, and Unpin, finding it STOPPED, left it counted active.
func TestUnpinStoppedServiceLeavesActiveCount(t *testing.T) {
	set, _ := newTestSet()
	sv := addTestSvcs(t, set, "dep", "svc")
	dep, svc := sv[0], sv[1]
	svc.autoStop = false
	set.StartServiceUnlessShuttingDown(dep, false)
	set.StartServiceUnlessShuttingDown(svc, false)
	set.StopService(svc)
	set.StartServiceUnlessShuttingDown(svc, true)
	if svc.State() != StateStopping || !svc.IsStartPinned() {
		t.Fatalf("svc=%v pinned=%v, want STOPPING and pinned", svc.State(), svc.IsStartPinned())
	}
	if err := set.AddDependency(svc, dep, DepRegular); err != nil {
		t.Fatal(err)
	}
	set.ForceStopService(dep)
	set.WithGraphLock(func() {
		svc.Stopped()
		set.processQueuesLocked()
	})
	set.StopAllServices(ShutdownPoweroff)
	assertAllStopped(t, set, dep, svc)
}

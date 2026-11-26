package service

import "testing"

// in depends-on sc, and `start --pin in` comes while sc is still
// starting, so in is STARTING too, waiting for it. The shutdown drops
// in's activation and its pin: with its pin up, Stop put the release of
// its dependencies off, and Unpin propagates that release only for a
// service already STARTED. When sc then started, in started, saw it was
// no longer wanted and stopped, still holding sc. sc stayed STARTED,
// with no stop or start logged after the shutdown began, and the
// shutdown sat out its emergency timeout (seen with a scripted sc in the
// -race stress run of the daemon).
func TestUnpinWhileStartingReleasesDependencies(t *testing.T) {
	set, _ := newTestSet()
	sc := newTestSvc(t, set, "sc")
	sc.autoStart = false
	in := newTestSvc(t, set, "in")
	set.AddService(sc)
	set.AddService(in)
	if err := set.AddDependency(in, sc, DepRegular); err != nil {
		t.Fatal(err)
	}

	set.StartServiceUnlessShuttingDown(in, true)
	if in.State() != StateStarting || sc.State() != StateStarting {
		t.Fatalf("in=%v sc=%v, want both STARTING", in.State(), sc.State())
	}

	set.StopAllServices(ShutdownPoweroff)
	set.WithGraphLock(func() {
		sc.Started()
		set.processQueuesLocked()
	})

	for _, s := range []*testSvc{in, sc} {
		if s.State() != StateStopped {
			t.Errorf("%s still %v after the shutdown (requiredBy %d)", s.Name(), s.State(), s.Record().RequiredBy())
		}
	}
	if n := set.CountActiveServices(); n != 0 {
		t.Errorf("%d services still counted active", n)
	}
}

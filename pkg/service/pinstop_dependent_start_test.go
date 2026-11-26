package service

import "testing"

// `stop --pin d` while d is still stopping, then a start of t, which
// depends on d: t requires d, d's desired state goes back to STARTED,
// and t waits for it. When d finished stopping, its stop pin refused
// the restart, so t was left waiting for good, and d stayed counted
// active, held by t. The shutdown then stopped t, which let go of d,
// but a release reaching 0 on a STOPPED service does not mark it
// inactive: every service was STOPPED, the active count stayed at 1,
// and the shutdown sat out its emergency timeout without being able to
// name a blocker.
func TestStopPinnedWhileStoppingFailsDependentStart(t *testing.T) {
	set, _ := newTestSet()
	d := newTestSvc(t, set, "d")
	d.autoStop = false
	dep := newTestSvc(t, set, "t")
	set.AddService(d)
	set.AddService(dep)
	if err := set.AddDependency(dep, d, DepRegular); err != nil {
		t.Fatal(err)
	}

	set.StartService(d)
	set.StopService(d)
	set.PinStopService(d)
	if d.State() != StateStopping {
		t.Fatalf("d %v, want STOPPING", d.State())
	}
	set.StartService(dep)
	set.WithGraphLock(func() {
		d.Stopped()
		set.processQueuesLocked()
	})
	if dep.State() != StateStopped {
		t.Errorf("t still %v after its dependency's stop pin refused to start it", dep.State())
	}

	set.StopAllServices(ShutdownPoweroff)
	for _, s := range []*testSvc{d, dep} {
		if s.State() != StateStopped {
			t.Errorf("%s still %v after the shutdown", s.Name(), s.State())
		}
	}
	if n := set.CountActiveServices(); n != 0 {
		t.Errorf("%d services still counted active after every service stopped", n)
	}
}

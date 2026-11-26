package service

import "testing"

// top depends-on mid depends-on leaf, and `start --pin top` passes the
// pin down both edges. Removing the edge mid -> leaf (rm-dep) left leaf
// marked as pinned by a dependent it no longer had: it could not be
// stopped any more, and the shutdown waited for it until its timeout.
func TestRmDepDropsPinItCarried(t *testing.T) {
	set, _ := newTestSet()
	leaf := NewInternalService(set, "leaf")
	mid := NewInternalService(set, "mid")
	top := NewInternalService(set, "top")
	for _, s := range []Service{leaf, mid, top} {
		set.AddService(s)
	}
	if err := set.AddDependency(mid, leaf, DepRegular); err != nil {
		t.Fatal(err)
	}
	if err := set.AddDependency(top, mid, DepRegular); err != nil {
		t.Fatal(err)
	}
	set.StartServiceUnlessShuttingDown(top, true)
	if !leaf.Record().IsStartPinned() {
		t.Fatal("leaf did not get the pin")
	}

	if !set.RemoveDependency(mid, leaf, DepRegular) {
		t.Fatal("rm-dep failed")
	}
	if leaf.Record().IsStartPinned() {
		t.Error("leaf still pinned after the edge that pinned it was removed")
	}

	set.StopAllServices(ShutdownPoweroff)
	for _, s := range []Service{leaf, mid, top} {
		if s.State() != StateStopped {
			t.Errorf("%s still %v after the shutdown", s.Name(), s.State())
		}
	}
	if n := set.CountActiveServices(); n != 0 {
		t.Errorf("%d services still counted active", n)
	}
}

// d depends-on leaf. A restart of d stops it (slowly), which releases
// leaf, and leaf starts stopping too, waiting for d; the restart's
// start then keeps d coming back. Removing the edge d -> leaf (rm-dep)
// took away the dependent leaf was waiting for without telling it, so
// leaf stayed STOPPING for good and the shutdown waited for it until
// its timeout.
func TestRmDepWakesDependencyWaitingToStop(t *testing.T) {
	set, _ := newTestSet()
	leaf := newTestSvc(t, set, "leaf")
	d := newTestSvc(t, set, "d")
	d.autoStop = false
	set.AddService(leaf)
	set.AddService(d)
	if err := set.AddDependency(d, leaf, DepRegular); err != nil {
		t.Fatal(err)
	}
	set.StartService(d)

	// As handleStopService with the restart flag.
	set.MarkRestartingService(d)
	set.StopService(d)
	set.StartService(d)
	if leaf.State() != StateStopping {
		t.Fatalf("leaf %v, want STOPPING (waiting for d)", leaf.State())
	}

	if !set.RemoveDependency(d, leaf, DepRegular) {
		t.Fatal("rm-dep failed")
	}
	if leaf.State() != StateStopped {
		t.Errorf("leaf still %v with no dependent left to wait for", leaf.State())
	}

	set.StopAllServices(ShutdownPoweroff)
	set.WithGraphLock(func() {
		if d.State() == StateStopping {
			d.Stopped()
			set.processQueuesLocked()
		}
	})
	for _, s := range []*testSvc{leaf, d} {
		if s.State() != StateStopped {
			t.Errorf("%s still %v after the shutdown", s.Name(), s.State())
		}
	}
	if n := set.CountActiveServices(); n != 0 {
		t.Errorf("%d services still counted active", n)
	}
}

package service

import "testing"

// top depends-on mid depends-on leaf, and mid is pinned stopped. `start
// --pin top` fails (mid refuses to start) and pins top anyway, which
// passes the pin down to mid and leaf. A later start of top failed
// again, and the failure dropped top's pin without telling its
// dependencies, so leaf stayed pinned by a dependent that no longer
// pinned anything: it could not be stopped, at shutdown either.
func TestFailedStartDropsPinFromDependencies(t *testing.T) {
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
	set.StartService(leaf)
	set.PinStopService(mid)
	set.StartServiceUnlessShuttingDown(top, true)
	set.StartService(top)
	if top.Record().IsStartPinned() {
		t.Fatal("top still pinned after its start failed")
	}
	if leaf.Record().IsStartPinned() {
		t.Error("leaf still pinned after the failed start dropped the pin of top")
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

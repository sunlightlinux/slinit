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

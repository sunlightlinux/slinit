package snapshot_test

import (
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
	"github.com/sunlightlinux/slinit/pkg/snapshot"
)

// buildPinnedPair returns a set where a depends on b.
func buildPinnedPair() (*service.ServiceSet, *service.InternalService, *service.InternalService) {
	set := newSet()
	a := service.NewInternalService(set, "a")
	b := service.NewInternalService(set, "b")
	set.AddService(a)
	set.AddService(b)
	a.AddDep(b, service.DepRegular)
	return set, a, b
}

// A start pin on a service holds its hard dependencies up, but only
// through it: the dependency's pin is inherited, not the operator's. The
// snapshot used to record it as an explicit pin, so after a soft reboot
// the dependency stayed pinned once the dependent was unpinned and
// stopped — measured: b STARTED and still pinned, where before the soft
// reboot the same unpin and stop took b down.
func TestCaptureRecordsOnlyExplicitStartPin(t *testing.T) {
	set, a, _ := buildPinnedPair()
	set.StartService(a)
	set.PinStartService(a)
	snap := snapshot.Capture(set)
	for _, e := range snap.Services {
		if e.Name == "b" && e.PinnedStart {
			t.Errorf("dependency captured as start-pinned: %+v", e)
		}
		if e.Name == "a" && !e.PinnedStart {
			t.Errorf("pinned service not captured as start-pinned: %+v", e)
		}
	}

	set2, a2, b2 := buildPinnedPair()
	if _, err := snapshot.Restore(set2, snap, nil); err != nil {
		t.Fatal(err)
	}
	if b2.State() != service.StateStarted || !b2.IsStartPinned() {
		t.Fatalf("restored: b %v, pinned %v; want STARTED and pinned through a",
			b2.State(), b2.IsStartPinned())
	}
	set2.UnpinService(a2)
	set2.StopService(a2)
	if b2.State() != service.StateStopped || b2.IsStartPinned() {
		t.Errorf("after unpin and stop of a: b %v, pinned %v; want STOPPED, unpinned",
			b2.State(), b2.IsStartPinned())
	}
}

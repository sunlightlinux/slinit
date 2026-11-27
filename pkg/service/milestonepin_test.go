package service

import "testing"

// A start pin reaches a milestone dependency while the dependent waits
// for it (the edge is hard until then). Once the milestone has started
// the edge is soft, and unpin walked only hard edges, so the dependency
// kept the pin it had inherited for good: neither a stop, a forced stop
// nor a shutdown could bring it down. Found by FuzzControlServer
// (start --pin on a service with a scripted depends-ms, then teardown
// left the scripted service STARTED forever).
func TestMilestonePinReleasedAfterDependencyStarted(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(set *ServiceSet, top, ms Service)
	}{
		{"unpin+stop", func(set *ServiceSet, top, ms Service) {
			set.UnpinService(top)
			set.StopService(top)
		}},
		{"force-stop", func(set *ServiceSet, top, ms Service) {
			set.UnpinService(top)
			set.ForceStopService(top)
			set.ForceStopService(ms)
		}},
		{"shutdown", func(set *ServiceSet, top, ms Service) {
			set.StopAllServices(ShutdownPoweroff)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set, _ := newTestSet()
			ms := NewTriggeredService(set, "ms")
			top := NewInternalService(set, "top")
			set.AddService(ms)
			set.AddService(top)
			top.Record().AddDep(ms, DepMilestone)

			// Pinned while ms is still STARTING: the pin reaches it.
			set.StartService(top)
			set.PinStartService(top)
			if ms.State() != StateStarting {
				t.Fatalf("ms is %v, want STARTING", ms.State())
			}
			set.SetTriggerService(ms, true)
			if top.State() != StateStarted || ms.State() != StateStarted {
				t.Fatalf("top %v, ms %v; want both STARTED", top.State(), ms.State())
			}

			tc.stop(set, top, ms)
			if top.State() != StateStopped {
				t.Fatalf("top is %v, want STOPPED", top.State())
			}
			if ms.Record().IsStartPinned() {
				t.Errorf("ms still start-pinned after its pinned dependent stopped")
			}
			if ms.State() != StateStopped {
				t.Errorf("ms is %v after its only dependent stopped, want STOPPED", ms.State())
			}
		})
	}
}

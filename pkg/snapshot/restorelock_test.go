package snapshot_test

import (
	"sync"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
	"github.com/sunlightlinux/slinit/pkg/snapshot"
)

// Restore runs after boot has started services, whose exit and timer
// handlers change state under the queue lock; its pins and triggers must
// take that lock too. Run under -race: before the fix the detector
// reported SetTrigger and PinStop against the churning goroutine.
func TestRestoreHoldsQueueLock(t *testing.T) {
	set := newSet()
	trig := service.NewTriggeredService(set, "trig")
	set.AddService(trig)
	dept := service.NewInternalService(set, "dept")
	set.AddService(dept)
	dept.AddDep(trig, service.DepRegular)
	down := service.NewInternalService(set, "down")
	set.AddService(down)

	snap := &snapshot.Snapshot{
		Services: []snapshot.ServiceSnapshot{
			{Name: "trig", Triggered: true},
			{Name: "down", PinnedStop: true},
		},
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			set.StartService(dept)
			set.StartService(down)
			set.StopService(down)
			set.StopService(dept)
		}
	}()
	for i := 0; i < 200; i++ {
		if _, err := snapshot.Restore(set, snap, nil); err != nil {
			t.Fatal(err)
		}
		set.SetTriggerService(trig, false)
		set.UnpinService(down)
	}
	close(stop)
	wg.Wait()
}

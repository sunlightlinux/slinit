package service

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

// Found by the cross-commit review. A service added after StopAllServices took its snapshot, and started
// (through StartServiceUnlessShuttingDown) before StopAllServices got
// queueMu, is never stopped.
func TestStartRacingShutdownOfNewlyLoadedService(t *testing.T) {
	escaped := 0
	for i := 0; i < 3000; i++ {
		ss, _ := newTestSet()
		var stop atomic.Bool
		var wg sync.WaitGroup
		var mu sync.Mutex
		var startedOK []Service
		for w := 0; w < 3; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for n := 0; !stop.Load(); n++ {
					x := NewInternalService(ss, fmt.Sprintf("x%d-%d", w, n))
					ss.WithGraphLock(func() { ss.AddService(x) }) // as handleLoadService
					if ss.StartServiceUnlessShuttingDown(x, false) {
						mu.Lock()
						startedOK = append(startedOK, x)
						mu.Unlock()
					} else {
						return
					}
					runtime.Gosched()
				}
			}(w)
		}
		runtime.Gosched()
		ss.StopAllServices(ShutdownPoweroff)
		stop.Store(true)
		wg.Wait()
		for _, x := range startedOK {
			if x.State() != StateStopped {
				escaped++
				if escaped == 1 {
					t.Logf("attempt %d: %s %v after StopAllServices", i, x.Name(), x.State())
				}
			}
		}
	}
	if escaped > 0 {
		t.Errorf("%d services loaded+started across a shutdown stayed up", escaped)
	}
}

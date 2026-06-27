package process

import (
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestExitRouter_RouteDeliversToRegisteredWaiter(t *testing.T) {
	r := NewExitRouter()
	ch := r.Register(1234)

	// Build a WaitStatus equivalent to "exited with code 7" so the
	// receiver path sees a non-zero exit, mirroring the production
	// scenario the router exists to fix.
	want := syscall.WaitStatus(7 << 8)

	if ok := r.Route(1234, want); !ok {
		t.Fatalf("Route returned false for registered pid")
	}
	select {
	case got := <-ch:
		if got != want {
			t.Errorf("got status %v, want %v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("Route did not deliver to channel")
	}
}

func TestExitRouter_RouteUnknownPidReturnsFalse(t *testing.T) {
	r := NewExitRouter()
	if ok := r.Route(9999, 0); ok {
		t.Errorf("Route(unregistered) returned true, want false")
	}
}

func TestExitRouter_UnregisterIsIdempotent(t *testing.T) {
	r := NewExitRouter()
	r.Register(42)
	r.Unregister(42)
	r.Unregister(42) // must not panic / leak

	// After Unregister, Route must report unknown.
	if ok := r.Route(42, 0); ok {
		t.Errorf("Route after Unregister returned true, want false")
	}
}

func TestExitRouter_RouteConsumesEntry(t *testing.T) {
	// First Route delivers; a second Route to the same pid (e.g. pid
	// reuse after the kernel recycles the number) must NOT replay the
	// old status into the channel.
	r := NewExitRouter()
	r.Register(100)
	if !r.Route(100, syscall.WaitStatus(5<<8)) {
		t.Fatalf("first Route returned false")
	}
	if r.Route(100, syscall.WaitStatus(6<<8)) {
		t.Errorf("second Route returned true; entry should be consumed")
	}
}

func TestExitRouter_ConcurrentRegisterRoute(t *testing.T) {
	// Stress: many goroutines Register+Route in parallel. With -race this
	// would flag any unsynchronized access to the internal map. The test
	// also checks that every Register sees its own Route, regardless of
	// scheduling.
	r := NewExitRouter()
	const N = 200
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		pid := 1000 + i
		want := syscall.WaitStatus(i << 8)
		go func() {
			defer wg.Done()
			ch := r.Register(pid)
			// Route from another goroutine after a tiny delay to make
			// the receiver block on the channel for at least one
			// scheduling round-trip.
			go func() {
				time.Sleep(time.Microsecond)
				r.Route(pid, want)
			}()
			select {
			case got := <-ch:
				if got != want {
					t.Errorf("pid %d: got %v, want %v", pid, got, want)
				}
			case <-time.After(2 * time.Second):
				t.Errorf("pid %d: Route never delivered", pid)
			}
		}()
	}
	wg.Wait()
}

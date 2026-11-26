package service

import "testing"

// asyncTestService is an internal service whose start completes only
// when the test calls Started.
type asyncTestService struct{ InternalService }

func (s *asyncTestService) BringUp() bool { return true }

func newAsyncTestService(set *ServiceSet, name string) *asyncTestService {
	s := &asyncTestService{}
	s.ServiceRecord = *NewServiceRecord(s, set, name, TypeInternal)
	set.AddService(s)
	return s
}

// started reports a pending start as complete and runs the queues, as a
// process service's readiness does.
func started(set *ServiceSet, s Service) {
	set.queueMu.Lock()
	defer set.queueMu.Unlock()
	s.Record().Started()
	set.processQueuesLocked()
}

// checkDepsStarted resumes where its last call stopped instead of
// rescanning every dependency. A start waiting on several dependencies
// must still complete when, and only when, the last one is up, in
// whatever order they come up.
func TestDepsStartedResumeCompletesOnLastDep(t *testing.T) {
	for _, order := range [][]int{{0, 1, 2, 3}, {3, 2, 1, 0}, {1, 3, 0, 2}} {
		set, _ := newTestSet()
		boot := NewInternalService(set, "boot")
		set.AddService(boot)
		deps := make([]*asyncTestService, 4)
		for i := range deps {
			deps[i] = newAsyncTestService(set, string(rune('a'+i)))
			boot.Record().AddDep(deps[i], DepRegular)
		}
		set.StartService(boot)
		for n, i := range order {
			if st := boot.State(); st != StateStarting {
				t.Fatalf("order %v: boot %v with %d of 4 deps up", order, st, n)
			}
			started(set, deps[i])
		}
		if st := boot.State(); st != StateStarted {
			t.Fatalf("order %v: boot %v with every dep up", order, st)
		}
	}
}

// An ordering edge (y after x) is skipped while x is not starting, so a
// check can resume past it; when x then starts, it marks that edge
// waited on again, behind the resume point. y must wait for x.
func TestDepsStartedResumeSeesOrderingEdgeRearmed(t *testing.T) {
	set, _ := newTestSet()
	x := newAsyncTestService(set, "x")
	w := newAsyncTestService(set, "w")
	z := newAsyncTestService(set, "z")
	y := NewInternalService(set, "y")
	set.AddService(y)
	y.Record().AddDep(x, DepAfter)
	y.Record().AddDep(w, DepRegular)
	y.Record().AddDep(z, DepRegular)

	set.StartService(y)
	started(set, w) // y checks its edges: past x and w, stops at z
	if got := y.Record().depsStartedFrom; got != 2 {
		t.Fatalf("resume point %d after w came up, want 2", got)
	}
	set.StartService(x) // x starting: y's edge to x is waited on again
	started(set, z)
	if st := y.State(); st != StateStarting {
		t.Fatalf("y %v while x, which it is ordered after, is still starting", st)
	}
	started(set, x)
	if st := y.State(); st != StateStarted {
		t.Fatalf("y %v once x is up", st)
	}
}

// Removing an edge ahead of the resume point shifts the rest down by
// one; the edge that moved into the resume point must still be checked.
func TestDepsStartedResumeAfterRemovingAnEdge(t *testing.T) {
	set, _ := newTestSet()
	a := newAsyncTestService(set, "a")
	b := newAsyncTestService(set, "b")
	c := newAsyncTestService(set, "c")
	y := NewInternalService(set, "y")
	set.AddService(y)
	y.Record().AddDep(a, DepRegular)
	y.Record().AddDep(b, DepRegular)
	y.Record().AddDep(c, DepRegular)

	set.StartService(y)
	started(set, a) // resume point now at b
	if got := y.Record().depsStartedFrom; got != 1 {
		t.Fatalf("resume point %d after a came up, want 1", got)
	}
	set.WithGraphLock(func() { y.Record().RmDep(a, DepRegular) }) // [b c]
	started(set, c)
	if st := y.State(); st != StateStarting {
		t.Fatalf("y %v while b is still starting", st)
	}
	started(set, b)
	if st := y.State(); st != StateStarted {
		t.Fatalf("y %v once b is up", st)
	}
}

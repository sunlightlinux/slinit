package service

import (
	"fmt"
	"sync"
	"testing"
)

// The dependency graph is two slices per record — dependsOn on the source
// and dependents on the target — and AddDep/RmDep append to and splice
// both. Every other mutator of service state runs under the set's
// queueMu; these did not, and the control server gives each connection
// its own goroutine. Two concurrent `slinitctl add-dep`, or one of them
// against the state machine walking the graph after a process exit, is a
// data race on those slices: a lost edge, or an index into a slice that
// was reallocated underneath the walk.
//
// This test runs the shape deliberately. Under `-race` it is the detector
// that decides; without it, the assertion at the end still catches lost
// edges, because N adds must leave N edges.
//
// The reader goroutines below go through the set's locked read API. The
// first version of this file had them call CheckCircularDep and
// Dependents() directly — which is literally what handleQueryDependents
// and handleQueryDependencies did — and `-race` reported it. That is the
// evidence the race was real and not just theoretically possible, and it
// is why those two handlers now snapshot under the lock as well.

func raceTestServices(set *ServiceSet, n int) []Service {
	svcs := make([]Service, n)
	for i := range svcs {
		svcs[i] = NewInternalService(set, fmt.Sprintf("race-svc-%d", i))
		set.AddService(svcs[i])
	}
	return svcs
}

// Concurrent add-dep from different sources onto one shared target. The
// contended slice is the target's `dependents`, which every one of them
// appends to.
func TestConcurrentAddDependencyOnSharedTarget(t *testing.T) {
	set, _ := newTestSet()
	const n = 16
	svcs := raceTestServices(set, n+1)
	target := svcs[n]

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(from Service) {
			defer wg.Done()
			if err := set.AddDependency(from, target, DepWaitsFor); err != nil {
				t.Errorf("AddDependency: %v", err)
			}
		}(svcs[i])
	}
	wg.Wait()

	if got := len(target.Record().Dependents()); got != n {
		t.Errorf("target has %d dependents, want %d — an append was lost", got, n)
	}
}

// Adds against a reader walking the same graph: the state machine does
// this on every propagation, and CheckCircularDep does it on the control
// path. A walk that observes a half-published append is the part `-race`
// reports and a crash in production would not explain.
func TestConcurrentAddDependencyWhileWalking(t *testing.T) {
	set, _ := newTestSet()
	const n = 12
	svcs := raceTestServices(set, n+1)
	target := svcs[n]

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// Exactly what the control path does: the circularity
				// question, and the dependents walk that
				// query-dependents answers with.
				_ = set.WouldCycle(svcs[0], target)
				for _, dep := range set.DependentsOf(target) {
					_ = dep.From.Record().DepDepth()
				}
			}
		}()
	}

	var writers sync.WaitGroup
	for i := 0; i < n; i++ {
		writers.Add(1)
		go func(from Service) {
			defer writers.Done()
			if err := set.AddDependency(from, target, DepWaitsFor); err != nil {
				t.Errorf("AddDependency: %v", err)
			}
		}(svcs[i])
	}
	writers.Wait()
	close(stop)
	readers.Wait()
}

// Add and remove the same edge repeatedly from several goroutines. RmDep
// splices both slices, which is the mutation most likely to leave a
// dangling index for a concurrent walk.
func TestConcurrentAddAndRemoveDependency(t *testing.T) {
	set, _ := newTestSet()
	svcs := raceTestServices(set, 9)
	target := svcs[8]

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(from Service) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if err := set.AddDependency(from, target, DepWaitsFor); err != nil {
					t.Errorf("AddDependency: %v", err)
					return
				}
				set.RemoveDependency(from, target, DepWaitsFor)
			}
		}(svcs[i])
	}
	wg.Wait()

	if got := len(target.Record().Dependents()); got != 0 {
		t.Errorf("target still has %d dependents after every add was removed", got)
	}
}

// A cycle must be impossible to create even when both halves race. The
// check and the append have to be one atomic step: two goroutines that
// each pass CheckCircularDep against the pre-add graph and then both
// append will have built the cycle neither of them was allowed to.
func TestConcurrentAddDependencyCannotBuildACycle(t *testing.T) {
	set, _ := newTestSet()
	svcs := raceTestServices(set, 2)
	a, b := svcs[0], svcs[1]

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = set.AddDependency(a, b, DepWaitsFor) }()
	go func() { defer wg.Done(); errs[1] = set.AddDependency(b, a, DepWaitsFor) }()
	wg.Wait()

	// Exactly one of the two directions may be installed.
	if errs[0] == nil && errs[1] == nil {
		t.Error("both a→b and b→a were accepted — that is a cycle, and the " +
			"circularity check was not atomic with the add")
	}
	if errs[0] != nil && errs[1] != nil {
		t.Errorf("both directions were refused (%v / %v) — one had to win",
			errs[0], errs[1])
	}
	if CheckCircularDep(a, b) && CheckCircularDep(b, a) {
		t.Error("the graph contains a cycle between a and b")
	}
}

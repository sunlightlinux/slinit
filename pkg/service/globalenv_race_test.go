package service

import (
	"fmt"
	"sync"
	"testing"
)

// The GLOBAL environment on ServiceSet had no concurrency coverage, and
// it is the same shape as the bug that killed PID 1 in b7faaad: a map
// and a slice kept in step, mutated from one goroutine per control
// connection (`slinitctl setenv`, `unsetenv`, `reset-env`) while others
// read them (`getallenv-global`, and every service start, which
// snapshots the environment).
//
// env_race_test.go covers the PER-SERVICE env — ServiceRecord's
// extraEnv, which is what b7faaad actually fixed. It passes with
// ServiceSet.envMu removed entirely, because it never touches this
// state. These tests fail within milliseconds in that case, which is
// how they were checked: strip the envMu Lock/Unlock pairs from set.go
// and they report a concurrent map read and map write.
//
// Why a Go test and not a shell storm against the daemon: one slinitctl
// invocation is a fork, an exec, a connect and a round-trip, so eight
// shell workers for twelve seconds produced no overlap inside the
// daemon at all and did not reproduce the race even with the lock
// removed. Contention has to be manufactured where it happens.

func TestServiceSet_ConcurrentGlobalEnvMutation(t *testing.T) {
	ss := NewServiceSet(profileTestLogger{})
	ss.SetGlobalEnv([]string{"BASE=1"})

	const workers = 6
	const iterations = 400

	var wg sync.WaitGroup

	// Writers that add new keys, and writers that overwrite existing
	// ones. Both paths matter: overwriting takes the index-hit branch
	// and appending takes the grow branch, and a lock around only one of
	// them would pass a test that exercised only the other.
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				ss.GlobalSetEnv(fmt.Sprintf("NEW%d_%d", w, i), "v")
				ss.GlobalSetEnv(fmt.Sprintf("HOT%d", w%3), fmt.Sprint(i))
			}
		}(w)
	}

	// Deleters: GlobalUnsetEnv rewrites the index for every key after
	// the removed one, so it touches the whole map rather than one slot.
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				ss.GlobalUnsetEnv(fmt.Sprintf("NEW%d_%d", w, i))
			}
		}(w)
	}

	// Readers, which were part of the dep-graph bug rather than
	// bystanders. GlobalEnv() also writes — it caches a snapshot — so
	// these are not read-only on the underlying state.
	for r := 0; r < 3; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				for _, e := range ss.GlobalEnv() {
					_ = e
				}
			}
		}()
	}

	wg.Wait()

	// Surviving without a runtime fatal is the assertion; the exact
	// contents are not specified, since the interleaving decides them.
	// What must hold is that the slice and its index still agree, which
	// is the invariant a lost update breaks.
	env := ss.GlobalEnv()
	if env == nil {
		t.Fatal("GlobalEnv returned nil after the storm")
	}
	for _, e := range env {
		if e == "" {
			t.Errorf("GlobalEnv contains an empty entry: %q", env)
			break
		}
	}
}

func TestServiceSet_ConcurrentGlobalEnvReset(t *testing.T) {
	// SetGlobalEnv replaces both the slice and the index wholesale,
	// which is the widest write of the three and races a reader holding
	// the old snapshot.
	ss := NewServiceSet(profileTestLogger{})

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				ss.SetGlobalEnv([]string{
					fmt.Sprintf("A%d=%d", w, i),
					fmt.Sprintf("B%d=%d", w, i),
				})
				ss.GlobalSetEnv(fmt.Sprintf("C%d", w), fmt.Sprint(i))
			}
		}(w)
	}
	for r := 0; r < 3; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				_ = ss.GlobalEnv()
			}
		}()
	}
	wg.Wait()
}

// TestServiceSet_ConcurrentSharedLogMux guards a map that had no lock at
// all until this test found it. It is written from the loader
// (DirLoader.setupSharedLogger, on a service load) and read from the
// start path (ScriptedService.BringUp) — one goroutine per control
// connection for the first, the state machine for the second.
//
// Before muxMu this failed in 7ms with `fatal error: concurrent map read
// and map write`, and -race reported two distinct data races. Whether an
// outer lock happened to serialise every pair of callers at the time was
// not worth relying on: all three of slinit's recorded map-race kernel
// panics were accidentally safe until a new caller appeared.
func TestServiceSet_ConcurrentSharedLogMux(t *testing.T) {
	ss := NewServiceSet(profileTestLogger{})

	var wg sync.WaitGroup
	// Creators: distinct logger names, so every call takes the write
	// branch rather than hitting an existing entry.
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				_, _ = ss.GetOrCreateSharedLogMux(
					fmt.Sprintf("logger-%d-%d", w, i), SharedLogMuxOptions{})
			}
		}(w)
	}
	// Contenders for the SAME name, which is what proves the create is
	// atomic: two first-callers must not each build a mux and have one
	// silently dropped, leaving its producers writing into a sink
	// nothing reads.
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				_, _ = ss.GetOrCreateSharedLogMux("shared", SharedLogMuxOptions{})
			}
		}()
	}
	// Readers, as the start path does.
	for r := 0; r < 3; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				_ = ss.GetSharedLogMux(fmt.Sprintf("logger-%d-%d", r, i))
				_ = ss.GetSharedLogMux("shared")
			}
		}(r)
	}
	wg.Wait()

	// Every contender must have been handed the same mux.
	first := ss.GetSharedLogMux("shared")
	if first == nil {
		t.Fatal("the contended logger has no mux")
	}
	again, err := ss.GetOrCreateSharedLogMux("shared", SharedLogMuxOptions{})
	if err != nil {
		t.Fatalf("GetOrCreateSharedLogMux: %v", err)
	}
	if again != first {
		t.Error("a second mux was created for a name that already had one")
	}

	for w := 0; w < 4; w++ {
		for i := 0; i < 300; i++ {
			ss.RemoveSharedLogMux(fmt.Sprintf("logger-%d-%d", w, i))
		}
	}
	ss.RemoveSharedLogMux("shared")
}

// TestServiceSet_ConcurrentRegistryMutation covers the name registry and
// the alias map, which are guarded by ss.mu but had no concurrency test.
// AddService and ReplaceService write both; FindService reads both, and
// it is called from every control verb that names a service.
//
// ReplaceService is in the mix on purpose: it deletes the old aliases
// and inserts the new ones, so it is the only path that mutates the
// alias map in two directions at once.
func TestServiceSet_ConcurrentRegistryMutation(t *testing.T) {
	ss := NewServiceSet(profileTestLogger{})

	// A stable set the readers can always find, so a reader miss is a
	// real failure rather than a race with its own churn.
	stable := make([]Service, 4)
	for i := range stable {
		s := NewInternalService(ss, fmt.Sprintf("stable-%d", i))
		ss.AddService(s)
		stable[i] = s
	}

	var wg sync.WaitGroup

	// Churn: distinct names, added and removed.
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				s := NewInternalService(ss, fmt.Sprintf("churn-%d-%d", w, i))
				ss.AddService(s)
				ss.RemoveService(s)
			}
		}(w)
	}

	// Alias churn through ReplaceService.
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				old := NewInternalService(ss, fmt.Sprintf("swap-%d-%d", w, i))
				ss.AddService(old)
				newer := NewInternalService(ss, fmt.Sprintf("swap-%d-%d", w, i))
				ss.ReplaceService(old, newer)
				ss.RemoveService(newer)
			}
		}(w)
	}

	// Readers. The stable names must never go missing: a lost entry
	// would mean a lookup raced a write and saw a torn map.
	for r := 0; r < 3; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 400; i++ {
				for _, s := range stable {
					if ss.FindService(s.Name(), false) == nil {
						t.Errorf("FindService lost %s mid-storm", s.Name())
						return
					}
				}
				_ = ss.ListServices()
			}
		}()
	}
	wg.Wait()
}

// TestServiceSet_ConcurrentTransitionQueues covers propQueue and
// stopQueue. Their discipline is "the caller holds queueMu", which the
// processQueuesLocked / ProcessQueues naming split states but nothing
// enforced — AddPropQueue and AddTransitionQueue take no lock of their
// own, so a caller that forgets corrupts a slice. A slice race does not
// announce itself the way a map race does: there is no runtime fatal,
// just a lost entry or an index panic later, which is why this one
// needs -race to be worth much.
func TestServiceSet_ConcurrentTransitionQueues(t *testing.T) {
	ss := NewServiceSet(profileTestLogger{})
	svcs := make([]Service, 8)
	for i := range svcs {
		s := NewInternalService(ss, fmt.Sprintf("queued-%d", i))
		ss.AddService(s)
		svcs[i] = s
	}

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				s := svcs[(w+i)%len(svcs)]
				// Through the lock the production callers are required
				// to hold. If the queue fields are ever touched outside
				// it, -race says so here.
				ss.queueMu.Lock()
				ss.AddPropQueue(s)
				ss.AddTransitionQueue(s)
				ss.queueMu.Unlock()
				ss.ProcessQueues()
			}
		}(w)
	}
	wg.Wait()
}

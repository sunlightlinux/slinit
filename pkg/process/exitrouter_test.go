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

// The bug this file's pending table exists for.
//
// Registration happens just after cmd.Start(), so a child that exits
// immediately can be reaped by PID 1's Wait4(-1) while StartProcess is
// still setting it up. Route then found no entry and threw the status
// away; cmd.Wait() returned ECHILD, which was flattened to a zero
// WaitStatus, and the child looked as though it had exited cleanly. A
// `type = scripted` service running /bin/false was reported STARTED and
// stopped blocking its dependents — seen twice in CI as
// 220-nosystemd-dep-failure before the cause was known.
func TestRouteBeforeRegisterIsNotLost(t *testing.T) {
	r := NewExitRouter()
	const pid = 4242
	want := syscall.WaitStatus(1 << 8) // exited, code 1

	// The reap happens first, with nobody registered.
	if delivered := r.Route(pid, want); delivered {
		t.Error("Route reported delivery with nothing registered")
	}
	if r.PendingLen() != 1 {
		t.Fatalf("pending = %d, want the status held for a late Register", r.PendingLen())
	}

	// Registration arrives late and must still collect it.
	ch := r.Register(pid)
	select {
	case got := <-ch:
		if got != want {
			t.Errorf("status = %v, want %v", got, want)
		}
	default:
		t.Fatal("a status reaped before Register was lost — the exit code becomes a silent zero")
	}
	if r.PendingLen() != 0 {
		t.Errorf("pending = %d after collection, want 0", r.PendingLen())
	}
}

// The ordinary path must keep working: registered first, routed after.
func TestRouteAfterRegisterStillDelivers(t *testing.T) {
	r := NewExitRouter()
	const pid = 4243
	want := syscall.WaitStatus(2 << 8)

	ch := r.Register(pid)
	if !r.Route(pid, want) {
		t.Fatal("Route did not report delivery to a registered waiter")
	}
	select {
	case got := <-ch:
		if got != want {
			t.Errorf("status = %v, want %v", got, want)
		}
	default:
		t.Fatal("nothing delivered")
	}
	if r.PendingLen() != 0 {
		t.Errorf("pending = %d, want 0 — a delivered status must not also be held", r.PendingLen())
	}
}

// Most unclaimed statuses belong to genuine orphans that nobody will ever
// register — double-forked daemons, setsid'd shells — and on PID 1 they
// arrive for the life of the machine. The table has to shed them.
func TestPendingExpires(t *testing.T) {
	r := NewExitRouter()
	r.Route(9001, syscall.WaitStatus(1<<8))
	if r.PendingLen() != 1 {
		t.Fatalf("pending = %d, want 1", r.PendingLen())
	}

	// Age it past the TTL, then let the next Route prune.
	r.mu.Lock()
	r.pending[9001] = pendingExit{at: time.Now().Add(-2 * pendingTTL)}
	r.mu.Unlock()

	r.Route(9002, syscall.WaitStatus(1<<8))
	if _, stillThere := r.pending[9001]; stillThere {
		t.Error("an expired entry survived a prune")
	}

	// And a registration for the expired pid must not receive a stale
	// status: pid numbers are reused, and replaying an old exit into a
	// fresh child would be worse than losing it.
	ch := r.Register(9001)
	select {
	case got := <-ch:
		t.Errorf("received a stale status %v for a recycled pid", got)
	default:
	}
}

func TestPendingIsBounded(t *testing.T) {
	r := NewExitRouter()
	for pid := 1; pid <= pendingMax+200; pid++ {
		r.Route(pid, syscall.WaitStatus(1<<8))
	}
	if got := r.PendingLen(); got > pendingMax {
		t.Errorf("pending = %d, want no more than %d", got, pendingMax)
	}
}

// A stress test rather than a proof, and worth having because it is what
// actually found the bug: with a reaper racing every start, a fast-exiting
// child's real exit code used to be lost roughly 6 times in 3000 starts,
// which is rare enough to read as a flaky test and frequent enough to
// matter on a machine that starts services all day.
//
// It mirrors eventloop.reapOrphans — Wait4(-1, WNOHANG) then Route — so
// the path under test is the one PID 1 takes. A correct implementation
// never loses a status, so this cannot fail spuriously; it can only fail
// to notice, which is the usual trade for a stress test.
func TestFastExitKeepsItsStatusUnderAHotReaper(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test: 3000 process starts")
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			var st syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &st, syscall.WNOHANG, nil)
			if pid > 0 && err == nil {
				DefaultExitRouter.Route(pid, st)
			}
		}
	}()
	defer func() { close(stop); <-done }()

	const n = 3000
	lost := 0
	for i := 0; i < n; i++ {
		_, exitCh, err := StartProcess(ExecParams{
			ServiceName: "exit-race",
			Command:     []string{"/bin/false"},
		})
		if err != nil {
			t.Fatalf("iteration %d: start: %v", i, err)
		}
		ex := <-exitCh
		if ex.Status.ExitStatus() != 1 {
			lost++
			t.Errorf("iteration %d: exit status %d, want 1 — /bin/false reported as success",
				i, ex.Status.ExitStatus())
		}
	}
	if lost > 0 {
		t.Errorf("%d of %d starts lost the real exit code", lost, n)
	}
}

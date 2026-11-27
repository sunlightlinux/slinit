package service

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// partOfRig: process x, and process m with part-of: x, neither depending
// on the other; each appends a line to its own file when it starts.
type partOfRig struct {
	set  *ServiceSet
	x, m *ProcessService
	dir  string
}

// newPartOfRig builds the rig; configure, if given, runs before x and m
// are started (a setting changed after, the monitor goroutine reads
// unlocked: -race reports it).
func newPartOfRig(t *testing.T, configure ...func(*partOfRig)) *partOfRig {
	t.Helper()
	r := &partOfRig{dir: t.TempDir()}
	r.set, _ = newTestSet()
	mk := func(name string) *ProcessService {
		s := NewProcessService(r.set, name)
		s.SetCommand([]string{"/bin/sh", "-c", "echo x >> " + filepath.Join(r.dir, name) + "; exec sleep 60"})
		s.autoRestart = RestartNever
		r.set.AddService(s)
		return s
	}
	r.x = mk("x")
	r.m = mk("m")
	r.m.SetPartOf([]string{"x"})
	t.Cleanup(func() {
		r.set.StopService(r.x)
		r.set.StopService(r.m)
		waitState(t, r.x, StateStopped, 3*time.Second)
		waitState(t, r.m, StateStopped, 3*time.Second)
	})
	for _, f := range configure {
		f(r)
	}
	for _, s := range []Service{r.x, r.m} {
		r.set.StartService(s)
		if got := waitState(t, s, StateStarted, 3*time.Second); got != StateStarted {
			t.Fatalf("setup: %s %v", s.Name(), got)
		}
		// STARTED comes at the fork; the line comes when sh runs.
		r.waitStarts(t, s.Name(), 1)
	}
	return r
}

func (r *partOfRig) starts(name string) int {
	b, _ := os.ReadFile(filepath.Join(r.dir, name))
	return strings.Count(string(b), "\n")
}

// waitStarts waits for name to have started n times.
func (r *partOfRig) waitStarts(t *testing.T, name string, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for r.starts(name) < n {
		if time.Now().After(deadline) {
			t.Fatalf("%s started %d times, want %d", name, r.starts(name), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// settle gives a propagation that should not happen the time to.
func partOfSettle() { time.Sleep(300 * time.Millisecond) }

func TestPartOfStopReachesMember(t *testing.T) {
	r := newPartOfRig(t)
	r.set.StopService(r.x)
	if got := waitState(t, r.m, StateStopped, 3*time.Second); got != StateStopped {
		t.Fatalf("m is %v after x was stopped, want STOPPED", got)
	}
	// Not the other way round: m's stop leaves x alone.
	r2 := newPartOfRig(t)
	r2.set.StopService(r2.m)
	waitState(t, r2.m, StateStopped, 3*time.Second)
	partOfSettle()
	if st := r2.x.State(); st != StateStarted {
		t.Errorf("x is %v after m was stopped, want STARTED", st)
	}
}

// A crash is not a stop job: x dies and stays down, m keeps running.
func TestPartOfCrashDoesNotReachMember(t *testing.T) {
	r := newPartOfRig(t)
	syscall.Kill(r.x.PID(), syscall.SIGKILL)
	if got := waitState(t, r.x, StateStopped, 3*time.Second); got != StateStopped {
		t.Fatalf("x %v", got)
	}
	partOfSettle()
	if st := r.m.State(); st != StateStarted {
		t.Errorf("m is %v after x crashed, want STARTED", st)
	}
}

// slinitctl restart: a stop marked as restarting, then a start.
func TestPartOfClientRestartRestartsMember(t *testing.T) {
	r := newPartOfRig(t)
	r.set.MarkRestartingService(r.x)
	r.set.StopService(r.x)
	waitState(t, r.x, StateStopped, 3*time.Second)
	if st := r.m.State(); st != StateStarted {
		t.Fatalf("m is %v during x's restart, want still STARTED (restarted, not stopped)", st)
	}
	r.set.StartService(r.x)
	r.waitStarts(t, "x", 2)
	r.waitStarts(t, "m", 2)
	if got := waitState(t, r.m, StateStarted, 3*time.Second); got != StateStarted {
		t.Fatalf("m %v after x's restart", got)
	}
	partOfSettle()
	if n := r.starts("m"); n != 2 {
		t.Errorf("m started %d times, want 2", n)
	}
}

// An automatic restart propagates too, once x starts again after its
// restart-delay; RestartMode=direct keeps m up.
func TestPartOfAutoRestart(t *testing.T) {
	for _, direct := range []bool{false, true} {
		r := newPartOfRig(t, func(r *partOfRig) {
			r.x.autoRestart = RestartAlways
			r.x.SetRestartDelay(200 * time.Millisecond)
			if direct {
				r.x.SetRestartMode(RestartModeDirect)
			}
		})
		syscall.Kill(r.x.PID(), syscall.SIGKILL)
		time.Sleep(100 * time.Millisecond)
		if n := r.starts("m"); n != 1 {
			t.Fatalf("direct=%v: m restarted while x waits out its delay", direct)
		}
		r.waitStarts(t, "x", 2)
		partOfSettle()
		want := 2
		if direct {
			want = 1
		}
		if n := r.starts("m"); n != want {
			t.Errorf("direct=%v: m started %d times, want %d", direct, n, want)
		}
		if st := r.m.State(); st != StateStarted {
			t.Errorf("direct=%v: m %v", direct, st)
		}
	}
}

// A restart of x restarts m only if it is running (try-restart).
func TestPartOfRestartLeavesStoppedMemberStopped(t *testing.T) {
	r := newPartOfRig(t)
	r.set.StopService(r.m)
	waitState(t, r.m, StateStopped, 3*time.Second)
	r.set.MarkRestartingService(r.x)
	r.set.StopService(r.x)
	waitState(t, r.x, StateStopped, 3*time.Second)
	r.set.StartService(r.x)
	r.waitStarts(t, "x", 2)
	partOfSettle()
	if st, n := r.m.State(), r.starts("m"); st != StateStopped || n != 1 {
		t.Errorf("m is %v and started %d times, want STOPPED once", st, n)
	}
}

// x part-of m and m part-of x: a restart of either restarts each once,
// and a stop of either stops both, and both settle.
func TestPartOfLoop(t *testing.T) {
	r := newPartOfRig(t)
	r.x.SetPartOf([]string{"m"})
	r.set.MarkRestartingService(r.x)
	r.set.StopService(r.x)
	waitState(t, r.x, StateStopped, 3*time.Second)
	r.set.StartService(r.x)
	r.waitStarts(t, "m", 2)
	partOfSettle()
	if nx, nm := r.starts("x"), r.starts("m"); nx != 2 || nm != 2 {
		t.Fatalf("x started %d, m %d times, want 2 each", nx, nm)
	}
	r.set.StopService(r.m)
	for _, s := range []Service{r.x, r.m} {
		if got := waitState(t, s, StateStopped, 3*time.Second); got != StateStopped {
			t.Errorf("%s %v", s.Name(), got)
		}
	}
}

// A stop asked while x waits out its restart-delay reaches m too, and
// the called-off restart leaves nothing that restarts m at x's next
// start.
func TestPartOfStopDuringRestartDelay(t *testing.T) {
	r := newPartOfRig(t, func(r *partOfRig) {
		r.x.autoRestart = RestartAlways
		r.x.SetRestartDelay(1500 * time.Millisecond)
	})
	syscall.Kill(r.x.PID(), syscall.SIGKILL)
	time.Sleep(200 * time.Millisecond) // x is parked on its delay
	r.set.StopService(r.x)
	if got := waitState(t, r.m, StateStopped, 3*time.Second); got != StateStopped {
		t.Fatalf("m is %v after x was stopped during its restart-delay", got)
	}
	r.set.StartService(r.m)
	r.waitStarts(t, "m", 2)
	r.set.StartService(r.x)
	r.waitStarts(t, "x", 2)
	partOfSettle()
	if n := r.starts("m"); n != 2 {
		t.Errorf("m started %d times, want 2: x's plain start restarted it", n)
	}
}

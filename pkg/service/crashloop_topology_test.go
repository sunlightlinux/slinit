package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Every shape of a crash loop must settle once the restart limit is used
// up: the crashing service FAILED, nothing left STARTING or STOPPING, and
// a dependent that needs it (a hard dependency) not STARTED. One such
// shape, a hard dependent with restart = yes, used to wait in STARTING
// for good (TestRestartLimitFailsWaitingDependent); this walks the
// others around it.
func TestCrashLoopTopologiesSettle(t *testing.T) {
	deps := []DependencyType{DepRegular, DepPreparedBy, DepMilestone, DepWaitsFor}
	for _, dt := range deps {
		for _, deptRestart := range []bool{false, true} {
			for _, deptProcess := range []bool{false, true} {
				for _, smooth := range []bool{false, true} {
					name := fmt.Sprintf("dep=%v/restart=%v/process=%v/smooth=%v", dt, deptRestart, deptProcess, smooth)
					t.Run(name, func(t *testing.T) {
						t.Parallel()
						crashLoopSettles(t, dt, deptRestart, deptProcess, smooth)
					})
				}
			}
		}
	}
}

func crashLoopSettles(t *testing.T, dt DependencyType, deptRestart, deptProcess, smooth bool) {
	set, _ := newTestSet()
	runs := filepath.Join(t.TempDir(), "runs")
	app := NewProcessService(set, "app")
	app.SetCommand([]string{"/bin/sh", "-c", "echo x >> " + runs + "; sleep 0.05; exit 3"})
	app.SetAutoRestart(RestartAlways)
	app.SetRestartDelay(20 * time.Millisecond)
	app.SetRestartLimits(time.Minute, 2)
	app.smoothRecovery = smooth
	set.AddService(app)

	var dept Service
	if deptProcess {
		p := NewProcessService(set, "dept")
		p.SetCommand([]string{"/bin/sh", "-c", "while :; do sleep 60; done"})
		dept = p
	} else {
		dept = NewInternalService(set, "dept")
	}
	if deptRestart {
		dept.Record().SetAutoRestart(RestartAlways)
	}
	dept.Record().AddDep(app, dt)
	set.AddService(dept)
	defer set.StopService(dept)

	set.StartService(dept)

	// A hard dependent without restart goes down at app's first crash
	// and lets go of it; with nothing wanting it, app just stops.
	letsGo := !deptRestart && (dt == DepRegular || dt == DepPreparedBy)
	settled := func() bool {
		set.queueMu.Lock()
		defer set.queueMu.Unlock()
		if app.State() != StateStopped || (!letsGo && !app.Record().DidStartFail()) {
			return false
		}
		ds := dept.State()
		return ds == StateStopped || ds == StateStarted
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !settled() {
		time.Sleep(20 * time.Millisecond)
	}
	// Past the point it settled, nothing may move again.
	time.Sleep(300 * time.Millisecond)
	if !settled() {
		t.Fatalf("did not settle: app %v (failed %v), dept %v",
			app.State(), app.Record().DidStartFail(), dept.State())
	}
	if (dt == DepRegular || dt == DepPreparedBy) && dept.State() == StateStarted {
		t.Fatalf("dept STARTED although its hard dependency app is FAILED")
	}
	// The limit of 2 allows 2 restarts: 3 runs, not one more for a
	// dependent that comes back after app gave up. (A hard dependent
	// without restart lets go of app at its first crash.)
	b, _ := os.ReadFile(runs)
	want := 3
	if letsGo {
		want = 1
	}
	if n := strings.Count(string(b), "x"); n != want && !smooth {
		t.Fatalf("app ran %d times, want %d", n, want)
	}
}

func parkedApp(set *ServiceSet, cmd string) *ProcessService {
	app := NewProcessService(set, "app")
	app.SetCommand([]string{"/bin/sh", "-c", cmd})
	app.SetAutoRestart(RestartAlways)
	app.SetRestartDelay(20 * time.Millisecond)
	set.AddService(app)
	return app
}

func parkedDept(set *ServiceSet, name string, dep Service) *ProcessService {
	p := NewProcessService(set, name)
	p.SetCommand([]string{"/bin/sh", "-c", "while :; do sleep 60; done"})
	p.SetAutoRestart(RestartAlways)
	p.Record().AddDep(dep, DepRegular)
	set.AddService(p)
	return p
}

// settles waits until none of svcs is STARTING or STOPPING and nothing is
// counted active, and reports what it saw if that never happens.
func settles(t *testing.T, set *ServiceSet, svcs ...Service) {
	t.Helper()
	ok := func() bool {
		set.queueMu.Lock()
		defer set.queueMu.Unlock()
		for _, s := range svcs {
			if st := s.State(); st == StateStarting || st == StateStopping {
				return false
			}
		}
		return true
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !(ok() && set.CountActiveServices() == 0) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if !ok() || set.CountActiveServices() != 0 {
		msg := ""
		for _, s := range svcs {
			msg += fmt.Sprintf(" %s=%v", s.Record().serviceName, s.State())
		}
		t.Fatalf("did not settle:%s, %d active", msg, set.CountActiveServices())
	}
}

// A dependent parked on its restart delay when its hard dependency gave
// up for good came back from the delay to wait in STARTING for good: the
// dependency no longer held, nothing to start it. However the dependency
// gave up, and however long the chain.
func TestParkedDependentOfAGoneDependency(t *testing.T) {
	t.Run("restart limit, chain of three", func(t *testing.T) {
		set, _ := newTestSet()
		app := parkedApp(set, "sleep 0.05; exit 3")
		app.SetRestartLimits(time.Minute, 2)
		dept := parkedDept(set, "dept", app)
		top := parkedDept(set, "top", dept)
		set.StartService(top)
		settles(t, set, app, dept, top)
	})
	t.Run("stop pin", func(t *testing.T) {
		set, _ := newTestSet()
		app := parkedApp(set, "sleep 0.1; exit 3")
		dept := parkedDept(set, "dept", app)
		dept.SetRestartDelay(300 * time.Millisecond)
		set.StartService(dept)
		time.Sleep(50 * time.Millisecond)
		set.queueMu.Lock()
		app.Record().PinStop()
		set.queueMu.Unlock()
		settles(t, set, app, dept)
	})
	t.Run("failed start", func(t *testing.T) {
		set, _ := newTestSet()
		app := parkedApp(set, "sleep 0.1; exit 3")
		dept := parkedDept(set, "dept", app)
		dept.SetRestartDelay(300 * time.Millisecond)
		set.StartService(dept)
		time.Sleep(50 * time.Millisecond)
		set.queueMu.Lock()
		app.SetCommand([]string{"/nonexistent/slinit-test-binary"})
		set.queueMu.Unlock()
		settles(t, set, app, dept)
	})
	t.Run("a new start requires the dependency again", func(t *testing.T) {
		set, _ := newTestSet()
		app := parkedApp(set, "sleep 0.05; exit 3")
		app.SetRestartLimits(time.Minute, 2)
		dept := parkedDept(set, "dept", app)
		set.StartService(dept)
		settles(t, set, app, dept)
		set.queueMu.Lock()
		app.SetCommand([]string{"/bin/sh", "-c", "while :; do sleep 60; done"})
		set.queueMu.Unlock()
		set.StartService(dept)
		if got := waitState(t, dept, StateStarted, 3*time.Second); got != StateStarted {
			t.Fatalf("dept %v, app %v after a new start", got, app.State())
		}
		set.StopService(dept)
	})
}

func longRunning(set *ServiceSet, name string) *ProcessService {
	p := NewProcessService(set, name)
	p.SetCommand([]string{"/bin/sh", "-c", "while :; do sleep 60; done"})
	set.AddService(p)
	return p
}

// A restart sent as one request (slinitctl --no-wait restart: a stop and
// a start back to back) lands its start while the service is STOPPING,
// after the stop already let go of its dependencies. The start took no
// hold, the dependency stopped, and the service waited for it in
// STARTING for good. It must require the dependency again and come up.
func TestOneRequestRestartRequiresDependencies(t *testing.T) {
	for _, internal := range []bool{true, false} {
		set, _ := newTestSet()
		var d Service
		if internal {
			d = NewInternalService(set, "d")
			set.AddService(d)
		} else {
			d = longRunning(set, "d")
		}
		s := longRunning(set, "s")
		s.Record().AddDep(d, DepRegular)
		set.StartService(s)
		if got := waitState(t, s, StateStarted, 3*time.Second); got != StateStarted {
			t.Fatalf("setup: s %v", got)
		}
		set.MarkRestartingService(s)
		set.StopService(s)
		set.StartService(s)
		if got := waitState(t, s, StateStarted, 3*time.Second); got != StateStarted {
			t.Fatalf("d internal %v: after a one-request restart s is %v (failed %v), d %v",
				internal, got, s.Record().DidStartFail(), d.State())
		}
		if d.State() != StateStarted {
			t.Fatalf("d internal %v: s STARTED with its dependency d %v", internal, d.State())
		}
		set.StopService(s)
		settles(t, set, s, d)
	}
}

// add-dep on a service parked on its restart delay: the restart requires
// the new dependency and starts with it, rather than waiting on it.
func TestAddDepToParkedServiceStartsIt(t *testing.T) {
	set, _ := newTestSet()
	mark := filepath.Join(t.TempDir(), "m")
	s := NewProcessService(set, "s")
	s.SetCommand([]string{"/bin/sh", "-c", "if [ -e " + mark + " ]; then while :; do sleep 60; done; fi; touch " + mark + "; sleep 0.05; exit 3"})
	s.SetAutoRestart(RestartAlways)
	s.SetRestartDelay(400 * time.Millisecond)
	set.AddService(s)
	d := longRunning(set, "d")
	set.StartService(s)
	time.Sleep(200 * time.Millisecond)
	if err := set.AddDependency(s, d, DepRegular); err != nil {
		t.Fatalf("add-dep: %v", err)
	}
	if got := waitState(t, s, StateStarted, 3*time.Second); got != StateStarted || d.State() != StateStarted {
		t.Fatalf("s %v (failed %v), d %v after its restart", got, s.Record().DidStartFail(), d.State())
	}
	set.StopService(s)
}

type startCounter struct{ n *atomic.Int32 }

func (c startCounter) ServiceEvent(_ Service, ev ServiceEvent) {
	if ev == EventStarted {
		c.n.Add(1)
	}
}

// A prepared-by dependency that crashed restarts on its own; its
// dependent, stopped by that crash, then restarted it again when it came
// back, killing the run just started for the same failure. That run was
// a spawn the restart limit never counted, and a prepare step killed
// part-way. One crash, one restart.
func TestPreparedByDependencyRestartedOncePerFailure(t *testing.T) {
	set, _ := newTestSet()
	mark := filepath.Join(t.TempDir(), "m")
	app := NewProcessService(set, "app")
	app.SetCommand([]string{"/bin/sh", "-c", "if [ -e " + mark + " ]; then while :; do sleep 60; done; fi; touch " + mark + "; sleep 0.05; exit 3"})
	app.SetAutoRestart(RestartAlways)
	// Shorter than the run: the restart is immediate, so app is up
	// again before dept has finished stopping.
	app.SetRestartDelay(20 * time.Millisecond)
	set.AddService(app)
	var starts atomic.Int32
	app.AddListener(startCounter{&starts})

	dept := NewProcessService(set, "dept")
	dept.SetCommand([]string{"/bin/sh", "-c", "while :; do sleep 60; done"})
	dept.SetAutoRestart(RestartAlways)
	dept.Record().AddDep(app, DepPreparedBy)
	set.AddService(dept)
	defer set.StopService(dept)

	set.StartService(dept)
	if got := waitState(t, dept, StateStarted, 3*time.Second); got != StateStarted {
		t.Fatalf("setup: dept %v", got)
	}
	// app crashes once, comes back; dept goes down with it and back up.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && dept.Record().restarts.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	set.queueMu.Lock()
	as, ds := app.State(), dept.State()
	set.queueMu.Unlock()
	if as != StateStarted || ds != StateStarted {
		t.Fatalf("not settled: app %v, dept %v", as, ds)
	}
	if n := starts.Load(); n != 2 {
		t.Fatalf("app started %d times for one crash, want 2", n)
	}
}

// A prepared-by edge added to a dependent already up (add-dep, a
// reload) still restarts its dependency at the dependent's next restart.
func TestPreparedByAddedToRunningDependentRestartsIt(t *testing.T) {
	set, _ := newTestSet()
	prep := sleeper(set, "prep")
	d := sleeper(set, "d")
	defer func() { set.StopService(d); set.StopService(prep) }()
	set.StartService(prep)
	set.StartService(d)
	waitState(t, prep, StateStarted, 3*time.Second)
	waitState(t, d, StateStarted, 3*time.Second)
	pid := prep.PID()
	set.queueMu.Lock()
	d.Record().AddDep(prep, DepPreparedBy)
	d.Record().Restart()
	set.processQueuesLocked()
	set.queueMu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for (prep.PID() == pid || prep.State() != StateStarted) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if prep.PID() == pid {
		t.Fatal("the dependent's restart left its new prepared-by dependency's run alone")
	}
}

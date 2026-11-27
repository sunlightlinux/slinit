package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A crash-looping process whose restart limit runs out while a dependent
// with restart = yes has just come back and is waiting for it: the
// dependent must fail, not wait for good.
//
// The process's last exit stops the dependent; the dependent restarts
// (a dependency stopping is a reason to), asks for the process again and
// waits. The process then settles into FAILED instead of restarting, and
// nothing told the dependent: boot stayed STARTING with app STOPPED, and
// a container's slinit, which ends when the workload fails, never ended.
func TestRestartLimitFailsWaitingDependent(t *testing.T) {
	set, _ := newTestSet()
	app := NewProcessService(set, "app")
	app.SetCommand([]string{"/bin/sh", "-c", "sleep 0.05; exit 3"})
	app.SetAutoRestart(RestartAlways)
	app.SetRestartDelay(50 * time.Millisecond)
	app.SetRestartLimits(time.Minute, 3)
	set.AddService(app)
	boot := NewInternalService(set, "boot")
	boot.SetAutoRestart(RestartAlways)
	boot.Record().AddDep(app, DepRegular)
	set.AddService(boot)

	set.StartService(boot)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if app.State() == StateStopped && app.Record().DidStartFail() && boot.State() != StateStarting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !app.Record().DidStartFail() {
		t.Fatalf("app never used up its restart limit (app %v)", app.State())
	}
	if got := boot.State(); got == StateStarting {
		t.Fatalf("boot waits for good for app, which used up its restart limit (boot %v, app %v)", got, app.State())
	}
}

// restart-limit-count = N allows N restarts inside the interval, so the
// process runs N+1 times. Each exit was counted twice, once for the
// service's own restart and once in stopDependents, so 3 allowed 2
// restarts.
func TestRestartLimitCountsEachExitOnce(t *testing.T) {
	for _, withDependent := range []bool{false, true} {
		set, _ := newTestSet()
		runs := filepath.Join(t.TempDir(), "runs")
		app := NewProcessService(set, "app")
		app.SetCommand([]string{"/bin/sh", "-c", "echo x >> " + runs + "; sleep 0.05; exit 3"})
		app.SetAutoRestart(RestartAlways)
		app.SetRestartDelay(50 * time.Millisecond)
		app.SetRestartLimits(time.Minute, 3)
		set.AddService(app)
		if withDependent {
			boot := NewInternalService(set, "boot")
			boot.SetAutoRestart(RestartAlways)
			boot.Record().AddDep(app, DepRegular)
			set.AddService(boot)
			set.StartService(boot)
		} else {
			set.StartService(app)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) && !(app.State() == StateStopped && app.Record().DidStartFail()) {
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond)
		b, _ := os.ReadFile(runs)
		if n := strings.Count(string(b), "x"); n != 4 {
			t.Errorf("with a dependent %v: restart-limit-count = 3 ran the process %d times, want 4 (3 restarts)", withDependent, n)
		}
	}
}

// A health check that fires on a start-pinned service stops nothing, so
// the restart answer it asked for must not be kept for the next failure.
func TestPinnedHealthCheckKeepsNoRestartAnswer(t *testing.T) {
	set, _ := newTestSet()
	app := NewProcessService(set, "app")
	app.SetCommand([]string{"/bin/sh", "-c", "while :; do sleep 1; done"})
	app.SetAutoRestart(RestartAlways)
	app.SetRestartLimits(time.Minute, 3)
	set.AddService(app)
	set.StartService(app)
	if got := waitState(t, app, StateStarted, 3*time.Second); got != StateStarted {
		t.Fatalf("setup: app %v", got)
	}
	defer set.StopService(app)
	set.PinStartService(app)
	app.fireHealthCheckStop(app.PID())
	set.queueMu.Lock()
	asked := app.stopRestartAsked
	set.queueMu.Unlock()
	if asked {
		t.Fatal("a pinned service kept the restart answer of a stop that did not happen")
	}
}

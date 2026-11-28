package service

import (
	"os"
	"strings"
	"testing"
	"time"
)

func monoJob(t *testing.T, set *ServiceSet, name string) (*ScriptedService, func() int) {
	t.Helper()
	runs := t.TempDir() + "/runs"
	s := NewScriptedService(set, name)
	s.SetStartCommand([]string{"/bin/sh", "-c", "echo x >> " + runs})
	set.AddService(s)
	return s, func() int {
		b, _ := os.ReadFile(runs)
		return strings.Count(string(b), "\n")
	}
}

func waitRuns(t *testing.T, n func() int, want int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for n() < want {
		if time.Now().After(deadline) {
			t.Fatalf("%d runs in %v, want %d", n(), within, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// start-on-boot-sec past already: once, at once, and not again.
func TestMonoBootSecOnce(t *testing.T) {
	set, logger := newTestSet()
	s, runs := monoJob(t, set, "boot")
	s.Record().SetStartOnMonotonic(time.Second, 0)
	m := NewMonoStarter(set, logger)
	m.bootTime = time.Now().Add(-time.Hour)
	defer m.Close()
	defer set.StopService(s)
	m.Add(s)
	waitRuns(t, runs, 1, 2*time.Second)
	time.Sleep(1500 * time.Millisecond)
	if n := runs(); n != 1 {
		t.Errorf("start-on-boot-sec ran %d times, want once", n)
	}
	// Not yet due: nothing.
	s2, runs2 := monoJob(t, set, "boot-later")
	s2.Record().SetStartOnMonotonic(2*time.Hour, 0)
	defer set.StopService(s2)
	m.Add(s2)
	time.Sleep(300 * time.Millisecond)
	if runs2() != 0 {
		t.Error("ran an hour early")
	}
}

// start-on-unit-active-sec: a second after each start, again and
// again; with nothing to start from (never started) it waits for the
// first start, an operator's here.
func TestMonoUnitActiveSecRepeats(t *testing.T) {
	set, logger := newTestSet()
	s, runs := monoJob(t, set, "active")
	s.Record().SetStartOnMonotonic(0, time.Second)
	m := NewMonoStarter(set, logger)
	defer m.Close()
	defer set.StopService(s)
	m.Add(s)
	time.Sleep(1300 * time.Millisecond)
	if runs() != 0 {
		t.Fatal("ran with no start to count from")
	}
	set.StartService(s) // the operator's first start
	waitRuns(t, runs, 3, 4*time.Second)
	if c := s.Record().RestartCount(); c != 0 {
		t.Errorf("reruns counted as %d restarts", c)
	}
}

// A slow job still coming up when its interval is past again: the last
// start the timer made counts, so it does not fire in a loop.
func TestMonoSlowJobNoLoop(t *testing.T) {
	set, logger := newTestSet()
	runs := t.TempDir() + "/runs"
	s := NewScriptedService(set, "slow")
	s.SetStartCommand([]string{"/bin/sh", "-c", "sleep 1.5; echo x >> " + runs})
	set.AddService(s)
	s.Record().SetStartOnMonotonic(0, 500*time.Millisecond)
	m := NewMonoStarter(set, logger)
	defer m.Close()
	defer set.StopService(s)
	set.StartService(s)
	waitState(t, s, StateStarted, 3*time.Second)
	m.Add(s)
	time.Sleep(2 * time.Second)
	got := make(chan int)
	m.do(func() { got <- m.fires })
	if n := <-got; n > 5 {
		t.Errorf("%d elapses in 2s with a 0.5s interval and a 1.5s job: firing in a loop", n)
	}
}

// A reload (Remove, then Add) does not fire a start-on-boot-sec that
// has fired, as systemd disables a past OnBootSec= on reload.
func TestMonoBootSecNotAgainOnReload(t *testing.T) {
	set, logger := newTestSet()
	s, runs := monoJob(t, set, "boot-reload")
	s.Record().SetStartOnMonotonic(time.Second, 0)
	m := NewMonoStarter(set, logger)
	m.bootTime = time.Now().Add(-time.Hour)
	defer m.Close()
	defer set.StopService(s)
	m.Add(s)
	waitRuns(t, runs, 1, 2*time.Second)
	set.StopService(s)
	waitState(t, s, StateStopped, 3*time.Second)
	m.Remove("boot-reload")
	m.Add(s)
	time.Sleep(700 * time.Millisecond)
	if n := runs(); n != 1 {
		t.Errorf("ran %d times across a reload, want once", n)
	}
}

// A type-change reload (OnServiceReplaced) moves the start listener to
// the new record: its starts still drive start-on-unit-active-sec.
func TestMonoReplaceMovesListener(t *testing.T) {
	set, logger := newTestSet()
	old := NewProcessService(set, "swap")
	old.SetCommand([]string{"/bin/sleep", "60"})
	old.Record().SetStartOnMonotonic(0, 500*time.Millisecond)
	set.AddService(old)
	m := NewMonoStarter(set, logger)
	defer m.Close()
	m.Add(old)
	runs := t.TempDir() + "/runs"
	nu := NewScriptedService(set, "swap")
	nu.SetStartCommand([]string{"/bin/sh", "-c", "echo x >> " + runs})
	nu.Record().SetStartOnMonotonic(0, 500*time.Millisecond)
	set.ReplaceService(old, nu)
	m.Replace(old, nu)
	defer set.StopService(nu)
	set.StartService(nu)
	waitRuns(t, func() int {
		b, _ := os.ReadFile(runs)
		return strings.Count(string(b), "\n")
	}, 3, 3*time.Second)
	got := make(chan bool)
	m.do(func() { got <- m.heard[old] })
	if <-got {
		t.Error("still listening to the replaced record")
	}
}

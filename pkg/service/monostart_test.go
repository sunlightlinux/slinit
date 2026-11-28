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

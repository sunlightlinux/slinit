package service

import (
	"os"
	"strings"
	"testing"
	"time"
)

func sleeper(set *ServiceSet, name string) *ProcessService {
	s := NewProcessService(set, name)
	s.SetCommand([]string{"/bin/sleep", "60"})
	s.autoRestart = RestartNever
	set.AddService(s)
	return s
}

// Starting a stops what it names in conflicts and what names it; a
// third service is left alone.
func TestConflictsBothWays(t *testing.T) {
	set, _ := newTestSet()
	a, x, other := sleeper(set, "a"), sleeper(set, "x"), sleeper(set, "other")
	a.SetConflicts([]string{"x"})
	defer func() {
		for _, s := range []Service{a, x, other} {
			set.StopService(s)
		}
	}()
	set.StartService(x)
	set.StartService(other)
	waitState(t, x, StateStarted, 3*time.Second)
	set.StartService(a)
	if got := waitState(t, x, StateStopped, 3*time.Second); got != StateStopped {
		t.Fatalf("x is %v after a, which conflicts with it, started", got)
	}
	if got := waitState(t, a, StateStarted, 3*time.Second); got != StateStarted {
		t.Fatalf("a %v", got)
	}
	// The other way: x names nothing, a names x.
	set.StartService(x)
	if got := waitState(t, a, StateStopped, 3*time.Second); got != StateStopped {
		t.Fatalf("a is %v after x started: conflicts is symmetric", got)
	}
	if st := other.State(); st != StateStarted {
		t.Errorf("an unrelated service is %v", st)
	}
}

// on-failure starts its services when the service ends failed with no
// restart to come, and not on a clean exit or a stop that was asked for.
func TestOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cmd     string
		stop    bool
		restart bool
		want    bool
	}{
		{name: "exit 1", cmd: "exit 1", want: true},
		{name: "exit 0", cmd: "exit 0", want: false},
		{name: "stopped on request", cmd: "exec sleep 60", stop: true, want: false},
		// restart = on-failure: the failure restarts it until the limit
		// runs out, and only then is it failed.
		{name: "restart limit", cmd: "echo x >> $RUNS; exit 1", restart: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set, _ := newTestSet()
			h := sleeper(set, "handler")
			s := NewProcessService(set, "s")
			runs := t.TempDir() + "/runs"
			s.SetCommand([]string{"/bin/sh", "-c", strings.ReplaceAll(tc.cmd, "$RUNS", runs)})
			s.autoRestart = RestartNever
			if tc.restart {
				s.autoRestart = RestartOnFailure
				s.SetRestartDelay(10 * time.Millisecond)
				s.SetRestartLimits(10*time.Second, 2)
			}
			s.SetOnFailure([]string{"handler"})
			set.AddService(s)
			defer set.StopService(h)
			set.StartService(s)
			if tc.stop {
				waitState(t, s, StateStarted, 3*time.Second)
				set.StopService(s)
			}
			waitState(t, s, StateStopped, 5*time.Second)
			if tc.want {
				if got := waitState(t, h, StateStarted, 3*time.Second); got != StateStarted {
					t.Fatalf("handler %v, want STARTED", got)
				}
				if tc.restart {
					b, _ := os.ReadFile(runs)
					if n := strings.Count(string(b), "\n"); n != 3 {
						t.Errorf("handler started after %d runs, want 3: only once the restarts ran out", n)
					}
				}
			} else {
				time.Sleep(200 * time.Millisecond)
				if st := h.State(); st != StateStopped {
					t.Errorf("handler %v, want left STOPPED", st)
				}
			}
		})
	}
}

// A handler whose start fails at once, naming its own failed service
// or a pair naming each other: the starts come after the queues drain
// and are limited, so StartService returns and the loop ends.
func TestOnFailureSynchronousLoopEnds(t *testing.T) {
	set, _ := newTestSet()
	mk := func(name string, onFail ...string) *ProcessService {
		s := NewProcessService(set, name)
		s.SetCommand([]string{"/bin/true"})
		s.autoRestart = RestartNever
		s.SetRequiredPaths([]string{"/nonexistent-onfailure-test"}, nil)
		s.SetOnFailure(onFail)
		set.AddService(s)
		return s
	}
	self := mk("self", "self")
	a := mk("a", "b")
	mk("b", "a")
	done := make(chan struct{})
	go func() {
		set.StartService(self)
		set.StartService(a)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StartService did not return: an on-failure loop inside the queue drain")
	}
}

// A start that failed because a dependency did starts no on-failure
// service, as systemd runs no OnFailure= for a dependency job; with
// failure-conditions counting dependency failures it does.
func TestOnFailureNotForADependencysFailure(t *testing.T) {
	for _, depCond := range []bool{false, true} {
		set, _ := newTestSet()
		h := sleeper(set, "handler")
		f := NewProcessService(set, "f")
		f.SetCommand([]string{"/bin/true"})
		f.SetRequiredPaths([]string{"/nonexistent-onfailure-test"}, nil)
		set.AddService(f)
		s := sleeper(set, "s")
		s.Record().AddDep(f, DepRegular)
		s.SetOnFailure([]string{"handler"})
		if depCond {
			fc, _ := ParseFailureConditions("process-fails dependency-fails")
			s.failureConditions = fc
		}
		set.StartService(s)
		waitState(t, s, StateStopped, 3*time.Second)
		time.Sleep(200 * time.Millisecond)
		want := StateStopped
		if depCond {
			want = StateStarted
		}
		if st := h.State(); st != want {
			t.Errorf("dependency counted %v: handler %v, want %v", depCond, st, want)
		}
		set.StopService(h)
	}
}

// upholds: while u is up, h is started whenever it is found stopped:
// at u's start, and after h stops, even on request; at most 5 times in
// 10 seconds. Once u is down, h stays down.
func TestUpholds(t *testing.T) {
	set, _ := newTestSet()
	h := sleeper(set, "h")
	u := sleeper(set, "u")
	u.SetUpholds([]string{"h"})
	defer func() { set.StopService(u); set.StopService(h) }()
	set.StartService(u)
	if got := waitState(t, h, StateStarted, 3*time.Second); got != StateStarted {
		t.Fatalf("h %v after u started, want STARTED", got)
	}
	set.StopService(h)
	if got := waitState(t, h, StateStarted, 3*time.Second); got != StateStarted {
		t.Fatalf("h %v after it was stopped while u is up, want started again", got)
	}
	set.StopService(u)
	waitState(t, u, StateStopped, 3*time.Second)
	set.StopService(h)
	time.Sleep(200 * time.Millisecond)
	if st := h.State(); st != StateStopped {
		t.Errorf("h %v with u down, want left STOPPED", st)
	}
}

// One that cannot stay up is started at most 5 times in 10 seconds.
func TestUpholdsLimited(t *testing.T) {
	set, _ := newTestSet()
	runs := t.TempDir() + "/runs"
	h := NewProcessService(set, "h")
	h.SetCommand([]string{"/bin/sh", "-c", "echo x >> " + runs + "; exit 1"})
	h.autoRestart = RestartNever
	set.AddService(h)
	u := sleeper(set, "u")
	u.SetUpholds([]string{"h"})
	defer set.StopService(u)
	set.StartService(u)
	time.Sleep(1500 * time.Millisecond)
	b, _ := os.ReadFile(runs)
	if n := strings.Count(string(b), "\n"); n != 5 {
		t.Errorf("h ran %d times, want 5 (the limit)", n)
	}
}

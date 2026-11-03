package service

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestParseFailureConditions(t *testing.T) {
	for _, c := range []struct {
		in   string
		want FailureConditions
	}{
		{"process-fails", FailureConditions{Primary: FailOnProcessError}},
		{"process-stops", FailureConditions{Primary: FailOnProcessStop}},
		{"none", FailureConditions{Primary: FailNever}},
		{"", FailureConditions{Primary: FailNever}},
		// The value replaces the whole set, as in dinit: naming only the
		// dependency condition drops the process one.
		{"dependency-fails", FailureConditions{Primary: FailNever, Dependency: true}},
		{"process-fails dependency-fails", FailureConditions{Primary: FailOnProcessError, Dependency: true}},
		{"none process-stops", FailureConditions{Primary: FailOnProcessStop}},
	} {
		got, err := ParseFailureConditions(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q: got %+v, want %+v", c.in, got, c.want)
		}
		// String must read back to the same setting.
		if again, err := ParseFailureConditions(got.String()); err != nil || again != got {
			t.Errorf("%q: String %q does not round-trip (%+v, %v)", c.in, got.String(), again, err)
		}
	}
	for _, bad := range []string{"process-dies", "process-fails process-stops"} {
		if _, err := ParseFailureConditions(bad); err == nil {
			t.Errorf("%q: accepted, want an error", bad)
		}
	}
	// The zero value is slinit's behaviour before the directive existed.
	if (FailureConditions{}).String() != "process-fails" {
		t.Errorf("zero value is %q, want process-fails", FailureConditions{}.String())
	}
}

// restart = on-failure follows the conditions: process-stops makes a
// clean exit a failure, none makes no exit one.
func TestFailureConditionsDriveOnFailureRestart(t *testing.T) {
	for _, c := range []struct {
		name    string
		script  string
		fc      string
		restart bool
	}{
		{"default-clean", "exit 0", "process-fails", false},
		{"default-failing", "exit 1", "process-fails", true},
		{"stops-clean", "exit 0", "process-stops", true},
		{"none-failing", "exit 1", "none", false},
		{"none-killed", "kill -KILL $$", "none", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			set, svc, counter := runCountingService(t, "fc-"+c.name, "sleep 0.1; "+c.script, RestartOnFailure)
			fc, err := ParseFailureConditions(c.fc)
			if err != nil {
				t.Fatal(err)
			}
			svc.Record().SetFailureConditions(fc)
			set.StartService(svc)

			if c.restart {
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) && runCount(t, counter) < 2 {
					time.Sleep(25 * time.Millisecond)
				}
				if n := runCount(t, counter); n < 2 {
					t.Errorf("ran %d time(s), want a restart", n)
				}
				return
			}
			mustReachState(t, svc, StateStopped)
			time.Sleep(300 * time.Millisecond)
			if n := runCount(t, counter); n != 1 {
				t.Errorf("ran %d times, want no restart", n)
			}
		})
	}
}

// dependency-fails: a dependent stopped because a running dependency
// failed reports dependency-failed — only when it opted in, and only when
// the dependency's stop was a failure by the dependency's own conditions.
// Without it the reason stays normal, which is what slinit always did.
func TestDependencyFailsSetsDependentReason(t *testing.T) {
	for _, c := range []struct {
		name   string
		script string
		optIn  bool
		want   StoppedReason
	}{
		{"failing-optin", "exit 1", true, ReasonDepFailed},
		{"clean-optin", "exit 0", true, ReasonNormal},
		{"failing-default", "exit 1", false, ReasonNormal},
	} {
		t.Run(c.name, func(t *testing.T) {
			set, _ := newTestSet()
			dep := NewProcessService(set, "dep")
			dep.SetCommand([]string{"/bin/sh", "-c", "sleep 0.2; " + c.script})
			set.AddService(dep)
			main := NewInternalService(set, "main")
			set.AddService(main)
			main.Record().AddDep(dep, DepRegular)
			if c.optIn {
				main.Record().SetFailureConditions(FailureConditions{Dependency: true})
			}

			set.StartService(main)
			if got := waitForState(t, main, StateStarted, 5*time.Second); got != StateStarted {
				t.Fatalf("setup: main %v", got)
			}
			if got := waitForState(t, main, StateStopped, 5*time.Second); got != StateStopped {
				t.Fatalf("main did not stop with its dependency: %v", got)
			}
			if got := main.Record().StopReason(); got != c.want {
				t.Errorf("dependent reason %v, want %v", got, c.want)
			}
			if got, want := main.Record().IsFailed(), c.want == ReasonDepFailed; got != want {
				t.Errorf("dependent IsFailed = %v, want %v", got, want)
			}
		})
	}
}

// The reason travels: a dependent that itself stopped dependency-failed
// counts as a failure for its own opted-in dependents.
func TestDependencyFailsIsTransitive(t *testing.T) {
	set, _ := newTestSet()
	dep := NewProcessService(set, "dep")
	dep.SetCommand([]string{"/bin/sh", "-c", "sleep 0.2; exit 1"})
	set.AddService(dep)
	mid := NewInternalService(set, "mid")
	top := NewInternalService(set, "top")
	set.AddService(mid)
	set.AddService(top)
	mid.Record().AddDep(dep, DepRegular)
	top.Record().AddDep(mid, DepRegular)
	optIn := FailureConditions{Dependency: true}
	mid.Record().SetFailureConditions(optIn)
	top.Record().SetFailureConditions(optIn)

	set.StartService(top)
	waitForState(t, top, StateStarted, 5*time.Second)
	if got := waitForState(t, top, StateStopped, 5*time.Second); got != StateStopped {
		t.Fatalf("top did not stop: %v", got)
	}
	for _, s := range []Service{mid, top} {
		if got := s.Record().StopReason(); got != ReasonDepFailed {
			t.Errorf("%s reason %v, want %v", s.Name(), got, ReasonDepFailed)
		}
	}
}

// failure-action and success-action follow the same verdict.
func TestFailureConditionsDriveStoppedAction(t *testing.T) {
	for _, c := range []struct {
		name   string
		script string
		fc     string
		want   SystemAction
	}{
		{"default-failing", "exit 1", "process-fails", ActionReboot},
		{"default-clean", "exit 0", "process-fails", ActionPoweroff},
		{"stops-clean", "exit 0", "process-stops", ActionReboot},
		{"none-failing", "exit 1", "none", ActionNone},
		{"none-clean", "exit 0", "none", ActionPoweroff},
	} {
		t.Run(c.name, func(t *testing.T) {
			set, svc, _ := runCountingService(t, "act-"+c.name, "sleep 0.1; "+c.script, RestartNever)
			// The callback runs on the service's goroutine.
			var got atomic.Uint32
			set.OnSystemAction = func(a SystemAction, _ string) { got.Store(uint32(a)) }
			fc, err := ParseFailureConditions(c.fc)
			if err != nil {
				t.Fatal(err)
			}
			svc.Record().SetFailureConditions(fc)
			svc.Record().SetFailureAction(ActionReboot)
			svc.Record().SetSuccessAction(ActionPoweroff)

			set.StartService(svc)
			waitForState(t, svc, StateStarted, 5*time.Second)
			mustReachState(t, svc, StateStopped)
			time.Sleep(100 * time.Millisecond)
			if got := SystemAction(got.Load()); got != c.want {
				t.Errorf("action %v, want %v", got, c.want)
			}
		})
	}
}

// A service stopped on request has not failed. is-failed used to say it
// had, for every such service: its rule read the v1 exit field, which is
// -1 for any process that did not exit by itself.
func TestIsFailedAfterRequestedStop(t *testing.T) {
	set, _ := newTestSet()
	in := NewInternalService(set, "in")
	set.AddService(in)
	set.StartService(in)
	set.StopService(in)

	p := NewProcessService(set, "p")
	p.SetCommand([]string{"/bin/sleep", "30"})
	set.AddService(p)
	set.StartService(p)
	waitForState(t, p, StateStarted, 5*time.Second)
	set.StopService(p)
	mustReachState(t, p, StateStopped)

	for _, s := range []Service{in, p} {
		if s.Record().IsFailed() {
			t.Errorf("%s: IsFailed after a requested stop (reason %v)", s.Name(), s.StopReason())
		}
	}
}

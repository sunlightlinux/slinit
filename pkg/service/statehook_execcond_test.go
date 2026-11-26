package service

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// An exec-condition runs a command, for up to ten seconds, and it ran
// under queueMu, in BringUp: every other transition and the control
// socket waited behind it. It is evaluated off the lock now, as a state
// hook, with the service STARTING.
func TestExecConditionRunsOffQueueMu(t *testing.T) {
	for _, assert := range []bool{false, true} {
		base := stateHookGoroutines()
		dir := t.TempDir()
		set, _ := newTestSet()
		s := NewProcessService(set, "ec")
		mark := filepath.Join(dir, "cond")
		s.SetPredicates([]Predicate{{Kind: PredExecCondition, IsAssert: assert,
			Param: "echo ran > " + mark + "; sleep " + hookSleep}})
		s.SetCommand([]string{"/bin/sh", "-c", "exec sleep 30"})
		set.AddService(s)
		done := make(chan struct{})
		go func() {
			set.StartService(s)
			close(done)
		}()
		if waitForFile(mark) == "" {
			t.Fatal("exec-condition did not run")
		}
		requireQueueMuFree(t, set, "exec-condition")
		select {
		case <-done:
		case <-time.After(200 * time.Millisecond):
			t.Error("StartService blocked behind the exec-condition")
		}
		if st := s.State(); st != StateStarting {
			t.Errorf("assert %v: state %v while the exec-condition runs, want STARTING", assert, st)
		}
		if got := s.RunningStateHook(); got != "exec-condition" {
			t.Errorf("running %q, want exec-condition", got)
		}
		if st := waitState(t, s, StateStarted, 5*time.Second); st != StateStarted {
			t.Fatalf("assert %v: state %v, want STARTED", assert, st)
		}
		if s.PID() <= 0 || s.WasStartSkipped() {
			t.Errorf("assert %v: pid %d skipped %v after a passing exec-condition", assert, s.PID(), s.WasStartSkipped())
		}
		set.StopService(s)
		waitState(t, s, StateStopped, 5*time.Second)
		waitStateHooksGone(t, base, 2*time.Second)
	}
}

// The outcomes are those of the predicates evaluated in BringUp: a
// failing exec-condition skips the service (STARTED, marked skipped, no
// process), a failing assert-exec-condition fails the start, and an
// assert outranks a condition whatever their order, a service-started
// one (answered under the lock) included.
func TestExecConditionOutcomesOffQueueMu(t *testing.T) {
	cases := []struct {
		name      string
		preds     []Predicate
		wantState ServiceState
		skipped   bool
		failed    bool
		reason    string
	}{
		{"pass", []Predicate{{Kind: PredExecCondition, Param: "true"}},
			StateStarted, false, false, ""},
		{"condition fails", []Predicate{{Kind: PredExecCondition, Param: "exit 3"}},
			StateStarted, true, false, "exited 3"},
		{"assert fails", []Predicate{{Kind: PredExecCondition, IsAssert: true, Param: "exit 4"}},
			StateStopped, false, true, ""},
		{"negated", []Predicate{{Kind: PredExecCondition, Negate: true, Param: "true"}},
			StateStarted, true, false, "negated"},
		{"assert outranks condition", []Predicate{
			{Kind: PredExecCondition, Param: "exit 1"},
			{Kind: PredServiceStarted, IsAssert: true, Param: "not-loaded"}},
			StateStopped, false, true, ""},
		{"first condition wins", []Predicate{
			{Kind: PredPathExists, Param: "/nonexistent/first"},
			{Kind: PredExecCondition, Param: "exit 5"}},
			StateStarted, true, false, "/nonexistent/first"},
	}
	for _, c := range cases {
		set, logger := newTestSet()
		s := NewProcessService(set, "ec")
		s.SetPredicates(c.preds)
		s.SetCommand([]string{"/bin/sh", "-c", "exec sleep 30"})
		set.AddService(s)
		set.StartService(s)
		deadline := time.Now().Add(5 * time.Second)
		for (s.State() == StateStarting || s.State() == StateStopping) && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if st := s.State(); st != c.wantState {
			t.Errorf("%s: state %v, want %v", c.name, st, c.wantState)
		}
		if got := s.WasStartSkipped(); got != c.skipped {
			t.Errorf("%s: skipped %v, want %v", c.name, got, c.skipped)
		}
		if got := s.DidStartFail(); got != c.failed {
			t.Errorf("%s: start failed %v, want %v", c.name, got, c.failed)
		}
		if c.reason != "" && !strings.Contains(s.StartSkipReason(), c.reason) {
			t.Errorf("%s: skip reason %q, want it to mention %q", c.name, s.StartSkipReason(), c.reason)
		}
		if (c.skipped || c.failed) && s.PID() > 0 {
			t.Errorf("%s: a process (%d) was launched", c.name, s.PID())
		}
		if c.failed {
			logger.mu.Lock()
			n := len(logger.failed)
			logger.mu.Unlock()
			if n == 0 {
				t.Errorf("%s: start failure not reported", c.name)
			}
		}
		set.StopService(s)
		waitState(t, s, StateStopped, 5*time.Second)
	}
}

// A stop while the exec-condition runs ends it, killing its process
// group, and the start ends there: nothing is launched.
func TestStopDuringExecCondition(t *testing.T) {
	base := stateHookGoroutines()
	dir := t.TempDir()
	set, _ := newTestSet()
	s := NewProcessService(set, "ec")
	mark := filepath.Join(dir, "cond")
	child := filepath.Join(dir, "child")
	// The condition's own child, in its process group.
	s.SetPredicates([]Predicate{{Kind: PredExecCondition,
		Param: "sleep 30 & echo $! > " + child + "; echo ran > " + mark + "; wait"}})
	s.SetCommand([]string{"/bin/sh", "-c", "echo start > " + filepath.Join(dir, "started") + "; exec sleep 30"})
	set.AddService(s)
	set.StartService(s)
	if waitForFile(mark) == "" {
		t.Fatal("exec-condition did not run")
	}
	pid, _ := strconv.Atoi(waitForFile(child))
	t0 := time.Now()
	set.StopService(s)
	if st := waitState(t, s, StateStopped, 3*time.Second); st != StateStopped {
		t.Fatalf("state %v after the stop, want STOPPED", st)
	}
	if d := time.Since(t0); d > 2*time.Second {
		t.Errorf("the stop took %v: it waited the exec-condition out", d)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "started")); err == nil {
		t.Error("process launched after the stop")
	}
	if pid > 0 && processAlive(pid) {
		killIfAlive(pid)
		t.Errorf("the exec-condition's child %d outlived the stop", pid)
	}
	waitStateHooksGone(t, base, 2*time.Second)
}

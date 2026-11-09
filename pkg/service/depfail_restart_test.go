package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// depRig is dinit's test_failure_cond: process p, whose exit code the test
// chooses, and a dependent tp on it with restart = on-failure.
type depRig struct {
	set   *ServiceSet
	p     *ProcessService
	tp    Service
	runs  string
	gofil string
}

func fc(t *testing.T, s string) FailureConditions {
	t.Helper()
	f, err := ParseFailureConditions(s)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func newDepRig(t *testing.T, conds string, processDependent bool) *depRig {
	t.Helper()
	dir := t.TempDir()
	r := &depRig{runs: filepath.Join(dir, "runs"), gofil: filepath.Join(dir, "go")}
	r.set, _ = newTestSet()
	r.p = NewProcessService(r.set, "p")
	r.p.SetCommand([]string{"/bin/sh", "-c", "echo x >> " + r.runs +
		"; while [ ! -e " + r.gofil + " ]; do sleep 0.02; done; c=$(cat " + r.gofil + "); rm -f " + r.gofil + "; exit $c"})
	r.p.autoRestart = RestartNever
	r.p.failureConditions = fc(t, "process-fails")
	r.set.AddService(r.p)
	if processDependent {
		d := NewProcessService(r.set, "tp")
		d.SetCommand([]string{"/bin/sh", "-c", "while :; do sleep 60; done"})
		d.autoRestart = RestartOnFailure
		d.failureConditions = fc(t, conds)
		r.tp = d
	} else {
		d := NewInternalService(r.set, "tp")
		d.autoRestart = RestartOnFailure
		d.failureConditions = fc(t, conds)
		r.tp = d
	}
	r.tp.Record().AddDep(r.p, DepRegular)
	r.set.AddService(r.tp)
	return r
}

func (r *depRig) start(t *testing.T) {
	t.Helper()
	r.set.StartService(r.tp)
	if got := waitState(t, r.tp, StateStarted, 3*time.Second); got != StateStarted {
		t.Fatalf("setup: tp %v", got)
	}
}

func (r *depRig) exit(t *testing.T, code string) {
	t.Helper()
	// By rename: the script could read a file still empty, and exit with the
	// status of its last command instead of this code.
	tmp := r.gofil + ".tmp"
	if err := os.WriteFile(tmp, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, r.gofil); err != nil {
		t.Fatal(err)
	}
}

func (r *depRig) runCount() int {
	b, _ := os.ReadFile(r.runs)
	return strings.Count(string(b), "x")
}

func (r *depRig) waitRuns(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for r.runCount() < n {
		if time.Now().After(deadline) {
			t.Fatalf("p ran %d times, wanted %d; tp %v reason %v", r.runCount(), n, r.tp.State(), r.tp.Record().stopReason)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *depRig) finish() {
	os.WriteFile(r.gofil, []byte("0"), 0o644)
	stopAll(r.set, r.tp, r.p)
}

func TestOnFailureRestartsOnDependencyFailure(t *testing.T) {
	for _, process := range []bool{false, true} {
		r := newDepRig(t, "process-fails dependency-fails", process)
		r.start(t)
		r.exit(t, "1")
		r.waitRuns(t, 2)
		if got := waitState(t, r.tp, StateStarted, 3*time.Second); got != StateStarted {
			t.Errorf("process=%v: tp %v after the dependency failed", process, got)
		}

		// A clean exit after that is not a failure, for all the reason the
		// earlier stop left behind.
		r.exit(t, "0")
		if got := waitState(t, r.tp, StateStopped, 3*time.Second); got != StateStopped {
			t.Fatalf("process=%v: tp %v after a clean exit", process, got)
		}
		n := r.runCount()
		time.Sleep(200 * time.Millisecond)
		if got := r.runCount(); got != n {
			t.Errorf("process=%v: p ran again after a clean exit: %d then %d", process, n, got)
		}
		if got := r.tp.Record().stopReason; got == ReasonDepFailed {
			t.Errorf("process=%v: a clean exit left the reason %v", process, got)
		}
		r.finish()
	}
}

// The default does not change, which is the point of the token.
func TestOnFailureStaysDownWithoutTheToken(t *testing.T) {
	r := newDepRig(t, "process-fails", false)
	r.start(t)
	r.exit(t, "1")
	if got := waitState(t, r.tp, StateStopped, 3*time.Second); got != StateStopped {
		t.Fatalf("tp %v", got)
	}
	time.Sleep(200 * time.Millisecond)
	if n := r.runCount(); n != 1 {
		t.Errorf("p ran %d times", n)
	}
	if got := r.tp.Record().stopReason; got != ReasonNormal {
		t.Errorf("reason %v", got)
	}
	r.finish()
}

// A dependency that dies at once has no restart of its own to count.
func TestDependencyRestartLoopIsLimited(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	set, _ := newTestSet()
	p := NewProcessService(set, "p")
	p.SetCommand([]string{"/bin/sh", "-c", "echo x >> " + runs + "; sleep 0.05; exit 1"})
	p.autoRestart = RestartNever
	p.failureConditions = fc(t, "process-fails")
	p.SetRestartLimits(10*time.Second, 3)
	set.AddService(p)
	tp := NewInternalService(set, "tp")
	tp.autoRestart = RestartOnFailure
	tp.failureConditions = fc(t, "dependency-fails")
	tp.Record().AddDep(p, DepRegular)
	set.AddService(tp)
	set.StartService(tp)

	deadline := time.Now().Add(8 * time.Second)
	for tp.State() != StateStopped || p.State() != StateStopped {
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(runs)
			t.Fatalf("still going after %d runs: tp %v p %v", strings.Count(string(b), "x"), tp.State(), p.State())
		}
		time.Sleep(20 * time.Millisecond)
	}
	b, _ := os.ReadFile(runs)
	if n := strings.Count(string(b), "x"); n < 2 || n > 6 {
		t.Errorf("p ran %d times, with a limit of 3", n)
	}
	if !tp.Record().IsFailed() {
		t.Error("tp not failed after the limit")
	}
}

// Failed whatever the replaced process's exit status says.
func TestRecoveryFailureIsAFailure(t *testing.T) {
	set, _ := newTestSet()
	s := NewProcessService(set, "r")
	s.SetCommand([]string{"/bin/sh", "-c", "while :; do sleep 60; done"})
	s.autoRestart = RestartNever
	set.AddService(s)
	set.StartService(s)
	if got := waitState(t, s, StateStarted, 3*time.Second); got != StateStarted {
		t.Fatalf("setup: %v", got)
	}
	set.queueMu.Lock()
	s.handleRecoveryFailureLocked()
	set.queueMu.Unlock()
	if got := waitState(t, s, StateStopped, 3*time.Second); got != StateStopped {
		t.Fatalf("state %v", got)
	}
	if s.stopReason != ReasonFailed {
		t.Errorf("reason %v", s.stopReason)
	}
	if !s.IsFailed() {
		t.Error("not failed")
	}
}

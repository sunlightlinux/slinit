package service

import (
	"fmt"
	"syscall"
	"testing"
	"time"
)

// Regression tests from the dependency-graph audit: each one reproduced a
// divergence from dinit's state machine (or a bug dinit shares) before
// its fix.

// logSvcs logs the dependency-relevant state of each service.
func logSvcs(t *testing.T, ss ...*testSvc) {
	t.Helper()
	for _, s := range ss {
		r := s.Record()
		t.Logf("%s: state=%v desired=%v requiredBy=%d explicit=%v reason=%v isFailed=%v",
			s.Name(), s.State(), r.desired.Load(), r.requiredBy, r.startExplicit, r.stopReason, r.IsFailed())
	}
}

// crashSvc does what ProcessService.handleTerminationLocked does when a
// STARTED service's process dies on its own.
func crashSvc(set *ServiceSet, s *testSvc) {
	set.queueMu.Lock()
	defer set.queueMu.Unlock()
	if s.State() != StateStarted {
		return
	}
	s.stopReason = ReasonTerminated
	s.forceStop = true
	s.doStop(false)
	set.processQueuesLocked()
}

// A crash restart queues releases while transitions are pending. The
// propagation queue must be drained completely before the next
// transition (dinit process_queues), or a dependency reaches STOPPED
// still counted required and the later release never uncounts it:
// every service STOPPED, but CountActiveServices stays above 0 and
// shutdown waits for the emergency timeout.
func TestDepAudit_ActiveCountAfterCrashRestart(t *testing.T) {
	set, _ := newTestSet()
	sv := addTestSvcs(t, set, "A", "W", "B", "D")
	a, w, b, d := sv[0], sv[1], sv[2], sv[3]
	b.AddDep(a, DepRegular)
	b.AddDep(w, DepWaitsFor)
	d.AddDep(b, DepRegular)
	d.SetAutoRestart(RestartAlways)
	set.StartService(d)
	set.StopService(w) // operator stops the waits-for dependency
	crashSvc(set, b)   // B dies; B restarts, D (restart=always) with it
	set.StopService(a)
	set.StopAllServices(ShutdownHalt)
	if n := set.CountActiveServices(); n != 0 {
		logSvcs(t, a, w, b, d)
		t.Errorf("all services stopped, but CountActiveServices=%d (shutdown waits for 0)", n)
	}
}

// Random dependency graphs (every dependency type, some restart=always)
// driven by random start/stop/release/force-stop/wake/restart/crash
// operations, then a full shutdown: every service must end STOPPED,
// unrequired, and uncounted. Deterministic seeds; a few seconds even
// under -race.
func TestDepAudit_RandomChurnShutsDownClean(t *testing.T) {
	types := []DependencyType{DepRegular, DepWaitsFor, DepMilestone, DepAfter, DepBefore, DepPreparedBy}
	seeds := 20000
	if testing.Short() {
		seeds = 2000
	}
	fails := 0
	for seed := 0; seed < seeds && fails < 5; seed++ {
		r := uint64(seed)*6364136223846793005 + 1442695040888963407
		rnd := func(n int) int {
			r = r*6364136223846793005 + 1442695040888963407
			return int((r >> 33) % uint64(n))
		}
		set, _ := newTestSet()
		sv := addTestSvcs(t, set, "s0", "s1", "s2", "s3", "s4")
		var log []string
		for i := 1; i < len(sv); i++ {
			for j := 0; j < i; j++ {
				if rnd(2) == 0 {
					dt := types[rnd(len(types))]
					sv[i].AddDep(sv[j], dt)
					log = append(log, fmt.Sprintf("%s->%s:%v", sv[i].Name(), sv[j].Name(), dt))
				}
			}
			if rnd(3) == 0 {
				sv[i].SetAutoRestart(RestartAlways)
				log = append(log, sv[i].Name()+":always")
			}
		}
		for k := 0; k < 12; k++ {
			s := sv[rnd(len(sv))]
			op := rnd(8)
			log = append(log, fmt.Sprintf("op%d(%s)", op, s.Name()))
			switch op {
			case 0, 6:
				set.StartService(s)
			case 1:
				set.StopService(s)
			case 2:
				set.ReleaseService(s)
			case 3:
				set.ForceStopService(s)
			case 4:
				set.WakeService(s)
			case 5:
				set.queueMu.Lock()
				s.Restart()
				set.processQueuesLocked()
				set.queueMu.Unlock()
			case 7:
				crashSvc(set, s)
			}
		}
		set.StopAllServices(ShutdownHalt)
		bad := set.CountActiveServices() != 0
		for _, s := range sv {
			if s.State() != StateStopped || s.Record().requiredBy != 0 {
				bad = true
			}
		}
		if bad {
			fails++
			t.Errorf("seed %d: %v (active=%d)", seed, log, set.CountActiveServices())
			logSvcs(t, sv...)
		}
	}
}

// A dependent the operator had already stopped cleanly keeps its reason
// when its dependency is force-stopped afterwards. stopDependents marked
// every hard dependent DEPFAILED on a forced stop, STOPPED ones too, so
// `slinitctl is-failed B` turned true after `--force stop A`. dinit
// skips a STOPPED dependent outright.
func TestDepAudit_ForceStopKeepsStoppedDependentReason(t *testing.T) {
	set, _ := newTestSet()
	sv := addTestSvcs(t, set, "A", "B")
	a, b := sv[0], sv[1]
	b.AddDep(a, DepRegular)
	set.StartService(a)
	set.StartService(b)
	set.StopService(b)
	if b.IsFailed() {
		t.Fatalf("setup: B failed after a plain stop (reason %v)", b.StopReason())
	}
	set.ForceStopService(a)
	if b.IsFailed() || b.StopReason() != ReasonNormal {
		logSvcs(t, a, b)
		t.Errorf("cleanly stopped B now reports reason %v, is-failed=%v", b.StopReason(), b.IsFailed())
	}
}

// The other half of dinit's is_dept_failure: a dependency forced down
// because its start failed (job-timeout-sec) takes its dependents down
// as DEPFAILED.
func TestDepAudit_FailedForcedStopMarksDependentDepFailed(t *testing.T) {
	set, _ := newTestSet()
	sv := addTestSvcs(t, set, "A", "B")
	a, b := sv[0], sv[1]
	b.AddDep(a, DepRegular)
	a.autoStart = false
	set.StartService(b)
	if b.State() != StateStarting {
		t.Fatalf("setup: B should wait for A, got %v", b.State())
	}
	// What the job-timeout timer does.
	set.queueMu.Lock()
	a.stopReason = ReasonTimedOut
	a.startFailed = true
	a.UnrecoverableStop()
	set.processQueuesLocked()
	set.queueMu.Unlock()
	if b.State() != StateStopped || b.StopReason() != ReasonDepFailed {
		logSvcs(t, a, b)
		t.Errorf("B: state %v reason %v; want STOPPED dependency-failed", b.State(), b.StopReason())
	}
}

// `slinitctl --force stop A` must stop A. ForceStopService only called
// ForcedStop, which leaves A's desired state STARTED, so a
// restart=always A, or A held by a restart=always dependent, came
// straight back. dinit's control.cc stops with stop(true) and then
// forced_stop().
func TestDepAudit_ForceStopWithRestartAlwaysDependent(t *testing.T) {
	set, _ := newTestSet()
	sv := addTestSvcs(t, set, "A", "B")
	a, b := sv[0], sv[1]
	b.AddDep(a, DepRegular)
	b.SetAutoRestart(RestartAlways)
	set.StartService(a)
	set.StartService(b)
	set.ForceStopService(a)
	if a.State() != StateStopped || b.State() != StateStopped {
		logSvcs(t, a, b)
		t.Fatalf("--force stop A: A=%v B=%v; want both STOPPED", a.State(), b.State())
	}
	// Nothing failed: the operator asked for the stop.
	if b.IsFailed() {
		t.Errorf("B reports reason %v after A was stopped on request", b.StopReason())
	}
	if n := set.CountActiveServices(); n != 0 {
		t.Errorf("CountActiveServices=%d after both stopped", n)
	}
}

func TestDepAudit_ForceStopRestartAlwaysSelf(t *testing.T) {
	set, _ := newTestSet()
	a := addTestSvcs(t, set, "A")[0]
	a.SetAutoRestart(RestartAlways)
	set.StartService(a)
	set.ForceStopService(a)
	if a.State() != StateStopped {
		t.Errorf("--force stop of restart=always A: %v (BringUp count %d)", a.State(), a.upCount)
	}
}

func TestDepAudit_ForceStopRestartAlwaysProcess(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "p")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	svc.SetAutoRestart(RestartAlways)
	set.AddService(svc)
	set.StartService(svc)
	waitForNewPID(set, svc, 0, 2*time.Second)
	t.Cleanup(func() { set.StopService(svc); waitState(t, svc, StateStopped, 3*time.Second) })
	set.ForceStopService(svc)
	if st := waitState(t, svc, StateStopped, 3*time.Second); st != StateStopped {
		t.Fatalf("--force stop of a restart=always process: %v", st)
	}
	time.Sleep(300 * time.Millisecond)
	if st := svc.State(); st != StateStopped {
		t.Errorf("restarted after --force stop: %v (restarts %d)", st, svc.RestartCount())
	}
}

// slinitctl(8): --force stops the service "even when it is pinned
// started", and dependents are force-stopped "pins and all".
func TestDepAudit_ForceStopPinnedStarted(t *testing.T) {
	set, _ := newTestSet()
	sv := addTestSvcs(t, set, "A", "B")
	a, b := sv[0], sv[1]
	b.AddDep(a, DepRegular)
	set.StartService(b)
	set.PinStartService(a)
	set.ForceStopService(a)
	if a.State() != StateStopped || b.State() != StateStopped {
		logSvcs(t, a, b)
		t.Fatalf("--force stop of pinned A: A=%v B=%v", a.State(), b.State())
	}
	if a.IsStartPinned() {
		t.Errorf("A still start-pinned after --force stop")
	}
}

func TestDepAudit_ForceStopPinnedDependent(t *testing.T) {
	set, _ := newTestSet()
	sv := addTestSvcs(t, set, "A", "B", "C")
	a, b, c := sv[0], sv[1], sv[2]
	b.AddDep(a, DepRegular)
	c.AddDep(b, DepRegular)
	set.StartService(c)
	set.PinStartService(c) // A and B inherit the pin
	set.ForceStopService(a)
	if a.State() != StateStopped || b.State() != StateStopped || c.State() != StateStopped {
		logSvcs(t, a, b, c)
		t.Fatalf("--force stop A under pinned C: A=%v B=%v C=%v", a.State(), b.State(), c.State())
	}
	for _, s := range sv {
		if s.IsStartPinned() {
			t.Errorf("%s still start-pinned", s.Name())
		}
	}
	if n := set.CountActiveServices(); n != 0 {
		t.Errorf("CountActiveServices=%d", n)
	}
}

// A (restart=always) dies while its hard dependent B is still STARTING,
// waiting for a slow C. B has started nothing, so dinit leaves it alone
// (stop_dependents skips a STARTING dependent that is fundamentally
// stopped) and B starts once A is back and C is up. slinit force-stopped
// B: its start was cancelled and its activation dropped for good.
func TestDepAudit_DepCrashLeavesWaitingDependent(t *testing.T) {
	set, _ := newTestSet()
	sv := addTestSvcs(t, set, "A", "B", "C")
	a, b, c := sv[0], sv[1], sv[2]
	a.SetAutoRestart(RestartAlways)
	b.AddDep(a, DepRegular)
	b.AddDep(c, DepRegular)
	c.autoStart = false
	set.StartService(b)
	ev := &testListener{}
	b.AddListener(ev)
	crashSvc(set, a)
	set.queueMu.Lock()
	c.Started()
	set.processQueuesLocked()
	set.queueMu.Unlock()
	if b.State() != StateStarted {
		logSvcs(t, a, b, c)
		t.Errorf("B never started after A's restart and C coming up: %v (events %v)", b.State(), ev.events)
	}
}

func TestDepAudit_DepCrashLeavesWaitingDependentProcess(t *testing.T) {
	set, _ := newTestSet()
	a := NewProcessService(set, "A")
	a.SetCommand([]string{"/bin/sleep", "60"})
	a.SetAutoRestart(RestartAlways)
	b := NewProcessService(set, "B")
	b.SetCommand([]string{"/bin/sleep", "60"})
	c := newTestSvc(t, set, "C")
	c.autoStart = false
	set.AddService(a)
	set.AddService(b)
	set.AddService(c)
	b.AddDep(a, DepRegular)
	b.AddDep(c, DepRegular)
	t.Cleanup(func() {
		set.StopService(b)
		set.StopService(a)
		waitState(t, a, StateStopped, 3*time.Second)
		waitState(t, b, StateStopped, 3*time.Second)
	})
	set.StartService(b)
	pa := waitForNewPID(set, a, 0, 2*time.Second)
	if err := syscall.Kill(pa, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitForNewPID(set, a, pa, 3*time.Second)
	set.queueMu.Lock()
	c.Started()
	set.processQueuesLocked()
	set.queueMu.Unlock()
	if st := waitState(t, b, StateStarted, 3*time.Second); st != StateStarted {
		t.Errorf("B never started after A was killed and restarted: %v (desired %v)", st, b.desired.Load())
	}
}

// The same guard on a restart (slinitctl restart, prepared-by): a hard
// dependent still waiting for another dependency is not restarted.
func TestDepAudit_RestartLeavesWaitingDependent(t *testing.T) {
	set, _ := newTestSet()
	sv := addTestSvcs(t, set, "A", "B", "C")
	a, b, c := sv[0], sv[1], sv[2]
	b.AddDep(a, DepRegular)
	b.AddDep(c, DepRegular)
	c.autoStart = false
	set.StartService(b)
	ev := &testListener{}
	b.AddListener(ev)
	set.queueMu.Lock()
	a.Restart()
	set.processQueuesLocked()
	set.queueMu.Unlock()
	for _, e := range ev.events {
		if e == EventStartCancelled {
			t.Errorf("B's pending start was cancelled by a restart of A")
		}
	}
	set.queueMu.Lock()
	c.Started()
	set.processQueuesLocked()
	set.queueMu.Unlock()
	if b.State() != StateStarted {
		logSvcs(t, a, b, c)
		t.Errorf("B: %v after C came up", b.State())
	}
}

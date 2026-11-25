package service

import (
	"fmt"
	"testing"
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

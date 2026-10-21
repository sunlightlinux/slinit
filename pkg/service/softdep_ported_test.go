package service

import "testing"

// Ported from dinit's src/tests/tests.cc, the test_softdep family.
//
// These are step-by-step state-machine tests: they drive each transition
// by hand and assert the intermediate states, which is what slinit's
// existing dependency tests cannot do (see testsvc_test.go). dinit keeps
// finding bugs in this area that slinit's own suite misses, and its
// tests are the ready-made specification, since slinit preserved dinit's
// two-phase transition model.
//
// Dependency type mapping: dinit REG -> DepRegular, WAITS -> DepWaitsFor,
// MS -> DepMilestone.

// test_softdep1: stopping a hard dependency forces its hard dependent
// down but leaves a waits-for dependent of THAT running.
func TestPortedSoftdep1(t *testing.T) {
	set, _ := newTestSet()
	svcs := addTestSvcs(t, set, "test-service-1", "test-service-2", "test-service-3")
	s1, s2, s3 := svcs[0], svcs[1], svcs[2]
	s2.Record().AddDep(s1, DepRegular)
	s3.Record().AddDep(s2, DepWaitsFor)

	set.StartService(s3)
	set.ProcessQueues()
	for _, s := range svcs {
		if s.State() != StateStarted {
			t.Fatalf("%s should be STARTED after starting s3, got %v", s.Name(), s.State())
		}
	}

	// Stopping s1 must take s2 with it, but not s3.
	set.StopService(s1)
	set.ProcessQueues()

	if s3.State() != StateStarted {
		t.Errorf("s3 (waits-for on s2) should stay STARTED, got %v", s3.State())
	}
	if s2.State() != StateStopped {
		t.Errorf("s2 (hard dep on s1) should be STOPPED, got %v", s2.State())
	}
	if s1.State() != StateStopped {
		t.Errorf("s1 should be STOPPED, got %v", s1.State())
	}
}

// test_softdep2: a milestone dependency releases its target once
// started, so stopping the target leaves the dependent running.
func TestPortedSoftdep2(t *testing.T) {
	set, _ := newTestSet()
	svcs := addTestSvcs(t, set, "test-service-1", "test-service-2")
	s1, s2 := svcs[0], svcs[1]
	s2.Record().AddDep(s1, DepMilestone)

	set.StartService(s2)
	set.ProcessQueues()
	if s2.State() != StateStarted || s1.State() != StateStarted {
		t.Fatalf("both should be STARTED, got s1=%v s2=%v", s1.State(), s2.State())
	}

	set.StopService(s1)
	set.ProcessQueues()

	if s2.State() != StateStarted {
		t.Errorf("s2 (milestone on s1) should stay STARTED after s1 stops, got %v", s2.State())
	}
	if s1.State() != StateStopped {
		t.Errorf("s1 should be STOPPED, got %v", s1.State())
	}
}

// test_softdep3: a milestone dependency that FAILS to start takes the
// dependent down with it — the milestone must be reached once.
func TestPortedSoftdep3(t *testing.T) {
	set, _ := newTestSet()
	svcs := addTestSvcs(t, set, "test-service-1", "test-service-2")
	s1, s2 := svcs[0], svcs[1]
	// Hold s1 in STARTING so the failure can be injected there, which
	// is what dinit uses its test_service for in this case.
	s1.autoStart = false
	s2.Record().AddDep(s1, DepMilestone)

	set.StartService(s2)
	set.ProcessQueues()
	if s1.State() != StateStarting {
		t.Fatalf("s1 should be STARTING before it reports, got %v", s1.State())
	}

	// (false, true): dinit's failed_to_start defaults immediate_stop to
	// TRUE, and slinit's real callers pass true almost everywhere. With
	// false the service stays STARTING, which looked like a slinit bug
	// and was a porting error.
	s1.Record().failedToStart(false, true)
	set.ProcessQueues()

	if s1.State() != StateStopped {
		t.Errorf("s1 should be STOPPED after failing to start, got %v", s1.State())
	}
	if s2.State() != StateStopped {
		t.Errorf("s2 should be STOPPED when its milestone failed, got %v", s2.State())
	}
}

// test_softdep4: a milestone dependency stopped while still STARTING
// takes the dependent down — the milestone was never reached.
func TestPortedSoftdep4(t *testing.T) {
	set, _ := newTestSet()
	svcs := addTestSvcs(t, set, "test-service-1", "test-service-2")
	s1, s2 := svcs[0], svcs[1]
	// Hold s1 in STARTING, but leave autoStop alone: dinit's
	// test_service defaults auto_stop to true and this case does not
	// change it, so the manual BringDown() below is what completes the
	// stop. Disabling it leaves s1 STARTING forever — my first porting
	// error here, not a slinit defect.
	s1.autoStart = false
	s2.Record().AddDep(s1, DepMilestone)

	set.StartService(s2)
	set.ProcessQueues()
	if s1.State() != StateStarting || s2.State() != StateStarting {
		t.Fatalf("both should be STARTING, got s1=%v s2=%v", s1.State(), s2.State())
	}

	s1.Record().Stop(true)
	set.ProcessQueues()
	s1.BringDown()
	set.ProcessQueues()

	if s1.State() != StateStopped {
		t.Errorf("s1 should be STOPPED, got %v", s1.State())
	}
	if s2.State() != StateStopped {
		t.Errorf("s2 should be STOPPED — its milestone never started, got %v", s2.State())
	}
	if n := set.CountActiveServices(); n != 0 {
		t.Errorf("active services = %d, want 0", n)
	}
}

// test_softdep5: deactivating a service without forcing it down leaves
// it running when a waits-for dependent reattached to it.
func TestPortedSoftdep5(t *testing.T) {
	set, _ := newTestSet()
	svcs := addTestSvcs(t, set, "test-service-1", "test-service-2")
	s1, s2 := svcs[0], svcs[1]
	s2.Record().AddDep(s1, DepWaitsFor)

	set.StartService(s2)
	set.ProcessQueues()

	set.StopService(s1)
	set.ProcessQueues()
	if s1.State() != StateStopped {
		t.Errorf("s1 should be STOPPED, got %v", s1.State())
	}
	if s2.State() != StateStarted {
		t.Errorf("s2 should stay STARTED (waits-for does not propagate), got %v", s2.State())
	}

	set.StartService(s1)
	set.ProcessQueues()
	if s1.State() != StateStarted || s2.State() != StateStarted {
		t.Fatalf("both should be STARTED, got s1=%v s2=%v", s1.State(), s2.State())
	}

	// Deactivate without bringing down: s1 must stay up because s2's
	// waits-for dependency reattached when s1 came back.
	s1.Record().Stop(false)
	set.ProcessQueues()
	if s1.State() != StateStarted {
		t.Errorf("s1 should stay STARTED — s2's waits-for reattached, got %v", s1.State())
	}
	if s2.State() != StateStarted {
		t.Errorf("s2 should stay STARTED, got %v", s2.State())
	}
	if n := set.CountActiveServices(); n != 2 {
		t.Errorf("active services = %d, want 2", n)
	}
}

// test_softdep6: two waits-for dependents on one target; the target
// stays up until the last of them goes.
func TestPortedSoftdep6(t *testing.T) {
	set, _ := newTestSet()
	svcs := addTestSvcs(t, set, "test-service-1", "test-service-2", "test-service-3")
	s1, s2, s3 := svcs[0], svcs[1], svcs[2]
	s2.Record().AddDep(s1, DepWaitsFor)
	s3.Record().AddDep(s1, DepWaitsFor)

	set.StartService(s2)
	set.ProcessQueues()
	set.StartService(s3)
	set.ProcessQueues()

	set.StopService(s2)
	set.ProcessQueues()
	if s1.State() != StateStarted {
		t.Errorf("s1 should stay STARTED — s3 still holds it, got %v", s1.State())
	}
	if s2.State() != StateStopped {
		t.Errorf("s2 should be STOPPED, got %v", s2.State())
	}
	if s3.State() != StateStarted {
		t.Errorf("s3 should be STARTED, got %v", s3.State())
	}

	set.StopService(s3)
	set.ProcessQueues()
	if s1.State() != StateStopped {
		t.Errorf("s1 should be STOPPED once its last dependent went, got %v", s1.State())
	}
	if s3.State() != StateStopped {
		t.Errorf("s3 should be STOPPED, got %v", s3.State())
	}
}

// test_softdep7: an unexpected stop of a service set to always restart
// keeps its target state STARTED and cycles back up, with the waits-for
// dependent untouched throughout.
func TestPortedSoftdep7(t *testing.T) {
	set, _ := newTestSet()
	svcs := addTestSvcs(t, set, "test-service-1", "test-service-2")
	s1, s2 := svcs[0], svcs[1]
	s1.autoStart = false
	s1.autoStop = false
	s1.Record().SetAutoRestart(RestartAlways)
	s2.Record().AddDep(s1, DepWaitsFor)

	set.StartService(s2)
	set.ProcessQueues()
	if got := s1.Record().TargetState(); got != StateStarted {
		t.Fatalf("s1 target should be STARTED, got %v", got)
	}
	s1.Started()
	set.ProcessQueues()
	if s1.State() != StateStarted || s2.State() != StateStarted {
		t.Fatalf("both should be STARTED, got s1=%v s2=%v", s1.State(), s2.State())
	}

	// Unexpected stop.
	s1.Record().ForcedStop()
	set.ProcessQueues()
	if got := s1.Record().TargetState(); got != StateStarted {
		t.Errorf("s1 will restart, so target should stay STARTED, got %v", got)
	}
	if s1.State() != StateStopping {
		t.Errorf("s1 should be STOPPING, got %v", s1.State())
	}

	s1.Stopped()
	set.ProcessQueues()
	if s1.State() != StateStarting {
		t.Errorf("s1 should be STARTING (restarting), got %v", s1.State())
	}

	s1.Started()
	set.ProcessQueues()
	if s1.State() != StateStarted || s2.State() != StateStarted {
		t.Errorf("both should be STARTED again, got s1=%v s2=%v", s1.State(), s2.State())
	}
	if n := set.CountActiveServices(); n != 2 {
		t.Errorf("active services = %d, want 2", n)
	}
}

// test_softdep8: the same unexpected stop WITHOUT auto-restart settles
// at STOPPED, and the waits-for dependent still stays up.
func TestPortedSoftdep8(t *testing.T) {
	set, _ := newTestSet()
	svcs := addTestSvcs(t, set, "test-service-1", "test-service-2")
	s1, s2 := svcs[0], svcs[1]
	s1.autoStart = false
	s1.autoStop = false
	s2.Record().AddDep(s1, DepWaitsFor)

	set.StartService(s2)
	set.ProcessQueues()
	s1.Started()
	set.ProcessQueues()
	if s1.State() != StateStarted || s2.State() != StateStarted {
		t.Fatalf("both should be STARTED, got s1=%v s2=%v", s1.State(), s2.State())
	}
	if n := set.CountActiveServices(); n != 2 {
		t.Fatalf("active services = %d, want 2", n)
	}

	s1.Record().ForcedStop()
	set.ProcessQueues()
	if got := s1.Record().TargetState(); got != StateStopped {
		t.Errorf("s1 will not restart, so target should be STOPPED, got %v", got)
	}

	s1.Stopped()
	set.ProcessQueues()
	if s1.State() != StateStopped {
		t.Errorf("s1 should stay STOPPED, got %v", s1.State())
	}
	if s2.State() != StateStarted {
		t.Errorf("s2 should stay STARTED, got %v", s2.State())
	}
}

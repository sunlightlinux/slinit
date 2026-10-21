package service

import "testing"

// Ported from dinit's test_order1..3 (test_order4 is already covered by
// TestStopOrderWaitsForHardDependentToActuallyStop, which was written
// for the bug it found).
//
// Mapping note, because this is where a port goes quietly wrong:
// dinit's BEFORE is "specified via the to service" (service-constants.h),
// so a link stored on s2 pointing at s1 with BEFORE means **s1 starts
// before s2** — the stored direction is the inverse of the declared one.
// slinit's DepBefore keeps the declared direction ("this starts before
// target"), so the faithful translation of dinit's `s2{{s1, BEFORE}}` is
// `s2.AddDep(s1, DepAfter)`. dinit's own assertions confirm it: starting
// s3 brings s1 up first.

// test_order1: an ordering constraint sequences the start of two hard
// dependencies of the same service, and the stop unwinds in reverse.
func TestPortedOrder1(t *testing.T) {
	set, _ := newTestSet()
	svcs := addTestSvcs(t, set, "test-service-1", "test-service-2", "test-service-3")
	s1, s2, s3 := svcs[0], svcs[1], svcs[2]
	for _, s := range svcs {
		s.autoStart = false
		s.autoStop = false
	}
	s2.Record().AddDep(s1, DepAfter) // s1 before s2
	s3.Record().AddDep(s2, DepRegular)
	s3.Record().AddDep(s1, DepRegular)

	set.StartService(s3)
	set.ProcessQueues()
	if !s1.upReqd {
		t.Fatal("s1 should be asked up first")
	}
	if s2.upReqd {
		t.Error("s2 must not be asked up before s1 reports started")
	}
	if s3.upReqd {
		t.Error("s3 must not be asked up before its dependencies")
	}

	s1.Started()
	set.ProcessQueues()
	if !s2.upReqd {
		t.Error("s2 should be asked up once s1 started")
	}
	if s3.upReqd {
		t.Error("s3 must still wait for s2")
	}

	s2.Started()
	set.ProcessQueues()
	if !s3.upReqd {
		t.Error("s3 should be asked up once s2 started")
	}

	s3.Started()
	set.ProcessQueues()
	for _, s := range svcs {
		if s.State() != StateStarted {
			t.Fatalf("%s should be STARTED, got %v", s.Name(), s.State())
		}
	}

	// Stopping s3 takes both dependencies with it.
	s3.Record().Stop(true)
	set.ProcessQueues()
	for _, s := range svcs {
		if s.State() != StateStopping {
			t.Errorf("%s should be STOPPING, got %v", s.Name(), s.State())
		}
		if got := s.Record().TargetState(); got != StateStopped {
			t.Errorf("%s target should be STOPPED, got %v", s.Name(), got)
		}
	}

	s3.Stopped()
	set.ProcessQueues()
	s2.Stopped()
	set.ProcessQueues()
	s1.Stopped()
	set.ProcessQueues()
	for _, s := range svcs {
		if s.State() != StateStopped {
			t.Errorf("%s should be STOPPED, got %v", s.Name(), s.State())
		}
	}
	if n := set.CountActiveServices(); n != 0 {
		t.Errorf("active services = %d, want 0", n)
	}
}

// test_order2: an ordering constraint alone does not pull a service up.
// s3 needs s2, and s2 is ordered after s1 — but s1 is never started,
// because ordering carries no requirement.
func TestPortedOrder2(t *testing.T) {
	set, _ := newTestSet()
	svcs := addTestSvcs(t, set, "test-service-1", "test-service-2", "test-service-3")
	s1, s2, s3 := svcs[0], svcs[1], svcs[2]
	for _, s := range svcs {
		s.autoStart = false
		s.autoStop = false
	}
	s2.Record().AddDep(s1, DepAfter) // s1 before s2, ordering only
	s3.Record().AddDep(s2, DepRegular)

	set.StartService(s3)
	set.ProcessQueues()
	if s1.upReqd {
		t.Error("s1 must NOT be started: ordering does not require it")
	}
	if !s2.upReqd {
		t.Error("s2 should be asked up")
	}

	s2.Started()
	set.ProcessQueues()
	if !s3.upReqd {
		t.Error("s3 should be asked up once s2 started")
	}
	s3.Started()
	set.ProcessQueues()
	if s1.State() != StateStopped {
		t.Errorf("s1 should still be STOPPED, got %v", s1.State())
	}
	if s2.State() != StateStarted || s3.State() != StateStarted {
		t.Errorf("s2/s3 should be STARTED, got s2=%v s3=%v", s2.State(), s3.State())
	}
}

// test_order3: the AFTER form of the same constraint. dinit's AFTER is
// "specified via the from service", so it keeps the declared direction
// and maps to slinit's DepBefore — the mirror of the note above. Like
// ordering generally, it pulls nothing up: s1 stays STOPPED throughout.
func TestPortedOrder3(t *testing.T) {
	set, _ := newTestSet()
	svcs := addTestSvcs(t, set, "test-service-1", "test-service-2", "test-service-3")
	s1, s2, s3 := svcs[0], svcs[1], svcs[2]
	for _, s := range svcs {
		s.autoStart = false
		s.autoStop = false
	}
	s2.Record().AddDep(s1, DepBefore)
	s3.Record().AddDep(s2, DepRegular)

	set.StartService(s3)
	set.ProcessQueues()
	if s1.upReqd {
		t.Error("s1 must NOT be started: ordering does not require it")
	}
	if !s2.upReqd {
		t.Error("s2 should be asked up")
	}
	if s3.upReqd {
		t.Error("s3 must wait for s2")
	}

	s2.Started()
	set.ProcessQueues()
	s3.Started()
	set.ProcessQueues()
	if s3.State() != StateStarted || s2.State() != StateStarted {
		t.Fatalf("s2/s3 should be STARTED, got s2=%v s3=%v", s2.State(), s3.State())
	}
	if s1.State() != StateStopped {
		t.Errorf("s1 should be STOPPED, got %v", s1.State())
	}

	s3.Record().Stop(true)
	set.ProcessQueues()
	if s3.State() != StateStopping {
		t.Errorf("s3 should be STOPPING, got %v", s3.State())
	}
	if s2.State() != StateStopping {
		t.Errorf("s2 should be STOPPING, got %v", s2.State())
	}
	if s1.State() != StateStopped {
		t.Errorf("s1 should still be STOPPED, got %v", s1.State())
	}
	for _, s := range svcs {
		if got := s.Record().TargetState(); got != StateStopped {
			t.Errorf("%s target should be STOPPED, got %v", s.Name(), got)
		}
	}

	s3.Stopped()
	set.ProcessQueues()
	s2.Stopped()
	set.ProcessQueues()
	for _, s := range svcs {
		if s.State() != StateStopped {
			t.Errorf("%s should be STOPPED, got %v", s.Name(), s.State())
		}
	}
	if n := set.CountActiveServices(); n != 0 {
		t.Errorf("active services = %d, want 0", n)
	}
}

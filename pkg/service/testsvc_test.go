package service

import "testing"

// testSvc is a service whose transitions the test drives, ported from
// dinit's src/tests/test_service.h.
//
// Why the suite needs this at all: InternalService calls Started() and
// Stopped() from inside BringUp/BringDown, so a service reaches its
// target state in the same instant it is asked to move. Nothing is ever
// observably STARTING or STOPPING, which means none of the 35 test
// files built on InternalService can check a transition in progress —
// and two real bugs lived in exactly that blind spot, both found by
// dinit rather than here: s6 readiness accepting any byte, and
// stopCheckDependents reading a flag instead of the dependent's state.
//
// autoStart and autoStop both default to true, so a testSvc behaves
// exactly like InternalService unless a test says otherwise. Setting
// either false is what makes STARTING or STOPPING observable, and that
// is the only reason this type exists.
//
// This differs deliberately from dinit, which has two classes: plain
// service_record for the auto-completing cases and test_service, with
// auto_stop only, for held ones. One type with two switches covers
// both, so a ported test differs from its original only in which
// switches it flips.
//
// The invariant checks in Stopped() and Started() are dinit's, and they
// are the quiet value of this harness: every test built on it is
// checking the state machine's contract whether or not the test author
// thought about it.
type testSvc struct {
	ServiceRecord
	t             *testing.T
	upReqd        bool
	downReqd      bool
	autoStart     bool
	autoStop      bool
	interruptible bool
}

func newTestSvc(t *testing.T, set *ServiceSet, name string) *testSvc {
	s := &testSvc{t: t, autoStart: true, autoStop: true}
	s.ServiceRecord = *NewServiceRecord(s, set, name, TypeInternal)
	return s
}

func (s *testSvc) BringUp() bool {
	s.upReqd = true
	if s.autoStart {
		s.Started()
	}
	return true
}

func (s *testSvc) BringDown() {
	s.downReqd = true
	if s.autoStop {
		s.Stopped()
	}
}

// Stopped asserts the contract dinit's harness asserts: a service must
// not be told it stopped twice, must have been asked to come down
// first, and must not still be waiting on dependencies.
func (s *testSvc) Stopped() {
	s.t.Helper()
	if s.State() == StateStopped {
		s.t.Errorf("%s: Stopped() while already STOPPED", s.Name())
	}
	if !s.downReqd {
		s.t.Errorf("%s: Stopped() without a preceding BringDown()", s.Name())
	}
	if s.Record().waitingForDeps {
		s.t.Errorf("%s: Stopped() while still waiting for dependencies", s.Name())
	}
	s.downReqd = false
	s.ServiceRecord.Stopped()
}

func (s *testSvc) Started() {
	s.t.Helper()
	if !s.upReqd {
		s.t.Errorf("%s: Started() without a preceding BringUp()", s.Name())
	}
	s.ServiceRecord.Started()
}

func (s *testSvc) CanInterruptStart() bool { return s.interruptible }
func (s *testSvc) InterruptStart() bool    { return true }

var _ Service = (*testSvc)(nil)

// addTestSvcs builds n services named test-service-1..n and registers
// them, which is the preamble of every ported case.
func addTestSvcs(t *testing.T, set *ServiceSet, names ...string) []*testSvc {
	t.Helper()
	out := make([]*testSvc, 0, len(names))
	for _, n := range names {
		s := newTestSvc(t, set, n)
		set.AddService(s)
		out = append(out, s)
	}
	return out
}

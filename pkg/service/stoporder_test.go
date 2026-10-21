package service

import "testing"

// A service whose transitions the test drives, mirroring dinit's
// test_service: BringUp/BringDown only record that they were asked, so
// the test decides when each service actually starts or stops.
type orderTestSvc struct {
	ServiceRecord
	upReqd   bool
	downReqd bool
}

func newOrderTestSvc(set *ServiceSet, name string) *orderTestSvc {
	s := &orderTestSvc{}
	s.ServiceRecord = *NewServiceRecord(s, set, name, TypeInternal)
	return s
}

func (s *orderTestSvc) BringUp() bool           { s.upReqd = true; return true }
func (s *orderTestSvc) BringDown()              { s.downReqd = true }
func (s *orderTestSvc) CanInterruptStart() bool { return true }
func (s *orderTestSvc) InterruptStart() bool    { return true }

var _ Service = (*orderTestSvc)(nil)

// A soft dependent must not short-circuit stop ordering: a dependency
// has to wait for its HARD dependents to actually reach STOPPED, not
// merely to have started stopping.
//
// The shape is dinit's test_order4, written for its issue #591: s2
// depends on s1 regularly, s3 waits-for both s2 and s1. Stopping s3
// tears all three down, and s1 must stay up until s2 is gone.
//
// This caught a real bug. stopCheckDependents tested the dependency
// link's HoldingAcq flag, which is released when the dependent *begins*
// stopping, so s1 was brought down while s2 was still STOPPING —
// measured, with s1.downReqd true one step too early. dinit had the
// same defect and fixed it in 5409175; its author noted that no test
// there failed either.
//
// Driving transitions by hand is the whole point. InternalService calls
// Started/Stopped inside BringUp/BringDown, so every service reaches its
// target state in the same instant and no ordering is observable at
// all. Two earlier attempts to measure this with real process services
// proved nothing for exactly that reason: the harness never execs, so
// all three transitioned within 130ns and the "order" I recorded was
// first Go's randomised map iteration and then my own array order.
func TestStopOrderWaitsForHardDependentToActuallyStop(t *testing.T) {
	set, _ := newTestSet()
	s1 := newOrderTestSvc(set, "p1")
	s2 := newOrderTestSvc(set, "p2")
	s3 := newOrderTestSvc(set, "p3")
	set.AddService(s1)
	set.AddService(s2)
	set.AddService(s3)
	s2.Record().AddDep(s1, DepRegular)
	s3.Record().AddDep(s2, DepWaitsFor)
	s3.Record().AddDep(s1, DepWaitsFor)

	st := func(tag string) {
		t.Logf("%-22s s1=%-8v s2=%-8v s3=%-8v  downReqd: s1=%-5v s2=%-5v s3=%v",
			tag, s1.State(), s2.State(), s3.State(), s1.downReqd, s2.downReqd, s3.downReqd)
	}

	set.StartService(s3)
	set.ProcessQueues()
	s1.Started()
	set.ProcessQueues()
	s2.Started()
	set.ProcessQueues()
	s3.Started()
	set.ProcessQueues()
	st("dupa pornire")

	set.StopService(s3)
	set.ProcessQueues()
	st("dupa stop(s3)")

	s3.Stopped()
	set.ProcessQueues()
	st("dupa s3.Stopped()")
	if s1.downReqd {
		t.Errorf("BUG: s1 a primit BringDown desi s2 (dependent hard) e %v", s2.State())
	}

	s2.Stopped()
	set.ProcessQueues()
	st("dupa s2.Stopped()")
	if !s1.downReqd {
		t.Errorf("s1 ar trebui sa coboare acum ca s2 e STOPPED, dar downReqd=false")
	}
}

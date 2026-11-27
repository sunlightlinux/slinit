package service

import "testing"

// MarkOperatorStop says a container that ends now was stopped on
// request (exit 0). A service that then stops on its own, not because
// anyone asked, takes that back: `slinitctl stop extra` followed later
// by a crash of the workload used to end the container with exit 0.
func TestUnrequestedStopClearsOperatorStop(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "app")
	set.AddService(svc)

	set.MarkOperatorStop()
	svc.Record().stopReason = ReasonNormal
	set.ServiceActive(svc)
	set.ServiceInactive(svc)
	if !set.OperatorStopRequested() {
		t.Fatal("a normal stop cleared the operator's stop request")
	}

	for _, r := range []StoppedReason{ReasonTerminated, ReasonFailed, ReasonDepFailed, ReasonExecFailed, ReasonTimedOut} {
		set.MarkOperatorStop()
		svc.Record().stopReason = r
		set.ServiceActive(svc)
		set.ServiceInactive(svc)
		if set.OperatorStopRequested() {
			t.Errorf("a stop for reason %v still counted as a stop on request", r)
		}
	}
}

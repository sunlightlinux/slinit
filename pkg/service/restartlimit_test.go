package service

import (
	"testing"
)

// TestRestartLimitFailedStatePostCondition verifies the post-condition
// of the restart-limit-exhausted branch in doStop: once the branch has
// marked desired=Stopped and startFailed=true, a subsequent Stopped()
// invocation does NOT call initiateStart and the failed flag is not
// silently reset.
//
// Background: when restart-limit-count is exhausted, doStop sets
// these flags so the auto-restart loop terminates. Without that the
// service thrashes initiateStart → exit → Stopped → initiateStart and
// slinitctl is-failed races on whichever moment it samples.
func TestRestartLimitFailedStatePostCondition(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "rl")
	set.AddService(svc)

	rec := svc.Record()
	rec.desired.Store(StateStopped) // restart-limit branch sets this
	rec.startFailed = true          // restart-limit branch sets this
	rec.state.Store(StateStopping)
	rec.Stopped()
	set.ProcessQueues()

	if rec.State() != StateStopped {
		t.Errorf("expected STOPPED after Stopped() with desired=Stopped, got %v", rec.State())
	}
	if !rec.DidStartFail() {
		t.Error("DidStartFail should remain true after Stopped(); got false")
	}
}

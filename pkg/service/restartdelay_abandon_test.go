package service

import (
	"testing"
	"time"
)

// A service whose restart was deferred to a restart-delay timer sits in
// STOPPED while still counted active — from the state machine's point
// of view it is mid-restart. If the restart is then called off, the
// active count has to come down, because nothing else will bring it
// down: every stop door is guarded by `state != STOPPED` and finds
// nothing to do on a service that is already stopped.
//
// Left unreleased, CountActiveServices never reaches zero again, so
// the event loop sits out the whole emergency timeout at shutdown
// (90s by default) and cannot even name a blocking service — every
// record really is STOPPED. One `slinitctl restart` of a type=process
// service was enough to arm it; performance case 730 did exactly that,
// which is how a reboot after the perf suite came to take 90 seconds.
func TestStopReleasesServiceWaitingOnRestartDelay(t *testing.T) {
	set, _ := newTestSet()

	svc := NewProcessService(set, "delayed")
	svc.SetCommand([]string{"/bin/sleep", "3600"})
	// Long enough that the stop below lands inside the delay window.
	svc.SetRestartDelay(10 * time.Second)
	set.AddService(svc)

	set.StartService(svc)
	mustReachState(t, svc, StateStarted)
	if got := set.CountActiveServices(); got != 1 {
		t.Fatalf("active count after start = %d, want 1", got)
	}

	// `slinitctl restart` is a stop followed by a start on the same
	// connection: the start arrives while the service is still
	// STOPPING, so Stopped() sees desired=STARTED and defers to the
	// restart-delay timer instead of going inactive.
	set.StopService(svc)
	set.StartService(svc)
	mustReachState(t, svc, StateStopped)
	if got := set.CountActiveServices(); got != 1 {
		t.Fatalf("active count while awaiting restart delay = %d, want 1", got)
	}

	// Now call the restart off, the way shutdown does.
	set.StopAllServices(ShutdownHalt)

	if got := set.CountActiveServices(); got != 0 {
		t.Errorf("active count after stopping a service that was awaiting "+
			"its restart delay = %d, want 0 (shutdown would wait out the "+
			"full emergency timeout with no blocker to report)", got)
	}
	if st := svc.State(); st != StateStopped {
		t.Errorf("state = %v, want STOPPED", st)
	}
}

// Same hole through the timer's own door: if the restart is called off
// while the timer is still armed and nothing cancels it, the callback
// must release the count when it declines to restart.
func TestAbandonedRestartTimerReleasesActiveCount(t *testing.T) {
	set, _ := newTestSet()

	svc := NewProcessService(set, "abandoned")
	svc.SetCommand([]string{"/bin/sleep", "3600"})
	svc.SetRestartDelay(300 * time.Millisecond)
	set.AddService(svc)

	set.StartService(svc)
	mustReachState(t, svc, StateStarted)

	set.StopService(svc)
	set.StartService(svc)
	mustReachState(t, svc, StateStopped)

	// Flip desired to STOPPED without going through Stop(), so the
	// armed timer survives and its callback is the one that has to
	// give up the restart.
	set.queueMu.Lock()
	svc.Record().desired.Store(StateStopped)
	set.queueMu.Unlock()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if set.CountActiveServices() == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("restart-delay timer declined to restart but left the active "+
		"count at %d, want 0", set.CountActiveServices())
}

// The same "STOPPED but still counted active" state is also what
// doStart's wasActive test reads, so a start that arrives while the
// restart-delay timer is holding the restart used to take the active
// count a second time. Three `slinitctl restart`s in a row were enough,
// because every third one landed inside the delay window — which is why
// performance case 730 (30 restarts of a type=process service) left the
// count permanently above zero.
func TestRestartInsideDelayWindowCountsOnce(t *testing.T) {
	set, _ := newTestSet()

	svc := NewProcessService(set, "recounted")
	svc.SetCommand([]string{"/bin/sleep", "3600"})
	svc.SetRestartDelay(10 * time.Second)
	set.AddService(svc)

	set.StartService(svc)
	mustReachState(t, svc, StateStarted)

	// Park it on the restart-delay timer: stop, then start again while
	// it is still STOPPING, exactly as `slinitctl restart` does.
	set.StopService(svc)
	set.StartService(svc)
	mustReachState(t, svc, StateStopped)

	// A further start now lands on a service that is STOPPED but
	// already counted.
	set.StartService(svc)
	if got := set.CountActiveServices(); got != 1 {
		t.Fatalf("active count after a start inside the restart-delay "+
			"window = %d, want 1 (the service was already counted)", got)
	}

	set.StopAllServices(ShutdownHalt)
	mustReachState(t, svc, StateStopped)
	if got := set.CountActiveServices(); got != 0 {
		t.Errorf("active count after shutdown = %d, want 0", got)
	}
}

func mustReachState(t *testing.T, svc Service, want ServiceState) {
	t.Helper()
	if got := waitState(t, svc, want, 5*time.Second); got != want {
		t.Fatalf("service %s stuck in %v, want %v", svc.Name(), got, want)
	}
}

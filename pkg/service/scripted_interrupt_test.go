package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// scriptedOutcome is what a test checks once a scripted start has ended,
// read under queueMu, which the state machine holds while it writes these.
type scriptedOutcome struct {
	state     ServiceState
	pid       int
	failures  int
	isFailed  bool
	reason    StoppedReason
	failedEv  int
	cancelEv  int
	stopCmdRn bool
}

func readScripted(set *ServiceSet, lg *testLogger, svc *ScriptedService, rec *eventRecorder, marker string) scriptedOutcome {
	set.queueMu.Lock()
	defer set.queueMu.Unlock()
	lg.mu.Lock()
	defer lg.mu.Unlock()
	_, err := os.Stat(marker)
	return scriptedOutcome{
		state:     svc.State(),
		pid:       svc.PID(),
		failures:  len(lg.failed),
		isFailed:  svc.IsFailed(),
		reason:    svc.StopReason(),
		failedEv:  rec.count(EventFailedStart),
		cancelEv:  rec.count(EventStartCancelled),
		stopCmdRn: err == nil,
	}
}

// newInterruptibleScripted returns a start-interruptible scripted service
// running start, whose stop command creates the returned marker file.
func newInterruptibleScripted(t *testing.T, set *ServiceSet, name, start string) (*ScriptedService, *eventRecorder, string) {
	marker := filepath.Join(t.TempDir(), "stopped")
	svc := NewScriptedService(set, name)
	svc.SetStartCommand([]string{"/bin/sh", "-c", start})
	svc.SetStopCommand([]string{"/bin/sh", "-c", "touch " + marker})
	f := svc.Flags
	f.StartInterruptible = true
	svc.SetFlags(f)
	svc.SetStartTimeout(0)
	rec := &eventRecorder{}
	svc.AddListener(rec)
	set.AddService(svc)
	return svc, rec, marker
}

// A stop while a start-interruptible start command runs cancels the
// start: STOPPING at once, and when the command dies of the SIGINT the
// stop command cleans up, as in dinit. It used to stay STARTING and take
// the command's death for a failed start — reported failed, FAILEDSTART
// emitted, no stop command.
func TestScriptedStopDuringStartIsNotAFailure(t *testing.T) {
	set, lg := newTestSet()
	svc, rec, marker := newInterruptibleScripted(t, set, "sc-cancel", "exec sleep 60")
	set.StartService(svc)
	time.Sleep(200 * time.Millisecond)
	set.StopService(svc)
	set.queueMu.Lock()
	st := svc.State()
	set.queueMu.Unlock()
	if st != StateStopping {
		t.Errorf("right after the stop: state %v, want STOPPING", st)
	}
	got := waitForState(t, svc, StateStopped, 3*time.Second)
	o := readScripted(set, lg, svc, rec, marker)
	if got != StateStopped {
		killLeftover(o.pid)
		t.Fatalf("state %v, want STOPPED", got)
	}
	if o.failures != 0 || o.failedEv != 0 || o.isFailed || o.reason != ReasonNormal || o.cancelEv != 1 {
		t.Errorf("cancelled start: %d failure(s), %d FAILEDSTART, IsFailed=%v, reason=%v, %d STARTCANCELLED",
			o.failures, o.failedEv, o.isFailed, o.reason, o.cancelEv)
	}
	if !o.stopCmdRn {
		t.Error("the stop command did not run after a start cancelled by a signal")
	}
}

// A start command that answers the SIGINT by exiting with a failure
// status needs no cleanup (dinit): the service stops without running the
// stop command, and still without a failure.
func TestScriptedCancelledStartFailureExitSkipsStopCommand(t *testing.T) {
	set, lg := newTestSet()
	svc, rec, marker := newInterruptibleScripted(t, set, "sc-cancel-exit",
		"trap 'exit 3' INT; while :; do sleep 0.05; done")
	set.StartService(svc)
	time.Sleep(200 * time.Millisecond)
	set.StopService(svc)
	got := waitForState(t, svc, StateStopped, 3*time.Second)
	o := readScripted(set, lg, svc, rec, marker)
	if got != StateStopped {
		killLeftover(o.pid)
		t.Fatalf("state %v, want STOPPED", got)
	}
	if o.failures != 0 || o.isFailed {
		t.Errorf("cancelled start: %d failure(s), IsFailed=%v", o.failures, o.isFailed)
	}
	if o.stopCmdRn {
		t.Error("the stop command ran after a start command that exited with a failure status")
	}
}

// A start command that ignores the SIGINT is killed at the stop-timeout.
// It used to keep the service STARTING for good.
func TestScriptedStopDuringStartEscalatesPastIgnoredSIGINT(t *testing.T) {
	set, lg := newTestSet()
	svc, rec, marker := newInterruptibleScripted(t, set, "sc-ignore",
		"trap '' INT; while :; do sleep 0.1; done")
	svc.SetStopTimeout(1 * time.Second)
	set.StartService(svc)
	time.Sleep(200 * time.Millisecond)
	set.StopService(svc)
	got := waitForState(t, svc, StateStopped, 5*time.Second)
	o := readScripted(set, lg, svc, rec, marker)
	if got != StateStopped {
		killLeftover(o.pid)
		t.Fatalf("5s after the stop (stop-timeout 1s): state %v, want STOPPED", got)
	}
	if o.failures != 0 || o.isFailed {
		t.Errorf("cancelled start: %d failure(s), IsFailed=%v", o.failures, o.isFailed)
	}
	if !o.stopCmdRn {
		t.Error("the stop command did not run after the start command was killed")
	}
}

// An operator's stop of a STARTING service is not a failure, so it must
// not fire the failure-action. Measured before: reboot.
func TestScriptedCancelledStartFiresNoFailureAction(t *testing.T) {
	set, _ := newTestSet()
	var fired []SystemAction
	set.OnSystemAction = func(a SystemAction, _ string) { fired = append(fired, a) }
	svc, _, _ := newInterruptibleScripted(t, set, "sc-action", "exec sleep 60")
	svc.SetFailureAction(ActionReboot)
	set.StartService(svc)
	time.Sleep(200 * time.Millisecond)
	set.StopService(svc)
	waitForState(t, svc, StateStopped, 3*time.Second)
	time.Sleep(100 * time.Millisecond)
	set.queueMu.Lock()
	defer set.queueMu.Unlock()
	if len(fired) != 0 {
		t.Errorf("an operator stop of a starting service fired %v", fired)
	}
}

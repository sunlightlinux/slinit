package service

import (
	"fmt"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

type eventRecorder struct{ events []ServiceEvent }

func (r *eventRecorder) ServiceEvent(_ Service, e ServiceEvent) { r.events = append(r.events, e) }

func (r *eventRecorder) count(e ServiceEvent) int {
	n := 0
	for _, x := range r.events {
		if x == e {
			n++
		}
	}
	return n
}

// startOutcome is what a test checks once a start has ended: read under
// queueMu, which the state machine holds while it writes these.
type startOutcome struct {
	pid      int
	failures int
	isFailed bool
	reason   StoppedReason
	failedEv int
}

func readOutcome(set *ServiceSet, lg *testLogger, svc *ProcessService, rec *eventRecorder) startOutcome {
	set.queueMu.Lock()
	defer set.queueMu.Unlock()
	return startOutcome{
		pid:      svc.PID(),
		failures: len(lg.failed),
		isFailed: svc.IsFailed(),
		reason:   svc.StopReason(),
		failedEv: rec.count(EventFailedStart),
	}
}

func newNotifyService(set *ServiceSet, name, script string) (*ProcessService, *eventRecorder) {
	svc := NewProcessService(set, name)
	svc.SetCommand([]string{"/bin/sh", "-c", script})
	svc.SetReadyNotification(3, "")
	rec := &eventRecorder{}
	svc.AddListener(rec)
	set.AddService(svc)
	return svc, rec
}

func killLeftover(pid int) {
	if pid > 0 {
		syscall.Kill(-pid, syscall.SIGKILL)
		syscall.Kill(pid, syscall.SIGKILL)
	}
}

// A stop while STARTING sends SIGINT. A process that ignores it used to
// hold the service in STARTING for good: no stop-timeout was armed. dinit
// moves to STOPPING and arms the stop-timeout, which escalates to SIGKILL.
// The start was cancelled by the operator, so it is not a failure.
func TestStopDuringStartEscalatesPastIgnoredSIGINT(t *testing.T) {
	set, lg := newTestSet()
	svc, rec := newNotifyService(set, "int-ignore", "trap '' INT; sleep 60")
	svc.SetStartTimeout(0)
	svc.SetStopTimeout(1 * time.Second)
	set.StartService(svc)
	time.Sleep(200 * time.Millisecond)
	pid := lockedPID(set, svc)
	if pid == 0 || svc.State() != StateStarting {
		t.Fatalf("not starting: state=%v pid=%d", svc.State(), pid)
	}
	set.StopService(svc)
	got := waitForState(t, svc, StateStopped, 5*time.Second)
	o := readOutcome(set, lg, svc, rec)
	if got != StateStopped {
		killLeftover(pid)
		t.Fatalf("stop of a STARTING service never completed: state=%v pid=%d", got, o.pid)
	}
	if o.failures != 0 || o.failedEv != 0 || o.isFailed {
		t.Errorf("cancelled start reported as failed: %d failure(s), %d FAILEDSTART, IsFailed=%v",
			o.failures, o.failedEv, o.isFailed)
	}
}

// A start cancelled by a stop, with a process that honours SIGINT, ends as
// a normal stop: it was reported as a failed start, twice.
func TestCancelledStartIsNotAFailure(t *testing.T) {
	set, lg := newTestSet()
	svc, rec := newNotifyService(set, "cancel", "exec sleep 60")
	set.StartService(svc)
	time.Sleep(200 * time.Millisecond)
	set.StopService(svc)
	got := waitForState(t, svc, StateStopped, 3*time.Second)
	o := readOutcome(set, lg, svc, rec)
	if got != StateStopped {
		killLeftover(o.pid)
		t.Fatalf("state %v, want STOPPED", got)
	}
	set.queueMu.Lock()
	cancelled := rec.count(EventStartCancelled)
	set.queueMu.Unlock()
	if o.failures != 0 || o.failedEv != 0 || o.isFailed || o.reason != ReasonNormal || cancelled != 1 {
		t.Errorf("cancelled start: %d failure(s), %d FAILEDSTART, IsFailed=%v, reason=%v, %d STARTCANCELLED",
			o.failures, o.failedEv, o.isFailed, o.reason, cancelled)
	}
}

// The start-timeout SIGINTs the process. One that ignores it used to stay
// STARTING with nothing left to escalate.
func TestStartTimeoutEscalatesPastIgnoredSIGINT(t *testing.T) {
	set, lg := newTestSet()
	svc, rec := newNotifyService(set, "st-ignore", "trap '' INT; sleep 60")
	svc.SetStartTimeout(500 * time.Millisecond)
	svc.SetStopTimeout(1 * time.Second)
	set.StartService(svc)
	time.Sleep(200 * time.Millisecond)
	pid := lockedPID(set, svc)
	got := waitForState(t, svc, StateStopped, 5*time.Second)
	o := readOutcome(set, lg, svc, rec)
	if got != StateStopped {
		killLeftover(pid)
		t.Fatalf("start-timeout never escalated past SIGINT: state=%v pid=%d", got, o.pid)
	}
	if o.failures != 1 || o.failedEv != 1 || o.reason != ReasonTimedOut {
		t.Errorf("timed-out start: %d failure(s), %d FAILEDSTART, reason %v; want 1, 1, timed-out",
			o.failures, o.failedEv, o.reason)
	}
}

// One timed-out start is one failure. It was reported three times: by
// the timeout, by the process's exit while still STARTING, and again.
func TestStartTimeoutReportsOneFailure(t *testing.T) {
	set, lg := newTestSet()
	svc, rec := newNotifyService(set, "st-once", "exec sleep 60")
	svc.SetStartTimeout(300 * time.Millisecond)
	set.StartService(svc)
	got := waitForState(t, svc, StateStopped, 3*time.Second)
	time.Sleep(200 * time.Millisecond) // let any late report land
	o := readOutcome(set, lg, svc, rec)
	if got != StateStopped {
		killLeftover(o.pid)
		t.Fatalf("state %v, want STOPPED", got)
	}
	if o.failures != 1 || o.failedEv != 1 {
		t.Errorf("one timed-out start reported %d time(s), %d FAILEDSTART event(s)",
			o.failures, o.failedEv)
	}
}

// A process that exits without signalling ready closes the readiness pipe
// and exits: one failed start, reported once — the pipe's EOF and the
// exit each reported it.
func TestReadyEOFAndExitReportOneFailure(t *testing.T) {
	for i := 0; i < 5; i++ { // EOF and exit race; try both orders
		set, lg := newTestSet()
		svc, rec := newNotifyService(set, fmt.Sprintf("eof-exit-%d", i), "exit 1")
		set.StartService(svc)
		got := waitForState(t, svc, StateStopped, 3*time.Second)
		time.Sleep(100 * time.Millisecond)
		o := readOutcome(set, lg, svc, rec)
		if got != StateStopped || o.failures != 1 || o.failedEv != 1 {
			t.Fatalf("run %d: state %v, %d failure(s), %d FAILEDSTART; want STOPPED, 1, 1",
				i, got, o.failures, o.failedEv)
		}
	}
}

// A process that closes the readiness fd without signalling, and keeps
// running, has failed to start. The service stayed STARTING with the
// process alive for good; dinit fails the start and brings it down.
func TestReadyEOFWhileAliveBringsProcessDown(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	set, lg := newTestSet()
	svc, rec := newNotifyService(set, "eof-alive",
		"echo $$ > "+pidFile+"; exec 3>&-; exec sleep 60")
	set.StartService(svc)
	got := waitForState(t, svc, StateStopped, 3*time.Second)
	pid := waitForDaemonPID(t, pidFile, 0, time.Second)
	o := readOutcome(set, lg, svc, rec)
	if got != StateStopped {
		killLeftover(pid)
		t.Fatalf("readiness EOF left the service %v with live pid %d", got, o.pid)
	}
	if pid == 0 {
		t.Fatal("the service never wrote its pid")
	}
	if syscall.Kill(pid, 0) == nil {
		killLeftover(pid)
		t.Errorf("process %d still running after the failed start", pid)
	}
	if o.failures != 1 || o.failedEv != 1 || o.reason != ReasonFailed {
		t.Errorf("%d failure(s), %d FAILEDSTART, reason %v; want 1, 1, failed",
			o.failures, o.failedEv, o.reason)
	}
}

// A smooth recovery whose new process never signals ready fails the
// recovery (dinit: process_service::start_timed_out → hard_failure). The
// start-timeout used to call failedToStart on the STARTED service instead.
func TestSmoothRecoveryStartTimeoutFailsRecovery(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran-once")
	set, lg := newTestSet()
	svc, rec := newNotifyService(set, "smooth-st", fmt.Sprintf(
		"if [ ! -e %[1]s ]; then touch %[1]s; echo ready >&3; fi; exec sleep 60", marker))
	svc.SetSmoothRecovery(true)
	svc.SetRestartDelay(10 * time.Millisecond)
	svc.SetStartTimeout(500 * time.Millisecond)
	set.StartService(svc)
	if waitForState(t, svc, StateStarted, 2*time.Second) != StateStarted {
		t.Fatal("not started")
	}
	first := lockedPID(set, svc)
	syscall.Kill(first, syscall.SIGKILL)
	second := waitForNewPID(set, svc, first, 2*time.Second)
	got := waitForState(t, svc, StateStopped, 4*time.Second)
	o := readOutcome(set, lg, svc, rec)
	if got != StateStopped {
		killLeftover(second)
		t.Fatalf("recovery that never became ready: state %v pid %d, want STOPPED", got, o.pid)
	}
	if o.failedEv != 0 || o.reason != ReasonFailed {
		t.Errorf("%d FAILEDSTART, reason %v; want a failed recovery (0, failed)", o.failedEv, o.reason)
	}
}

// The bgprocess twin of the ignored-SIGINT hang: a launcher that ignores
// the start-timeout's SIGINT is now killed by the stop-timeout, and the
// failure is reported once.
func TestBGStartTimeoutEscalatesPastIgnoredSIGINT(t *testing.T) {
	set, lg := newTestSet()
	svc := NewBGProcessService(set, "bg-st-ignore")
	svc.SetCommand([]string{"/bin/sh", "-c", "trap '' INT; sleep 60"})
	svc.SetPIDFile(filepath.Join(t.TempDir(), "never.pid"))
	svc.SetStartTimeout(500 * time.Millisecond)
	svc.SetStopTimeout(1 * time.Second)
	set.AddService(svc)
	set.StartService(svc)
	time.Sleep(200 * time.Millisecond)
	set.queueMu.Lock()
	pid := svc.PID()
	set.queueMu.Unlock()
	got := waitForState(t, svc, StateStopped, 5*time.Second)
	time.Sleep(100 * time.Millisecond)
	set.queueMu.Lock()
	failures, reason := len(lg.failed), svc.StopReason()
	set.queueMu.Unlock()
	if got != StateStopped {
		killLeftover(pid)
		t.Fatalf("bgprocess start-timeout never escalated past SIGINT: state %v", got)
	}
	if failures != 1 || reason != ReasonTimedOut {
		t.Errorf("%d failure(s), reason %v; want 1, timed-out", failures, reason)
	}
}

// A stop while a bgprocess launcher runs cancels the start: it ends as a
// stop, not a failure, even when the launcher ignores SIGINT.
func TestBGStopDuringStartIsNotAFailure(t *testing.T) {
	set, lg := newTestSet()
	svc := NewBGProcessService(set, "bg-cancel")
	svc.SetCommand([]string{"/bin/sh", "-c", "trap '' INT; sleep 60"})
	svc.SetPIDFile(filepath.Join(t.TempDir(), "never.pid"))
	svc.SetStartTimeout(0)
	svc.SetStopTimeout(1 * time.Second)
	set.AddService(svc)
	set.StartService(svc)
	time.Sleep(200 * time.Millisecond)
	set.queueMu.Lock()
	pid := svc.PID()
	set.queueMu.Unlock()
	set.StopService(svc)
	got := waitForState(t, svc, StateStopped, 5*time.Second)
	set.queueMu.Lock()
	failures, isFailed := len(lg.failed), svc.IsFailed()
	set.queueMu.Unlock()
	if got != StateStopped {
		killLeftover(pid)
		t.Fatalf("stop of a STARTING bgprocess never completed: state %v", got)
	}
	if failures != 0 || isFailed {
		t.Errorf("cancelled start: %d failure(s), IsFailed=%v; want none", failures, isFailed)
	}
}

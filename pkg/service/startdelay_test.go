package service

import (
	"os"
	"testing"
	"time"
)

// Nothing delayed a service's FIRST start: restart-delay governs only
// the gap before a re-start. The workaround was a sleeping
// pre-start-command, which runs inside the scheduling lock.
func TestStartDelayHoldsTheLaunchBack(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "late")
	svc.SetCommand([]string{"/bin/sh", "-c", "while :; do sleep 60; done"})
	svc.SetStartDelay(1500 * time.Millisecond)
	set.AddService(svc)

	set.StartService(svc)

	// Still STARTING, with no process, while the timer runs.
	time.Sleep(400 * time.Millisecond)
	if st := svc.State(); st != StateStarting {
		t.Errorf("state = %v during the delay, want STARTING", st)
	}
	if pid := svc.PID(); pid > 0 {
		t.Errorf("process already launched (pid %d) — the delay did nothing", pid)
	}

	if got := waitState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("state = %v, want STARTED after the delay", got)
	}
	if svc.PID() <= 0 {
		t.Error("no process after the delay elapsed")
	}

	set.StopService(svc)
	time.Sleep(500 * time.Millisecond)
}

// The whole point of a timer over a sleeping hook: other services keep
// moving while one waits.
func TestStartDelayDoesNotStallOtherServices(t *testing.T) {
	set, _ := newTestSet()

	slow := NewProcessService(set, "slow")
	slow.SetCommand([]string{"/bin/sh", "-c", "while :; do sleep 60; done"})
	slow.SetStartDelay(3 * time.Second)
	set.AddService(slow)

	other := newScripted(t, set, "other", []string{"/bin/true"}, nil)

	set.StartService(slow)

	start := time.Now()
	set.StartService(other)
	elapsed := time.Since(start)

	if got := waitState(t, other, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("other service state = %v, want STARTED", got)
	}
	if elapsed > time.Second {
		t.Errorf("starting an unrelated service took %v while another was in "+
			"its start-delay — the delay is holding the scheduling lock", elapsed)
	}

	set.StopService(slow)
	set.StopService(other)
	time.Sleep(500 * time.Millisecond)
}

// A stop during the wait must cancel it, not leave a timer that launches
// a process for a service nobody wants any more.
func TestStopDuringStartDelayCancelsTheLaunch(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "abandoned")
	svc.SetCommand([]string{"/bin/sh", "-c", "while :; do sleep 60; done"})
	svc.SetStartDelay(1500 * time.Millisecond)
	set.AddService(svc)

	set.StartService(svc)
	time.Sleep(300 * time.Millisecond)
	set.StopService(svc)

	// Well past when the timer would have fired.
	time.Sleep(2 * time.Second)

	if st := svc.State(); st != StateStopped {
		t.Errorf("state = %v, want STOPPED", st)
	}
	if pid := svc.PID(); pid > 0 {
		t.Errorf("a process was launched (pid %d) after the service was stopped", pid)
	}
}

// Without the directive, nothing waits.
func TestNoStartDelayLaunchesImmediately(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "prompt")
	svc.SetCommand([]string{"/bin/sh", "-c", "while :; do sleep 60; done"})
	set.AddService(svc)

	start := time.Now()
	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("state = %v, want STARTED", got)
	}
	if elapsed := time.Since(start); elapsed > 700*time.Millisecond {
		t.Errorf("took %v with no start-delay configured", elapsed)
	}

	set.StopService(svc)
	time.Sleep(500 * time.Millisecond)
}

// post-start-command lived after the start-delay early return, so a
// service with both never ran its post-start hook.
func TestStartDelayStillRunsPostStartCommand(t *testing.T) {
	set, _ := newTestSet()
	marker := t.TempDir() + "/post-start-ran"
	svc := NewProcessService(set, "delayed-hook")
	svc.SetCommand([]string{"/bin/sh", "-c", "while :; do sleep 60; done"})
	svc.SetStartDelay(300 * time.Millisecond)
	svc.SetPostStartCommand([]string{"/bin/touch", marker})
	set.AddService(svc)

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("state = %v, want STARTED", got)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("post-start-command never ran with start-delay set")
		}
		time.Sleep(50 * time.Millisecond)
	}

	set.StopService(svc)
	time.Sleep(500 * time.Millisecond)
}

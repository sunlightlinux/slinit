package service

import (
	"testing"
	"time"
)

// TestJitterZero and TestJitterInRange pin the contract of the jitter
// helper used by restart-randomized-delay. Non-positive → 0; positive
// bound → half-open [0, max).
func TestJitterZero(t *testing.T) {
	if d := jitter(0); d != 0 {
		t.Errorf("jitter(0) = %v, want 0", d)
	}
	if d := jitter(-time.Second); d != 0 {
		t.Errorf("jitter(-1s) = %v, want 0", d)
	}
}

func TestJitterInRange(t *testing.T) {
	const iterations = 200
	const bound = 100 * time.Millisecond
	sawNonZero := false
	for i := 0; i < iterations; i++ {
		d := jitter(bound)
		if d < 0 || d >= bound {
			t.Fatalf("jitter(%v) returned %v, out of [0, %v)", bound, d, bound)
		}
		if d > 0 {
			sawNonZero = true
		}
	}
	if !sawNonZero {
		t.Error("jitter always returned 0 across 200 iterations — suspect")
	}
}

// TestRestartRandomizedDelayFlatBaseline exercises the ProcessService
// nextRestartDelay path with jitter but no exponential backoff: the
// returned delay is base + jitter, always ≥ base and < base + max.
func TestRestartRandomizedDelayFlatBaseline(t *testing.T) {
	set, _ := newTestSet()
	svc := &ProcessService{}
	svc.services = set
	svc.SetRestartDelay(200 * time.Millisecond)
	svc.SetRestartRandomizedDelay(50 * time.Millisecond)
	for i := 0; i < 30; i++ {
		d := svc.nextRestartDelay()
		if d < 200*time.Millisecond || d >= 250*time.Millisecond {
			t.Fatalf("nextRestartDelay=%v out of [200ms, 250ms)", d)
		}
	}
}

// TestScheduleRestartWithBackoff exercises the hook that Stopped()
// consults before firing initiateStart on the willRestart path. The
// bug this fix closes: on `restart = on-failure` (non-smooth-recovery)
// slinit skipped the whole restart-delay / restart-delay-step /
// restart-delay-cap machinery — nextRestartDelay was only called from
// doSmoothRecovery — so a crash-looping service respawned as fast as
// the state machine could turn over, and functional test 53-restart-
// backoff caught it (gap1=1s from timing noise, gap2=0s from actual
// zero-delay). Now ScheduleRestartWithBackoff arms timerRestartDelay
// on the non-smooth path too. This test locks the four decision
// branches so future refactors can't quietly reintroduce the gap.
func TestScheduleRestartWithBackoff(t *testing.T) {
	t.Run("no delay configured → immediate", func(t *testing.T) {
		set, _ := newTestSet()
		svc := &ProcessService{}
		svc.services = set
		svc.ServiceRecord = *NewServiceRecord(svc, set, "no-delay", TypeProcess)
		if svc.ScheduleRestartWithBackoff() {
			t.Error("no restart-delay configured, want immediate (false)")
		}
		if svc.restartDelayTimer != nil {
			t.Error("restartDelayTimer set without delay")
		}
	})

	t.Run("delay elapsed since last start → immediate", func(t *testing.T) {
		set, _ := newTestSet()
		svc := &ProcessService{}
		svc.services = set
		svc.ServiceRecord = *NewServiceRecord(svc, set, "elapsed", TypeProcess)
		svc.SetRestartDelay(50 * time.Millisecond)
		svc.lastStartTime = time.Now().Add(-time.Hour) // long ago
		if svc.ScheduleRestartWithBackoff() {
			t.Error("delay already elapsed, want immediate (false)")
		}
		if svc.restartDelayTimer != nil {
			t.Error("restartDelayTimer set for elapsed delay")
		}
	})

	t.Run("delay not elapsed → armed", func(t *testing.T) {
		set, _ := newTestSet()
		svc := &ProcessService{}
		svc.services = set
		svc.ServiceRecord = *NewServiceRecord(svc, set, "arm", TypeProcess)
		svc.SetRestartDelay(5 * time.Second)
		svc.lastStartTime = time.Now() // just now
		if !svc.ScheduleRestartWithBackoff() {
			t.Error("delay not elapsed, want armed (true)")
		}
		if svc.restartDelayTimer == nil {
			t.Error("restartDelayTimer should be armed")
		} else {
			// Stop before end of test so AfterFunc callback doesn't
			// fire against a torn-down set + acquire its mutex.
			svc.restartDelayTimer.Stop()
			svc.restartDelayTimer = nil
		}
	})

	t.Run("progressive backoff advances step per call", func(t *testing.T) {
		set, _ := newTestSet()
		svc := &ProcessService{}
		svc.services = set
		svc.ServiceRecord = *NewServiceRecord(svc, set, "backoff", TypeProcess)
		svc.SetRestartDelay(1 * time.Second)
		svc.SetRestartBackoff(1*time.Second, 3*time.Second)
		// Simulate three restart cycles; nextRestartDelay returns
		// 1s, 2s, 3s (cap), then stays at 3s.
		want := []time.Duration{
			1 * time.Second,
			2 * time.Second,
			3 * time.Second,
			3 * time.Second,
		}
		for i, w := range want {
			got := svc.nextRestartDelay()
			if got != w {
				t.Errorf("nextRestartDelay call %d = %v, want %v", i+1, got, w)
			}
		}
	})

	t.Run("smooth-recovery bypasses this hook", func(t *testing.T) {
		set, _ := newTestSet()
		svc := &ProcessService{}
		svc.services = set
		svc.ServiceRecord = *NewServiceRecord(svc, set, "smooth", TypeProcess)
		svc.SetRestartDelay(5 * time.Second)
		svc.lastStartTime = time.Now()
		svc.doingSmoothRecov = true
		if svc.ScheduleRestartWithBackoff() {
			t.Error("smooth-recovery path owns its own delay; hook must return false")
		}
	})
}

// TestResetFailedClearsFlag confirms ResetFailed flips startFailed off
// while leaving other state untouched, matching systemctl reset-failed.
func TestResetFailedClearsFlag(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "rf")
	set.AddService(svc)
	rec := svc.Record()

	rec.startFailed = true
	if !rec.DidStartFail() {
		t.Fatal("precondition: DidStartFail should be true")
	}
	rec.ResetFailed()
	if rec.DidStartFail() {
		t.Error("ResetFailed did not clear startFailed")
	}
}

// TestRefusesManualStartStopDefault verifies both flags default to
// false so existing services keep behaving exactly as before.
func TestRefusesManualStartStopDefault(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "rmss")
	rec := svc.Record()
	if rec.RefusesManualStart() || rec.RefusesManualStop() {
		t.Error("refuse-manual-* must default to false")
	}
	rec.SetRefuseManualStart(true)
	rec.SetRefuseManualStop(true)
	if !rec.RefusesManualStart() || !rec.RefusesManualStop() {
		t.Error("setters did not stick")
	}
}

// TestStopWhenUnneededDefault confirms the flag is off by default so
// dependency graphs without the directive are unaffected.
func TestStopWhenUnneededDefault(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "swu")
	rec := svc.Record()
	if rec.StopsWhenUnneeded() {
		t.Error("stop-when-unneeded must default to false")
	}
	rec.SetStopWhenUnneeded(true)
	if !rec.StopsWhenUnneeded() {
		t.Error("SetStopWhenUnneeded did not stick")
	}
}

// TestStartLimitActionDefault confirms the action defaults to None so
// pre-existing services keep behaving exactly as they did before this
// hook landed.
func TestStartLimitActionDefault(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "sla")
	rec := svc.Record()
	if rec.StartLimitAction() != ActionNone {
		t.Errorf("start-limit-action default = %v, want ActionNone", rec.StartLimitAction())
	}
	rec.SetStartLimitAction(ActionReboot)
	if rec.StartLimitAction() != ActionReboot {
		t.Errorf("SetStartLimitAction did not stick: got %v", rec.StartLimitAction())
	}
}

package service

import (
	"testing"
	"time"
)

func TestScriptedServiceStartStop(t *testing.T) {
	set, _ := newTestSet()

	svc := NewScriptedService(set, "scripted-svc")
	svc.SetStartCommand([]string{"/bin/true"})
	svc.SetStopCommand([]string{"/bin/true"})
	set.AddService(svc)

	set.StartService(svc)

	// Wait for start command to complete
	time.Sleep(300 * time.Millisecond)

	if svc.State() != StateStarted {
		t.Errorf("expected STARTED, got %v", svc.State())
	}

	// Stop the service
	set.StopService(svc)

	time.Sleep(300 * time.Millisecond)

	if svc.State() != StateStopped {
		t.Errorf("expected STOPPED, got %v", svc.State())
	}
}

func TestScriptedServiceStartFail(t *testing.T) {
	set, _ := newTestSet()

	svc := NewScriptedService(set, "fail-svc")
	svc.SetStartCommand([]string{"/bin/false"})
	set.AddService(svc)

	set.StartService(svc)

	time.Sleep(300 * time.Millisecond)

	if svc.State() != StateStopped {
		t.Errorf("expected STOPPED after failed start, got %v", svc.State())
	}
	if !svc.DidStartFail() {
		t.Error("expected start to be marked as failed")
	}
}

func TestScriptedServiceExecFail(t *testing.T) {
	set, _ := newTestSet()

	svc := NewScriptedService(set, "exec-fail-svc")
	svc.SetStartCommand([]string{"/nonexistent/script"})
	set.AddService(svc)

	set.StartService(svc)

	time.Sleep(300 * time.Millisecond)

	if svc.State() != StateStopped {
		t.Errorf("expected STOPPED after exec fail, got %v", svc.State())
	}
}

// TestScriptedServiceNoStrayTimers is a defense-in-depth check that the
// process timer never survives past a lifecycle transition. Dinit had a
// bug (upstream fix 6ee41c74, 2026-08-27) where scripted_service's
// stop_timer clear sat inside a nested `if (interrupting_start)` branch
// and missed the normal start-OK / stop-OK exit paths, leaving a live
// timer that would spuriously fire on the next boot cycle. slinit's
// handleStartExit/handleStopExit both call cancelTimer() unconditionally
// at the top of the handler, and armTimer() cancels first before setting
// — so the bug is structurally prevented. This test locks in that
// invariant across five representative exit paths so any regression that
// moves the cancel into a conditional is caught.
func TestScriptedServiceNoStrayTimers(t *testing.T) {
	cases := []struct {
		name        string
		startCmd    []string
		stopCmd     []string
		alsoStop    bool
		startTO     time.Duration
		stopTO      time.Duration
		wantState   ServiceState
	}{
		{
			name:      "successful start + stop",
			startCmd:  []string{"/bin/true"},
			stopCmd:   []string{"/bin/true"},
			alsoStop:  true,
			startTO:   30 * time.Second,
			stopTO:    30 * time.Second,
			wantState: StateStopped,
		},
		{
			name:      "start-command failure",
			startCmd:  []string{"/bin/false"},
			startTO:   30 * time.Second,
			wantState: StateStopped,
		},
		{
			name:      "start-command exec failure",
			startCmd:  []string{"/nonexistent/script"},
			startTO:   30 * time.Second,
			wantState: StateStopped,
		},
		{
			name:      "start OK, stop-command failure",
			startCmd:  []string{"/bin/true"},
			stopCmd:   []string{"/bin/false"},
			alsoStop:  true,
			startTO:   30 * time.Second,
			stopTO:    30 * time.Second,
			wantState: StateStopped,
		},
		{
			name:      "no commands set (immediate transitions)",
			startTO:   30 * time.Second,
			stopTO:    30 * time.Second,
			alsoStop:  true,
			wantState: StateStopped,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			set, _ := newTestSet()
			svc := NewScriptedService(set, "timer-canary-"+c.name)
			if c.startCmd != nil {
				svc.SetStartCommand(c.startCmd)
			}
			if c.stopCmd != nil {
				svc.SetStopCommand(c.stopCmd)
			}
			svc.SetStartTimeout(c.startTO)
			svc.SetStopTimeout(c.stopTO)
			set.AddService(svc)

			set.StartService(svc)
			time.Sleep(300 * time.Millisecond)

			if c.alsoStop {
				set.StopService(svc)
				time.Sleep(300 * time.Millisecond)
			}

			if svc.State() != c.wantState {
				t.Errorf("state = %v, want %v", svc.State(), c.wantState)
			}
			// The load-bearing assertion: after any final transition the
			// scripted service must have released its process timer.
			set.queueMu.Lock()
			leaked := svc.processTimer != nil || svc.timerPurpose != scriptedTimerNone
			timerPurpose := svc.timerPurpose
			set.queueMu.Unlock()
			if leaked {
				t.Errorf("process timer leaked: processTimer=%v timerPurpose=%v",
					svc.processTimer != nil, timerPurpose)
			}
		})
	}
}

func TestScriptedServiceNoCommands(t *testing.T) {
	set, _ := newTestSet()

	// Scripted service with no commands = starts/stops immediately
	svc := NewScriptedService(set, "empty-svc")
	set.AddService(svc)

	set.StartService(svc)

	if svc.State() != StateStarted {
		t.Errorf("expected STARTED (no command), got %v", svc.State())
	}

	set.StopService(svc)

	if svc.State() != StateStopped {
		t.Errorf("expected STOPPED (no stop command), got %v", svc.State())
	}
}

func TestScriptedServiceWithDependency(t *testing.T) {
	set, _ := newTestSet()

	dep := NewInternalService(set, "dep-svc")
	set.AddService(dep)

	svc := NewScriptedService(set, "scripted-dep-svc")
	svc.SetStartCommand([]string{"/bin/true"})
	svc.SetStopCommand([]string{"/bin/true"})
	set.AddService(svc)

	svc.Record().AddDep(dep, DepRegular)

	set.StartService(svc)
	time.Sleep(300 * time.Millisecond)

	if dep.State() != StateStarted {
		t.Errorf("dependency should be STARTED, got %v", dep.State())
	}
	if svc.State() != StateStarted {
		t.Errorf("scripted service should be STARTED, got %v", svc.State())
	}

	set.StopService(svc)
	time.Sleep(300 * time.Millisecond)

	if svc.State() != StateStopped {
		t.Errorf("scripted service should be STOPPED, got %v", svc.State())
	}
	if dep.State() != StateStopped {
		t.Errorf("dependency should be STOPPED, got %v", dep.State())
	}
}

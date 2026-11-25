package service

import (
	"sync/atomic"
	"testing"
	"time"
)

// --- ParseSystemAction / AsShutdownType ---

func TestParseSystemAction(t *testing.T) {
	cases := []struct {
		in   string
		want SystemAction
		ok   bool
	}{
		{"", ActionNone, true},
		{"none", ActionNone, true},
		{"reboot", ActionReboot, true},
		{"poweroff", ActionPoweroff, true},
		{"halt", ActionHalt, true},
		{"exit", ActionExit, true},
		{"REBOOT", ActionNone, false}, // case sensitive
		{"bogus", ActionNone, false},
	}
	for _, c := range cases {
		got, err := ParseSystemAction(c.in)
		if (err == nil) != c.ok {
			t.Errorf("%q: ok=%v want %v err=%v", c.in, err == nil, c.ok, err)
			continue
		}
		if c.ok && got != c.want {
			t.Errorf("%q: got %v want %v", c.in, got, c.want)
		}
	}
}

func TestAsShutdownType(t *testing.T) {
	cases := []struct {
		in   SystemAction
		want ShutdownType
	}{
		{ActionNone, ShutdownNone},
		{ActionExit, ShutdownNone},
		{ActionReboot, ShutdownReboot},
		{ActionPoweroff, ShutdownPoweroff},
		{ActionHalt, ShutdownHalt},
	}
	for _, c := range cases {
		if got := c.in.AsShutdownType(); got != c.want {
			t.Errorf("%v: got %v want %v", c.in, got, c.want)
		}
	}
}

// --- chooseStoppedAction logic ---

func TestChooseStoppedActionSkipsDuringRestart(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "svc")
	set.AddService(svc)
	svc.Record().SetFailureAction(ActionReboot)
	svc.Record().SetSuccessAction(ActionPoweroff)

	if got := svc.Record().chooseStoppedAction(true); got != ActionNone {
		t.Errorf("willRestart=true should yield ActionNone, got %v", got)
	}
}

func TestChooseStoppedActionFailureOnStartFailed(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "svc")
	set.AddService(svc)
	svc.Record().SetFailureAction(ActionReboot)

	svc.Record().startFailed = true
	if got := svc.Record().chooseStoppedAction(false); got != ActionReboot {
		t.Errorf("startFailed should yield failure action, got %v", got)
	}
}

func TestChooseStoppedActionFailureOnReasonFailed(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "svc")
	set.AddService(svc)
	svc.Record().SetFailureAction(ActionReboot)

	svc.Record().stopReason = ReasonFailed
	if got := svc.Record().chooseStoppedAction(false); got != ActionReboot {
		t.Errorf("ReasonFailed should yield failure action, got %v", got)
	}
}

func TestChooseStoppedActionNoneOnOperatorStop(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "svc")
	set.AddService(svc)
	svc.Record().SetFailureAction(ActionReboot)
	svc.Record().SetSuccessAction(ActionPoweroff)

	// ReasonNormal = operator-issued stop; neither action should fire.
	svc.Record().stopReason = ReasonNormal
	if got := svc.Record().chooseStoppedAction(false); got != ActionNone {
		t.Errorf("operator stop should yield ActionNone, got %v", got)
	}
}

// --- integration: OnSystemAction callback fires through Stopped() ---

func TestOnSystemActionFiresOnFailure(t *testing.T) {
	set, _ := newTestSet()

	type call struct {
		action SystemAction
		arg    string
	}
	calls := make(chan call, 1)
	set.OnSystemAction = func(a SystemAction, arg string) {
		calls <- call{a, arg}
	}

	svc := NewInternalService(set, "svc")
	set.AddService(svc)
	svc.Record().SetFailureAction(ActionPoweroff)
	svc.Record().SetRebootArgument("kexec-config")

	rec := svc.Record()
	rec.startFailed = true
	rec.stopReason = ReasonFailed
	rec.desired.Store(StateStopped)
	rec.state.Store(StateStarting)
	rec.Stopped()
	set.ProcessQueues()

	select {
	case c := <-calls:
		if c.action != ActionPoweroff {
			t.Errorf("expected ActionPoweroff, got %v", c.action)
		}
		if c.arg != "kexec-config" {
			t.Errorf("expected reboot arg, got %q", c.arg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnSystemAction never fired")
	}
}

// Stopped() runs with queueMu held, and the shutdown the action starts
// takes queueMu to stop every service. Called directly, the callback
// deadlocked the daemon (any action; success-action=exit logged "Shutting down
// slinit (remain)..." and nothing after it, not even on SIGTERM).
func TestOnSystemActionCanStopAllServices(t *testing.T) {
	set, _ := newTestSet()
	done := make(chan struct{})
	set.OnSystemAction = func(SystemAction, string) {
		set.StopAllServices(ShutdownRemain)
		close(done)
	}

	svc := NewInternalService(set, "svc")
	set.AddService(svc)
	svc.Record().SetFailureAction(ActionExit)

	rec := svc.Record()
	rec.startFailed = true
	rec.stopReason = ReasonFailed
	rec.desired.Store(StateStopped)
	rec.state.Store(StateStarting)
	// As the child-exit path calls it.
	go func() {
		set.queueMu.Lock()
		rec.Stopped()
		set.queueMu.Unlock()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("failure-action deadlocked: StopAllServices never returned")
	}
}

func TestOnSystemActionSilentOnOperatorStop(t *testing.T) {
	set, _ := newTestSet()
	var fired atomic.Bool
	set.OnSystemAction = func(SystemAction, string) { fired.Store(true) }

	svc := NewInternalService(set, "svc")
	set.AddService(svc)
	svc.Record().SetFailureAction(ActionReboot)
	svc.Record().SetSuccessAction(ActionPoweroff)

	// Drive a normal start/stop cycle.
	set.StartService(svc)
	if svc.State() != StateStarted {
		t.Fatalf("setup: expected STARTED, got %v", svc.State())
	}
	set.StopService(svc)
	if svc.State() != StateStopped {
		t.Fatalf("setup: expected STOPPED, got %v", svc.State())
	}

	time.Sleep(100 * time.Millisecond) // the callback runs on its own goroutine
	if fired.Load() {
		t.Errorf("OnSystemAction must NOT fire on operator-issued stop")
	}
}

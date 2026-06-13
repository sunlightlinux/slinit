package service

import (
	"testing"
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

	var (
		gotAction SystemAction = ActionNone
		gotArg    string
	)
	set.OnSystemAction = func(a SystemAction, arg string) {
		gotAction = a
		gotArg = arg
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

	if gotAction != ActionPoweroff {
		t.Errorf("expected ActionPoweroff, got %v", gotAction)
	}
	if gotArg != "kexec-config" {
		t.Errorf("expected reboot arg, got %q", gotArg)
	}
}

func TestOnSystemActionSilentOnOperatorStop(t *testing.T) {
	set, _ := newTestSet()
	fired := false
	set.OnSystemAction = func(SystemAction, string) { fired = true }

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

	if fired {
		t.Errorf("OnSystemAction must NOT fire on operator-issued stop")
	}
}

package service

import (
	"testing"
	"time"
)

// `once` suppresses the restart of one run, as runit's `sv once` does. It
// used to set the restart setting itself to never, so every later run of
// the service, started the ordinary way, was not restarted either, until
// the service was reloaded.
func TestOnceAppliesToOneRunOnly(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "once-svc")
	svc.SetCommand([]string{"/bin/sleep", "0.1"})
	svc.SetAutoRestart(RestartAlways)
	svc.SetRestartDelay(50 * time.Millisecond)
	set.AddService(svc)
	t.Cleanup(func() {
		set.StopService(svc)
		waitForState(t, svc, StateStopped, 2*time.Second)
	})

	set.StartOnceService(svc)
	// The run ends after 0.1s and, being a once run, is not restarted.
	time.Sleep(500 * time.Millisecond)
	if st := svc.State(); st != StateStopped {
		t.Fatalf("once: state %v after the run ended, want STOPPED (no restart)", st)
	}

	set.StartService(svc)
	if got := waitForState(t, svc, StateStarted, 2*time.Second); got != StateStarted {
		t.Fatalf("start: state %v", got)
	}
	first := svc.PID()
	// restart = always must bring the next run back after it exits.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pid := svc.PID(); pid > 0 && pid != first {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("an ordinary start after once was not restarted (state %v): once outlived its run", svc.State())
}

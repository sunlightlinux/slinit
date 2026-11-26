package service

import (
	"testing"
	"time"
)

// The exit handlers (process main exit, bgprocess launcher exit, scripted
// start- and stop-command exit) read the service's flags and
// inittab-id/line before they take queueMu, while a reload of the same
// service rewrites them under queueMu (config.applyToService inside
// WithGraphLock). The inittab fields are strings, so the read could tear.
// A -race stress run of the daemon reported the process case; this test
// gives the detector the same interleaving for every type: short-lived
// commands exiting repeatedly while a "reload" rewrites flags and utmp
// details.
func TestChildExitReadsFlagsUnderReload(t *testing.T) {
	set, _ := newTestSet()

	proc := NewProcessService(set, "exit-reload-proc")
	proc.SetCommand([]string{"/bin/sh", "-c", "exit 0"})
	bg := NewBGProcessService(set, "exit-reload-bg")
	bg.SetCommand([]string{"/bin/sh", "-c", "exit 1"})
	bg.SetPIDFile(t.TempDir() + "/none.pid")
	scr := NewScriptedService(set, "exit-reload-scr")
	scr.SetStartCommand([]string{"/bin/true"})
	scr.SetStopCommand([]string{"/bin/true"})
	svcs := []Service{proc, bg, scr}
	for _, s := range svcs {
		set.AddService(s)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 600; i++ {
			set.WithGraphLock(func() {
				for _, s := range svcs {
					s.Record().SetFlags(ServiceFlags{})
					s.Record().SetUtmpDetails("", "")
				}
			})
			time.Sleep(time.Millisecond)
		}
	}()

	wait := func(s Service, st ServiceState) {
		deadline := time.Now().Add(2 * time.Second)
		for s.State() != st && time.Now().Before(deadline) {
			time.Sleep(2 * time.Millisecond)
		}
	}
	for i := 0; i < 15; i++ {
		set.StartService(proc)
		set.StartService(bg)
		set.StartService(scr)
		wait(proc, StateStopped)
		wait(bg, StateStopped)
		wait(scr, StateStarted)
		set.StopService(scr)
		wait(scr, StateStopped)
	}
	<-done
}

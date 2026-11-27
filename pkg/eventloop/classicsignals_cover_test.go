package eventloop

import (
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// The signal table in slinit(8) (SIGNALS): what each classic signal does
// as PID 1, in a container, and as a user instance.
func TestHandleSignalClassicTable(t *testing.T) {
	type mode struct {
		name            string
		pid1, container bool
		term, quit      service.ShutdownType
	}
	for _, m := range []mode{
		{"pid1", true, false, service.ShutdownReboot, service.ShutdownPoweroff},
		{"container", true, true, service.ShutdownHalt, service.ShutdownPoweroff},
		{"user", false, false, service.ShutdownHalt, service.ShutdownPoweroff},
	} {
		for _, tc := range []struct {
			sig  syscall.Signal
			want service.ShutdownType
		}{
			{syscall.SIGTERM, m.term},
			{syscall.SIGINT, m.term},
			{syscall.SIGQUIT, m.quit},
			{syscall.SIGUSR2, m.quit},
		} {
			logger := logging.New(logging.LevelError)
			el := New(service.NewServiceSet(logger), logger)
			el.SetPID1Mode(m.pid1)
			el.SetContainerMode(m.container)
			if !el.handleSignal(tc.sig) {
				t.Errorf("%s: %v did not start a shutdown", m.name, tc.sig)
				continue
			}
			if got := el.GetShutdownType(); got != tc.want {
				t.Errorf("%s: %v -> %v, want %v", m.name, tc.sig, got, tc.want)
			}
		}
	}
}

// SIGUSR1 re-opens the control socket and SIGHUP is only logged:
// neither starts a shutdown.
func TestHandleSignalUSR1AndHUPDoNotShutDown(t *testing.T) {
	logger := logging.New(logging.LevelError)
	el := New(service.NewServiceSet(logger), logger)
	el.SetPID1Mode(true)
	reopened := 0
	el.OnReopenSocket = func() { reopened++ }

	for _, sig := range []syscall.Signal{syscall.SIGUSR1, syscall.SIGHUP} {
		if el.handleSignal(sig) {
			t.Errorf("%v started a shutdown", sig)
		}
	}
	if reopened != 1 {
		t.Errorf("OnReopenSocket called %d times, want 1 (SIGUSR1 only)", reopened)
	}
	if got := el.GetShutdownType(); got != service.ShutdownNone {
		t.Errorf("shutdown type = %v after SIGUSR1/SIGHUP, want none", got)
	}
}

// A non-PID-1 instance with nothing active does not exit on its own:
// only PID 1 treats an empty set as a collapse, and a container calls it
// a finished workload.
func TestCheckInactiveByMode(t *testing.T) {
	logger := logging.New(logging.LevelError)

	user := New(service.NewServiceSet(logger), logger)
	if user.checkInactive() {
		t.Error("user instance exited with an empty service set and no shutdown")
	}

	ctr := New(service.NewServiceSet(logger), logger)
	ctr.SetPID1Mode(true)
	ctr.SetContainerMode(true)
	if !ctr.checkInactive() {
		t.Error("container PID 1 kept running with nothing left to run")
	}

	sd := New(service.NewServiceSet(logger), logger)
	called := false
	sd.OnAllStopped = func() { called = true }
	sd.InitiateShutdown(service.ShutdownHalt)
	if !sd.checkInactive() || !called {
		t.Errorf("after a shutdown with nothing active: exit=%v OnAllStopped=%v, want both", sd.checkInactive(), called)
	}
}

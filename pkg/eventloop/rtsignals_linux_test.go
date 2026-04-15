//go:build linux

package eventloop

import (
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestRTShutdownTypeMapping(t *testing.T) {
	cases := []struct {
		sig     syscall.Signal
		want    service.ShutdownType
		wantSig string
	}{
		{sigHalt, service.ShutdownHalt, "SIGRTMIN+3"},
		{sigPoweroff, service.ShutdownPoweroff, "SIGRTMIN+4"},
		{sigReboot, service.ShutdownReboot, "SIGRTMIN+5"},
		{sigKexec, service.ShutdownKexec, "SIGRTMIN+6"},
	}
	for _, tc := range cases {
		got, name, ok := rtShutdownType(tc.sig)
		if !ok {
			t.Errorf("%s not recognised", tc.wantSig)
			continue
		}
		if got != tc.want {
			t.Errorf("rtShutdownType(%d) = %v, want %v", tc.sig, got, tc.want)
		}
		if name != tc.wantSig {
			t.Errorf("rtShutdownType(%d) name = %q, want %q", tc.sig, name, tc.wantSig)
		}
	}
}

func TestRTShutdownTypeIgnoresOtherSignals(t *testing.T) {
	ignored := []syscall.Signal{
		syscall.SIGTERM,
		syscall.SIGINT,
		syscall.SIGHUP,
		syscall.SIGUSR1,
		syscall.SIGUSR2,
		syscall.Signal(sigRTMin), // +0: not an action we handle
		syscall.Signal(sigRTMin + 1),
		syscall.Signal(sigRTMin + 2),
		syscall.Signal(sigRTMin + 7),
	}
	for _, s := range ignored {
		if _, _, ok := rtShutdownType(s); ok {
			t.Errorf("signal %d should not be recognised as an RT shutdown signal", s)
		}
	}
}

func TestExtraShutdownSignalsContainsAllFour(t *testing.T) {
	got := extraShutdownSignals()
	want := []syscall.Signal{sigHalt, sigPoweroff, sigReboot, sigKexec}
	if len(got) != len(want) {
		t.Fatalf("extraShutdownSignals len = %d, want %d", len(got), len(want))
	}
	seen := make(map[syscall.Signal]bool, len(got))
	for _, s := range got {
		seen[s] = true
	}
	for _, w := range want {
		if !seen[w] {
			t.Errorf("extraShutdownSignals missing %v", w)
		}
	}
}

func TestHandleSignal_RTPoweroffInitiatesShutdown(t *testing.T) {
	logger := logging.New(logging.LevelDebug)
	set := service.NewServiceSet(logger)
	el := New(set, logger)

	if !el.handleSignal(sigPoweroff) {
		t.Fatal("handleSignal(SIGRTMIN+4) should return true")
	}
	if got := el.GetShutdownType(); got != service.ShutdownPoweroff {
		t.Errorf("shutdown type = %v, want ShutdownPoweroff", got)
	}
}

func TestHandleSignal_RTRebootInitiatesShutdown(t *testing.T) {
	logger := logging.New(logging.LevelDebug)
	set := service.NewServiceSet(logger)
	el := New(set, logger)

	if !el.handleSignal(sigReboot) {
		t.Fatal("handleSignal(SIGRTMIN+5) should return true")
	}
	if got := el.GetShutdownType(); got != service.ShutdownReboot {
		t.Errorf("shutdown type = %v, want ShutdownReboot", got)
	}
}

func TestHandleSignal_RTKexec(t *testing.T) {
	logger := logging.New(logging.LevelDebug)
	set := service.NewServiceSet(logger)
	el := New(set, logger)

	if !el.handleSignal(sigKexec) {
		t.Fatal("handleSignal(SIGRTMIN+6) should return true")
	}
	if got := el.GetShutdownType(); got != service.ShutdownKexec {
		t.Errorf("shutdown type = %v, want ShutdownKexec", got)
	}
}

func TestHandleSignal_RTDuringShutdownEscalates(t *testing.T) {
	logger := logging.New(logging.LevelDebug)
	set := service.NewServiceSet(logger)
	el := New(set, logger)

	// First RT signal: starts shutdown.
	if !el.handleSignal(sigPoweroff) {
		t.Fatal("first RT signal should initiate shutdown")
	}
	if got := el.shutdownSignals.Load(); got != 1 {
		t.Fatalf("after first signal, shutdownSignals = %d, want 1", got)
	}

	// Second RT signal: escalates (counter → 2).
	if !el.handleSignal(sigPoweroff) {
		t.Fatal("second RT signal should escalate")
	}
	if got := el.shutdownSignals.Load(); got != 2 {
		t.Errorf("after escalation, shutdownSignals = %d, want 2", got)
	}
}

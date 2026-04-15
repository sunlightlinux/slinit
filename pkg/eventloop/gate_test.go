package eventloop

import (
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestSignalShutdownGate_DeniesSIGTERM(t *testing.T) {
	logger := logging.New(logging.LevelDebug)
	set := service.NewServiceSet(logger)
	el := New(set, logger)
	el.SetPID1Mode(true)

	var gateCalls []string
	el.SignalShutdownGate = func(reason string) bool {
		gateCalls = append(gateCalls, reason)
		return false
	}

	if el.handleSignal(syscall.SIGTERM) {
		t.Error("handleSignal should return false when gate denies")
	}
	if el.shutdownInitiated {
		t.Error("shutdown should not start when gate denies")
	}
	if len(gateCalls) != 1 || gateCalls[0] != "SIGTERM" {
		t.Errorf("gate calls = %v, want [SIGTERM]", gateCalls)
	}
}

func TestSignalShutdownGate_AllowsWhenTrue(t *testing.T) {
	logger := logging.New(logging.LevelDebug)
	set := service.NewServiceSet(logger)
	el := New(set, logger)
	el.SetPID1Mode(true)

	el.SignalShutdownGate = func(reason string) bool { return true }

	if !el.handleSignal(syscall.SIGTERM) {
		t.Error("handleSignal should return true when gate allows")
	}
	if !el.shutdownInitiated {
		t.Error("shutdown should have started")
	}
}

func TestSignalShutdownGate_NilGateAlwaysAllows(t *testing.T) {
	logger := logging.New(logging.LevelDebug)
	set := service.NewServiceSet(logger)
	el := New(set, logger)
	el.SetPID1Mode(true)
	el.SignalShutdownGate = nil

	if !el.handleSignal(syscall.SIGINT) {
		t.Error("nil gate should default-allow")
	}
}

func TestSignalShutdownGate_DoesNotInterceptEscalation(t *testing.T) {
	logger := logging.New(logging.LevelDebug)
	set := service.NewServiceSet(logger)
	el := New(set, logger)
	el.SetPID1Mode(true)

	// Deny-always gate.
	el.SignalShutdownGate = func(reason string) bool { return false }

	// Pretend a shutdown is already in progress; a second signal must
	// still escalate (gate only protects the *initial* trigger, not
	// repeated escalations from an operator holding Ctrl+Alt+Del down).
	el.shutdownInitiated = true
	el.shutdownSignals.Store(1)

	if !el.handleSignal(syscall.SIGTERM) {
		t.Error("escalation should bypass the gate")
	}
	if got := el.shutdownSignals.Load(); got != 2 {
		t.Errorf("shutdownSignals = %d, want 2", got)
	}
}

func TestSignalShutdownGate_DeniesRTPoweroff(t *testing.T) {
	logger := logging.New(logging.LevelDebug)
	set := service.NewServiceSet(logger)
	el := New(set, logger)
	el.SetPID1Mode(true)

	var gateCalls []string
	el.SignalShutdownGate = func(reason string) bool {
		gateCalls = append(gateCalls, reason)
		return false
	}

	if el.handleSignal(sigPoweroff) {
		t.Error("RT signal should be denied by gate")
	}
	if el.shutdownInitiated {
		t.Error("shutdown should not start when gate denies RT signal")
	}
	if len(gateCalls) != 1 || gateCalls[0] != "SIGRTMIN+4" {
		t.Errorf("gate calls = %v, want [SIGRTMIN+4]", gateCalls)
	}
}

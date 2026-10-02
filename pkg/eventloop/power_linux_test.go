//go:build linux

package eventloop

import (
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// SIGPWR has to be claimed or it is never seen. Unlike the shutdown set
// an unclaimed SIGPWR does not kill PID 1 — it is _SigNotify without
// _SigKill in the Go runtime's table, so it is simply discarded, which
// is why this was a missing feature rather than a way to lose init.
func TestClaimedSignalSetIncludesSIGPWR(t *testing.T) {
	var seen bool
	for _, s := range claimedSignalSet() {
		if s == syscall.SIGPWR {
			seen = true
		}
	}
	if !seen {
		t.Error("claimedSignalSet is missing SIGPWR — a UPS daemon's signal would be discarded")
	}
	// And it must not have leaked into the shutdown set, which the
	// dispatch treats as "bring the system down".
	for _, s := range shutdownSignalSet() {
		if s == syscall.SIGPWR {
			t.Error("SIGPWR is in shutdownSignalSet — a power event is not a shutdown request")
		}
	}
}

// The policy decision, pinned: a power event runs the operator's hook and
// nothing else. Not even a low battery shuts the machine down on slinit's
// own initiative — that belongs to the hook, which can call
// `slinitctl poweroff` in one line.
func TestSIGPWRDoesNotShutDown(t *testing.T) {
	logger := logging.New(logging.LevelDebug)
	set := service.NewServiceSet(logger)
	el := New(set, logger)
	el.SetPID1Mode(true)

	// No status file and no hook: HandlePowerSignal reports "failing"
	// and finds nothing to run, which is the worst case for this
	// assertion — the state most likely to tempt an automatic poweroff.
	if el.handleSignal(syscall.SIGPWR) {
		t.Error("handleSignal(SIGPWR) returned true — it must not request a shutdown")
	}
	if el.shutdownInitiated {
		t.Fatal("SIGPWR initiated a shutdown; the hook decides, not slinit")
	}
}

// While a shutdown is already running, SIGPWR must not consume the status
// file: the hook may still be mid-flight and there is nothing left to
// decide anyway.
func TestSIGPWRIgnoredWhileShuttingDown(t *testing.T) {
	logger := logging.New(logging.LevelDebug)
	set := service.NewServiceSet(logger)
	el := New(set, logger)
	el.SetPID1Mode(true)
	el.shutdownInitiated = true

	if el.handleSignal(syscall.SIGPWR) {
		t.Error("handleSignal(SIGPWR) returned true during shutdown")
	}
}

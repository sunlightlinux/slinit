//go:build linux

package eventloop

import (
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
	"github.com/sunlightlinux/slinit/pkg/shutdown"
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
//
// The shutdown state is set the way initiateShutdown sets it:
// isShuttingDown reads shutdownSignals, not shutdownInitiated. The hook
// is a stand-in so a failure cannot consume a real /run/powerstatus.
func TestSIGPWRIgnoredWhileShuttingDown(t *testing.T) {
	logger := logging.New(logging.LevelDebug)
	var buf strings.Builder
	logger.SetOutput(&buf)
	set := service.NewServiceSet(logger)
	el := New(set, logger)
	el.SetPID1Mode(true)
	var runs atomic.Int32
	el.powerHook = func(*logging.Logger) shutdown.PowerState {
		runs.Add(1)
		return shutdown.PowerFailing
	}
	el.shutdownInitiated = true
	el.shutdownSignals.Store(1)

	if el.handleSignal(syscall.SIGPWR) {
		t.Error("handleSignal(SIGPWR) returned true during shutdown")
	}
	time.Sleep(50 * time.Millisecond)
	if n := runs.Load(); n != 0 {
		t.Errorf("power hook ran %d times during shutdown, want 0", n)
	}
	if !strings.Contains(buf.String(), "Received SIGPWR while shutting down, ignoring") {
		t.Errorf("SIGPWR was not ignored at the signal; log:\n%s", buf.String())
	}
}

// The power hook is the operator's program and may take as long as it
// likes. Run on the event loop goroutine it stalled orphan reaping,
// Ctrl+Alt+Del and SIGTERM, and even the `slinitctl poweroff` the hook
// itself sends. It runs on its own goroutine now, one run at a time, and
// SIGPWRs that arrive meanwhile collapse into one more run.
func TestSIGPWRHookDoesNotBlockTheLoop(t *testing.T) {
	release := make(chan struct{})
	var running, runs, overlap atomic.Int32
	logger := logging.New(logging.LevelError)
	el := New(service.NewServiceSet(logger), logger)
	el.SetPID1Mode(true)
	el.powerHook = func(*logging.Logger) shutdown.PowerState {
		if running.Add(1) > 1 {
			overlap.Store(1)
		}
		runs.Add(1)
		<-release
		running.Add(-1)
		return shutdown.PowerFailing
	}

	returned := make(chan struct{})
	go func() {
		el.handleSignal(syscall.SIGPWR)
		for running.Load() == 0 {
			time.Sleep(time.Millisecond)
		}
		// Two more while the first run is still going.
		el.handleSignal(syscall.SIGPWR)
		el.handleSignal(syscall.SIGPWR)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("handleSignal(SIGPWR) waited for the power hook")
	}

	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for runs.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if n := runs.Load(); n != 2 {
		t.Errorf("hook ran %d times, want 2: the one running and one for the signals that came meanwhile", n)
	}
	if overlap.Load() != 0 {
		t.Error("two power hooks ran at once")
	}
}

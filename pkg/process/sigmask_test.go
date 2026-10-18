package process

import (
	"os"
	"syscall"
	"testing"
)

// Real /proc/<pid>/status text, tabs and all. The separator is a TAB,
// not a space, and a check that trimmed a fixed "SigCgt: " prefix would
// match nothing while looking entirely correct — so the fixture keeps
// the tabs rather than tidying them.
const procStatus = `Name:	sleep
State:	S (sleeping)
Tgid:	4242
Pid:	4242
PPid:	1
SigQ:	0/63692
SigPnd:	0000000000000000
ShdPnd:	0000000000000000
SigBlk:	0000000000000000
SigIgn:	0000000000001000
SigCgt:	0000000180000001
CapInh:	0000000000000000
`

func TestReloadSignalHandledReadsSigCgt(t *testing.T) {
	// SigCgt 0x...0001 has bit 0 set, which is signal 1: SIGHUP.
	disp, err := reloadSignalDispositionIn(procStatus, syscall.SIGHUP)
	if err != nil {
		t.Fatalf("SIGHUP: %v", err)
	}
	if disp != SignalHandled {
		t.Errorf("SIGHUP is in SigCgt (bit 0) but got disposition %v", disp)
	}
}

func TestReloadSignalHandledReportsAnUncaughtSignal(t *testing.T) {
	// SIGUSR1 is signal 10, bit 9 — absent from both masks above.
	disp, err := reloadSignalDispositionIn(procStatus, syscall.SIGUSR1)
	if err != nil {
		t.Fatalf("SIGUSR1: %v", err)
	}
	if disp != SignalDefault {
		t.Errorf("SIGUSR1 is in no mask, want SignalDefault, got %v", disp)
	}
}

// Blocked counts as handled: the signal will not run its default
// action, so a reload will not kill the service. It will not reload it
// either, but that is the service's business, not a fault slinit should
// announce.
func TestBlockedCountsAsHandled(t *testing.T) {
	blocked := "SigBlk:\t0000000000000200\nSigCgt:\t0000000000000000\n"
	disp, err := reloadSignalDispositionIn(blocked, syscall.SIGUSR1)
	if err != nil {
		t.Fatalf("SIGUSR1: %v", err)
	}
	if disp != SignalHandled {
		t.Errorf("SIGUSR1 is bit 9 of SigBlk (0x200), want SignalHandled, got %v", disp)
	}
}

// "Could not tell" must be an error rather than a confident false, or
// the caller would warn about every process whose status it failed to
// read.
func TestUnreadableStatusIsAnErrorNotAFalse(t *testing.T) {
	if _, err := reloadSignalDispositionIn("Name:\tsleep\n", syscall.SIGHUP); err == nil {
		t.Error("status with no SigCgt/SigBlk line returned no error")
	}
	if _, err := reloadSignalDispositionIn("SigCgt:\tnothex\n", syscall.SIGHUP); err == nil {
		t.Error("unparseable mask returned no error")
	}
}

// Signal 64 is the last bit of the mask; 65 cannot be represented, and
// shifting by 64 in Go would wrap rather than overflow to zero.
func TestSignalRangeIsBounded(t *testing.T) {
	if _, err := reloadSignalDispositionIn(procStatus, syscall.Signal(65)); err == nil {
		t.Error("signal 65 was accepted for a 64-bit mask")
	}
	if _, err := reloadSignalDispositionIn("SigCgt:\t8000000000000000\n", syscall.Signal(64)); err != nil {
		t.Errorf("signal 64 should be representable: %v", err)
	}
}

// And the real thing, against this test process: Go's runtime catches
// SIGCHLD, so a live read must agree with the parser.
func TestReloadSignalHandledAgainstThisProcess(t *testing.T) {
	disp, err := ReloadSignalDisposition(os.Getpid(), syscall.SIGCHLD)
	if err != nil {
		t.Fatalf("reading our own status: %v", err)
	}
	if disp != SignalHandled {
		t.Errorf("the Go runtime catches SIGCHLD, want SignalHandled, got %v", disp)
	}
}

// The case that made the disposition an enum instead of a bool: an
// ignored signal lives in SigIgn, so reading only SigCgt and SigBlk
// concludes "default action" and the warning then claims a reload would
// terminate a service it would merely fail to reload.
func TestIgnoredIsNotMistakenForDefault(t *testing.T) {
	ignoring := "SigBlk:\t0000000000000000\nSigIgn:\t0000000000000001\nSigCgt:\t0000000000000000\n"
	disp, err := reloadSignalDispositionIn(ignoring, syscall.SIGHUP)
	if err != nil {
		t.Fatalf("SIGHUP: %v", err)
	}
	if disp != SignalIgnored {
		t.Errorf("SIGHUP is bit 0 of SigIgn, want SignalIgnored, got %v", disp)
	}
}

// Caught outranks ignored, and the masks are not ordered in the file,
// so finding SigIgn first must not end the search.
func TestCaughtOutranksIgnoredRegardlessOfOrder(t *testing.T) {
	both := "SigIgn:\t0000000000000001\nSigCgt:\t0000000000000001\n"
	disp, err := reloadSignalDispositionIn(both, syscall.SIGHUP)
	if err != nil {
		t.Fatalf("SIGHUP: %v", err)
	}
	if disp != SignalHandled {
		t.Errorf("SIGHUP is caught as well as ignored, want SignalHandled, got %v", disp)
	}
}

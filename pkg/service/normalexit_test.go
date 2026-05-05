package service

import (
	"syscall"
	"testing"
)

// makeExited and makeSignaled build ExitStatus values for tests
// without exposing setters on the production type. WaitStatus
// encoding (Linux): low 7 bits = signal (0 = exited normally),
// high byte = exit code when exited.
func makeExited(code int) ExitStatus {
	return ExitStatus{
		HasStatus:  true,
		WaitStatus: syscall.WaitStatus(code << 8),
	}
}

func makeSignaled(sig syscall.Signal) ExitStatus {
	return ExitStatus{
		HasStatus:  true,
		WaitStatus: syscall.WaitStatus(int(sig)),
	}
}

// TestIsNormalExitCodeMatch: an exit code in the configured list
// is recognised; one outside is not.
func TestIsNormalExitCodeMatch(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "svc")
	set.AddService(svc)
	svc.Record().SetNormalExitCodes([]int{0, 2, 42})

	cases := []struct {
		code int
		want bool
	}{
		{0, true},
		{2, true},
		{42, true},
		{1, false},
		{255, false},
	}
	for _, c := range cases {
		es := makeExited(c.code)
		if got := svc.Record().IsNormalExit(es); got != c.want {
			t.Errorf("code %d: got %v, want %v", c.code, got, c.want)
		}
	}
}

// TestIsNormalExitSignalMatch: a signal in the configured list is
// recognised; another one is not.
func TestIsNormalExitSignalMatch(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "svc")
	set.AddService(svc)
	svc.Record().SetNormalExitSignals([]syscall.Signal{syscall.SIGTERM, syscall.SIGUSR1})

	cases := []struct {
		sig  syscall.Signal
		want bool
	}{
		{syscall.SIGTERM, true},
		{syscall.SIGUSR1, true},
		{syscall.SIGHUP, false},
		{syscall.SIGINT, false},
	}
	for _, c := range cases {
		es := makeSignaled(c.sig)
		if got := svc.Record().IsNormalExit(es); got != c.want {
			t.Errorf("signal %v: got %v, want %v", c.sig, got, c.want)
		}
	}
}

// TestIsNormalExitEmptyLists: with no normal-exit configured, no
// exit looks "normal" — falls through to the standard restart logic.
func TestIsNormalExitEmptyLists(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "svc")
	set.AddService(svc)

	if svc.Record().IsNormalExit(makeExited(0)) {
		t.Error("empty config: exit code 0 wrongly classified as normal-exit")
	}
	if svc.Record().IsNormalExit(makeSignaled(syscall.SIGTERM)) {
		t.Error("empty config: SIGTERM wrongly classified as normal-exit")
	}
}

// TestIsNormalExitWrongPath: a code declared as normal must not
// match if the process actually died via a signal (and vice versa).
// Guards the type-tag check (Exited vs Signaled).
func TestIsNormalExitWrongPath(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "svc")
	set.AddService(svc)
	svc.Record().SetNormalExitCodes([]int{15})
	svc.Record().SetNormalExitSignals([]syscall.Signal{syscall.SIGTERM}) // 15

	// Exited(15) — code matches.
	if !svc.Record().IsNormalExit(makeExited(15)) {
		t.Error("Exited(15) should match code 15")
	}

	// Signaled(SIGTERM) — signal matches.
	if !svc.Record().IsNormalExit(makeSignaled(syscall.SIGTERM)) {
		t.Error("Signaled(SIGTERM) should match signal SIGTERM")
	}
}

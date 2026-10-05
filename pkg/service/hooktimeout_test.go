package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Every synchronous hook was bounded by a hardcoded five seconds with
// no way to change it, so a pre-start-command that legitimately waits
// on something — a mount, a socket — was killed and, because a
// non-zero exit fails the start, took the service with it.
func TestHookTimeoutAllowsASlowPreStartCommand(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "slowhook")
	svc.SetPreStartCommand([]string{"/bin/sleep", "7"})
	svc.SetCommand([]string{"/bin/sh", "-c", "while :; do sleep 60; done"})
	svc.SetHookTimeout(20 * time.Second)
	set.AddService(svc)

	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 25*time.Second); got != StateStarted {
		t.Fatalf("service state = %v, want STARTED — a 7s pre-start-command "+
			"under a 20s hook-timeout must be allowed to finish", got)
	}

	set.StopService(svc)
	time.Sleep(500 * time.Millisecond)
}

// The default is unchanged, so a hook that overruns it still fails the
// start rather than hanging.
func TestDefaultHookTimeoutStillBoundsThePreStartCommand(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "overrun")
	svc.SetPreStartCommand([]string{"/bin/sleep", "30"})
	svc.SetCommand([]string{"/bin/true"})
	set.AddService(svc)

	start := time.Now()
	set.StartService(svc)
	if got := waitState(t, svc, StateStopped, 20*time.Second); got != StateStopped {
		t.Fatalf("service state = %v, want STOPPED", got)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("took %v — the default bound is not being applied", elapsed)
	}
}

// The kill used to be reported as "signal: killed", which names the
// mechanism and hides the cause. annotateHookTimeout is tested directly
// because the test logger records format strings, not formatted
// messages — asserting against it would be asserting against nothing.
func TestHookTimeoutIsNamedInTheError(t *testing.T) {
	// Deadline exceeded: the annotation must name the timeout and keep
	// the original error.
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()

	inner := errors.New("signal: killed")
	got := annotateHookTimeout(inner, ctx, 5*time.Second)
	if got == nil {
		t.Fatal("annotateHookTimeout dropped the error")
	}
	msg := got.Error()
	for _, want := range []string{"timed out", "hook-timeout", "5s", "signal: killed"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not contain %q", msg, want)
		}
	}
	if !errors.Is(got, inner) {
		t.Error("the original error is no longer unwrappable")
	}

	// A hook that merely failed must not be relabelled as a timeout.
	live, cancel2 := context.WithTimeout(context.Background(), time.Minute)
	defer cancel2()
	plain := annotateHookTimeout(inner, live, 5*time.Second)
	if plain.Error() != inner.Error() {
		t.Errorf("a non-timeout failure was rewritten to %q", plain.Error())
	}

	// Success stays success.
	if annotateHookTimeout(nil, ctx, time.Second) != nil {
		t.Error("a successful hook was turned into an error")
	}
}

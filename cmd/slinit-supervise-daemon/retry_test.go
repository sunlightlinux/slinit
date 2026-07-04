package main

import (
	"syscall"
	"testing"
	"time"
)

func TestParseRetryInteger(t *testing.T) {
	steps, err := ParseRetry("5", syscall.SIGTERM)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 {
		t.Fatalf("len=%d", len(steps))
	}
	if steps[0].Signal != syscall.SIGTERM || steps[0].Timeout != 5*time.Second {
		t.Errorf("step 0: %+v", steps[0])
	}
	if steps[1].Signal != syscall.SIGKILL || steps[1].Timeout != 5*time.Second {
		t.Errorf("step 1: %+v", steps[1])
	}
}

func TestParseRetrySlashSpec(t *testing.T) {
	steps, err := ParseRetry("TERM/30/KILL/5", syscall.SIGTERM)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 {
		t.Fatalf("len=%d", len(steps))
	}
	if steps[0].Signal != syscall.SIGTERM || steps[0].Timeout != 30*time.Second {
		t.Errorf("step 0: %+v", steps[0])
	}
	if steps[1].Signal != syscall.SIGKILL || steps[1].Timeout != 5*time.Second {
		t.Errorf("step 1: %+v", steps[1])
	}
}

func TestParseSignalForms(t *testing.T) {
	cases := map[string]syscall.Signal{
		"TERM":    syscall.SIGTERM,
		"SIGTERM": syscall.SIGTERM,
		"15":      syscall.SIGTERM,
		"HUP":     syscall.SIGHUP,
		"USR1":    syscall.SIGUSR1,
	}
	for in, want := range cases {
		got, err := ParseSignal(in)
		if err != nil || got != want {
			t.Errorf("ParseSignal(%q) = (%v, %v), want %v", in, got, err, want)
		}
	}
	if _, err := ParseSignal("BOGUS"); err == nil {
		t.Errorf("expected error for BOGUS")
	}
}

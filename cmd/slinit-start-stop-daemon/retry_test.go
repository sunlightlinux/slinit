package main

import (
	"syscall"
	"testing"
	"time"
)

func TestParseSignal(t *testing.T) {
	cases := []struct {
		in   string
		want syscall.Signal
		ok   bool
	}{
		{"TERM", syscall.SIGTERM, true},
		{"SIGTERM", syscall.SIGTERM, true},
		{"sigterm", syscall.SIGTERM, true},
		{"HUP", syscall.SIGHUP, true},
		{"9", syscall.SIGKILL, true},
		{"15", syscall.SIGTERM, true},
		{"", 0, false},
		{"NOPE", 0, false},
		{"0", 0, false},
		{"99", 0, false},
	}
	for _, tc := range cases {
		got, err := ParseSignal(tc.in)
		if (err == nil) != tc.ok {
			t.Errorf("ParseSignal(%q) err=%v, want ok=%v", tc.in, err, tc.ok)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("ParseSignal(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseRetryInteger(t *testing.T) {
	steps, err := ParseRetry("5", syscall.SIGTERM)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(steps))
	}
	if steps[0].Signal != syscall.SIGTERM || steps[0].Timeout != 5*time.Second {
		t.Errorf("step 0 = %v", steps[0])
	}
	if steps[1].Signal != syscall.SIGKILL || steps[1].Timeout != 5*time.Second {
		t.Errorf("step 1 = %v", steps[1])
	}
}

func TestParseRetrySchedule(t *testing.T) {
	steps, err := ParseRetry("TERM/30/KILL/5", syscall.SIGTERM)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(steps))
	}
	if steps[0].Signal != syscall.SIGTERM || steps[0].Timeout != 30*time.Second {
		t.Errorf("step 0 = %v", steps[0])
	}
	if steps[1].Signal != syscall.SIGKILL || steps[1].Timeout != 5*time.Second {
		t.Errorf("step 1 = %v", steps[1])
	}
}

func TestParseRetryTrailingSignal(t *testing.T) {
	// Trailing signal without timeout → "forever" (Timeout==0).
	steps, err := ParseRetry("HUP/2/KILL", syscall.SIGTERM)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(steps))
	}
	if steps[1].Signal != syscall.SIGKILL || steps[1].Timeout != 0 {
		t.Errorf("step 1 = %v (want forever KILL)", steps[1])
	}
}

func TestParseRetryErrors(t *testing.T) {
	bad := []string{
		"",
		"5/TERM",     // timeout without preceding signal
		"TERM//KILL", // empty token
		"BOGUS/30",   // unknown signal
		"TERM/-5",    // negative timeout
	}
	for _, s := range bad {
		if _, err := ParseRetry(s, syscall.SIGTERM); err == nil {
			t.Errorf("ParseRetry(%q) should have failed", s)
		}
	}
}

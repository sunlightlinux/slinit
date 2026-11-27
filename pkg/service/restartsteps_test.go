package service

import (
	"testing"
	"time"
)

// The curve of systemd's service_restart_usec_nth, in its test's terms.
func TestExpBackoffDelay(t *testing.T) {
	s := time.Second
	for _, tc := range []struct {
		base, max time.Duration
		steps, n  int
		want      time.Duration
	}{
		{s, time.Minute, 5, 1, s},
		{s, time.Minute, 5, 2, 2267933155 * time.Nanosecond},
		{s, time.Minute, 5, 5, 26455806187 * time.Nanosecond},
		{s, time.Minute, 5, 6, time.Minute},
		{s, time.Minute, 5, 100, time.Minute},
		{100 * time.Millisecond, 10 * s, 2, 2, s}, // 0.1 * 100^(1/2)
		{0, time.Minute, 5, 3, 0},                 // no base to grow from
		{2 * s, s, 5, 3, 2 * s},                   // max not above base
		{s, time.Minute, 0, 3, s},                 // off
	} {
		got := expBackoffDelay(tc.base, tc.max, tc.steps, tc.n)
		if d := got - tc.want; d < -time.Microsecond || d > time.Microsecond {
			t.Errorf("expBackoffDelay(%v, %v, %d, %d) = %v, want %v", tc.base, tc.max, tc.steps, tc.n, got, tc.want)
		}
	}
}

// nextRestartDelay walks the curve, one step per restart, and starts
// over when a stable period resets the backoff.
func TestRestartStepsWalk(t *testing.T) {
	set, _ := newTestSet()
	s := NewProcessService(set, "p")
	s.SetRestartDelay(time.Second)
	s.SetRestartBackoff(0, 8*time.Second)
	s.SetRestartSteps(3)
	var got []time.Duration
	for i := 0; i < 5; i++ {
		got = append(got, s.nextRestartDelay().Round(time.Millisecond))
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delays %v, want %v", got, want)
		}
	}
	s.backoffRestarts = 0 // what a stable period does (checkRestart)
	if d := s.nextRestartDelay(); d != time.Second {
		t.Errorf("after the reset: %v, want 1s", d)
	}
}

package main

import (
	"testing"
	"time"
)

func TestRespawnLimiterUnlimited(t *testing.T) {
	// max=0 → always allow.
	r := newRespawnLimiter(0, time.Second)
	now := time.Now()
	for i := 0; i < 100; i++ {
		if !r.allowRespawn(now.Add(time.Duration(i) * time.Millisecond)) {
			t.Fatalf("unlimited rejected at i=%d", i)
		}
	}
}

func TestRespawnLimiterWithinBudget(t *testing.T) {
	r := newRespawnLimiter(3, 100*time.Millisecond)
	base := time.Now()
	for i := 1; i <= 3; i++ {
		if !r.allowRespawn(base.Add(time.Duration(i) * time.Millisecond)) {
			t.Errorf("crash %d/3 rejected", i)
		}
	}
	// 4th crash within the window: rejected.
	if r.allowRespawn(base.Add(5 * time.Millisecond)) {
		t.Errorf("4th crash within window should be rejected")
	}
}

func TestRespawnLimiterWindowRolls(t *testing.T) {
	// max=2 within a 10ms window; spread crashes 20ms apart so each
	// falls outside the previous one's window.
	r := newRespawnLimiter(2, 10*time.Millisecond)
	base := time.Now()
	if !r.allowRespawn(base) {
		t.Errorf("crash 1 rejected")
	}
	if !r.allowRespawn(base.Add(50 * time.Millisecond)) {
		t.Errorf("crash 2 far past window rejected")
	}
	if !r.allowRespawn(base.Add(100 * time.Millisecond)) {
		t.Errorf("crash 3 with only 1 in window rejected")
	}
}

func TestBackoffDelay(t *testing.T) {
	opts := Options{
		RespawnDelay:     100 * time.Millisecond,
		RespawnDelayStep: 200 * time.Millisecond,
		RespawnDelayCap:  600 * time.Millisecond,
	}
	// respawn=1 → 100 + 200 = 300ms
	if got, want := backoffDelay(opts, 1), 300*time.Millisecond; got != want {
		t.Errorf("respawn=1: %v, want %v", got, want)
	}
	// respawn=2 → 100 + 400 = 500ms
	if got, want := backoffDelay(opts, 2), 500*time.Millisecond; got != want {
		t.Errorf("respawn=2: %v, want %v", got, want)
	}
	// respawn=5 → 100 + 1000 = capped at 600ms
	if got, want := backoffDelay(opts, 5), 600*time.Millisecond; got != want {
		t.Errorf("respawn=5 (capped): %v, want %v", got, want)
	}
}

func TestBackoffZeroStepReturnsBase(t *testing.T) {
	opts := Options{
		RespawnDelay:     250 * time.Millisecond,
		RespawnDelayStep: 0,
		RespawnDelayCap:  time.Second,
	}
	if got := backoffDelay(opts, 10); got != 250*time.Millisecond {
		t.Errorf("stepless: got %v, want 250ms", got)
	}
}

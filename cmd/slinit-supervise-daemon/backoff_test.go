package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The stepped respawn delay grew with every restart for the life of
// the supervisor, so a daemon that stayed up longer than the respawn
// period between crashes still waited longer each time, up to the cap.
// Each run here outlives the period, so every restart should take the
// first step only.
func TestBackoffResetsAfterStablePeriod(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	opts := Options{
		Service:          "test",
		PidFile:          filepath.Join(dir, "svc.pid"),
		Exec:             sh,
		Args:             []string{"-c", `echo x >> "$0"; sleep 0.6`, runs},
		Retry:            "TERM/2/KILL/2",
		RespawnPeriod:    400 * time.Millisecond,
		RespawnDelayStep: 300 * time.Millisecond,
		RespawnDelayCap:  10 * time.Second,
	}

	done := make(chan int, 1)
	go func() { done <- runSupervisor(opts) }()
	t.Cleanup(func() {
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	})

	// Time each start as the run file grows.
	var starts []time.Time
	deadline := time.Now().Add(10 * time.Second)
	for len(starts) < 5 && time.Now().Before(deadline) {
		b, _ := os.ReadFile(runs)
		if n := bytes.Count(b, []byte("\n")); n > len(starts) {
			starts = append(starts, time.Now())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(starts) < 5 {
		t.Fatalf("only %d starts in 10s", len(starts))
	}
	first := starts[1].Sub(starts[0])
	last := starts[4].Sub(starts[3])
	// Without the reset the 4th gap is three steps (900ms) longer.
	if last-first > 250*time.Millisecond {
		t.Errorf("restart gap grew from %s to %s although each run outlived the period", first, last)
	}
}

func TestNextRespawn(t *testing.T) {
	if got := nextRespawn(7, 2*time.Second, time.Second); got != 1 {
		t.Errorf("after a stable run: %d, want 1", got)
	}
	if got := nextRespawn(7, 100*time.Millisecond, time.Second); got != 8 {
		t.Errorf("after a quick crash: %d, want 8", got)
	}
	if got := nextRespawn(3, 13*time.Second, 0); got != 1 {
		t.Errorf("no period: default %s applies, got %d", defaultRespawnPeriod, got)
	}
}

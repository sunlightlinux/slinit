package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCronRunner_BasicExecution(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "cron-test")

	marker := filepath.Join(t.TempDir(), "cron-ran")

	cr := NewCronRunner(svc, []string{"/bin/sh", "-c", "echo ok > " + marker},
		100*time.Millisecond, 0, "continue", set.logger)

	cr.Start()
	time.Sleep(350 * time.Millisecond)
	cr.Stop()

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("cron command did not execute: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("marker file is empty")
	}
}

func TestCronRunner_InitialDelay(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "delay-test")

	marker := filepath.Join(t.TempDir(), "delay-ran")

	cr := NewCronRunner(svc, []string{"/bin/sh", "-c", "echo ok > " + marker},
		time.Second, 200*time.Millisecond, "continue", set.logger)

	cr.Start()
	// Before delay expires, marker should not exist
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("cron ran before delay expired")
	}

	// After delay + first execution
	time.Sleep(300 * time.Millisecond)
	cr.Stop()

	if _, err := os.Stat(marker); err != nil {
		t.Fatal("cron did not execute after delay")
	}
}

func TestCronRunner_OnErrorStop(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "error-test")

	cr := NewCronRunner(svc, []string{"/bin/sh", "-c", "exit 1"},
		50*time.Millisecond, 0, "stop", set.logger)

	cr.Start()
	// Wait for the cron loop to exit on its own (on-error=stop)
	time.Sleep(200 * time.Millisecond)

	// Should have stopped on its own
	if cr.IsRunning() {
		t.Error("cron should not be running after error with on-error=stop")
	}
	cr.Stop() // should be safe to call
}

func TestCronRunner_OnErrorContinue(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "continue-test")

	counter := filepath.Join(t.TempDir(), "count")

	// Command that fails first, then succeeds
	cmd := []string{"/bin/sh", "-c", "echo x >> " + counter + "; exit 1"}
	cr := NewCronRunner(svc, cmd, 50*time.Millisecond, 0, "continue", set.logger)

	cr.Start()
	time.Sleep(200 * time.Millisecond)
	cr.Stop()

	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal("counter file not created")
	}
	// Should have run multiple times despite errors
	lines := 0
	for _, b := range data {
		if b == 'x' {
			lines++
		}
	}
	if lines < 2 {
		t.Errorf("expected multiple runs with on-error=continue, got %d", lines)
	}
}

func TestCronRunner_StopWaitsForCompletion(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "wait-test")

	marker := filepath.Join(t.TempDir(), "done")

	// Command that takes 200ms
	cr := NewCronRunner(svc, []string{"/bin/sh", "-c", "sleep 0.2 && echo done > " + marker},
		5*time.Second, 0, "continue", set.logger)

	cr.Start()
	time.Sleep(50 * time.Millisecond) // Let the first execution start

	// Stop should wait for completion
	cr.Stop()

	if _, err := os.Stat(marker); err != nil {
		t.Fatal("Stop did not wait for in-progress execution to complete")
	}
}

func TestCronRunner_DoubleStartStop(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "double-test")

	cr := NewCronRunner(svc, []string{"/bin/true"}, time.Second, 0, "continue", set.logger)

	// Double start should be safe
	cr.Start()
	cr.Start()

	// Double stop should be safe
	cr.Stop()
	cr.Stop()
}

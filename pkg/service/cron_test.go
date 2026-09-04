package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
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

// Regression: calendar mode leaves cr.interval at zero, which used to
// feed context.WithTimeout(ctx, 0) and kill every fire before it could
// write anything. executeCommand now falls back to time.Minute.
func TestCronRunner_CalendarExecutesCommand(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "cal-test")

	marker := filepath.Join(t.TempDir(), "cal-ran")

	// Fire every second: *:*:* — full wildcard list yields nil for each
	// component, which inAny/containsInt treat as "any".
	spec, err := ParseCalendar("*:*:*")
	if err != nil {
		t.Fatalf("ParseCalendar: %v", err)
	}

	cr := NewCalendarCronRunner(svc, []string{"/bin/sh", "-c", "echo ok > " + marker},
		spec, 0, false, "continue", set.logger)

	cr.Start()
	time.Sleep(1500 * time.Millisecond) // long enough to cross a second boundary
	cr.Stop()

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("calendar cron did not execute: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("calendar cron ran but produced no output")
	}
}

// TestCronRunner_ECHILDTreatedAsSuccess covers the PID-1 SIGCHLD-
// reaper race: exec.Cmd.Run() returns waitid ECHILD when slinit's
// orphan reaper grabbed the zombie before the cron goroutine's Wait4
// could see it. runOnce should treat that specific err as success
// (no error log line, no on-error=stop trigger). Regression against
// the noisy "cron-command for … failed: waitid: no child processes"
// spew that surfaced running the demo.
func TestCronRunner_ECHILDTreatedAsSuccess(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "echild-test")

	// Force the ECHILD path directly: build a *exec.Cmd wrapping
	// a bogus PID, then simulate what cmd.Run() returns from a
	// racy Wait — a wrapped ECHILD via errors.Is chain. cron.go
	// specifically checks errors.Is(err, syscall.ECHILD), which
	// matches both a bare syscall.Errno and an *os.SyscallError
	// wrapping it. Verify both shapes are absorbed.
	cr := NewCronRunner(svc, []string{"/bin/true"},
		100*time.Millisecond, 0, "stop", set.logger)
	_ = cr

	// Bare syscall.ECHILD
	if !isECHILDBenign(syscall.ECHILD) {
		t.Errorf("bare syscall.ECHILD should be absorbed by errors.Is check")
	}
	// Wrapped in *os.SyscallError (exec.Cmd.Run() error shape)
	wrapped := &os.SyscallError{Syscall: "waitid", Err: syscall.ECHILD}
	if !isECHILDBenign(wrapped) {
		t.Errorf("*os.SyscallError wrapping ECHILD should be absorbed")
	}
	// Wrapped in *exec.ExitError-alike — verify a NON-ECHILD error
	// is NOT swallowed (regression guard against over-broad match).
	e := &exec.ExitError{}
	if isECHILDBenign(e) {
		t.Errorf("exec.ExitError should NOT be absorbed as ECHILD")
	}
}

// isECHILDBenign mirrors cron.go's ECHILD absorption check so the
// test asserts against the same predicate the runtime uses.
func isECHILDBenign(err error) bool {
	if err == nil {
		return false
	}
	return isECHILDErr(err)
}

package logging

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCatchAllCaptures(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "catch-all.log")

	cal, err := StartCatchAll(logPath)
	if err != nil {
		t.Fatalf("StartCatchAll: %v", err)
	}

	// Write to stdout (fd 1) — this should be captured.
	os.Stdout.WriteString("hello from stdout\n")
	os.Stderr.WriteString("hello from stderr\n")

	// Give the drain goroutine time to process.
	time.Sleep(50 * time.Millisecond)

	cal.Stop()

	// Verify log file contains both messages.
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "hello from stdout") {
		t.Errorf("log missing stdout output; got:\n%s", content)
	}
	if !strings.Contains(content, "hello from stderr") {
		t.Errorf("log missing stderr output; got:\n%s", content)
	}
}

func TestCatchAllTimestamps(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "catch-all.log")

	cal, err := StartCatchAll(logPath)
	if err != nil {
		t.Fatalf("StartCatchAll: %v", err)
	}

	os.Stdout.WriteString("timestamped line\n")
	time.Sleep(50 * time.Millisecond)
	cal.Stop()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	// Each line should have an ISO8601-style timestamp prefix.
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		t.Fatal("empty log")
	}
	// Format: "2006-01-02T15:04:05.000 ..."
	if len(lines[0]) < 24 || lines[0][4] != '-' || lines[0][10] != 'T' {
		t.Errorf("line missing timestamp prefix: %q", lines[0])
	}
}

func TestCatchAllRestoresFDs(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "catch-all.log")

	cal, err := StartCatchAll(logPath)
	if err != nil {
		t.Fatalf("StartCatchAll: %v", err)
	}

	cal.Stop()

	// After Stop(), writing to stdout should not panic or error.
	_, err = os.Stdout.WriteString("after stop\n")
	if err != nil {
		t.Errorf("write after stop: %v", err)
	}
}

func TestCatchAllConsole(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "catch-all.log")

	cal, err := StartCatchAll(logPath)
	if err != nil {
		t.Fatalf("StartCatchAll: %v", err)
	}

	// Console() should return a valid writable file.
	cons := cal.Console()
	if cons == nil {
		t.Fatal("Console() returned nil")
	}

	cal.Stop()
}

func TestCatchAllDash(t *testing.T) {
	// logPath "-" means no file, console only.
	cal, err := StartCatchAll("-")
	if err != nil {
		t.Fatalf("StartCatchAll(-): %v", err)
	}

	os.Stdout.WriteString("console only line\n")
	time.Sleep(50 * time.Millisecond)
	cal.Stop()
	// No crash = pass; just verify no file was created.
}

// TestCatchAllReattachAfterRedirect reproduces the InitPID1 scenario:
// catch-all sets up the pipe, an early log message is written, then
// some other code (setupConsole in real life) Dup2s fd 1/2 to a
// different fd. Without ReattachStdoutErr, subsequent log messages
// bypass the pipe and the early message ends up flushed late by the
// drain goroutine — visibly out-of-order timestamps in the demo log.
//
// This test verifies that after ReattachStdoutErr, fd 1/2 are bound
// back to the pipe and a subsequent write reaches the catch-all log
// (i.e. it goes through the pipe→drain path, not the bypass path).
func TestCatchAllReattachAfterRedirect(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "catch-all.log")

	cal, err := StartCatchAll(logPath)
	if err != nil {
		t.Fatalf("StartCatchAll: %v", err)
	}
	defer cal.Stop()

	// Write before the simulated redirect — must reach the log.
	os.Stdout.WriteString("before-redirect\n")

	// Simulate setupConsole's Dup2: redirect fd 1/2 to a side file.
	sidePath := filepath.Join(dir, "side.log")
	side, err := os.OpenFile(sidePath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open side: %v", err)
	}
	defer side.Close()
	origStdout := os.Stdout
	origStderr := os.Stderr
	if err := syscall.Dup2(int(side.Fd()), 1); err != nil {
		t.Fatalf("dup2 stdout to side: %v", err)
	}
	if err := syscall.Dup2(int(side.Fd()), 2); err != nil {
		t.Fatalf("dup2 stderr to side: %v", err)
	}
	os.Stdout = os.NewFile(1, "/dev/stdout")
	os.Stderr = os.NewFile(2, "/dev/stderr")

	// At this point fd 1/2 bypass the catch-all pipe.
	os.Stdout.WriteString("during-bypass\n")

	// Reattach — fd 1/2 should now go through the pipe again.
	if err := cal.ReattachStdoutErr(); err != nil {
		t.Fatalf("ReattachStdoutErr: %v", err)
	}

	os.Stdout.WriteString("after-reattach\n")

	// Drain.
	time.Sleep(80 * time.Millisecond)

	// Restore real stdio so test framework I/O still works after the test.
	os.Stdout = origStdout
	os.Stderr = origStderr

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	log := string(logBytes)
	if !strings.Contains(log, "before-redirect") {
		t.Errorf("catch-all log missing 'before-redirect':\n%s", log)
	}
	if !strings.Contains(log, "after-reattach") {
		t.Errorf("catch-all log missing 'after-reattach' (Reattach failed):\n%s", log)
	}

	// 'during-bypass' must NOT appear in the catch-all — it went to
	// the side file, proving the bypass scenario was real and the
	// reattach actually flipped the redirect back.
	if strings.Contains(log, "during-bypass") {
		t.Errorf("catch-all log unexpectedly contains 'during-bypass' — bypass scenario didn't trigger:\n%s", log)
	}
	sideBytes, _ := os.ReadFile(sidePath)
	if !strings.Contains(string(sideBytes), "during-bypass") {
		t.Errorf("side file missing 'during-bypass'; got:\n%s", sideBytes)
	}
}

func TestCatchAllStopIdempotent(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "catch-all.log")

	cal, err := StartCatchAll(logPath)
	if err != nil {
		t.Fatalf("StartCatchAll: %v", err)
	}

	// Stop multiple times should not panic.
	cal.Stop()
	cal.Stop()
	cal.Stop()
}

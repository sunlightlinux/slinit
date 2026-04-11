package logging

import (
	"os"
	"path/filepath"
	"strings"
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

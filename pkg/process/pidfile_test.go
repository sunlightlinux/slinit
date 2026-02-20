package process

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestReadPIDFileValid(t *testing.T) {
	// Write our own PID to a temp file
	dir := t.TempDir()
	path := filepath.Join(dir, "test.pid")

	myPID := os.Getpid()
	if err := os.WriteFile(path, []byte(strconv.Itoa(myPID)+"\n"), 0644); err != nil {
		t.Fatalf("failed to write PID file: %v", err)
	}

	pid, result, err := ReadPIDFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != PIDResultOK {
		t.Errorf("expected PIDResultOK, got %v", result)
	}
	if pid != myPID {
		t.Errorf("expected PID %d, got %d", myPID, pid)
	}
}

func TestReadPIDFileInvalidContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.pid")

	if err := os.WriteFile(path, []byte("not-a-number\n"), 0644); err != nil {
		t.Fatalf("failed to write PID file: %v", err)
	}

	_, result, _ := ReadPIDFile(path)
	if result != PIDResultFailed {
		t.Errorf("expected PIDResultFailed for invalid content, got %v", result)
	}
}

func TestReadPIDFileNonexistentPID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.pid")

	// Use a very high PID that almost certainly doesn't exist
	if err := os.WriteFile(path, []byte("4194304\n"), 0644); err != nil {
		t.Fatalf("failed to write PID file: %v", err)
	}

	pid, result, _ := ReadPIDFile(path)
	if result != PIDResultTerminated {
		t.Errorf("expected PIDResultTerminated for nonexistent PID, got %v", result)
	}
	if pid != 4194304 {
		t.Errorf("expected PID 4194304, got %d", pid)
	}
}

func TestReadPIDFileNotFound(t *testing.T) {
	_, result, err := ReadPIDFile("/nonexistent/path/test.pid")
	if result != PIDResultFailed {
		t.Errorf("expected PIDResultFailed for missing file, got %v", result)
	}
	if err == nil {
		t.Error("expected error for missing file")
	}
}

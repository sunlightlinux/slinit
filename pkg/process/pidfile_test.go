package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestReadPIDFileValid(t *testing.T) {
	// Write a live child's PID to a temp file (not our own: a pid file
	// naming the service manager itself is refused).
	dir := t.TempDir()
	path := filepath.Join(dir, "test.pid")

	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { child.Process.Kill(); child.Wait() }()
	myPID := child.Process.Pid
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

// A FIFO at the pid-file path used to block the open, and with it PID 1
// (ReadPIDFile runs under the service set's lock), until something
// opened the write end.
func TestReadPIDFileFIFODoesNotBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.pid")
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Fatal(err)
	}
	type res struct {
		result PIDResult
		err    error
	}
	done := make(chan res, 1)
	go func() {
		_, r, err := ReadPIDFile(path)
		done <- res{r, err}
	}()
	select {
	case r := <-done:
		if r.result != PIDResultFailed || r.err == nil {
			t.Errorf("FIFO pid file: result %v err %v, want PIDResultFailed with an error", r.result, r.err)
		}
	case <-time.After(2 * time.Second):
		// Unblock the reader so the test can finish.
		if w, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
			w.Close()
		}
		<-done
		t.Fatal("ReadPIDFile blocked on a FIFO")
	}
}

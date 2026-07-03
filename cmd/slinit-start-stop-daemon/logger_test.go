package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStartLoggerRoutesData(t *testing.T) {
	// Use `tee SINK` as a whitespace-splittable "logger": it reads its
	// stdin (our pipe) and writes to a file we can assert on. Widely
	// available on Alpine/Void/Debian without shell redirection tricks.
	dir := t.TempDir()
	sink := filepath.Join(dir, "sink")

	w, err := startLogger("tee " + sink)
	if err != nil {
		t.Fatalf("startLogger: %v", err)
	}
	msg := "hello from producer\n"
	if _, err := w.Write([]byte(msg)); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Close the write end so tee sees EOF and exits.
	w.Close()

	// Poll for the file with a short timeout; logger runs in a goroutine.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(sink)
		if err == nil && string(data) == msg {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	got, _ := os.ReadFile(sink)
	t.Errorf("timed out waiting for logger sink; got %q, want %q", string(got), msg)
}

func TestStartLoggerRejectsEmpty(t *testing.T) {
	if _, err := startLogger(""); err == nil {
		t.Error("empty spec should fail")
	}
	if _, err := startLogger("   "); err == nil {
		t.Error("whitespace-only spec should fail")
	}
}

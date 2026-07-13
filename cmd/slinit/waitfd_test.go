package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestWaitForFDCloseReturnsOnEOF drives the happy path: opening a pipe,
// closing the writer end from a goroutine after a short delay, and
// asserting that waitForFDClose returns nil.
func TestWaitForFDCloseReturnsOnEOF(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	// waitForFDClose takes ownership (defer f.Close on the read end),
	// so we only track the write end from here.
	fd := int(r.Fd())

	done := make(chan error, 1)
	go func() { done <- waitForFDClose(fd) }()

	// Give the goroutine a moment to enter Read, then release.
	time.Sleep(20 * time.Millisecond)
	w.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waitForFDClose returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForFDClose did not return within 2s of EOF")
	}
}

// TestWaitForFDCloseDrainsBytes verifies the reader tolerates writes
// before the writer end closes — the container manager may emit
// progress text on the sync fd, and slinit should silently drain it
// rather than error.
func TestWaitForFDCloseDrainsBytes(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	fd := int(r.Fd())

	done := make(chan error, 1)
	go func() { done <- waitForFDClose(fd) }()

	// A short burst of writes, then close.
	w.Write([]byte("preparing mounts\n"))
	w.Write([]byte("attaching namespaces\n"))
	w.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waitForFDClose returned %v, want nil after write+close", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForFDClose did not return within 2s of writer close")
	}
}

// TestWaitForFDCloseRejectsReservedFDs guards against a mis-wired flag
// where the operator points --wait-fd at stdin/stdout/stderr. Blocking
// on a terminal fd would deadlock the boot cascade with no way out.
func TestWaitForFDCloseRejectsReservedFDs(t *testing.T) {
	for _, fd := range []int{0, 1, 2} {
		err := waitForFDClose(fd)
		if err == nil {
			t.Errorf("fd %d: expected error, got nil", fd)
			continue
		}
		if !strings.Contains(err.Error(), "reserved") {
			t.Errorf("fd %d: error = %v, want mention of 'reserved'", fd, err)
		}
	}
}

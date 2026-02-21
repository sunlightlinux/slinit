package service

import (
	"bytes"
	"os"
	"sync"
	"testing"
	"time"
)

func TestLogBuffer_Basic(t *testing.T) {
	lb := NewLogBuffer(1024)

	// Write directly to buffer via pipe
	w, err := lb.CreatePipe()
	if err != nil {
		t.Fatalf("CreatePipe: %v", err)
	}

	lb.StartReader()

	msg := "hello world\n"
	w.Write([]byte(msg))
	w.Close()
	lb.pipeW = nil // already closed

	// Wait for reader to finish
	<-lb.doneCh

	got := lb.GetBuffer()
	if string(got) != msg {
		t.Errorf("GetBuffer = %q, want %q", got, msg)
	}
}

func TestLogBuffer_MaxSize(t *testing.T) {
	lb := NewLogBuffer(16)

	w, err := lb.CreatePipe()
	if err != nil {
		t.Fatalf("CreatePipe: %v", err)
	}

	lb.StartReader()

	// Write more than max
	w.Write([]byte("0123456789abcdef_excess_data"))
	w.Close()
	lb.pipeW = nil

	<-lb.doneCh

	got := lb.GetBuffer()
	if len(got) != 16 {
		t.Errorf("buffer length = %d, want 16", len(got))
	}
	if string(got) != "0123456789abcdef" {
		t.Errorf("buffer = %q, want %q", got, "0123456789abcdef")
	}
}

func TestLogBuffer_Clear(t *testing.T) {
	lb := NewLogBuffer(1024)

	// Simulate buffer with data
	lb.buf = []byte("some data\n")

	got := lb.GetBufferAndClear()
	if string(got) != "some data\n" {
		t.Errorf("GetBufferAndClear = %q, want %q", got, "some data\n")
	}

	// Buffer should be empty now
	got2 := lb.GetBuffer()
	if got2 != nil {
		t.Errorf("GetBuffer after clear = %q, want nil", got2)
	}
}

func TestLogBuffer_RestartMarker(t *testing.T) {
	lb := NewLogBuffer(1024)

	// Empty buffer: no marker added
	lb.AppendRestartMarker()
	if len(lb.buf) != 0 {
		t.Errorf("marker added to empty buffer")
	}

	// Buffer with trailing newline
	lb.buf = []byte("line1\n")
	lb.AppendRestartMarker()
	expected := "line1\n(slinit: note: service restarted)\n"
	if string(lb.buf) != expected {
		t.Errorf("buf = %q, want %q", lb.buf, expected)
	}

	// Buffer without trailing newline
	lb.buf = []byte("partial")
	lb.AppendRestartMarker()
	expected = "partial\n(slinit: note: service restarted)\n"
	if string(lb.buf) != expected {
		t.Errorf("buf = %q, want %q", lb.buf, expected)
	}
}

func TestLogBuffer_PipeCapture(t *testing.T) {
	lb := NewLogBuffer(4096)

	w, err := lb.CreatePipe()
	if err != nil {
		t.Fatalf("CreatePipe: %v", err)
	}

	lb.StartReader()

	// Write multiple lines
	lines := []string{
		"line 1\n",
		"line 2\n",
		"line 3\n",
	}
	for _, line := range lines {
		w.Write([]byte(line))
	}
	w.Close()
	lb.pipeW = nil

	<-lb.doneCh

	got := lb.GetBuffer()
	want := "line 1\nline 2\nline 3\n"
	if string(got) != want {
		t.Errorf("buffer = %q, want %q", got, want)
	}
}

func TestLogBuffer_ConcurrentAccess(t *testing.T) {
	lb := NewLogBuffer(8192)

	w, err := lb.CreatePipe()
	if err != nil {
		t.Fatalf("CreatePipe: %v", err)
	}

	lb.StartReader()

	// Writer goroutine
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			w.Write([]byte("data\n"))
		}
		w.Close()
	}()

	// Concurrent reads while writing
	for i := 0; i < 10; i++ {
		_ = lb.GetBuffer()
		time.Sleep(time.Millisecond)
	}

	wg.Wait()
	<-lb.doneCh

	got := lb.GetBuffer()
	if !bytes.Contains(got, []byte("data\n")) {
		t.Error("buffer should contain written data")
	}
}

func TestLogBuffer_OutputPipeIntegration(t *testing.T) {
	// Test that OutputPipe works with a real child process
	lb := NewLogBuffer(4096)

	w, err := lb.CreatePipe()
	if err != nil {
		t.Fatalf("CreatePipe: %v", err)
	}

	// Simulate what StartProcess does: dup the write end to child stdout
	// Here we just write directly as a stand-in
	lb.StartReader()

	// Write as if from a child process
	os.NewFile(w.Fd(), "pipe-write")
	w.Write([]byte("child output\n"))
	lb.CloseWriteEnd()

	<-lb.doneCh

	got := lb.GetBuffer()
	if string(got) != "child output\n" {
		t.Errorf("buffer = %q, want %q", got, "child output\n")
	}
}

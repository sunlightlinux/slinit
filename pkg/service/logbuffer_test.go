package service

import (
	"bytes"
	"sync"
	"syscall"
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

	// Write as if from a child process.
	//
	// There used to be an `os.NewFile(w.Fd(), "pipe-write")` here whose
	// result was discarded. It did nothing — the write below goes through
	// `w` — but it wrapped `w`'s descriptor in a SECOND *os.File, which
	// owns that fd and carries a finalizer. CloseWriteEnd() then closed
	// `w`, the number was handed out to another test's log file, and
	// whenever the GC got round to the orphan its finalizer closed that
	// number out from under its new owner.
	//
	// That is the "logrotate EBADF" flake: some other test failed in
	// `TempDir` cleanup with "bad file descriptor", never in an assertion,
	// and never when the logrotate tests were run on their own — because
	// the culprit was in this file and `-run TestLogRotator` did not
	// select it. Traced with `strace -f -k -e trace=close -e status=failed`:
	// `close(6) = -1 EBADF` under `runtime.runFinalizers` ->
	// `os.(*file).close`.
	//
	// `w.Fd()` is a second trap on the same line: it takes the file out of
	// nonblocking mode. Neither is needed, so neither is here.
	w.Write([]byte("child output\n"))
	lb.CloseWriteEnd()

	<-lb.doneCh

	got := lb.GetBuffer()
	if string(got) != "child output\n" {
		t.Errorf("buffer = %q, want %q", got, "child output\n")
	}
}

// A restart creates a new pipe while the reader of the previous one may
// still be running: the old process left a child holding the pipe's
// write end, or the reader has simply not seen EOF yet. StartReader used
// to return early whenever any reader was running, so nothing ever read
// the new pipe: the restarted service's output was lost and, once the
// pipe filled, the service blocked writing to stdout for ever.
func TestLogBuffer_RestartWhileOldReaderRuns(t *testing.T) {
	lb := NewLogBuffer(4096)

	w1, err := lb.CreatePipe()
	if err != nil {
		t.Fatalf("CreatePipe: %v", err)
	}
	// The old process's child keeps the first pipe open.
	fd, err := syscall.Dup(int(w1.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	lb.StartReader()
	lb.CloseWriteEnd()

	// Restart: a new pipe for the new process.
	w2, err := lb.CreatePipe()
	if err != nil {
		t.Fatalf("CreatePipe: %v", err)
	}
	lb.StartReader()
	if _, err := w2.Write([]byte("after restart\n")); err != nil {
		t.Fatal(err)
	}
	lb.CloseWriteEnd()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Contains(lb.GetBuffer(), []byte("after restart")) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("output of the restarted process never reached the buffer: %q", lb.GetBuffer())
}

// The same through a process service: the first run leaves a setsid'd
// child holding stdout and exits (the sleep lets setsid run before the
// exit kills the process group), so the restart happens while the
// reader of the first pipe is still running. The second run's output
// must reach the buffer.
func TestProcessBufferLogAfterRestartWithLeakedStdout(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	svc := NewProcessService(set, "buf-restart")
	svc.SetCommand([]string{"/bin/sh", "-c",
		"if [ -e " + dir + "/ran ]; then echo second-run; exec sleep 30; fi; " +
			"echo first-run; touch " + dir + "/ran; setsid sleep 3 & sleep 0.2; exit 1"})
	svc.SetLogType(LogToBuffer)
	svc.SetAutoRestart(RestartAlways)
	svc.SetRestartDelay(20 * time.Millisecond)
	svc.SetRestartLimits(time.Minute, 20)
	set.AddService(svc)
	set.StartService(svc)
	defer set.StopService(svc)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if lb := svc.GetLogBuffer(); lb != nil && bytes.Contains(lb.GetBuffer(), []byte("second-run")) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	var got []byte
	if lb := svc.GetLogBuffer(); lb != nil {
		got = lb.GetBuffer()
	}
	t.Fatalf("the restarted process's output never reached the buffer: %q", got)
}

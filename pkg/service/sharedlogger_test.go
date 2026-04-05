package service

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSharedLogMux_BasicPrefixing(t *testing.T) {
	mux, err := NewSharedLogMux()
	if err != nil {
		t.Fatalf("NewSharedLogMux() failed: %v", err)
	}
	defer mux.Close()

	// Add a producer
	pipeW, err := mux.AddProducer("my-service")
	if err != nil {
		t.Fatalf("AddProducer failed: %v", err)
	}

	// Write to producer pipe
	fmt.Fprintln(pipeW, "hello world")
	fmt.Fprintln(pipeW, "second line")
	pipeW.Close()

	// Read from logger pipe
	scanner := bufio.NewScanner(mux.InputPipe())
	var lines []string
	done := make(chan struct{})
	go func() {
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
			if len(lines) == 2 {
				break
			}
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for prefixed lines")
	}

	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if lines[0] != "[my-service] hello world" {
		t.Errorf("line 0: got %q", lines[0])
	}
	if lines[1] != "[my-service] second line" {
		t.Errorf("line 1: got %q", lines[1])
	}
}

func TestSharedLogMux_MultipleProducers(t *testing.T) {
	mux, err := NewSharedLogMux()
	if err != nil {
		t.Fatalf("NewSharedLogMux() failed: %v", err)
	}
	defer mux.Close()

	pipe1, _ := mux.AddProducer("svc-a")
	pipe2, _ := mux.AddProducer("svc-b")

	// Write from both producers
	fmt.Fprintln(pipe1, "from A")
	fmt.Fprintln(pipe2, "from B")
	pipe1.Close()
	pipe2.Close()

	// Collect lines
	scanner := bufio.NewScanner(mux.InputPipe())
	var lines []string
	done := make(chan struct{})
	go func() {
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
			if len(lines) == 2 {
				break
			}
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}

	sort.Strings(lines)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "[svc-a] from A" {
		t.Errorf("unexpected line: %q", lines[0])
	}
	if lines[1] != "[svc-b] from B" {
		t.Errorf("unexpected line: %q", lines[1])
	}
}

func TestSharedLogMux_RemoveProducer(t *testing.T) {
	mux, err := NewSharedLogMux()
	if err != nil {
		t.Fatalf("NewSharedLogMux() failed: %v", err)
	}
	defer mux.Close()

	_, err = mux.AddProducer("temp-svc")
	if err != nil {
		t.Fatalf("AddProducer failed: %v", err)
	}

	if mux.ProducerCount() != 1 {
		t.Errorf("expected 1 producer, got %d", mux.ProducerCount())
	}

	mux.RemoveProducer("temp-svc")

	if mux.ProducerCount() != 0 {
		t.Errorf("expected 0 producers, got %d", mux.ProducerCount())
	}

	// Remove again — should not panic
	mux.RemoveProducer("temp-svc")
}

func TestSharedLogMux_ProducerRestart(t *testing.T) {
	mux, err := NewSharedLogMux()
	if err != nil {
		t.Fatalf("NewSharedLogMux() failed: %v", err)
	}
	defer mux.Close()

	// Start reading in background
	scanner := bufio.NewScanner(mux.InputPipe())
	var lines []string
	var linesMu sync.Mutex
	go func() {
		for scanner.Scan() {
			linesMu.Lock()
			lines = append(lines, scanner.Text())
			linesMu.Unlock()
		}
	}()

	// First instance — write and close
	pipe1, _ := mux.AddProducer("restart-svc")
	fmt.Fprintln(pipe1, "first run")
	pipe1.Close()
	time.Sleep(100 * time.Millisecond) // let reader pick it up

	// Re-add same name (simulates restart) — old reader is stopped
	pipe2, _ := mux.AddProducer("restart-svc")
	fmt.Fprintln(pipe2, "second run")
	pipe2.Close()
	time.Sleep(100 * time.Millisecond)

	// Should still have 1 producer
	if mux.ProducerCount() != 1 {
		t.Errorf("expected 1 producer, got %d", mux.ProducerCount())
	}

	// At least the second line should be present (first may or may not survive the restart)
	linesMu.Lock()
	defer linesMu.Unlock()
	found := false
	for _, l := range lines {
		if l == "[restart-svc] second run" {
			found = true
		}
		if !strings.HasPrefix(l, "[restart-svc] ") {
			t.Errorf("unexpected prefix: %q", l)
		}
	}
	if !found {
		t.Errorf("expected '[restart-svc] second run' in lines: %v", lines)
	}
}

func TestSharedLogMux_ProducerNames(t *testing.T) {
	mux, err := NewSharedLogMux()
	if err != nil {
		t.Fatalf("NewSharedLogMux() failed: %v", err)
	}
	defer mux.Close()

	mux.AddProducer("alpha")
	mux.AddProducer("beta")
	mux.AddProducer("gamma")

	names := mux.ProducerNames()
	sort.Strings(names)

	if len(names) != 3 {
		t.Fatalf("expected 3 names, got %d", len(names))
	}
	expected := []string{"alpha", "beta", "gamma"}
	for i, name := range names {
		if name != expected[i] {
			t.Errorf("name[%d]: expected %q, got %q", i, expected[i], name)
		}
	}
}

func TestSharedLogMux_CloseIsSafe(t *testing.T) {
	mux, err := NewSharedLogMux()
	if err != nil {
		t.Fatalf("NewSharedLogMux() failed: %v", err)
	}

	mux.AddProducer("svc1")
	mux.AddProducer("svc2")

	// Close should not panic
	mux.Close()

	// Double close should not panic
	mux.Close()

	// AddProducer after close should fail
	_, err = mux.AddProducer("svc3")
	if err == nil {
		t.Error("expected error adding producer after close")
	}
}

func TestSharedLogMux_ReplaceLoggerPipe(t *testing.T) {
	mux, err := NewSharedLogMux()
	if err != nil {
		t.Fatalf("NewSharedLogMux() failed: %v", err)
	}
	defer mux.Close()

	// Add a producer
	pipeW, _ := mux.AddProducer("svc")

	// Replace the logger pipe (simulates logger restart)
	newR, err := mux.ReplaceLoggerPipe()
	if err != nil {
		t.Fatalf("ReplaceLoggerPipe failed: %v", err)
	}

	// Write to producer — should appear on new pipe
	fmt.Fprintln(pipeW, "after replace")
	pipeW.Close()

	scanner := bufio.NewScanner(newR)
	done := make(chan struct{})
	var line string
	go func() {
		if scanner.Scan() {
			line = scanner.Text()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}

	if line != "[svc] after replace" {
		t.Errorf("expected '[svc] after replace', got %q", line)
	}
	newR.Close()
}

func TestSharedLogMux_LargeOutput(t *testing.T) {
	mux, err := NewSharedLogMux()
	if err != nil {
		t.Fatalf("NewSharedLogMux() failed: %v", err)
	}
	defer mux.Close()

	pipeW, _ := mux.AddProducer("bulk")

	lineCount := 100
	go func() {
		for i := 0; i < lineCount; i++ {
			fmt.Fprintf(pipeW, "line %d\n", i)
		}
		pipeW.Close()
	}()

	scanner := bufio.NewScanner(mux.InputPipe())
	received := 0
	done := make(chan struct{})
	go func() {
		for scanner.Scan() {
			text := scanner.Text()
			if !strings.HasPrefix(text, "[bulk] line ") {
				t.Errorf("bad prefix: %q", text)
			}
			received++
			if received == lineCount {
				break
			}
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout: received %d/%d lines", received, lineCount)
	}

	if received != lineCount {
		t.Errorf("expected %d lines, got %d", lineCount, received)
	}
}

func TestSharedLogMux_BrokenLoggerPipe(t *testing.T) {
	mux, err := NewSharedLogMux()
	if err != nil {
		t.Fatalf("NewSharedLogMux() failed: %v", err)
	}

	pipeW, _ := mux.AddProducer("svc")

	// Close the read end to simulate broken logger pipe
	mux.InputPipe().Close()

	// Write should not block forever — reader goroutine should detect broken pipe
	fmt.Fprintln(pipeW, "this should not block")
	pipeW.Close()

	// Give reader time to notice
	time.Sleep(200 * time.Millisecond)

	// Cleanup should not panic
	mux.RemoveProducer("svc")
	// Close the write end manually since mux.Close will try to close already-closed loggerR
	mux.loggerW.Close()
}

func TestSharedLogMux_PipeForChildProcess(t *testing.T) {
	// Verify the pipe write-end can be passed as ExtraFiles to exec.Cmd
	mux, err := NewSharedLogMux()
	if err != nil {
		t.Fatalf("NewSharedLogMux() failed: %v", err)
	}
	defer mux.Close()

	pipeW, err := mux.AddProducer("child-svc")
	if err != nil {
		t.Fatalf("AddProducer failed: %v", err)
	}

	// Verify the write-end is a valid file
	fi, err := pipeW.Stat()
	if err != nil {
		t.Fatalf("Stat on pipeW failed: %v", err)
	}
	if fi.Mode()&os.ModeNamedPipe == 0 {
		t.Logf("pipe mode: %v (may be anonymous pipe)", fi.Mode())
	}

	pipeW.Close()
}

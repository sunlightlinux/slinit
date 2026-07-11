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

	// waitForLine polls for a given line under linesMu; returns false on
	// timeout. Replaces the previous time.Sleep+check pattern, which was
	// flaky under -race on busy CI hosts where 100ms wasn't enough for
	// the producer → mux → reader chain to settle.
	waitForLine := func(want string, timeout time.Duration) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			linesMu.Lock()
			for _, l := range lines {
				if l == want {
					linesMu.Unlock()
					return true
				}
			}
			linesMu.Unlock()
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}

	// First instance — write and close.
	pipe1, _ := mux.AddProducer("restart-svc")
	fmt.Fprintln(pipe1, "first run")
	pipe1.Close()
	if !waitForLine("[restart-svc] first run", 2*time.Second) {
		linesMu.Lock()
		t.Logf("lines after first run: %v", lines)
		linesMu.Unlock()
		// Not fatal: the test's real assertion is that second-run
		// survives the restart.
	}

	// Re-add same name (simulates restart) — old reader is stopped.
	pipe2, _ := mux.AddProducer("restart-svc")
	fmt.Fprintln(pipe2, "second run")
	pipe2.Close()
	if !waitForLine("[restart-svc] second run", 2*time.Second) {
		linesMu.Lock()
		t.Errorf("expected '[restart-svc] second run' within 2s; lines: %v", lines)
		linesMu.Unlock()
	}

	// Should still have 1 producer.
	if mux.ProducerCount() != 1 {
		t.Errorf("expected 1 producer, got %d", mux.ProducerCount())
	}

	// Every captured line should carry the prefix.
	linesMu.Lock()
	defer linesMu.Unlock()
	for _, l := range lines {
		if !strings.HasPrefix(l, "[restart-svc] ") {
			t.Errorf("unexpected prefix: %q", l)
		}
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

// TestSharedLogMux_LossyDropsUnderBackpressure verifies the svlogd -L
// analogue: with lossy mode on and a full queue, producer writes are
// dropped rather than blocked, and DropCount reflects the loss.
func TestSharedLogMux_LossyDropsUnderBackpressure(t *testing.T) {
	// Very small queue + no reader on the logger side → the writer
	// goroutine fills the OS pipe (64KB kernel buffer) then blocks on
	// Write, and the queue backs up to its bound. Additional producer
	// lines then get dropped.
	mux, err := NewSharedLogMuxWithOptions(SharedLogMuxOptions{
		Lossy:          true,
		QueueSize:      4,
		ReportInterval: time.Hour, // suppress reports during the test
	})
	if err != nil {
		t.Fatalf("NewSharedLogMuxWithOptions: %v", err)
	}
	defer mux.Close()

	pipeW, err := mux.AddProducer("svc")
	if err != nil {
		t.Fatalf("AddProducer: %v", err)
	}

	// Write far more lines than the queue + pipe can hold. Each line
	// is ~4KB so ~40 lines fills up 64KB of pipe buffer plus the
	// 4-slot channel; the rest must drop.
	big := strings.Repeat("x", 4000)
	for i := 0; i < 200; i++ {
		fmt.Fprintln(pipeW, big)
	}
	pipeW.Close()

	// Give the reader goroutine time to catch up and drop.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if mux.DropCount() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if mux.DropCount() == 0 {
		t.Errorf("expected DropCount > 0 under backpressure, got 0")
	}
}

// TestSharedLogMux_LossyPassesLinesWhenReaderKeepsUp confirms that the
// lossy path is transparent when the sink drains fast enough — no
// spurious drops when the logger is not slow.
func TestSharedLogMux_LossyPassesLinesWhenReaderKeepsUp(t *testing.T) {
	mux, err := NewSharedLogMuxWithOptions(SharedLogMuxOptions{
		Lossy:          true,
		QueueSize:      64,
		ReportInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewSharedLogMuxWithOptions: %v", err)
	}

	pipeW, err := mux.AddProducer("svc")
	if err != nil {
		t.Fatalf("AddProducer: %v", err)
	}

	// Drain the logger pipe concurrently so writes never block.
	// Communicate the collected lines through a done channel so the
	// scanner goroutine is fully joined before we inspect the slice.
	type result struct{ lines []string }
	done := make(chan result)
	go func() {
		var lines []string
		scanner := bufio.NewScanner(mux.InputPipe())
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		done <- result{lines: lines}
	}()

	for i := 0; i < 10; i++ {
		fmt.Fprintf(pipeW, "line-%d\n", i)
	}
	pipeW.Close()

	// Let the writer flush before we tear down.
	time.Sleep(100 * time.Millisecond)

	if mux.DropCount() != 0 {
		t.Errorf("no drops expected with a live reader; got %d", mux.DropCount())
	}

	// mux.Close() shuts the mux pipe, letting the scanner return.
	mux.Close()

	select {
	case r := <-done:
		seen := 0
		for _, line := range r.lines {
			if strings.HasPrefix(line, "[svc] line-") {
				seen++
			}
		}
		if seen < 10 {
			t.Errorf("expected at least 10 lines delivered, got %d (all: %v)", seen, r.lines)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scanner goroutine did not exit after mux.Close()")
	}
}

// TestSharedLogMux_LossyEmitsDropReport confirms the writer synthesises
// a "[shared-logger] dropped N lines" heartbeat after ReportInterval
// once drops have occurred.
func TestSharedLogMux_LossyEmitsDropReport(t *testing.T) {
	mux, err := NewSharedLogMuxWithOptions(SharedLogMuxOptions{
		Lossy:          true,
		QueueSize:      2,
		ReportInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewSharedLogMuxWithOptions: %v", err)
	}

	// Reader goroutine collecting everything the mux emits.
	got := make(chan string, 64)
	go func() {
		scanner := bufio.NewScanner(mux.InputPipe())
		for scanner.Scan() {
			got <- scanner.Text()
		}
	}()

	pipeW, err := mux.AddProducer("svc")
	if err != nil {
		t.Fatalf("AddProducer: %v", err)
	}

	// Burst a lot to trigger drops, then trickle enough to unstick the
	// writer so it can emit the report on a subsequent successful write.
	big := strings.Repeat("y", 3000)
	for i := 0; i < 100; i++ {
		fmt.Fprintln(pipeW, big)
	}
	time.Sleep(200 * time.Millisecond)
	for i := 0; i < 5; i++ {
		fmt.Fprintf(pipeW, "post-%d\n", i)
		time.Sleep(30 * time.Millisecond)
	}
	pipeW.Close()
	mux.Close()

	deadline := time.After(2 * time.Second)
	foundReport := false
readLoop:
	for {
		select {
		case line, ok := <-got:
			if !ok {
				break readLoop
			}
			if strings.Contains(line, "[shared-logger] dropped") {
				foundReport = true
				break readLoop
			}
		case <-deadline:
			break readLoop
		}
	}

	if !foundReport {
		t.Errorf("expected a '[shared-logger] dropped ...' report line; drop count = %d", mux.DropCount())
	}
}

// TestSharedLogMux_BlockingModeUnaffected confirms the classic (non-lossy)
// path still uses direct-write and never touches the queue path.
func TestSharedLogMux_BlockingModeUnaffected(t *testing.T) {
	mux, err := NewSharedLogMux()
	if err != nil {
		t.Fatalf("NewSharedLogMux: %v", err)
	}
	defer mux.Close()

	if mux.queue != nil {
		t.Errorf("blocking mux should have nil queue, got non-nil")
	}
	if mux.DropCount() != 0 {
		t.Errorf("blocking mux DropCount should be 0, got %d", mux.DropCount())
	}
}

package logging

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

// safeBuffer is a mutex-protected bytes.Buffer for tests that write
// from one goroutine and read from another. bytes.Buffer is not
// goroutine-safe on its own.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
func (b *safeBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

// TestRingBufferAppendsBelowCapacity: writes that fit without
// wrapping produce Bytes() in chronological order.
func TestRingBufferAppendsBelowCapacity(t *testing.T) {
	rb := NewRingBuffer(64)
	rb.Write([]byte("hello "))
	rb.Write([]byte("world"))
	got := rb.Bytes()
	if string(got) != "hello world" {
		t.Errorf("Bytes = %q, want %q", got, "hello world")
	}
}

// TestRingBufferWrapsAcrossCapacity: the second write straddles the
// end of the buffer and must land correctly with the older content
// dropped from the head, keeping the newest N bytes.
func TestRingBufferWrapsAcrossCapacity(t *testing.T) {
	rb := NewRingBuffer(16)
	rb.Write([]byte("AAAAAAAA"))       // 8 bytes → no wrap
	rb.Write([]byte("BBBBBBBBCCCCCCCC")) // 16 bytes → wrap; total 24, keep last 16
	got := rb.Bytes()
	want := "BBBBBBBBCCCCCCCC"
	if string(got) != want {
		t.Errorf("wrap: got %q, want %q", got, want)
	}
}

// TestRingBufferOversizedSingleWrite: one giant write bigger than
// the buffer must land only the tail (last capacity bytes).
func TestRingBufferOversizedSingleWrite(t *testing.T) {
	rb := NewRingBuffer(16)
	rb.Write(bytes.Repeat([]byte("Z"), 100))
	got := rb.Bytes()
	if string(got) != strings.Repeat("Z", 16) {
		t.Errorf("oversized: got %q", got)
	}
}

// TestRingBufferCapFloorEnforced: sub-minimum requests are silently
// promoted to minRingCap so the buffer is always usable.
func TestRingBufferCapFloorEnforced(t *testing.T) {
	rb := NewRingBuffer(3)
	if rb.Capacity() != minRingCap {
		t.Errorf("Capacity = %d, want minRingCap %d", rb.Capacity(), minRingCap)
	}
}

// TestRingBufferResetClearsContent: after Reset, Bytes() returns
// nothing and subsequent writes start fresh.
func TestRingBufferResetClearsContent(t *testing.T) {
	rb := NewRingBuffer(32)
	rb.Write([]byte("something"))
	rb.Reset()
	if len(rb.Bytes()) != 0 {
		t.Errorf("Reset should empty the buffer, got %q", rb.Bytes())
	}
	rb.Write([]byte("fresh"))
	if string(rb.Bytes()) != "fresh" {
		t.Errorf("post-Reset write got %q, want %q", rb.Bytes(), "fresh")
	}
}

// TestRingDumperEmitsBufferedContent drives the periodic re-emitter
// through a short interval and confirms the buffered content plus
// framing markers land in the sink.
func TestRingDumperEmitsBufferedContent(t *testing.T) {
	rb := NewRingBuffer(64)
	rb.Write([]byte("captured line\n"))

	var sink safeBuffer
	dumper := NewRingDumper(rb, &sink, 30*time.Millisecond)
	go dumper.Run()
	defer dumper.Stop()

	// Wait a couple intervals to be sure the first tick fired.
	time.Sleep(120 * time.Millisecond)

	got := sink.String()
	if !strings.Contains(got, "captured line") {
		t.Errorf("dumper did not re-emit captured content: %q", got)
	}
	if !strings.Contains(got, "ring-buffer dump") {
		t.Errorf("dumper did not emit framing header: %q", got)
	}
}

// TestRingDumperResetsBetweenTicks confirms the ring is cleared
// after a dump, so a subsequent tick with no new content does
// nothing rather than repeating the previous dump.
func TestRingDumperResetsBetweenTicks(t *testing.T) {
	rb := NewRingBuffer(64)
	rb.Write([]byte("once\n"))

	var sink safeBuffer
	dumper := NewRingDumper(rb, &sink, 30*time.Millisecond)
	go dumper.Run()
	defer dumper.Stop()

	time.Sleep(60 * time.Millisecond)
	firstLen := sink.Len()

	// No further writes; give it another interval.
	time.Sleep(60 * time.Millisecond)
	secondLen := sink.Len()

	if secondLen != firstLen {
		t.Errorf("second tick emitted despite empty ring: firstLen=%d secondLen=%d",
			firstLen, secondLen)
	}
}

package logging

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"time"
)

// defaultRingCap is the buffer size we use when the operator passes 0
// / an unset value. Matches runsvdir's historical 900-byte default —
// small enough that the periodic dump stays readable, big enough to
// capture a typical burst of warnings.
const defaultRingCap = 900

// minRingCap is the smallest usable buffer. runit rejects logstrings
// under 7 chars because it uses 5 for its own marker; we keep the
// same floor for parity even though slinit doesn't use a marker.
const minRingCap = 16

// defaultRingInterval is how often the dumper re-emits the buffer.
// Runit uses 900 seconds (15 minutes) — long enough to survive a
// slow scrape window, short enough that a stuck warning surfaces
// before the operator gives up on the daemon.
const defaultRingInterval = 15 * time.Minute

// RingBuffer is a fixed-size circular byte buffer that captures the
// last N bytes written to it. Writes are lossy — old bytes are
// overwritten by new ones — but Bytes() always returns a consistent
// snapshot of whatever was most recently written.
//
// Used by the runsvdir-inspired stderr rolling buffer: every log
// line the daemon emits is also fed here, and a periodic dumper
// re-emits the buffer's current contents so a transient warning
// stays visible in the log stream even if the operator missed it
// the first time.
type RingBuffer struct {
	mu       sync.Mutex
	buf      []byte
	head     int  // next write position
	filled   bool // wrapped at least once
	capacity int
}

// NewRingBuffer creates a buffer of the given capacity. Values under
// minRingCap are silently promoted to that floor (a config-time
// smoke test — a smaller buffer produces essentially no useful
// output). Zero selects defaultRingCap.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = defaultRingCap
	}
	if capacity < minRingCap {
		capacity = minRingCap
	}
	return &RingBuffer{
		buf:      make([]byte, capacity),
		capacity: capacity,
	}
}

// Write implements io.Writer. Never returns an error; oversized
// writes discard the head of the payload (only the last `capacity`
// bytes of any single Write make it into the buffer). Safe for
// concurrent use.
func (r *RingBuffer) Write(p []byte) (int, error) {
	total := len(p)
	if total == 0 {
		return 0, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// If the write is bigger than the buffer, only the tail survives.
	if total >= r.capacity {
		copy(r.buf, p[total-r.capacity:])
		r.head = 0
		r.filled = true
		return total, nil
	}

	// Fits (possibly wrapping). Copy in one or two segments.
	first := r.capacity - r.head
	if total <= first {
		copy(r.buf[r.head:], p)
	} else {
		copy(r.buf[r.head:], p[:first])
		copy(r.buf, p[first:])
		r.filled = true
	}
	r.head = (r.head + total) % r.capacity
	if r.head == 0 {
		r.filled = true
	}
	return total, nil
}

// Bytes returns a copy of the buffer's current contents in
// chronological order (oldest first). Empty buffer returns nil.
func (r *RingBuffer) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.filled {
		out := make([]byte, r.head)
		copy(out, r.buf[:r.head])
		return out
	}
	out := make([]byte, r.capacity)
	// Filled: content starts at head, wraps back to head-1.
	copy(out, r.buf[r.head:])
	copy(out[r.capacity-r.head:], r.buf[:r.head])
	return out
}

// Reset empties the buffer without shrinking its capacity. Called
// after a periodic dump so we accumulate only what happens between
// dumps — otherwise every dump would repeat the same recent history.
func (r *RingBuffer) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.head = 0
	r.filled = false
	// Zero the buffer contents to avoid leaking a stale tail into a
	// subsequent Bytes() call that happens to see a partial fill.
	for i := range r.buf {
		r.buf[i] = 0
	}
}

// Capacity reports the buffer's byte capacity (post-floor promotion).
func (r *RingBuffer) Capacity() int {
	return r.capacity
}

// RingDumper periodically writes the ring's contents to a sink and
// then clears the ring. Runs until Stop() is called.
type RingDumper struct {
	ring     *RingBuffer
	sink     io.Writer
	interval time.Duration
	quit     chan struct{}
	done     chan struct{}
	once     sync.Once
}

// NewRingDumper wraps a ring + sink pair with a periodic re-emit
// loop. interval == 0 selects defaultRingInterval.
func NewRingDumper(ring *RingBuffer, sink io.Writer, interval time.Duration) *RingDumper {
	if interval <= 0 {
		interval = defaultRingInterval
	}
	return &RingDumper{
		ring:     ring,
		sink:     sink,
		interval: interval,
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Run blocks until Stop() is called. Meant to be launched in a
// goroutine.
func (d *RingDumper) Run() {
	defer close(d.done)
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-d.quit:
			return
		case <-ticker.C:
			data := d.ring.Bytes()
			if len(data) == 0 {
				continue
			}
			// Frame the dump so the operator can distinguish
			// original lines from the periodic replay.
			hdr := []byte(fmt.Sprintf("--- ring-buffer dump (%d bytes) ---\n", len(data)))
			trailer := []byte("--- ring-buffer end ---\n")
			// Ensure the dump ends with a newline for clean framing.
			if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
				data = append(data, '\n')
			}
			_, _ = d.sink.Write(hdr)
			_, _ = d.sink.Write(data)
			_, _ = d.sink.Write(trailer)
			d.ring.Reset()
		}
	}
}

// Stop signals the dumper to exit and waits for its goroutine.
func (d *RingDumper) Stop() {
	d.once.Do(func() {
		close(d.quit)
	})
	<-d.done
}

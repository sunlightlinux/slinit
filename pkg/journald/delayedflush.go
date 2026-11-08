package journald

import (
	"sync"
	"time"
)

// DefaultFlushDelay is the longest an event waits for an fsync. Counting events
// alone leaves the last few of a quiet system unsynced for as long as it stays
// quiet, and the last few before a crash are the ones wanted.
const DefaultFlushDelay = time.Second

// DelayedFlush runs flush once, delay after the first Note since the last run.
// An idle system arms nothing, so it wakes nothing and writes nothing.
type DelayedFlush struct {
	delay time.Duration
	flush func()

	mu      sync.Mutex
	pending bool
	stopped bool
	timer   *time.Timer
}

// NewDelayedFlush returns a DelayedFlush with nothing pending.
func NewDelayedFlush(delay time.Duration, flush func()) *DelayedFlush {
	return &DelayedFlush{delay: delay, flush: flush}
}

// Note says that something was written.
func (d *DelayedFlush) Note() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pending || d.stopped {
		return
	}
	d.pending = true
	d.timer = time.AfterFunc(d.delay, d.fire)
}

func (d *DelayedFlush) fire() {
	d.mu.Lock()
	d.pending = false
	stopped := d.stopped
	d.mu.Unlock()
	// Cleared first: what is written during the flush may miss it, and
	// needs a run of its own.
	if !stopped {
		d.flush()
	}
}

// Stop cancels what is pending and rules out more. Closing the sink is what
// syncs the last events.
func (d *DelayedFlush) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	if d.timer != nil {
		d.timer.Stop()
	}
}

package journal

import (
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// monoBase pairs one CLOCK_MONOTONIC reading with the time.Time taken
// at the same instant. Later readings add the runtime's monotonic
// elapsed time to it: Go's monotonic clock on Linux is CLOCK_MONOTONIC
// itself (runtime nanotime1 and time.now pass clock id 1 to the vDSO
// clock_gettime), so the sum is CLOCK_MONOTONIC without a clock_gettime
// system call per event. The only error is the base's own: half the
// gap between the two raw readings that bracket it.
var monoBase struct {
	once sync.Once
	t    time.Time
	ns   int64
}

func rawMonotonicNanos() int64 {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		// Fallback: fall back to wall clock. Never returns error on
		// Linux for CLOCK_MONOTONIC, but be defensive so the caller
		// never observes a zero Mts.
		return time.Now().UnixNano()
	}
	return ts.Nano()
}

// monotonicAt returns the CLOCK_MONOTONIC value in nanoseconds at t,
// which must carry a monotonic reading (any time.Now() does). Used
// by Event.Now to populate Mts, which is stable across wall-clock
// jumps (NTP correction, manual date -s) and is the timestamp that
// tools like slinit-journalctl use for ordering events within a boot.
//
// Split into its own file so a future BSD/Darwin build can stub it
// with t.UnixNano() (which is close enough for non-init
// contexts) without touching the main event.go.
func monotonicAt(t time.Time) int64 {
	monoBase.once.Do(func() {
		before := rawMonotonicNanos()
		monoBase.t = time.Now()
		after := rawMonotonicNanos()
		monoBase.ns = before + (after-before)/2
	})
	return monoBase.ns + int64(t.Sub(monoBase.t))
}

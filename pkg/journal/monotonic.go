package journal

import (
	"time"

	"golang.org/x/sys/unix"
)

// monotonicNanos returns the CLOCK_MONOTONIC value in nanoseconds. Used
// by Event.Now to populate Mts, which is stable across wall-clock
// jumps (NTP correction, manual date -s) and is the timestamp that
// tools like slinit-journalctl use for ordering events within a boot.
//
// Split into its own file so a future BSD/Darwin build can stub it
// with time.Now().UnixNano() (which is close enough for non-init
// contexts) without touching the main event.go.
func monotonicNanos() int64 {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		// Fallback: fall back to wall clock. Never returns error on
		// Linux for CLOCK_MONOTONIC, but be defensive so the caller
		// never observes a zero Mts.
		return time.Now().UnixNano()
	}
	return ts.Sec*int64(time.Second) + ts.Nano()
}

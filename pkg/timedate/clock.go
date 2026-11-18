package timedate

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// SetSystemTime sets CLOCK_REALTIME; it needs CAP_SYS_TIME.
func SetSystemTime(t time.Time) error {
	ts := unix.NsecToTimespec(t.UnixNano())
	if err := unix.ClockSettime(unix.CLOCK_REALTIME, &ts); err != nil {
		return fmt.Errorf("clock_settime: %w", err)
	}
	return nil
}

// NTPSynchronized reports whether the kernel considers the system clock
// synchronized, as systemd-timedated decides it: the maximum error adjtimex
// reports is below its 16 s ceiling. STA_UNSYNC is not consulted, since it
// may be set only to keep the kernel from writing the RTC.
func NTPSynchronized() bool {
	var tx unix.Timex
	if _, err := unix.Adjtimex(&tx); err != nil {
		return false
	}
	return tx.Maxerror < 16000000
}

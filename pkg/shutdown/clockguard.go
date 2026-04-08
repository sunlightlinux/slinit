// clockguard.go implements boot-time clock protection for slinit.
//
// Problem: systems without a working RTC (or with a dead CMOS battery) may
// boot with the clock set to the Unix epoch (1970-01-01) or another stale
// date.  This causes certificate validation failures, wrong log timestamps,
// and general confusion.
//
// Solution (same approach as systemd-timesyncd):
//  1. A compile-time floor prevents the clock from ever being older than the
//     build date of the binary.
//  2. A persistent timestamp file (/var/lib/slinit/clock) records the last
//     known-good time.  At boot the clock is advanced to whichever is newer:
//     the floor or the file.
//  3. At shutdown (and optionally periodically) the file is updated so the
//     next boot starts from a reasonable baseline.
package shutdown

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"golang.org/x/sys/unix"
)

const (
	// clockTimestampPath is the persistent file that stores the last
	// known-good Unix timestamp (seconds since epoch, ASCII).
	clockTimestampPath = "/var/lib/slinit/clock"

	// clockFloorYear is the absolute minimum year we consider valid.
	// Any system clock before this is definitely wrong.
	clockFloorYear = 2024
)

// clockFloor is the compile-time minimum timestamp.
// Initialised once at package init; the actual binary build date could be
// injected via ldflags, but a fixed known-good date is simpler and
// sufficient (the timestamp file provides the real precision).
var clockFloor = time.Date(clockFloorYear, 1, 1, 0, 0, 0, 0, time.UTC)

// settimeofdayFunc is mockable for tests.
var settimeofdayFunc = settimeofday

// ClockGuard checks the system clock at boot and advances it if it appears
// to be in the past.  It returns the adjustment made (zero if none).
//
// The algorithm:
//   - Determine the "minimum acceptable time" = max(clockFloor, timestamp-file)
//   - If now < minimum, set the clock to minimum and log a warning.
//   - Otherwise do nothing.
//
// This should be called early in InitPID1, after /proc is mounted but before
// any service is started.
func ClockGuard(logger *logging.Logger) time.Duration {
	now := time.Now()
	minimum := clockFloor

	// Read the persistent timestamp file
	if ts, err := readClockTimestamp(); err == nil {
		if ts.After(minimum) {
			minimum = ts
		}
	} else {
		logger.Debug("Clock timestamp file: %v", err)
	}

	if now.Before(minimum) {
		delta := minimum.Sub(now)
		logger.Warn("System clock is in the past (%s), advancing to %s (delta: %v)",
			now.Format(time.RFC3339), minimum.Format(time.RFC3339), delta)

		if err := setSystemClock(minimum); err != nil {
			logger.Error("Failed to set system clock: %v", err)
			return 0
		}
		logger.Notice("System clock corrected to %s", minimum.Format(time.RFC3339))

		// Update the timestamp file immediately after correction
		if err := WriteClockTimestamp(); err != nil {
			logger.Debug("Failed to update clock timestamp: %v", err)
		}
		return delta
	}

	logger.Debug("System clock OK: %s (floor: %s)",
		now.Format(time.RFC3339), minimum.Format(time.RFC3339))
	return 0
}

// WriteClockTimestamp persists the current time to the timestamp file.
// Called at shutdown and optionally at periodic intervals.
func WriteClockTimestamp() error {
	dir := filepath.Dir(clockTimestampPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	// Write atomically: temp file + rename
	tmp := clockTimestampPath + ".tmp"
	content := fmt.Sprintf("%d\n", time.Now().Unix())

	if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, clockTimestampPath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename %s → %s: %w", tmp, clockTimestampPath, err)
	}
	return nil
}

// readClockTimestamp reads the persistent timestamp file and returns the
// stored time.  Returns an error if the file doesn't exist or is malformed.
func readClockTimestamp() (time.Time, error) {
	data, err := os.ReadFile(clockTimestampPath)
	if err != nil {
		return time.Time{}, err
	}

	s := strings.TrimSpace(string(data))
	if s == "" {
		return time.Time{}, fmt.Errorf("empty timestamp file")
	}

	epoch, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timestamp %q: %w", s, err)
	}

	if epoch <= 0 {
		return time.Time{}, fmt.Errorf("timestamp %d is not positive", epoch)
	}

	return time.Unix(epoch, 0), nil
}

// setSystemClock sets the system clock to the given time using settimeofday(2).
func setSystemClock(t time.Time) error {
	return settimeofdayFunc(t)
}

// settimeofday is the real implementation using the unix package.
func settimeofday(t time.Time) error {
	tv := unix.Timeval{
		Sec:  t.Unix(),
		Usec: int64(t.Nanosecond() / 1000),
	}
	return unix.Settimeofday(&tv)
}

// rtc.go — /dev/rtc time read + /etc/adjtime parsing/writing.
//
// The RTC keeps its own time independent from the system clock. Its
// storage convention (UTC vs local) is recorded in /etc/adjtime,
// line 3, using the exact tokens `UTC` or `LOCAL` that hwclock(8)
// writes. When the file is absent, the kernel default is UTC — same
// assumption systemd's timedated makes.

package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Paths overridable in tests.
var (
	rtcDevice   = "/dev/rtc"
	adjtimePath = "/etc/adjtime"
)

// readRTCTime reads /dev/rtc via RTC_RD_TIME. The kernel returns a
// naked struct rtc_time (broken-down calendar fields with no
// timezone); interpret those fields according to the /etc/adjtime
// LOCAL/UTC flag when converting back to a wall-clock instant.
//
// Returns (nil, err) on any failure — callers treat that as "RTC
// unavailable" and drop the field from the status output rather
// than propagating the error.
func readRTCTime(inLocalTZ bool) (*time.Time, error) {
	f, err := os.Open(rtcDevice)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rt, err := unix.IoctlGetRTCTime(int(f.Fd()))
	if err != nil {
		return nil, err
	}
	loc := time.UTC
	if inLocalTZ {
		loc = time.Local
	}
	// struct rtc_time uses "years since 1900" and "months 0..11",
	// matching struct tm; add 1 back for the month.
	t := time.Date(
		int(rt.Year)+1900, time.Month(int(rt.Mon)+1), int(rt.Mday),
		int(rt.Hour), int(rt.Min), int(rt.Sec), 0,
		loc)
	return &t, nil
}

// rtcInLocal returns true when /etc/adjtime pins the RTC to local
// time. Missing file or missing/unknown line 3 → false (UTC default).
func rtcInLocal() bool {
	mode, _ := readAdjtimeMode()
	return mode == "LOCAL"
}

// readAdjtimeMode returns the RTC storage mode as written on
// /etc/adjtime line 3 — "UTC" or "LOCAL" — plus any error worth
// surfacing (I/O failure other than "missing"). Missing file is not
// an error; it yields ("UTC", nil) to match the kernel default.
func readAdjtimeMode() (string, error) {
	f, err := os.Open(adjtimePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "UTC", nil
		}
		return "", err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	var line3 string
	for i := 0; s.Scan() && i < 3; i++ {
		if i == 2 {
			line3 = strings.TrimSpace(s.Text())
		}
	}
	if err := s.Err(); err != nil {
		return "", err
	}
	switch strings.ToUpper(line3) {
	case "LOCAL":
		return "LOCAL", nil
	default:
		return "UTC", nil
	}
}

// writeAdjtimeMode rewrites the storage-mode line (line 3) of
// /etc/adjtime while preserving the drift factor + last-adjust
// timestamp on lines 1-2. Creates a fresh file with zeroed drift
// values when adjtime is missing.
func writeAdjtimeMode(mode string) error {
	mode = strings.ToUpper(mode)
	if mode != "UTC" && mode != "LOCAL" {
		return fmt.Errorf("invalid RTC mode %q (want UTC or LOCAL)", mode)
	}

	// Read existing lines 1-2 if the file exists; else start fresh
	// with hwclock's canonical zeroed drift record.
	line1 := "0.000000 0 0.000000"
	line2 := "0"
	if f, err := os.Open(adjtimePath); err == nil {
		s := bufio.NewScanner(f)
		if s.Scan() {
			if got := strings.TrimSpace(s.Text()); got != "" {
				line1 = got
			}
		}
		if s.Scan() {
			if got := strings.TrimSpace(s.Text()); got != "" {
				line2 = got
			}
		}
		f.Close()
	} else if !os.IsNotExist(err) {
		return err
	}

	body := line1 + "\n" + line2 + "\n" + mode + "\n"
	return writeAtomic(adjtimePath, []byte(body), 0o644)
}

// writeAtomic writes buf via tmp+rename in the same dir so partial
// files never appear to readers. Duplicated here from set.go to keep
// each file self-contained for testing; the two implementations are
// intentionally identical.
func writeAtomic(path string, buf []byte, mode os.FileMode) error {
	dir := "."
	if idx := strings.LastIndex(path, "/"); idx > 0 {
		dir = path[:idx]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".timedatectl.")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

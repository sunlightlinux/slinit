package main

import (
	"testing"
	"time"
)

// TestFormatUptime covers the boundary where boot-time's millisecond
// precision stops being useful — a soft-rebooting box can be up for
// months, and the seconds form says nothing at that scale.
func TestFormatUptime(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{22180 * time.Millisecond, "22.180s"}, // short: defers to formatDuration
		{45 * time.Second, "45.000s"},
		{90 * time.Second, "1m"},
		{59*time.Minute + 59*time.Second, "59m"},
		{time.Hour + 30*time.Minute, "1h 30m"},
		{25 * time.Hour, "1d 1h 0m"},
		{183 * 24 * time.Hour, "183d 0h 0m"},
	}
	for _, c := range cases {
		if got := formatUptime(c.in); got != c.want {
			t.Errorf("formatUptime(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

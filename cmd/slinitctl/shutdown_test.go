package main

import (
	"testing"
	"time"
)

func TestParseShutdownTimeNow(t *testing.T) {
	d, err := parseShutdownTime("now")
	if err != nil {
		t.Fatalf("parseShutdownTime(now): %v", err)
	}
	if d != 0 {
		t.Errorf("d = %v, want 0", d)
	}
}

func TestParseShutdownTimeEmpty(t *testing.T) {
	d, err := parseShutdownTime("")
	if err != nil {
		t.Fatalf("parseShutdownTime(): %v", err)
	}
	if d != 0 {
		t.Errorf("d = %v, want 0", d)
	}
}

func TestParseShutdownTimePlusMinutes(t *testing.T) {
	d, err := parseShutdownTime("+5")
	if err != nil {
		t.Fatalf("parseShutdownTime(+5): %v", err)
	}
	if d != 5*time.Minute {
		t.Errorf("d = %v, want 5m", d)
	}
}

func TestParseShutdownTimePlainMinutes(t *testing.T) {
	d, err := parseShutdownTime("10")
	if err != nil {
		t.Fatalf("parseShutdownTime(10): %v", err)
	}
	if d != 10*time.Minute {
		t.Errorf("d = %v, want 10m", d)
	}
}

func TestParseShutdownTimeAbsolute(t *testing.T) {
	// Use a time in the future.
	now := time.Now()
	target := now.Add(30 * time.Minute)
	timeStr := target.Format("15:04")

	d, err := parseShutdownTime(timeStr)
	if err != nil {
		t.Fatalf("parseShutdownTime(%s): %v", timeStr, err)
	}
	// Should be approximately 30 minutes (allow 2 minute tolerance).
	if d < 28*time.Minute || d > 32*time.Minute {
		t.Errorf("d = %v, want ~30m for %s", d, timeStr)
	}
}

func TestParseShutdownTimeAbsolutePast(t *testing.T) {
	// Use a time in the past — should wrap to tomorrow.
	now := time.Now()
	target := now.Add(-30 * time.Minute)
	timeStr := target.Format("15:04")

	d, err := parseShutdownTime(timeStr)
	if err != nil {
		t.Fatalf("parseShutdownTime(%s): %v", timeStr, err)
	}
	// Should be approximately 23h30m.
	if d < 23*time.Hour || d > 24*time.Hour {
		t.Errorf("d = %v, want ~23h30m for past time %s", d, timeStr)
	}
}

func TestParseShutdownTimeInvalid(t *testing.T) {
	bad := []string{"foo", "+abc", "25:00", "12:61", "-5"}
	for _, s := range bad {
		_, err := parseShutdownTime(s)
		if err == nil {
			t.Errorf("parseShutdownTime(%q) should fail", s)
		}
	}
}

func TestParseShutdownType(t *testing.T) {
	valid := map[string]bool{
		"halt": true, "poweroff": true, "reboot": true,
		"kexec": true, "softreboot": true, "soft-reboot": true,
	}
	for s := range valid {
		_, err := parseShutdownType(s)
		if err != nil {
			t.Errorf("parseShutdownType(%q): %v", s, err)
		}
	}

	_, err := parseShutdownType("invalid")
	if err == nil {
		t.Error("parseShutdownType(invalid) should fail")
	}
}

func TestFormatHumanDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{2 * time.Hour, "2h"},
		{90 * time.Minute, "1h30m"},
	}
	for _, tc := range cases {
		got := formatHumanDuration(tc.d)
		if got != tc.want {
			t.Errorf("formatHumanDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

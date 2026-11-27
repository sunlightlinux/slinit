package main

import (
	"errors"
	"net"
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

// A word after the kind that is not a time used to become the wall
// message of an immediate shutdown, so a mistyped time — `reboot
// 10:3O`, `poweroff +5min` — took the machine down at once instead of
// scheduling it. SysV shutdown and slinit-shutdown both read that word
// as the time and refuse it. The connection is closed, so a call that
// gets past the argument check fails on the write instead, which is
// not a usage error.
func TestShutdownRejectsBadTimeAfterKind(t *testing.T) {
	for _, c := range []struct {
		args  []string
		usage bool
	}{
		{[]string{"reboot", "bogus"}, true},
		{[]string{"reboot", "10:3O"}, true},
		{[]string{"poweroff", "+5min"}, true},
		{[]string{"reboot", "--fast", "msg"}, true},
		{[]string{"bogus"}, true},
		// A time first, then the message, still works.
		{[]string{"reboot", "now", "going", "down"}, false},
		{[]string{"reboot", "+5", "maintenance"}, false},
		{[]string{"reboot", "-m", "maintenance"}, false},
		{[]string{"reboot"}, false},
		// -k schedules nothing; its words are only a message.
		{[]string{"-k", "reboot", "maintenance"}, false},
	} {
		a, b := net.Pipe()
		b.Close()
		err := cmdShutdownDispatch(a, append([]string(nil), c.args...))
		a.Close()
		var ue usageError
		if got := errors.As(err, &ue); got != c.usage {
			t.Errorf("shutdown %q: err = %v, usage error = %v, want %v", c.args, err, got, c.usage)
		}
	}
}

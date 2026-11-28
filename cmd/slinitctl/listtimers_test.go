package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestListTimersRows(t *testing.T) {
	now := time.Date(2026, 11, 28, 10, 0, 0, 0, time.Local)
	daily, ok := timerRowFrom("logrotate", "Type=scripted\nOnCalendar=daily\nOnCalendarRandomizedDelay=1h0m0s\n", now)
	if !ok || !daily.next.Equal(time.Date(2026, 11, 29, 0, 0, 0, 0, time.Local)) || daily.delay != "1h0m0s" {
		t.Fatalf("daily: %+v %v", daily, ok)
	}
	hourly, _ := timerRowFrom("fwupd", "OnCalendar=*-*-* *:30:00\n", now)
	if _, ok := timerRowFrom("sshd", "Type=process\n", now); ok {
		t.Error("a service without start-on-calendar listed")
	}
	past, _ := timerRowFrom("once", "OnCalendar=2020-01-01 00:00:00\n", now)
	var b bytes.Buffer
	printTimers(&b, []timerRow{daily, past, hourly}, now)
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "NEXT") {
		t.Fatalf("%q", b.String())
	}
	// By next run: fwupd at 10:30, logrotate at midnight, the past one last.
	for i, want := range []string{"fwupd", "logrotate", "once"} {
		if !strings.Contains(lines[i+1], want) {
			t.Errorf("line %d %q, want %s", i+1, lines[i+1], want)
		}
	}
	if !strings.Contains(lines[1], "30min") || !strings.Contains(lines[2], "14h 0min (+ up to 1h0m0s)") {
		t.Errorf("left: %q / %q", lines[1], lines[2])
	}
	var e bytes.Buffer
	printTimers(&e, nil, now)
	if !strings.Contains(e.String(), "No loaded service") {
		t.Errorf("empty: %q", e.String())
	}
}

func TestRoughDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		40 * time.Second:             "40s",
		5 * time.Minute:              "5min",
		5*time.Hour + 12*time.Minute: "5h 12min",
		50 * time.Hour:               "2d 2h",
	} {
		if got := roughDuration(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}

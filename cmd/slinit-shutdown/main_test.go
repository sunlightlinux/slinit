package main

import (
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// shutdown(8)'s operands: a time, then the words of a message. A later
// time used to be refused; it is scheduled now, so it must parse to the
// delay it names and never to 0 (going down at once when someone asked
// for ten minutes is the one wrong answer).
func TestParseOperands(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.Local)
	for _, c := range []struct {
		in    []string
		delay time.Duration
		msg   string
	}{
		{nil, 0, ""},
		{[]string{"now"}, 0, ""},
		{[]string{"+0"}, 0, ""},
		{[]string{"+5"}, 5 * time.Minute, ""},
		{[]string{"10", "disk", "swap"}, 10 * time.Minute, "disk swap"},
		{[]string{"10:30", "kernel", "update"}, 30 * time.Minute, "kernel update"},
		{[]string{"09:00"}, 23 * time.Hour, ""},
	} {
		d, msg, err := parseOperands(c.in, now)
		if err != nil || d != c.delay || msg != c.msg {
			t.Errorf("%q: %v %q %v, want %v %q", c.in, d, msg, err, c.delay, c.msg)
		}
	}
	for _, bad := range [][]string{{"soon"}, {"+x"}, {"25:00"}} {
		if _, _, err := parseOperands(bad, now); err == nil || !strings.Contains(err.Error(), bad[0]) {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
}

func TestShutdownDashH(t *testing.T) {
	for _, c := range []struct{ from, want service.ShutdownType }{
		{service.ShutdownPoweroff, service.ShutdownPoweroff},
		{service.ShutdownReboot, service.ShutdownPoweroff},
		{service.ShutdownHalt, service.ShutdownHalt},
	} {
		if got := shutdownDashH(c.from); got != c.want {
			t.Errorf("from %v: %v", c.from, got)
		}
	}
}

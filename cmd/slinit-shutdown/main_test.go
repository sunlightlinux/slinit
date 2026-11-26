package main

import (
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
	"github.com/sunlightlinux/slinit/pkg/shutdown"
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
		d, msg, err := parseOperands(c.in, false, now)
		if err != nil || d != c.delay || msg != c.msg {
			t.Errorf("%q: %v %q %v, want %v %q", c.in, d, msg, err, c.delay, c.msg)
		}
	}
	for _, bad := range [][]string{{"soon"}, {"+x"}, {"25:00"}} {
		if _, _, err := parseOperands(bad, false, now); err == nil || !strings.Contains(err.Error(), bad[0]) {
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

// shutdown -k only sends the warning, as in sysvinit and systemd; it
// used to kexec. The other names keep -k as kexec.
func TestDashKUnderShutdownOnlyWarns(t *testing.T) {
	o, err := parseArgs("shutdown", []string{"-k", "+5", "maintenance"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.warnOnly || o.shutdownType == service.ShutdownKexec {
		t.Errorf("shutdown -k: warnOnly=%v type=%v, want a warning and no kexec", o.warnOnly, o.shutdownType)
	}
	for _, name := range []string{"slinit-shutdown", "slinit-reboot", "reboot"} {
		o, err := parseArgs(name, []string{"-k"})
		if err != nil || o.warnOnly || o.shutdownType != service.ShutdownKexec {
			t.Errorf("%s -k: %+v %v, want kexec", name, o, err)
		}
	}
	if s := shutdown.ShutdownNoticeText(service.ShutdownPoweroff, 5*time.Minute, "maintenance"); !strings.Contains(s, "power-off") || !strings.Contains(s, "maintenance") {
		t.Errorf("warning text %q", s)
	}
}

// Options after the time are honoured, as getopt permutes: shutdown
// now -r reboots. Parsing used to stop at the first operand, so the -r
// became part of the message and the machine powered off.
func TestShutdownOptionsAfterOperands(t *testing.T) {
	o, err := parseArgs("shutdown", []string{"now", "-r"})
	if err != nil || o.shutdownType != service.ShutdownReboot || len(o.operands) != 1 || o.operands[0] != "now" {
		t.Errorf("shutdown now -r: %+v %v, want a reboot at now", o, err)
	}
	o, err = parseArgs("shutdown", []string{"+5", "disk", "-H", "swap"})
	if err != nil || o.shutdownType != service.ShutdownHalt || strings.Join(o.operands, " ") != "+5 disk swap" {
		t.Errorf("shutdown +5 disk -H swap: %+v %v", o, err)
	}
	o, err = parseArgs("shutdown", []string{"+5", "--", "-r", "is", "a", "word"})
	if err != nil || o.shutdownType == service.ShutdownReboot || strings.Join(o.operands, " ") != "+5 -r is a word" {
		t.Errorf("-- did not end the options: %+v %v", o, err)
	}
}

// shutdown -c takes a wall message, not a time: "-c reason" used to
// fail parsing "reason" as a time.
func TestCancelOperandsAreTheMessage(t *testing.T) {
	o, err := parseArgs("shutdown", []string{"-c", "false", "alarm"})
	if err != nil || !o.cancel {
		t.Fatalf("%+v %v", o, err)
	}
	d, msg, err := parseOperands(o.operands, o.cancel, time.Now())
	if err != nil || d != 0 || msg != "false alarm" {
		t.Errorf("shutdown -c false alarm: %v %q %v", d, msg, err)
	}
}

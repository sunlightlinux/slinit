package logging

import (
	"bytes"
	"strings"
	"testing"
)

// TestBootConsoleStatusLines verifies that with the boot console enabled,
// service events render as compact bracketed status lines instead of the
// verbose timestamped stream.
func TestBootConsoleStatusLines(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelWarn)
	l.SetOutput(&buf)
	l.SetBootConsole(true, false)

	l.ServiceStarted("udevd")
	l.ServiceFailed("sshd", false)
	l.ServiceStopped("dbus")

	got := buf.String()
	want := "[ OK ] udevd\n[FAIL] sshd\n[ OK ] dbus\n"
	if got != want {
		t.Errorf("boot console output:\n got %q\nwant %q", got, want)
	}
}

// TestBootConsoleShutdownMarkers verifies that once shutdown is signalled,
// service stop events render as "[STOPPD] name" while starts and failures
// keep their boot markers.
func TestBootConsoleShutdownMarkers(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelWarn)
	l.SetOutput(&buf)
	l.SetBootConsole(true, false)
	l.SetShutdownConsole(true)

	l.ServiceStopped("network")
	l.ServiceStopped("sshd")
	l.ServiceFailed("dbus", false)

	got := buf.String()
	want := "[STOPPD] network\n[STOPPD] sshd\n[FAIL] dbus\n"
	if got != want {
		t.Errorf("shutdown console output:\n got %q\nwant %q", got, want)
	}
}

// TestBootConsoleColor verifies the OK/FAIL markers are colored when
// requested, and the service name is still present.
func TestBootConsoleColor(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelWarn)
	l.SetOutput(&buf)
	l.SetBootConsole(true, true)

	l.ServiceStarted("udevd")
	l.ServiceFailed("sshd", true)

	got := buf.String()
	if !strings.Contains(got, ansiGreen+"OK"+ansiReset) {
		t.Errorf("expected green OK marker, got %q", got)
	}
	if !strings.Contains(got, ansiRed+"FAIL"+ansiReset) {
		t.Errorf("expected red FAIL marker, got %q", got)
	}
	if !strings.Contains(got, "udevd") || !strings.Contains(got, "sshd") {
		t.Errorf("expected service names in output, got %q", got)
	}
}

// TestBootConsoleSuppressesChatter verifies that with the boot console on and
// the console level raised to Warn, Info/Notice chatter never reaches the
// console (it would still go to the main log/syslog, which is not wired here).
func TestBootConsoleSuppressesChatter(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelWarn)
	l.SetOutput(&buf)
	l.SetBootConsole(true, false)

	l.Info("Service 'cron-demo': starting cron task")
	l.Notice("Control socket listening on /run/slinit.socket")

	if buf.Len() != 0 {
		t.Errorf("expected no verbose console output, got %q", buf.String())
	}
}

// TestBootConsoleDisabledKeepsVerbose verifies the default (boot console off)
// still emits the verbose "Service '...' started" console line.
func TestBootConsoleDisabledKeepsVerbose(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelInfo)
	l.SetOutput(&buf)
	SetTimestampFormat(TimestampNone)
	defer SetTimestampFormat(TimestampWallclock)

	l.ServiceStarted("udevd")

	got := buf.String()
	if got != "INFO: Service 'udevd' started\n" {
		t.Errorf("verbose console output: got %q", got)
	}
}

package dbusservice

import "testing"

type fakeSyslog struct{ lines []string }

func (f *fakeSyslog) Info(m string) error { f.lines = append(f.lines, m); return nil }

// The daemons are started by D-Bus activation, with dbus-daemon's stderr
// (usually /dev/null) as theirs, so Logf has to reach syslog too or a
// change such as "Changed system time" leaves no trace.
func TestLogfReachesSyslog(t *testing.T) {
	f := &fakeSyslog{}
	s := &Service{Name: "slinit-test", syslog: f}
	s.Logf("Changed time zone to '%s'.", "Europe/Bucharest")
	if len(f.lines) != 1 || f.lines[0] != "Changed time zone to 'Europe/Bucharest'." {
		t.Errorf("syslog got %q", f.lines)
	}
	// Without OpenSyslog nothing is sent anywhere but stderr.
	(&Service{Name: "slinit-test"}).Logf("no syslog")
}

package config

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
	"golang.org/x/sys/unix"
)

func TestMoreRlimitsParsed(t *testing.T) {
	desc, err := Parse(strings.NewReader("type = process\ncommand = /bin/true\n"+
		"rlimit-nproc = 64\nrlimit-cpu = 10:unlimited\nrlimit-nice = 25\n"), "r", "r")
	if err != nil {
		t.Fatal(err)
	}
	want := map[int][2]uint64{
		unix.RLIMIT_NPROC: {64, 64},
		unix.RLIMIT_CPU:   {10, ^uint64(0)},
		unix.RLIMIT_NICE:  {25, 25},
	}
	for r, w := range want {
		if desc.Rlimits[r] != w {
			t.Errorf("resource %d = %v, want %v", r, desc.Rlimits[r], w)
		}
	}
	for _, bad := range []string{"rlimit-nproc = many", "rlimit-stack = 9:8"} {
		if _, err := Parse(strings.NewReader("type = process\ncommand = /bin/true\n"+bad+"\n"), "r", "r"); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}

	// On the record, in resource order; a reload does not keep a limit
	// the description dropped (applyRlimits starts from nothing).
	svc := service.NewProcessService(service.NewServiceSet(&testConsumerLogger{}), "r")
	applyRlimits(svc.Record(), desc)
	show := service.RenderShow(svc)
	for _, line := range []string{"LimitNPROC=64\n", "LimitCPUSoft=10\n", "LimitNICE=25\n"} {
		if !strings.Contains(show, line) {
			t.Errorf("show lacks %q:\n%s", line, show)
		}
	}
	desc.Rlimits = nil
	applyRlimits(svc.Record(), desc)
	if strings.Contains(service.RenderShow(svc), "LimitNPROC") {
		t.Error("a dropped rlimit-nproc stayed on the record")
	}
}

func TestSystemdLimitsMapped(t *testing.T) {
	for _, c := range []struct{ unit, want string }{
		{"LimitNPROC=1", "rlimit-nproc = 1"},
		{"LimitNPROC=infinity", "rlimit-nproc = unlimited"},
		{"LimitNPROC=10:20", "rlimit-nproc = 10:20"},
		{"LimitFSIZE=1G", "rlimit-fsize = 1073741824"},
		{"LimitSTACK=8M:infinity", "rlimit-stack = 8388608:unlimited"},
		{"LimitCPU=90", "rlimit-cpu = 90"},
		{"LimitCPU=1min 30s", "rlimit-cpu = 90"},
		{"LimitCPU=1.5", "rlimit-cpu = 2"},         // rounded up, as systemd does
		{"LimitRTTIME=500", "rlimit-rttime = 500"}, // microseconds
		{"LimitRTTIME=1s", "rlimit-rttime = 1000000"},
		{"LimitNICE=-5", "rlimit-nice = 25"},
		{"LimitNICE=+5", "rlimit-nice = 15"},
		{"LimitNICE=30", "rlimit-nice = 30"},
	} {
		lines, _ := convertService(t, c.unit+"\n")
		out := strings.Join(lines, "\n")
		if !strings.Contains(out, c.want+"\n") {
			t.Errorf("%s: no %q in\n%s", c.unit, c.want, out)
		}
	}
	// An invalid value is dropped with a warning, as systemd ignores it;
	// an empty assignment unsets an earlier one.
	for _, c := range []string{"LimitNPROC=lots", "LimitNICE=+25", "LimitSTACK=9:8", "LimitNPROC=5\nLimitNPROC="} {
		lines, warns := convertService(t, c+"\n")
		out := strings.Join(lines, "\n")
		if strings.Contains(out, "rlimit-") {
			t.Errorf("%q: emitted a limit:\n%s", c, out)
		}
		if !strings.HasSuffix(c, "=") && len(warns) == 0 {
			t.Errorf("%q: dropped without a warning", c)
		}
	}
}

func TestLogTypeJournalLoads(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "p", "type = process\ncommand = /bin/true\nlog-type = journal\n")
	writeServiceFile(t, dir, "s", "type = scripted\ncommand = /bin/true\nlog-type = journal\n")
	ss := service.NewServiceSet(&testConsumerLogger{})
	dl := NewDirLoader(ss, []string{dir})
	ss.SetLoader(dl)
	if _, err := dl.LoadService("p"); err != nil {
		t.Fatalf("log-type = journal on a process service: %v", err)
	}
	if _, err := dl.LoadService("s"); err == nil || !strings.Contains(err.Error(), "only supported for type = process") {
		t.Fatalf("log-type = journal on a scripted service: %v", err)
	}
}

// A converted simple service sends its output to the journal, as
// systemd's default StandardOutput=journal does; one that names another
// destination, or is not a process service, gets no log-type.
func TestSystemdOutputDefaultsToJournal(t *testing.T) {
	for _, c := range []struct {
		body string
		want bool
	}{
		{"", true},
		{"StandardOutput=journal\n", true},
		{"StandardOutput=append:/var/log/x.log\n", false},
		{"StandardOutput=null\n", false},
		{"Type=oneshot\n", false},
		{"Type=forking\n", false},
	} {
		lines, _ := convertService(t, c.body)
		got := strings.Contains(strings.Join(lines, "\n"), "log-type = journal")
		if got != c.want {
			t.Errorf("%q: log-type = journal emitted %v, want %v", c.body, got, c.want)
		}
	}
}

func TestSyslogMetaParsed(t *testing.T) {
	desc, err := Parse(strings.NewReader("type = process\ncommand = /bin/true\n"+
		"syslog-identifier = webd\nsyslog-level = notice\nsyslog-level-prefix = no\n"+
		"log-extra-fields = TEAM=web TIER=1\nlog-extra-fields += ZONE=a\n"), "s", "s")
	if err != nil {
		t.Fatal(err)
	}
	if desc.SyslogIdentifier != "webd" || desc.SyslogLevel != 5 || desc.SyslogLevelPrefix == nil || *desc.SyslogLevelPrefix {
		t.Errorf("got ident %q level %d prefix %v", desc.SyslogIdentifier, desc.SyslogLevel, desc.SyslogLevelPrefix)
	}
	if len(desc.LogExtraFields) != 3 || desc.LogExtraFields["ZONE"] != "a" {
		t.Errorf("extra fields %v", desc.LogExtraFields)
	}
	for _, bad := range []string{"log-extra-fields = _PID=1", "log-extra-fields = lower=x", "log-extra-fields = NOVALUE", "syslog-level = loud"} {
		if _, err := Parse(strings.NewReader("type = process\ncommand = /bin/true\n"+bad+"\n"), "s", "s"); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSystemdSyslogMapped(t *testing.T) {
	for _, c := range []struct{ unit, want string }{
		{"SyslogIdentifier=webd", "syslog-identifier = webd"},
		{"SyslogIdentifier=%N-x", "syslog-identifier = probe-x"},
		{"SyslogLevel=warning", "syslog-level = warning"},
		{"SyslogLevelPrefix=off", "syslog-level-prefix = no"},
		{"LogExtraFields=TEAM=web TIER=1", "log-extra-fields = TEAM=web TIER=1"},
		{"LogExtraFields=TEAM=web\nLogExtraFields=ZONE=a", "log-extra-fields = TEAM=web ZONE=a"},
	} {
		lines, _ := convertService(t, c.unit+"\n")
		if out := strings.Join(lines, "\n"); !strings.Contains(out, c.want+"\n") {
			t.Errorf("%s: no %q in\n%s", c.unit, c.want, out)
		}
	}
	for _, c := range []string{"SyslogLevel=loud", "LogExtraFields=_PID=1", "LogExtraFields=\"NOTE=two words\"",
		"SyslogIdentifier=x\nSyslogIdentifier=", "LogExtraFields=A=1\nLogExtraFields="} {
		lines, _ := convertService(t, c+"\n")
		out := strings.Join(lines, "\n")
		if strings.Contains(out, "syslog-") || strings.Contains(out, "log-extra-fields") {
			t.Errorf("%q: emitted\n%s", c, out)
		}
	}
}

func TestSystemdNotifyMapped(t *testing.T) {
	for _, c := range []struct {
		body     string
		want     []string
		absent   []string
		gapNoted bool
	}{
		{"Type=notify\n", []string{"ready-notification = sd-notify"}, nil, false},
		{"Type=notify-reload\n", []string{"ready-notification = sd-notify"}, nil, false},
		{"Type=notify\nWatchdogSec=30\n", []string{"ready-notification = sd-notify", "watchdog-timeout = 30"}, nil, false},
		// Type= after WatchdogSec=, as it may be in a unit or a drop-in.
		{"WatchdogSec=1min\nType=notify\n", []string{"watchdog-timeout = 60"}, nil, false},
		{"Type=simple\nWatchdogSec=30\n", nil, []string{"watchdog-timeout", "ready-notification"}, true},
		{"Type=notify\nNotifyAccess=none\n", nil, []string{"ready-notification"}, false},
		{"Type=notify\nWatchdogSec=0\n", []string{"ready-notification = sd-notify"}, []string{"watchdog-timeout"}, false},
	} {
		lines, warns := convertService(t, c.body)
		out := strings.Join(lines, "\n")
		for _, w := range c.want {
			if !strings.Contains(out, w+"\n") {
				t.Errorf("%q: no %q in\n%s", c.body, w, out)
			}
		}
		for _, a := range c.absent {
			if strings.Contains(out, a) {
				t.Errorf("%q: %q emitted:\n%s", c.body, a, out)
			}
		}
		noted := false
		for _, w := range warns {
			noted = noted || strings.Contains(w.Msg, "WatchdogSec")
		}
		if noted != c.gapNoted {
			t.Errorf("%q: WatchdogSec warning %v, want %v: %v", c.body, noted, c.gapNoted, warns)
		}
	}

	// Loaded directly: the watchdog needs the readiness channel, which
	// sd-notify is, so the unit loads.
	root := t.TempDir()
	units := root + "/units"
	writeUnit(t, units, "nd.service", "[Service]\nType=notify\nWatchdogSec=10\nExecStart=/usr/bin/nd\n")
	ss := service.NewServiceSet(&testConsumerLogger{})
	dl := NewDirLoader(ss, []string{root + "/slinit.d"})
	dl.SetSystemdDirs([]string{units})
	ss.SetLoader(dl)
	if _, err := dl.LoadService("nd"); err != nil {
		t.Fatalf("Type=notify + WatchdogSec= does not load: %v", err)
	}
}

func TestCoredumpFilterValidatedAtLoad(t *testing.T) {
	for v, ok := range map[string]bool{"0x33": true, "51": true, "0": true, "0xzz": false, "lots": false, "": false} {
		_, err := Parse(strings.NewReader("type = process\ncommand = /bin/true\ncoredump-filter = "+v+"\n"), "c", "c")
		if (err == nil) != ok {
			t.Errorf("coredump-filter = %q: err %v, want ok=%v", v, err, ok)
		}
	}
}

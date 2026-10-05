package config

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// `logfile` and `output-logger` each claimed log-type only while it was
// still unset, so the destination a service got depended on which
// directive appeared FIRST, and the other one was parsed, stored, and
// then silently discarded. Order must not decide, and neither may be
// dropped.
func TestLogfileAndOutputLoggerComposeRegardlessOfOrder(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"logfile first", "type = process\ncommand = /bin/true\n" +
			"logfile = /var/log/x.log\noutput-logger = /usr/bin/svlogd /var/log/x\n"},
		{"output-logger first", "type = process\ncommand = /bin/true\n" +
			"output-logger = /usr/bin/svlogd /var/log/x\nlogfile = /var/log/x.log\n"},
	}
	for _, tc := range cases {
		desc, err := Parse(strings.NewReader(tc.body), "svc", "svc")
		if err != nil {
			t.Fatalf("%s: parse: %v", tc.name, err)
		}
		if desc.LogType != service.LogToFile {
			t.Errorf("%s: LogType = %v, want LogToFile (the pipeline that can "+
				"carry a second destination)", tc.name, desc.LogType)
		}
		if desc.LogFile == "" {
			t.Errorf("%s: logfile was dropped", tc.name)
		}
		if len(desc.OutputLogger) == 0 {
			t.Errorf("%s: output-logger was dropped", tc.name)
		}
	}
}

// An operator who writes log-type explicitly means it, even next to a
// stale directive for the other destination.
func TestExplicitLogTypeIsNotOverriddenByComposition(t *testing.T) {
	body := "type = process\ncommand = /bin/true\n" +
		"log-type = command\nlogfile = /var/log/x.log\n" +
		"output-logger = /usr/bin/svlogd /var/log/x\n"
	desc, err := Parse(strings.NewReader(body), "svc", "svc")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.LogType != service.LogToCommand {
		t.Errorf("LogType = %v, want LogToCommand — an explicit log-type must win",
			desc.LogType)
	}
}

// Each directive on its own keeps selecting its own destination.
func TestSingleLogDestinationUnchanged(t *testing.T) {
	fileOnly, err := Parse(strings.NewReader(
		"type = process\ncommand = /bin/true\nlogfile = /var/log/x.log\n"), "a", "a")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if fileOnly.LogType != service.LogToFile {
		t.Errorf("logfile alone: LogType = %v, want LogToFile", fileOnly.LogType)
	}

	cmdOnly, err := Parse(strings.NewReader(
		"type = process\ncommand = /bin/true\noutput-logger = /usr/bin/cat\n"), "b", "b")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cmdOnly.LogType != service.LogToCommand {
		t.Errorf("output-logger alone: LogType = %v, want LogToCommand", cmdOnly.LogType)
	}
}

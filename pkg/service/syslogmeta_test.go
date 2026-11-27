package service

import (
	"testing"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// The journal metadata of output lines: the level from "<N>" (stripped
// from the text, as systemd strips it), a keyword, or syslog-level; the
// prefix left alone with syslog-level-prefix = no; the identifier; extra
// fields beside the producing pid.
func TestSyslogMetaOnOutputLines(t *testing.T) {
	events := cronJournal(t)
	feed := func(name string, cfg LogRotatorConfig, lines ...string) []*journal.Event {
		cfg.ServiceName = name
		cfg.GetPID = func() int { return 4242 }
		lr, err := NewLogRotator(cfg)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range lines {
			lr.processLine([]byte(l + "\n"))
		}
		return events(name)
	}

	got := feed("plain", LogRotatorConfig{DefaultLevel: -1, LogLevelMax: -1}, "<3>boom", "hello", "ERROR: bad")
	want := []struct {
		msg  string
		prio journal.Priority
	}{{"boom", 3}, {"hello", 6}, {"ERROR: bad", 3}}
	if len(got) != len(want) {
		t.Fatalf("plain: %d events, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Msg != w.msg || got[i].Prio != w.prio {
			t.Errorf("plain line %d: %q prio %d, want %q prio %d", i, got[i].Msg, got[i].Prio, w.msg, w.prio)
		}
	}

	got = feed("leveled", LogRotatorConfig{DefaultLevel: 5, LogLevelMax: -1}, "hello", "ERROR: bad")
	if got[0].Prio != 5 || got[1].Prio != 3 {
		t.Errorf("syslog-level = notice: prios %d %d, want 5 3", got[0].Prio, got[1].Prio)
	}

	got = feed("noprefix", LogRotatorConfig{DefaultLevel: 4, NoLevelPrefix: true, LogLevelMax: -1}, "<3>boom")
	if got[0].Msg != "<3>boom" || got[0].Prio != 4 {
		t.Errorf("syslog-level-prefix = no: %q prio %d, want \"<3>boom\" prio 4", got[0].Msg, got[0].Prio)
	}

	got = feed("tagged", LogRotatorConfig{DefaultLevel: -1, LogLevelMax: -1, SyslogIdent: "webd",
		ExtraFields: map[string]string{"TEAM": "web"}}, "hello")
	if got[0].SyslogIdentifier != "webd" {
		t.Errorf("identifier %q, want webd", got[0].SyslogIdentifier)
	}
	if got[0].Fields["TEAM"] != "web" || got[0].Fields["SLINIT_TARGET_PID"] != "4242" {
		t.Errorf("fields %v, want TEAM=web and SLINIT_TARGET_PID=4242", got[0].Fields)
	}

	// log-level-max judges the same level the journal records.
	got = feed("gated", LogRotatorConfig{DefaultLevel: 7, LogLevelMax: 6}, "chatty", "<6>kept")
	if len(got) != 1 || got[0].Msg != "kept" {
		t.Errorf("log-level-max = info with syslog-level = debug: %v, want only \"kept\"", got)
	}
}

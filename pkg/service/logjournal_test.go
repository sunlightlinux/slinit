package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// journalMsgs waits until the journal holds want lines of unit's output,
// and returns them.
func journalMsgs(t *testing.T, events func(string) []*journal.Event, unit string, want int) []string {
	t.Helper()
	var out []string
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		out = out[:0]
		for _, e := range events(unit) {
			if e.Transport == journal.TransportStdout {
				out = append(out, e.Msg)
			}
		}
		if len(out) >= want {
			break
		}
	}
	return out
}

// log-type = journal: a service's output reaches the journal, with no
// logfile. Before it, output reached the journal only through a
// logfile's rotator, so a service without one (every systemd unit loaded
// directly: StandardOutput=journal is systemd's default) wrote to
// /dev/null.
func TestLogTypeJournal(t *testing.T) {
	events := cronJournal(t)
	set, _ := newTestSet()
	svc := NewProcessService(set, "jsvc")
	svc.SetCommand([]string{"/bin/sh", "-c", "echo first; echo second >&2; exec sleep 5"})
	svc.SetLogType(LogToJournal)
	set.AddService(svc)
	set.StartService(svc)
	defer func() {
		set.StopService(svc)
		waitForState(t, svc, StateStopped, 5*time.Second)
	}()
	got := journalMsgs(t, events, "jsvc", 2)
	if strings.Join(got, "|") != "first|second" && strings.Join(got, "|") != "second|first" {
		t.Fatalf("journal holds %q, want the service's stdout and stderr lines", got)
	}
}

// With a logfile as well, the lines go to both.
func TestLogTypeJournalWithLogfile(t *testing.T) {
	events := cronJournal(t)
	set, _ := newTestSet()
	logPath := filepath.Join(t.TempDir(), "out.log")
	svc := NewProcessService(set, "jboth")
	svc.SetCommand([]string{"/bin/sh", "-c", "echo hello; exec sleep 5"})
	svc.SetLogType(LogToJournal)
	svc.SetLogFileDetails(logPath, 0o600, -1, -1)
	set.AddService(svc)
	set.StartService(svc)
	defer func() {
		set.StopService(svc)
		waitForState(t, svc, StateStopped, 5*time.Second)
	}()
	if got := journalMsgs(t, events, "jboth", 1); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("journal holds %q, want [hello]", got)
	}
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		b, _ := os.ReadFile(logPath)
		if string(b) == "hello\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("logfile holds %q, want \"hello\\n\"", b)
		}
	}
}

package service

import (
	"path/filepath"
	"testing"
)

// The state event's SLINIT_TARGET_PID came from looking the record's
// name up in the ServiceSet under its lock; it now asks the record's own
// Service. It must still carry the running process's pid, and no field
// for a service without one.
func TestJournalStateEventTargetPID(t *testing.T) {
	events := cronJournal(t)
	set, _ := newTestSet()
	s := NewProcessService(set, "sshd")
	set.AddService(s)
	internal := NewInternalService(set, "boot")
	set.AddService(internal)

	s.pid = 4321
	s.pidPublished.Store(4321)
	s.Record().emitJournalStateEvent(EventStarted)
	internal.Record().emitJournalStateEvent(EventStarted)

	got := events("sshd")
	if len(got) != 1 {
		t.Fatalf("sshd: %d events, want 1", len(got))
	}
	if pid := got[0].Fields["SLINIT_TARGET_PID"]; pid != "4321" {
		t.Errorf("sshd SLINIT_TARGET_PID = %q, want 4321", pid)
	}
	if ev := got[0].Fields["SLINIT_EVENT"]; ev != "STARTED" {
		t.Errorf("sshd SLINIT_EVENT = %q", ev)
	}
	got = events("boot")
	if len(got) != 1 {
		t.Fatalf("boot: %d events, want 1", len(got))
	}
	if pid, ok := got[0].Fields["SLINIT_TARGET_PID"]; ok {
		t.Errorf("internal service has SLINIT_TARGET_PID %q", pid)
	}
}

// Captured output lines reach the journal in file order, without the
// lines the level gate dropped, each with the producer's pid as it was
// when the line was read: the rotator caches the pid's string and must
// notice a new one (a restart).
func TestLogRotatorJournalLines(t *testing.T) {
	events := cronJournal(t)
	pid := 100
	lr, err := NewLogRotator(LogRotatorConfig{
		ServiceName: "svc-lines",
		FilePath:    filepath.Join(t.TempDir(), "log"),
		LogLevelMax: 6,
		AlertLevel:  -1,
		GetPID:      func() int { return pid },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lr.Close()

	lr.processLine([]byte("one\n"))
	lr.processLine([]byte("<7>debug, dropped\n"))
	lr.processLine([]byte("two\n"))
	pid = 200
	lr.processLine([]byte("three\n"))
	pid = 0
	lr.processLine([]byte("four\n"))

	got := events("svc-lines")
	want := []struct{ msg, pid string }{
		{"one", "100"}, {"two", "100"}, {"three", "200"}, {"four", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("%d journal events, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Msg != w.msg || got[i].Fields["SLINIT_TARGET_PID"] != w.pid {
			t.Errorf("event %d: msg %q pid %q, want %q %q",
				i, got[i].Msg, got[i].Fields["SLINIT_TARGET_PID"], w.msg, w.pid)
		}
	}
}

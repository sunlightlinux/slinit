package service

import "testing"

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

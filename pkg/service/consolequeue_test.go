package service

import (
	"testing"
	"time"
)

// options = starts-on-console with nobody else on the console: the
// service queued for it and nothing ever pulled the queue, since only a
// holder releasing the console did. It stayed STARTING forever, and a
// process service was never forked.
func TestStartsOnConsoleStartsWhenTheConsoleIsFree(t *testing.T) {
	set, _ := newTestSet()
	s := NewInternalService(set, "fsck")
	s.Flags.StartsOnConsole = true
	set.AddService(s)
	set.StartService(s)
	if st := s.State(); st != StateStarted {
		t.Fatalf("internal starts-on-console: state %v, want STARTED", st)
	}
	if s.HasConsole() {
		t.Error("a started service without runs-on-console kept the console")
	}

	p := NewProcessService(set, "p")
	p.SetCommand([]string{"/bin/sh", "-c", "exec sleep 30"})
	p.Flags.StartsOnConsole = true
	set.AddService(p)
	set.StartService(p)
	defer set.StopService(p)
	if got := waitState(t, p, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("process starts-on-console: state %v after 5s, want STARTED", got)
	}
}

// A waiter behind a holder still waits, and gets the console when the
// holder lets go.
func TestStartsOnConsoleWaitsForTheHolder(t *testing.T) {
	set, _ := newTestSet()
	holder := NewProcessService(set, "holder")
	holder.SetCommand([]string{"/bin/sh", "-c", "exec sleep 30"})
	holder.Flags.StartsOnConsole = true
	holder.Flags.RunsOnConsole = true
	set.AddService(holder)
	set.StartService(holder)
	if got := waitState(t, holder, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("holder: state %v", got)
	}

	w := NewInternalService(set, "waiter")
	w.Flags.StartsOnConsole = true
	set.AddService(w)
	set.StartService(w)
	if st := w.State(); st != StateStarting || !w.WaitingForConsole() {
		t.Fatalf("waiter: state %v waiting=%v, want STARTING and waiting while the holder runs", st, w.WaitingForConsole())
	}

	set.StopService(holder)
	if got := waitState(t, w, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("waiter after the holder stopped: state %v, want STARTED", got)
	}
}

// Leaving the queue must not reorder the others: it is first come,
// first served.
func TestUnqueueConsoleKeepsOrder(t *testing.T) {
	set, _ := newTestSet()
	set.consoleHeld = true // someone has the console; nobody is pulled
	var q []*InternalService
	for _, n := range []string{"a", "b", "c", "d"} {
		s := NewInternalService(set, n)
		set.AddService(s)
		q = append(q, s)
		set.AppendConsoleQueue(s)
	}
	set.UnqueueConsole(q[1])
	var order []string
	for _, s := range set.consoleQueue {
		order = append(order, s.Name())
	}
	if len(order) != 3 || order[0] != "a" || order[1] != "c" || order[2] != "d" {
		t.Errorf("queue after b leaves = %v, want [a c d]", order)
	}
}

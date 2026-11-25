package main

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// blockingStopper's Stop waits until released, as Debugger.Stop waits
// for an open menu to close.
type blockingStopper struct {
	release chan struct{}
	calls   atomic.Int32
}

func (b *blockingStopper) Stop() {
	b.calls.Add(1)
	<-b.release
}

// The boot service's STARTED event reaches this listener with the
// scheduling lock held. Stop must not run there: with the debugger menu
// open it blocked the whole state machine, and the menu's [f] action
// (which needs the lock) deadlocked PID 1.
func TestDebuggerStopDoesNotBlockTheListener(t *testing.T) {
	b := &blockingStopper{release: make(chan struct{})}
	l := &debuggerStopOnStart{dbg: b}

	done := make(chan struct{})
	go func() {
		l.ServiceEvent(nil, service.EventStarted)
		l.ServiceEvent(nil, service.EventStarted)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServiceEvent waited for Debugger.Stop (the menu) while the caller holds the scheduling lock")
	}
	close(b.release)
	deadline := time.Now().Add(2 * time.Second)
	for b.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if n := b.calls.Load(); n != 1 {
		t.Errorf("Stop called %d times, want exactly 1", n)
	}
}

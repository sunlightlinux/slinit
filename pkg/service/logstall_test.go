package service

import (
	"testing"
	"time"
)

// A stalled log consumer — a logger SIGSTOPped by an operator, hung on a
// full disk, or simply slow — must never stop PID 1. These tests pin the
// paths an audit found where it did: work done while holding queueMu
// that waits on the consumer, so the state machine, the control socket
// and reaping all stopped with it. Each probes queueMu from another
// goroutine, the way the control server would.

// queueMuFree reports whether another goroutine can take queueMu within d.
func queueMuFree(set *ServiceSet, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if set.queueMu.TryLock() {
			set.queueMu.Unlock()
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// runWithin runs f and reports whether it returned within d. f keeps
// running in the background when it does not.
func runWithin(d time.Duration, f func()) (time.Duration, bool) {
	done := make(chan struct{})
	t0 := time.Now()
	go func() { f(); close(done) }()
	select {
	case <-done:
		return time.Since(t0), true
	case <-time.After(d):
		return time.Since(t0), false
	}
}

// A shared logger that reads nothing (stopped) leaves the producer's mux
// reader blocked writing into a full pipe. Restarting the producer used
// to wait for that reader under queueMu — forever.
func TestSharedLoggerStalledProducerRestartKeepsQueueMuFree(t *testing.T) {
	set, _ := newTestSet()
	// Nobody reads mux.InputPipe(): the logger is stopped.
	if _, err := set.GetOrCreateSharedLogMux("logger", SharedLogMuxOptions{}); err != nil {
		t.Fatal(err)
	}
	svc := NewProcessService(set, "prod")
	svc.SetCommand([]string{"/bin/sh", "-c", "exec yes 0123456789012345678901234567890123456789"})
	svc.SetLogType(LogToPipe)
	svc.SetSharedLoggerName("logger")
	set.AddService(svc)
	set.StartService(svc)
	if got := waitState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("producer did not start: %v", got)
	}
	time.Sleep(300 * time.Millisecond) // let the 64 KiB mux pipe fill

	set.StopService(svc)
	if got := waitState(t, svc, StateStopped, 5*time.Second); got != StateStopped {
		t.Fatalf("producer did not stop: %v", got)
	}

	el, ok := runWithin(3*time.Second, func() { set.StartService(svc) })
	if !ok {
		t.Fatalf("restarting the producer of a stalled shared logger did not return within %v", el.Round(time.Millisecond))
	}
	if !queueMuFree(set, 2*time.Second) {
		t.Fatal("queueMu still held 2s after the producer restart")
	}
	runWithin(5*time.Second, func() { set.StopService(svc) })
}

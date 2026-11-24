package service

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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

// logfile + output-logger: slinit copies every line to the logger. With
// the logger stopped, that copy blocked the rotator's reader (so the
// logfile stopped growing) and the auto-restart's logRotator.Close()
// waited for it under queueMu.
func TestOutputLoggerStalledKeepsLogfileAndRestart(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "svc.log")
	loggerSh := filepath.Join(dir, "logger.sh")
	writeExec(t, loggerSh, "#!/bin/sh\nkill -STOP $$\ncat >/dev/null\n")

	set, _ := newTestSet()
	svc := NewProcessService(set, "rot")
	svc.SetCommand([]string{"/bin/sh", "-c", "exec yes 0123456789012345678901234567890123456789"})
	svc.SetLogType(LogToFile)
	svc.SetLogFileDetails(logFile, 0o600, -1, -1)
	svc.SetOutputLogger([]string{loggerSh})
	svc.SetAutoRestart(RestartAlways)
	svc.SetRestartDelay(50 * time.Millisecond)
	set.AddService(svc)
	set.StartService(svc)
	defer runWithin(5*time.Second, func() { set.StopService(svc) })
	if got := waitState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("service did not start: %v", got)
	}

	time.Sleep(500 * time.Millisecond)
	sz1 := fileSize(logFile)
	time.Sleep(500 * time.Millisecond)
	sz2 := fileSize(logFile)
	if sz2 <= sz1 {
		t.Errorf("logfile stopped growing with the output-logger stalled: %d -> %d bytes", sz1, sz2)
	}

	pid := svc.PID()
	_ = syscall.Kill(pid, syscall.SIGKILL)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p := svc.PID(); p > 0 && p != pid && svc.State() == StateStarted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p := svc.PID(); p <= 0 || p == pid {
		t.Errorf("auto-restart did not happen within 5s: state=%v pid=%d", svc.State(), p)
	}
	if !queueMuFree(set, 2*time.Second) {
		t.Fatal("queueMu held across the auto-restart of a service with a stalled output-logger")
	}
}

func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// glibc's pututxline waits up to 10 s for a utmp lock any local user
// can hold with a read-only open. The utmp write for a getty used to run
// in startProcess under queueMu, so each start held it that long.
func TestUtmpWriteDoesNotHoldQueueMu(t *testing.T) {
	set, _ := newTestSet()
	release := make(chan struct{})
	defer close(release)
	set.OnUtmpCreate = func(id, line, mode string, pid int) { <-release }
	set.OnUtmpClear = func(id, line string) { <-release }

	svc := NewProcessService(set, "getty")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	svc.SetUtmpDetails("tty1", "tty1")
	set.AddService(svc)

	el, ok := runWithin(2*time.Second, func() { set.StartService(svc) })
	if !ok {
		t.Fatalf("StartService waited %v on a blocked utmp write", el.Round(time.Millisecond))
	}
	if !queueMuFree(set, 2*time.Second) {
		t.Fatal("queueMu held while the utmp write is blocked")
	}
	runWithin(5*time.Second, func() { set.StopService(svc) })
}

// The utmp writes are queued, so a clear must never overtake the create
// for the same process — even when the create is slow and the process
// exits at once.
func TestUtmpClearNeverOvertakesCreate(t *testing.T) {
	set, _ := newTestSet()
	var mu sync.Mutex
	var calls []string
	cleared := make(chan struct{}, 8)
	set.OnUtmpCreate = func(id, line, mode string, pid int) {
		time.Sleep(200 * time.Millisecond)
		mu.Lock()
		calls = append(calls, "create")
		mu.Unlock()
	}
	set.OnUtmpClear = func(id, line string) {
		mu.Lock()
		calls = append(calls, "clear")
		mu.Unlock()
		cleared <- struct{}{}
	}

	svc := NewProcessService(set, "getty")
	svc.SetCommand([]string{"/bin/true"})
	svc.SetUtmpDetails("tty1", "tty1")
	set.AddService(svc)
	set.StartService(svc)
	select {
	case <-cleared:
	case <-time.After(5 * time.Second):
		t.Fatal("utmp entry never cleared")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) < 2 || calls[0] != "create" || calls[1] != "clear" {
		t.Fatalf("utmp writes out of order: %v", calls)
	}
}

// A log-forward-udp destination naming a host used to be resolved in
// startProcess under queueMu; with a resolver that does not answer that
// held it for 10 s. The resolver here is a socket that never replies.
func TestSyslogForwardHostNotResolvedUnderQueueMu(t *testing.T) {
	silent, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	saved := syslogForwardResolver
	syslogForwardResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", silent.LocalAddr().String())
		},
	}
	defer func() { syslogForwardResolver = saved }()

	set, _ := newTestSet()
	svc := NewProcessService(set, "fwd")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	svc.SetLogForward("loghost.invalid:514", "", 3, "")
	set.AddService(svc)

	el, ok := runWithin(2*time.Second, func() { set.StartService(svc) })
	if !ok {
		t.Fatalf("StartService waited %v on the syslog-forward lookup", el.Round(time.Millisecond))
	}
	if !queueMuFree(set, 2*time.Second) {
		t.Fatal("queueMu held during the syslog-forward lookup")
	}
	runWithin(5*time.Second, func() { set.StopService(svc) })
}

// A destination by host name still forwards once the lookup is done.
func TestSyslogForwardHostResolvesInBackground(t *testing.T) {
	rx, dest := listenUDP(t)
	defer rx.Close()
	_, port, _ := net.SplitHostPort(dest)
	fw, err := NewSyslogForwarder(net.JoinHostPort("localhost", port), "rfc3164", 3, "app", "svc", &syslogTestLogger{})
	if err != nil {
		t.Fatalf("NewSyslogForwarder: %v", err)
	}
	defer fw.Close()

	buf := make([]byte, 2048)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fw.Send([]byte("hello\n"))
		_ = rx.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		if n, err := rx.Read(buf); err == nil {
			if !strings.HasSuffix(string(buf[:n]), "hello") {
				t.Fatalf("unexpected packet %q", buf[:n])
			}
			return
		}
	}
	t.Fatal("nothing forwarded to localhost within 5s")
}

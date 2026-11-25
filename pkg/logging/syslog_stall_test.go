package logging

import (
	"fmt"
	"io"
	"log/syslog"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// syslogAt points SetSyslog at a datagram socket at path for the test.
func syslogAt(t *testing.T, path string) {
	t.Helper()
	orig := newSyslog
	newSyslog = func() (*syslog.Writer, error) {
		return syslog.Dial("unixgram", path, syslog.LOG_DAEMON|syslog.LOG_NOTICE, "slinit")
	}
	t.Cleanup(func() { newSyslog = orig })
}

// The main log goes to /dev/log with no deadline. In container mode with
// the host's /dev/log mounted in, or not as PID 1, a syslog daemon that
// stops reading used to stop slinit at the next message, with the
// scheduling lock held: every "Service 'x' started" is one.
func TestMainSyslogStalledDaemonDoesNotBlock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	ln, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: p, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	syslogAt(t, p)

	l := New(LevelInfo)
	l.SetOutput(io.Discard)
	l.SetBootConsole(true, false)
	if err := l.SetSyslog(); err != nil {
		t.Fatalf("SetSyslog: %v", err)
	}

	var done atomic.Int64
	finished := make(chan struct{})
	go func() {
		for i := 0; i < 5000; i++ {
			l.ServiceStarted(fmt.Sprintf("svc-%04d", i))
			done.Add(1)
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatalf("logging to a stalled syslog daemon blocked after %d of 5000 service starts", done.Load())
	}

	// The daemon reads again: it hears about the gap, then gets new lines.
	got := make(chan string, 1)
	go func() {
		var all strings.Builder
		buf := make([]byte, 64*1024)
		for {
			ln.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, err := ln.Read(buf)
			if err != nil {
				got <- all.String()
				return
			}
			all.Write(buf[:n])
			if strings.Contains(all.String(), "after-resume") {
				got <- all.String()
				return
			}
		}
	}()
	// What was queued while it was stalled goes out first.
	for deadline := time.Now().Add(5 * time.Second); len(l.syslogQ.q) > 0; {
		if time.Now().After(deadline) {
			t.Fatal("the syslog queue did not drain after the daemon resumed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	l.ServiceStarted("after-resume")
	all := <-got
	if !strings.Contains(all, "after-resume") {
		t.Fatalf("the syslog daemon never got the line written after it resumed")
	}
	if !strings.Contains(all, "dropped") {
		t.Errorf("the syslog daemon was not told that messages were dropped")
	}
	// Not deferred: before the fix, Close waited on the stuck write.
	l.CloseSyslog()
}

// slowSyslogDaemon reads datagrams at path, sleeping perMsg after each,
// and counts the messages naming a service.
func slowSyslogDaemon(t *testing.T, path string, perMsg time.Duration) (*atomic.Int64, *atomic.Bool) {
	t.Helper()
	ln, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var got atomic.Int64
	var toldDrops atomic.Bool
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, err := ln.Read(buf)
			if err != nil {
				return
			}
			if strings.Contains(string(buf[:n]), "svc-") {
				got.Add(1)
			}
			if strings.Contains(string(buf[:n]), "dropped") {
				toldDrops.Store(true)
			}
			time.Sleep(perMsg)
		}
	}()
	return &got, &toldDrops
}

// A syslog daemon slower than a burst of messages is not a stalled one.
// The queue dropped every message past its bound, where a direct write
// waited for the daemon; it now waits for a daemon that keeps reading,
// and drops only after a second in which it takes nothing.
func TestMainSyslogSlowDaemonIsLossless(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	// About 1000 messages a second.
	got, toldDrops := slowSyslogDaemon(t, p, 500*time.Microsecond)
	syslogAt(t, p)
	l := New(LevelInfo)
	l.SetOutput(io.Discard)
	if err := l.SetSyslog(); err != nil {
		t.Fatalf("SetSyslog: %v", err)
	}
	const n = 3000
	for i := 0; i < n; i++ {
		l.ServiceStarted(fmt.Sprintf("svc-%04d", i))
	}
	l.CloseSyslog()
	for deadline := time.Now().Add(2 * time.Second); got.Load() < n && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if got.Load() != n || toldDrops.Load() {
		t.Errorf("a slow but working syslog daemon got %d of %d messages (told of drops: %v)",
			got.Load(), n, toldDrops.Load())
	}
}

// CloseSyslog passes on what is queued to a slow daemon, for a bounded
// time.
func TestCloseSyslogIsBounded(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	got, _ := slowSyslogDaemon(t, p, 3*time.Millisecond)
	syslogAt(t, p)
	l := New(LevelInfo)
	l.SetOutput(io.Discard)
	if err := l.SetSyslog(); err != nil {
		t.Fatalf("SetSyslog: %v", err)
	}
	done := make(chan struct{})
	go func() {
		// ~6 s of messages at this rate: the queue and the socket fill,
		// and some 4 s of them are still queued at CloseSyslog.
		for i := 0; i < 2000; i++ {
			l.ServiceStarted(fmt.Sprintf("svc-%04d", i))
		}
		close(done)
	}()
	<-done
	t0 := time.Now()
	l.CloseSyslog()
	took := time.Since(t0)
	t.Logf("CloseSyslog took %v; the daemon got %d", took.Round(time.Millisecond), got.Load())
	if took > syslogFlushMax+time.Second {
		t.Errorf("CloseSyslog waited %v for a slow daemon, past the %v bound", took, syslogFlushMax)
	}
}

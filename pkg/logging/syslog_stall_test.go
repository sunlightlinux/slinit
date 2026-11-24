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

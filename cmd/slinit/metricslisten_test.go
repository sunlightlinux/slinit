package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// --metrics-listen takes either a TCP address or "unix:/path". The unix
// form is the one with sharp edges: the socket node survives a crash, so
// binding again would fail on a file nobody is listening on, and the
// scraper is not root, so the node has to be reachable.
func TestListenMetricsUnixSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "metrics.sock")

	ln, err := listenMetrics("unix:" + path)
	if err != nil {
		t.Fatalf("listenMetrics: %v", err)
	}
	defer ln.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("socket not created (its directory should have been): %v", err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		t.Errorf("%s is not a socket (mode %v)", path, fi.Mode())
	}
	if perm := fi.Mode().Perm(); perm&0o066 == 0 {
		t.Errorf("socket mode %v leaves a non-root scraper unable to read it", perm)
	}
	if c, err := net.Dial("unix", path); err != nil {
		t.Errorf("dial own socket: %v", err)
	} else {
		c.Close()
	}
}

// A leftover socket from a previous life must not stop the daemon from
// listening — that would make the endpoint work only until the first
// unclean shutdown.
func TestListenMetricsReplacesAStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.sock")

	// Bound with the raw syscalls, not net.Listen: Go's listener
	// unlinks the node on Close, which is exactly what a crashed daemon
	// does not do. This leaves the node behind the way a kill -9 would.
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socket: %v", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrUnix{Name: path}); err != nil {
		unix.Close(fd)
		t.Fatalf("bind: %v", err)
	}
	unix.Close(fd)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected a leftover socket node: %v", err)
	}

	ln, err := listenMetrics("unix:" + path)
	if err != nil {
		t.Fatalf("listenMetrics over a stale socket: %v", err)
	}
	ln.Close()
}

func TestListenMetricsTCP(t *testing.T) {
	ln, err := listenMetrics("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listenMetrics tcp: %v", err)
	}
	defer ln.Close()
	if _, ok := ln.Addr().(*net.TCPAddr); !ok {
		t.Errorf("listener is %T, want TCP", ln.Addr())
	}

	if _, err := listenMetrics("this is not an address"); err == nil {
		t.Error("a malformed address should fail rather than listen on something surprising")
	}
}

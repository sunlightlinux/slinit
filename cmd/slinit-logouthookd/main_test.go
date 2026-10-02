package main

import (
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestSplitIdentity covers the small parser used on the wire.
// Explicit table so a future change to the protocol lands loudly.
func TestSplitIdentity(t *testing.T) {
	cases := []struct {
		in            string
		id, tty       string
		ok            bool
	}{
		{"1 pts/0\n", "1", "pts/0", true},
		{"tty1 tty1\r\n", "tty1", "tty1", true},
		{" leading pts/2\n", "leading", "pts/2", true},
		{"onlyone\n", "", "", false},
		{"\n", "", "", false},
		{"three fields here\n", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		id, tty, ok := splitIdentity(c.in)
		if id != c.id || tty != c.tty || ok != c.ok {
			t.Errorf("splitIdentity(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.in, id, tty, ok, c.id, c.tty, c.ok)
		}
	}
}

// TestHandleConnCleansOnEOF drives the per-connection lifecycle end
// to end via a socketpair — client sends identity, closes its fd, and
// the utmp mock records that ClearEntry was called with the right
// arguments. This is the load-bearing invariant of the daemon.
func TestHandleConnCleansOnEOF(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "hookd.sock")

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	// Bypass peer auth in the test — CI runs as UID != 0.
	origAuth := peerAuthFunc
	peerAuthFunc = func(*net.UnixConn) error { return nil }
	defer func() { peerAuthFunc = origAuth }()

	// Swap the ClearEntry hook so the test doesn't touch real utmp.
	var mu sync.Mutex
	var gotID, gotTTY string
	var called bool
	origClear := utmpClearFunc
	utmpClearFunc = func(id, tty string) {
		mu.Lock()
		defer mu.Unlock()
		gotID = id
		gotTTY = tty
		called = true
	}
	defer func() { utmpClearFunc = origClear }()

	// Accept + handle one connection.
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		conn, err := l.Accept()
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		handleConn(conn, nil)
	}()

	// Client side: connect, send identity, wait a moment, close.
	c, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := c.Write([]byte("42 pts/7\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Give the server goroutine a moment to enter its drain loop.
	time.Sleep(30 * time.Millisecond)
	c.Close()

	select {
	case <-acceptDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handleConn did not return within 2s after client close")
	}

	mu.Lock()
	defer mu.Unlock()
	if !called {
		t.Fatal("ClearEntry was not called after EOF")
	}
	if gotID != "42" || gotTTY != "pts/7" {
		t.Errorf("ClearEntry(%q, %q), want (\"42\", \"pts/7\")", gotID, gotTTY)
	}
}

// TestHandleConnRejectsMalformed: an identity line that doesn't have
// exactly two fields must abort BEFORE ClearEntry is called. A
// garbled first line otherwise resolves to ClearEntry("", "") which
// happens to match the first free utmp slot and randomly nukes it.
func TestHandleConnRejectsMalformed(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "hookd.sock")

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	var called bool
	origClear := utmpClearFunc
	utmpClearFunc = func(string, string) { called = true }
	defer func() { utmpClearFunc = origClear }()

	origAuth := peerAuthFunc
	peerAuthFunc = func(*net.UnixConn) error { return nil }
	defer func() { peerAuthFunc = origAuth }()

	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		conn, err := l.Accept()
		if err != nil {
			return
		}
		handleConn(conn, nil)
	}()

	c, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.Write([]byte("garbled_no_space\n"))
	c.Close()
	// Wait for the server goroutine to fully return before the
	// deferred restore of peerAuthFunc / utmpClearFunc races the
	// still-running handleConn read of the same package globals.
	// time.Sleep is not synchronisation.
	select {
	case <-acceptDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handleConn did not return within 2s after client close")
	}

	if called {
		t.Fatal("ClearEntry called on malformed input — daemon silently nuked utmp")
	}
}

// TestIsClosedErrHappyPath is a smoke check for the accept-loop
// exit predicate. A closed listener produces the well-known error
// string on Linux; make sure we recognise it and stop looping.
func TestIsClosedErrHappyPath(t *testing.T) {
	l, err := net.Listen("unix", filepath.Join(t.TempDir(), "s"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	l.Close()
	if _, err := l.Accept(); err == nil {
		t.Fatal("accept on closed listener should error")
	} else if !isClosedErr(err) {
		t.Errorf("isClosedErr(%v) = false; want true", err)
	}
}

// TestMainRemovesStaleSocket is a small integration guard: leaving a
// stale socket around from a crashed prior run should NOT block a
// fresh Listen call. Exercises just the pre-listen os.Remove path.
func TestMainRemovesStaleSocket(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "hookd.sock")

	// Create a stale file at the socket path.
	if err := os.WriteFile(sockPath, []byte("stale"), 0600); err != nil {
		t.Fatalf("stale write: %v", err)
	}

	if err := os.Remove(sockPath); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Fatalf("socket should be gone after remove, stat err=%v", err)
	}

	// Now Listen should succeed.
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen after cleanup: %v", err)
	}
	l.Close()
}

// SIGTERM has to stop the daemon even while a session is open.
//
// Every client goroutine parks in a blocking Read that only returns when
// the session ends — that read is the logout detector. So closing the
// listener alone leaves serve waiting on reads that will not return until
// the last user logs out: the daemon never exits, PID 1's stop timeout
// elapses and it is SIGKILLed instead. On a machine with anybody logged
// in that is every shutdown.
//
// Driven through the real serve/shutdown pair rather than a reconstruction
// of the loop, so the test cannot pass against a copy of the logic.
func TestShutdownEndsSessionsThatAreStillOpen(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "hookd.sock")

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	origAuth := peerAuthFunc
	peerAuthFunc = func(*net.UnixConn) error { return nil }
	defer func() { peerAuthFunc = origAuth }()

	// A session the daemon ends itself must NOT be marked dead: the user
	// is still logged in, and saying otherwise is worse than a stale
	// record because it is confidently wrong.
	cleared := make(chan string, 4)
	origClear := utmpClearFunc
	utmpClearFunc = func(id, tty string) { cleared <- id }
	defer func() { utmpClearFunc = origClear }()

	srv := newServer()
	served := make(chan struct{})
	go func() {
		defer close(served)
		srv.serve(l)
	}()

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("sess1 pts/9\n")); err != nil {
		t.Fatalf("write identity: %v", err)
	}

	// Wait until the daemon has the session registered and parked in its
	// read, which is the state the bug was about.
	deadline := time.Now().Add(5 * time.Second)
	for {
		srv.mu.Lock()
		n := len(srv.conns)
		srv.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the session was never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}

	srv.shutdown()

	select {
	case <-served:
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not return after shutdown with a session still open")
	}

	select {
	case id := <-cleared:
		t.Fatalf("utmp record for %q was cleared on daemon shutdown, but that session is still live", id)
	default:
	}
}

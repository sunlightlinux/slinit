package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestInheritedListenersRejectsMissingEnv(t *testing.T) {
	t.Setenv("LISTEN_FDS", "")
	if _, err := inheritedListeners(); err == nil {
		t.Error("no LISTEN_FDS was accepted")
	} else if !strings.Contains(err.Error(), "LISTEN_FDS") {
		t.Errorf("error does not name the variable: %v", err)
	}
}

func TestInheritedListenersRejectsBadCount(t *testing.T) {
	for _, v := range []string{"0", "-1", "nope", "1.5"} {
		t.Setenv("LISTEN_FDS", v)
		if _, err := inheritedListeners(); err == nil {
			t.Errorf("LISTEN_FDS=%q was accepted", v)
		}
	}
}

func TestInheritedListenersRejectsForeignListenPID(t *testing.T) {
	// The variable survives an exec into a process that never got the
	// fds, so a mismatch has to be refused rather than reading whatever
	// fd 3 happens to be.
	t.Setenv("LISTEN_FDS", "1")
	t.Setenv("LISTEN_PID", fmt.Sprint(os.Getpid()+1))
	_, err := inheritedListeners()
	if err == nil {
		t.Fatal("a foreign LISTEN_PID was accepted")
	}
	if !strings.Contains(err.Error(), "LISTEN_PID") {
		t.Errorf("error does not name the variable: %v", err)
	}
}

func TestConnFileReturnsTheSocket(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback tcp: %v", err)
	}
	defer ln.Close()
	go func() {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err == nil {
			defer c.Close()
			time.Sleep(100 * time.Millisecond)
		}
	}()
	conn, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer conn.Close()
	f, err := connFile(conn)
	if err != nil {
		t.Fatalf("connFile: %v", err)
	}
	defer f.Close()
	if f.Fd() == 0 {
		t.Error("connFile returned fd 0")
	}
}

// buildHelper compiles slinit-inetd once for the end-to-end tests.
func buildHelper(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "slinit-inetd")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("building helper: %v\n%s", err, out)
	}
	return bin
}

// startHelper runs the helper against a fresh listener, handing it the
// socket the way slinit does, and returns the address to dial.
func startHelper(t *testing.T, bin string, args ...string) (string, *exec.Cmd) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback tcp: %v", err)
	}
	addr := ln.Addr().String()
	lf, err := ln.(*net.TCPListener).File()
	if err != nil {
		t.Fatalf("listener file: %v", err)
	}

	cmd := exec.Command(bin, args...)
	cmd.ExtraFiles = []*os.File{lf} // becomes fd 3 in the child
	cmd.Env = append(os.Environ(), "LISTEN_FDS=1")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting helper: %v", err)
	}
	// The helper owns the socket now; this side must let go of both
	// copies or the test itself holds the port open.
	lf.Close()
	ln.Close()
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	// LISTEN_PID is deliberately unset here: the helper only enforces it
	// when present, and the test cannot know the child's pid in advance.
	return addr, cmd
}

func TestEndToEndConnectedSocketOnStdio(t *testing.T) {
	bin := buildHelper(t)
	// `cat` echoes stdin to stdout. Both are the connected socket, so a
	// line sent must come back — which is only true if the ACCEPTED fd
	// reached the child, not the listener.
	addr, _ := startHelper(t, bin, "--quiet", "--", "/bin/cat")

	var conn net.Conn
	var err error
	for i := 0; i < 50; i++ {
		conn, err = net.Dial("tcp", addr)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dialling the helper: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(buf[:n]); got != "ping\n" {
		t.Errorf("echo = %q, want %q", got, "ping\n")
	}
}

func TestEndToEndOneProcessPerConnection(t *testing.T) {
	bin := buildHelper(t)
	// Each instance prints its own pid, so two connections that report
	// different pids prove a process per connection rather than one
	// shared server.
	addr, _ := startHelper(t, bin, "--quiet", "--",
		"/bin/sh", "-c", "echo $$")

	readPID := func() string {
		var conn net.Conn
		var err error
		for i := 0; i < 50; i++ {
			conn, err = net.Dial("tcp", addr)
			if err == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 32)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return strings.TrimSpace(string(buf[:n]))
	}

	first, second := readPID(), readPID()
	if first == "" || second == "" {
		t.Fatalf("no pid reported (%q, %q)", first, second)
	}
	if first == second {
		t.Errorf("both connections reported pid %s; expected one process per connection", first)
	}
}

func TestEndToEndDoesNotLeakDescriptors(t *testing.T) {
	bin := buildHelper(t)
	addr, cmd := startHelper(t, bin, "--quiet", "--", "/bin/cat")

	fdCount := func() int {
		entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", cmd.Process.Pid))
		if err != nil {
			t.Skipf("cannot read the helper's fd table: %v", err)
		}
		return len(entries)
	}

	exercise := func(n int) {
		for i := 0; i < n; i++ {
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			conn.Write([]byte("x\n"))
			buf := make([]byte, 8)
			conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			conn.Read(buf)
			conn.Close()
		}
	}

	// Warm up first: the Go runtime opens epoll and pipe fds lazily, so
	// a baseline taken before any connection would count those as a
	// leak. The question is whether the count grows per connection.
	exercise(5)
	time.Sleep(500 * time.Millisecond)
	before := fdCount()

	exercise(25)
	time.Sleep(1 * time.Second)
	after := fdCount()

	// Every connection that leaked would add at least one fd, so 25
	// connections would move this by 25. A couple of fds of slack covers
	// a handler still being reaped as the count is taken.
	if after > before+3 {
		t.Errorf("fd count grew from %d to %d across 25 connections — "+
			"the parent is not closing its copy of the accepted socket",
			before, after)
	}
}

// scriptedListener returns the errors it is given, in order, then
// net.ErrClosed so serve ends.
type scriptedListener struct {
	errs  []error
	calls int
}

func (l *scriptedListener) Accept() (net.Conn, error) {
	l.calls++
	if len(l.errs) == 0 {
		return nil, net.ErrClosed
	}
	err := l.errs[0]
	l.errs = l.errs[1:]
	return nil, err
}
func (l *scriptedListener) Close() error   { return nil }
func (l *scriptedListener) Addr() net.Addr { return &net.UnixAddr{Name: "test", Net: "unix"} }

// Running out of descriptors is the moment a busy inetd most needs to
// keep its listener: the handlers finishing will free them. accept(2)
// reports it as EMFILE (or ENFILE, ENOBUFS, ENOMEM), which Go does not
// call a timeout, so serve has to recognise it itself and retry.
func TestServeKeepsAcceptingAfterResourceErrors(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.EMFILE, syscall.ENFILE, syscall.ENOBUFS, syscall.ENOMEM} {
		ln := &scriptedListener{errs: []error{
			&net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept4", errno)},
		}}
		done := make(chan struct{})
		go func() {
			serve(ln, []string{"/bin/true"}, make(chan struct{}, 1), nil, true)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%v: serve did not return after the listener closed", errno)
		}
		if ln.calls != 2 {
			t.Errorf("%v: Accept called %d times, want 2 — serve gave up on the listener", errno, ln.calls)
		}
	}
}

// TestEndToEndMaxPerSource: --max-concurrent alone let one remote host
// hold every slot with idle connections. With --max-per-source 1 a
// second connection from the same IP is closed at once while the
// first is still being served.
func TestEndToEndMaxPerSource(t *testing.T) {
	bin := buildHelper(t)
	addr, _ := startHelper(t, bin, "--quiet", "--max-per-source", "1", "--", "/bin/cat")

	dial := func() net.Conn {
		t.Helper()
		var conn net.Conn
		var err error
		for i := 0; i < 50; i++ {
			conn, err = net.Dial("tcp", addr)
			if err == nil {
				return conn
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("dial: %v", err)
		return nil
	}
	echo := func(conn net.Conn) error {
		if _, err := conn.Write([]byte("ping\n")); err != nil {
			return err
		}
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 16)
		n, err := conn.Read(buf)
		if err != nil {
			return err
		}
		if got := string(buf[:n]); got != "ping\n" {
			return fmt.Errorf("echo = %q", got)
		}
		return nil
	}

	first := dial()
	defer first.Close()
	if err := echo(first); err != nil {
		t.Fatalf("first connection: %v", err)
	}
	second := dial()
	defer second.Close()
	if err := echo(second); err == nil {
		t.Error("a second connection from the same source was served past --max-per-source 1")
	}
	// The first holds its slot still; closing it frees the source.
	if err := echo(first); err != nil {
		t.Errorf("first connection after the refusal: %v", err)
	}
	first.Close()
	var err error
	for i := 0; i < 50; i++ {
		third := dial()
		err = echo(third)
		third.Close()
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Errorf("the source's slot was not released: %v", err)
	}
}

func TestSourceLimiter(t *testing.T) {
	var unlimited *sourceLimiter
	for i := 0; i < 3; i++ {
		if !unlimited.acquire("10.0.0.1") {
			t.Fatal("a nil limiter limited")
		}
	}
	l := newSourceLimiter(2)
	if !l.acquire("10.0.0.1") || !l.acquire("10.0.0.1") {
		t.Fatal("refused under the limit")
	}
	if l.acquire("10.0.0.1") {
		t.Error("third from one source accepted at limit 2")
	}
	if !l.acquire("10.0.0.2") {
		t.Error("another source refused")
	}
	if !l.acquire("") || !l.acquire("") || !l.acquire("") {
		t.Error("a source with no IP was limited")
	}
	l.release("10.0.0.1")
	if !l.acquire("10.0.0.1") {
		t.Error("release did not free a slot")
	}
}

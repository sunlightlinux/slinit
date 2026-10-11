package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

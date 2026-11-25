// slinit-inetd runs one instance of a program per accepted connection,
// with the connected socket on stdin, stdout and stderr. This is inetd's
// `nowait` mode, and the shape of launchd's InetdCompatibility with
// Wait=false.
//
// WHY THIS IS A SEPARATE BINARY, and not something slinit does itself:
// launchd does not do it in launchd either. Its accept/fork/exec loop
// lives in a helper, launchproxy(8), which is handed the listener and
// runs as an ordinary job. slinit follows that, and the reason matters
// more here than it does on macOS — slinit is PID 1, and a per-
// connection lifecycle inside PID 1 would be the same pattern that once
// turned a concurrent-load race into a kernel panic. Here the accept
// loop and the fork storm belong to a supervised service: if this
// process dies, slinit restarts it like anything else, and PID 1 is
// never in the path.
//
// slinit normally passes a service its LISTENER and lets the service
// accept — inetd's `wait` mode, and what socket-listen has always done.
// That remains the default. This helper is what `socket-accept = yes`
// prepends to the command.
package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// listenFDsStart is the first fd passed by the sd_listen_fds protocol,
// which slinit implements: 0,1,2 are stdio, so inherited sockets begin
// at 3.
const listenFDsStart = 3

// version is set at build time with -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	// --version, alone: checked before anything else runs, so no other
	// argument changes meaning. The value is stamped by the package
	// build (-X main.version=...).
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Printf("slinit-inetd version %s\n", version)
		return
	}
	maxConc := flag.Int("max-concurrent", 64,
		"maximum simultaneous connection handlers; further connections wait")
	maxPerSource := flag.Int("max-per-source", 0,
		"maximum simultaneous connections from one remote IP; further ones are closed (0 = unlimited)")
	quiet := flag.Bool("quiet", false, "do not log accepted connections")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr,
			"Usage: %s [options] -- program [args...]\n\n"+
				"Accepts connections on the sockets slinit passed via LISTEN_FDS and\n"+
				"runs one instance of program per connection, with the connected socket\n"+
				"on stdin, stdout and stderr (inetd `nowait`).\n\nOptions:\n",
			os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	argv := flag.Args()
	if len(argv) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	if *maxConc < 1 {
		fmt.Fprintln(os.Stderr, "slinit-inetd: --max-concurrent must be at least 1")
		os.Exit(2)
	}
	if *maxPerSource < 0 {
		fmt.Fprintln(os.Stderr, "slinit-inetd: --max-per-source must not be negative")
		os.Exit(2)
	}

	listeners, err := inheritedListeners()
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-inetd: %v\n", err)
		os.Exit(1)
	}

	// A semaphore rather than an unbounded fork per connection.
	// launchproxy has no limit, but classic inetd does for good reason:
	// an unbounded fork storm from one port is a denial of service that
	// takes the whole machine, not just the service.
	sem := make(chan struct{}, *maxConc)
	// The semaphore alone lets one remote host take every slot with
	// idle connections and lock everyone else out. Off by default, as
	// xinetd's per_source is, so existing setups see no change.
	var perSource *sourceLimiter
	if *maxPerSource > 0 {
		perSource = newSourceLimiter(*maxPerSource)
	}

	var wg sync.WaitGroup
	for _, ln := range listeners {
		wg.Add(1)
		go func(ln net.Listener) {
			defer wg.Done()
			serve(ln, argv, sem, perSource, *quiet)
		}(ln)
	}
	wg.Wait()
	// Every listener has failed. That is never the normal way out —
	// the sockets are ours until slinit stops us with a signal — so
	// exit non-zero for the service's restart policy to see it, rather
	// than 0, which `restart = on-failure` would take as a clean stop.
	os.Exit(1)
}

// inheritedListeners turns the fds slinit passed into listeners.
//
// LISTEN_PID is checked because the variable survives an exec into a
// process that was never given the fds: without the check, a wrapper or
// a shell between slinit and here would make us treat fd 3 — whatever it
// happens to be — as a listener.
func inheritedListeners() ([]net.Listener, error) {
	nStr := os.Getenv("LISTEN_FDS")
	if nStr == "" {
		return nil, errors.New("LISTEN_FDS is not set: this must be run by slinit " +
			"with at least one socket-listen")
	}
	n, err := strconv.Atoi(nStr)
	if err != nil || n < 1 {
		return nil, fmt.Errorf("LISTEN_FDS=%q is not a positive count", nStr)
	}
	if pidStr := os.Getenv("LISTEN_PID"); pidStr != "" {
		pid, err := strconv.Atoi(pidStr)
		if err != nil || pid != os.Getpid() {
			return nil, fmt.Errorf("LISTEN_PID=%q is not this process (%d): "+
				"the sockets were passed to someone else", pidStr, os.Getpid())
		}
	}

	var out []net.Listener
	for i := 0; i < n; i++ {
		fd := listenFDsStart + i
		syscall.CloseOnExec(fd)
		f := os.NewFile(uintptr(fd), fmt.Sprintf("listen-fd-%d", fd))
		if f == nil {
			return nil, fmt.Errorf("fd %d is not valid", fd)
		}
		ln, err := net.FileListener(f)
		// FileListener dups, so our own copy is finished with either way.
		f.Close()
		if err != nil {
			// A datagram socket cannot accept, so there is nothing this
			// helper can do with one. Saying which fd and why beats
			// "invalid argument".
			return nil, fmt.Errorf("fd %d is not a listening stream socket "+
				"(inetd nowait needs one; a udp: listener cannot accept): %w", fd, err)
		}
		// tcp and unix stream listeners both arrive here; serve only
		// needs net.Listener, so neither is special-cased.
		out = append(out, ln)
	}
	return out, nil
}

// serve accepts in a loop, running one instance of argv per connection.
// A connection from a source already at its perSource limit is closed
// at once; a nil perSource means no per-source limit.
func serve(ln net.Listener, argv []string, sem chan struct{}, perSource *sourceLimiter, quiet bool) {
	var backoff time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			// A temporary error (EMFILE, ECONNABORTED) must not end the
			// loop: the listener is still good and dropping out would
			// take the service down over one refused connection. Go
			// retries ECONNABORTED itself; running out of descriptors
			// or memory reaches here, and is not a "timeout", so it is
			// matched by errno. Back off as net/http does, so a full
			// descriptor table is not spun on while handlers drain it.
			var ne net.Error
			if (errors.As(err, &ne) && ne.Timeout()) || isResourceExhaustion(err) {
				if backoff == 0 {
					backoff = 5 * time.Millisecond
				} else if backoff *= 2; backoff > time.Second {
					backoff = time.Second
				}
				fmt.Fprintf(os.Stderr, "slinit-inetd: accept: %v; retrying in %v\n", err, backoff)
				time.Sleep(backoff)
				continue
			}
			fmt.Fprintf(os.Stderr, "slinit-inetd: accept: %v\n", err)
			return
		}
		backoff = 0
		src := sourceOf(conn)
		if !perSource.acquire(src) {
			if !quiet {
				fmt.Fprintf(os.Stderr, "slinit-inetd: connection from %s refused: "+
					"too many from %s\n", conn.RemoteAddr(), src)
			}
			conn.Close()
			continue
		}
		sem <- struct{}{}
		go func(conn net.Conn) {
			defer func() { <-sem }()
			defer perSource.release(src)
			if err := handle(conn, argv, quiet); err != nil {
				fmt.Fprintf(os.Stderr, "slinit-inetd: %v\n", err)
			}
		}(conn)
	}
}

// sourceLimiter counts live connections per remote IP. Its methods
// are safe on a nil receiver, which limits nothing.
type sourceLimiter struct {
	max  int
	mu   sync.Mutex
	live map[string]int
}

func newSourceLimiter(max int) *sourceLimiter {
	return &sourceLimiter{max: max, live: make(map[string]int)}
}

// acquire takes a slot for src, or reports false if src already holds
// max. An empty src (a peer with no IP, such as a unix socket client)
// is not limited.
func (l *sourceLimiter) acquire(src string) bool {
	if l == nil || src == "" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.live[src] >= l.max {
		return false
	}
	l.live[src]++
	return true
}

// release gives back a slot acquire took.
func (l *sourceLimiter) release(src string) {
	if l == nil || src == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.live[src]--; l.live[src] <= 0 {
		delete(l.live, src)
	}
}

// sourceOf names the remote IP a connection comes from, or "" when it
// has none.
func sourceOf(conn net.Conn) string {
	if a, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		return a.IP.String()
	}
	return ""
}

// isResourceExhaustion reports whether accept failed for want of a
// descriptor or of kernel memory — conditions that pass once handlers
// exit, unlike a closed or broken listener.
func isResourceExhaustion(err error) bool {
	return errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) ||
		errors.Is(err, syscall.ENOBUFS) || errors.Is(err, syscall.ENOMEM)
}

// handle runs one instance of argv with conn on stdin, stdout and
// stderr, and waits for it so the child is reaped here rather than
// becoming slinit's problem.
func handle(conn net.Conn, argv []string, quiet bool) error {
	defer conn.Close()

	if !quiet {
		fmt.Fprintf(os.Stderr, "slinit-inetd: connection from %s on %s\n",
			conn.RemoteAddr(), conn.LocalAddr())
	}

	f, err := connFile(conn)
	if err != nil {
		return err
	}
	// The child gets the socket on all three descriptors; this copy is
	// ours and must be closed once the child holds its own, or the
	// server never sees EOF when the peer hangs up and every connection
	// leaks an fd until the process runs out. launchproxy's close(r)
	// after fork is the same line.
	defer f.Close()

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = childEnv(os.Environ())
	cmd.Stdin = f
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", argv[0], err)
	}
	return cmd.Wait()
}

// childEnv is env without the socket-activation variables. They
// describe the listeners slinit passed us, not the connection the
// child gets on stdio; left in, a child that speaks the protocol (or
// a library acting on it) would take fd 3 for a listener, and
// LISTEN_PID's check cannot save it once a wrapper sits in between.
func childEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "LISTEN_FDS=") || strings.HasPrefix(kv, "LISTEN_PID=") ||
			strings.HasPrefix(kv, "LISTEN_FDNAMES=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// connFile returns the connection's fd as a file. net.Conn has no such
// method in general, so the concrete types that do are asked; anything
// else is a programming error here rather than a user-facing one.
func connFile(conn net.Conn) (*os.File, error) {
	type filer interface{ File() (*os.File, error) }
	fc, ok := conn.(filer)
	if !ok {
		return nil, fmt.Errorf("connection type %T cannot expose its fd", conn)
	}
	f, err := fc.File()
	if err != nil {
		return nil, fmt.Errorf("taking the connection fd: %w", err)
	}
	return f, nil
}

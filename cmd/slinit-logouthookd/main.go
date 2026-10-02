// slinit-logouthookd is the s6-linux-init-logouthookd analogue for
// slinit. It listens on a Unix domain socket and, for each connected
// client, tracks a utmp record that must be cleaned up when the
// client dies (typically the user's shell exiting after login).
//
// The intended integration pattern mirrors s6's:
//
//  1. login(1) / getty is patched to call a helper before exec'ing
//     the user shell. The helper opens a connection to this daemon,
//     sends a single line `id line\n` (utmp inittab-id + line/tty),
//     then keeps the fd around across exec.
//  2. login runs the shell; the connection to slinit-logouthookd stays
//     open for the shell's lifetime because the shell inherits the fd.
//  3. When the shell dies, the fd is closed and this daemon's per-
//     connection goroutine wakes on EOF and calls utmp.ClearEntry so
//     `who` / `w` correctly report the user as logged out.
//
// Without the patched login/getty this binary sits idle — no shell
// ever connects to it. Slinit ships it so distros that DO want the
// clean-utmp behaviour (sunlight-os may adopt it) can wire their login
// stack against it without having to invent a new IPC channel.
//
// Root-only by design: an arbitrary user asking to clear the wrong
// utmp record is a footgun. Enforced via peer credentials.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/sunlightlinux/slinit/pkg/utmp"
)

// version is injected at build time via -ldflags.
var version = "dev"

// utmpClearFunc is overridable so unit tests can drive the accept /
// per-connection lifecycle without touching real utmp records.
var utmpClearFunc = utmp.ClearEntry

// peerAuthFunc guards the identity handshake. Overridable for tests
// so the CI can drive the client lifecycle without needing to be
// root — production always uses requireRootPeer.
var peerAuthFunc = requireRootPeer

func main() {
	var (
		sockPath    string
		perms       int
		showVersion bool
	)
	flag.StringVar(&sockPath, "socket", "/run/slinit-logouthookd.sock",
		"Unix socket path to listen on")
	flag.IntVar(&perms, "perms", 0600,
		"socket file permissions (octal); default 0600 restricts access to root")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()

	if showVersion {
		fmt.Printf("slinit-logouthookd version %s\n", version)
		return
	}

	// Remove a stale socket from a previous run before binding —
	// otherwise Listen returns EADDRINUSE and the daemon fails to start.
	// The rm is safe because we're root and the daemon is single-writer.
	if err := os.Remove(sockPath); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "slinit-logouthookd: remove stale socket %s: %v\n",
			sockPath, err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(sockPath), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-logouthookd: mkdir %s: %v\n",
			filepath.Dir(sockPath), err)
		os.Exit(1)
	}

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-logouthookd: listen %s: %v\n",
			sockPath, err)
		os.Exit(1)
	}
	defer l.Close()
	if err := os.Chmod(sockPath, os.FileMode(perms)); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-logouthookd: chmod %s: %v\n",
			sockPath, err)
		os.Exit(1)
	}

	srv := newServer()

	// Graceful shutdown on SIGTERM / SIGINT.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		srv.shutdown()
	}()

	srv.serve(l)
}

// server tracks the connections that are open so shutdown can end them.
//
// Closing the listener alone is not enough. Every client goroutine is
// parked in a blocking Read that only returns when the session at the
// other end goes away, which is the whole mechanism — that read IS the
// logout detector. So on SIGTERM the accept loop breaks immediately and
// then waits for reads that will not return until the last user logs
// out. The daemon never exits, PID 1's stop timeout elapses, and it is
// SIGKILLed instead: on any machine with somebody logged in, that is
// every single shutdown.
type server struct {
	mu       sync.Mutex
	ln       net.Listener
	conns    map[net.Conn]struct{}
	stopping bool
}

func newServer() *server {
	return &server{conns: map[net.Conn]struct{}{}}
}

// add registers a connection, or refuses it if shutdown has begun.
func (s *server) add(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *server) remove(c net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, c)
}

// isStopping reports whether the daemon, not the client, ended a session.
func (s *server) isStopping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopping
}

// shutdown closes the listener and every open connection, which is what
// unblocks the parked reads.
func (s *server) shutdown() {
	s.mu.Lock()
	s.stopping = true
	ln := s.ln
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	if ln != nil {
		ln.Close()
	}
	for _, c := range conns {
		c.Close()
	}
}

// serve accepts until the listener is closed, then waits for the client
// goroutines — which shutdown has already unblocked.
func (s *server) serve(l net.Listener) {
	s.mu.Lock()
	s.ln = l
	stopping := s.stopping
	s.mu.Unlock()
	if stopping {
		// A signal that arrived before serve was reached.
		l.Close()
		return
	}

	var wg sync.WaitGroup
	for {
		conn, err := l.Accept()
		if err != nil {
			// A closed listener returns net.OpError with ErrClosed.
			// Anything else is a real failure worth surfacing.
			if isClosedErr(err) {
				break
			}
			fmt.Fprintf(os.Stderr, "slinit-logouthookd: accept: %v\n", err)
			continue
		}
		if !s.add(conn) {
			conn.Close()
			continue
		}
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			defer s.remove(c)
			handleConn(c, s)
		}(conn)
	}
	wg.Wait()
}

// handleConn implements the per-client contract:
//
//   - Verify peer credentials (root only).
//   - Read a single line with `id line` (space-separated).
//   - Block on Read until the connection closes.
//   - Clear the utmp record via ClearEntry.
//
// Errors are logged to stderr and terminate the goroutine without
// affecting other clients.
func handleConn(c net.Conn, s *server) {
	defer c.Close()

	uc, ok := c.(*net.UnixConn)
	if !ok {
		fmt.Fprintln(os.Stderr, "slinit-logouthookd: non-unix connection?")
		return
	}
	if err := peerAuthFunc(uc); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-logouthookd: peer rejected: %v\n", err)
		return
	}

	rdr := bufio.NewReader(uc)
	line, err := rdr.ReadString('\n')
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-logouthookd: read identity: %v\n", err)
		return
	}
	id, tty, ok := splitIdentity(line)
	if !ok {
		fmt.Fprintf(os.Stderr, "slinit-logouthookd: malformed identity %q\n", line)
		return
	}

	// Drain the connection until the client dies. A well-behaved client
	// won't send anything after the initial line — we only need Read
	// to block. When the fd closes (client shell exits), Read returns
	// EOF (or a connection-reset-ish error) and we proceed to cleanup.
	buf := make([]byte, 64)
	for {
		_, err := uc.Read(buf)
		if err != nil {
			break
		}
	}

	// Only on a real logout. If the daemon is the one that closed this
	// connection, the session is still live and marking its utmp record
	// DEAD_PROCESS would be a lie about a user who is still logged in —
	// worse than the record going stale, because stale is at least
	// visibly wrong.
	if s != nil && s.isStopping() {
		return
	}
	utmpClearFunc(id, tty)
}

// splitIdentity parses `id line` from the client's first message.
// Both fields are required; empty inputs are rejected so we can't
// call utmp.ClearEntry("", "") on a garbled line and racily match
// the first free slot.
func splitIdentity(line string) (id, tty string, ok bool) {
	line = strings.TrimRight(line, "\r\n")
	fields := strings.Fields(line)
	if len(fields) != 2 {
		return "", "", false
	}
	if fields[0] == "" || fields[1] == "" {
		return "", "", false
	}
	return fields[0], fields[1], true
}

// requireRootPeer verifies the SCM_CREDENTIALS UID of the connection
// is 0. Anything else means an unprivileged process trying to lie
// about which utmp record to clear.
func requireRootPeer(uc *net.UnixConn) error {
	raw, err := uc.SyscallConn()
	if err != nil {
		return fmt.Errorf("SyscallConn: %w", err)
	}
	var ucred *unix.Ucred
	var opErr error
	if err := raw.Control(func(fd uintptr) {
		ucred, opErr = unix.GetsockoptUcred(int(fd),
			unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return fmt.Errorf("Control: %w", err)
	}
	if opErr != nil {
		return fmt.Errorf("getsockopt SO_PEERCRED: %w", opErr)
	}
	if ucred.Uid != 0 {
		return fmt.Errorf("peer uid=%d (root required)", ucred.Uid)
	}
	return nil
}

// isClosedErr detects the "listener closed" flavour of net.OpError so
// the accept loop can break cleanly on shutdown without treating the
// close as a real error.
func isClosedErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "use of closed network connection")
}

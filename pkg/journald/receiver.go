// Package journald implements the persistent slinit-journald daemon
// side of the journal pipeline: bind the events.sock DGRAM listener,
// read datagrams with SO_PASSCRED ancillary data so external client
// PID/UID/GID are trustworthy, snapshot /proc/PID/{comm,exe,cmdline}
// for external senders, then hand each event to a Sink for
// persistence.
//
// Phase 3 splits into small, composable pieces:
//   - 3a (this file) — receiver + SCM_CREDENTIALS + Sink interface
//   - 3b — JSONL file writer sink
//   - 3c — .idx bisect companion
//   - 3d/3e — rotation + vacuum
//   - 3f — LZ4 on rotate
//   - 3g — /run fallback when /var unwritable
//
// Everything in this package is optional. slinit itself works whether
// or not slinit-journald is running; the daemon consumes what the
// emitter fans out and persists it. Non-listeners see ECONNREFUSED
// on emit and drop silently.
package journald

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// Sink is the receiver-side handler for events. Phase 3b provides a
// JSONL file sink; other implementations (broadcast to N follower
// sockets, forward to remote syslog, …) can slot in without touching
// the Receiver code. Handle must be safe for concurrent use if the
// Receiver ever grows a worker pool; the current single-reader loop
// serializes calls.
type Sink interface {
	Handle(evt *journal.Event) error
	Close() error
}

// Receiver owns the events.sock listener and the recvmsg loop.
type Receiver struct {
	path string
	sink Sink

	conn   *net.UnixConn
	stopCh chan struct{}
	wg     sync.WaitGroup

	received atomic.Uint64 // total datagrams parsed successfully
	dropped  atomic.Uint64 // parse/sink failures
}

// NewReceiver binds path as a SOCK_DGRAM Unix socket with SO_PASSCRED
// enabled. Any pre-existing file at the path is removed first (stale
// socket from a prior daemon crash). The listener is inactive until
// Run is called.
//
// path defaults to journal.SocketPath when empty.
func NewReceiver(path string, sink Sink) (*Receiver, error) {
	if sink == nil {
		return nil, errors.New("journald: nil sink")
	}
	if path == "" {
		path = journal.SocketPath
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("journald: mkdir %s: %w", filepath.Dir(path), err)
	}
	_ = os.Remove(path)

	addr := &net.UnixAddr{Name: path, Net: "unixgram"}
	conn, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		return nil, fmt.Errorf("journald: bind %s: %w", path, err)
	}
	if err := os.Chmod(path, 0666); err != nil {
		conn.Close()
		os.Remove(path)
		return nil, fmt.Errorf("journald: chmod: %w", err)
	}

	// Enable SO_PASSCRED so each recvmsg returns SCM_CREDENTIALS in
	// ancillary data. Without this we'd have no way to trust the
	// external client's claimed PID/UID/GID.
	if raw, err := conn.SyscallConn(); err == nil {
		var setErr error
		_ = raw.Control(func(fd uintptr) {
			setErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PASSCRED, 1)
		})
		if setErr != nil {
			conn.Close()
			os.Remove(path)
			return nil, fmt.Errorf("journald: SO_PASSCRED: %w", setErr)
		}
	}

	return &Receiver{
		path:   path,
		sink:   sink,
		conn:   conn,
		stopCh: make(chan struct{}),
	}, nil
}

// Path returns the on-disk socket path.
func (r *Receiver) Path() string { return r.path }

// Stats returns cumulative (received, dropped) counters. Diagnostic;
// wired to slinit-journald status output in a later batch.
func (r *Receiver) Stats() (received, dropped uint64) {
	return r.received.Load(), r.dropped.Load()
}

// Run blocks reading datagrams until ctx is done or Stop is called.
// Each parsed event has its trusted metadata (Pid/Uid/Gid/Comm/Exe/
// Cmdline) stamped from SCM_CREDENTIALS + /proc lookups before being
// passed to the sink. Parse and sink failures are counted but do not
// stop the loop — one bad client cannot silence the daemon.
func (r *Receiver) Run(ctx context.Context) {
	r.wg.Add(1)
	go r.readLoop(ctx)
}

// Stop signals the read loop to exit, closes the listener, removes
// the on-disk path, and closes the sink. Idempotent.
func (r *Receiver) Stop() error {
	select {
	case <-r.stopCh:
		return nil
	default:
		close(r.stopCh)
	}
	var firstErr error
	if r.conn != nil {
		if err := r.conn.Close(); err != nil {
			firstErr = err
		}
	}
	_ = os.Remove(r.path)
	r.wg.Wait()
	if err := r.sink.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (r *Receiver) readLoop(ctx context.Context) {
	defer r.wg.Done()

	// Body: sized to MaxEventSize so an oversized JSON payload
	// still fits in one read; oversized datagrams get truncated by
	// the kernel and marshal-fail on parse, incrementing dropped.
	bodyBuf := make([]byte, journal.MaxEventSize)
	oobBuf := make([]byte, unix.CmsgSpace(unix.SizeofUcred))

	rawConn, err := r.conn.SyscallConn()
	if err != nil {
		return
	}

	for {
		select {
		case <-r.stopCh:
			return
		case <-ctx.Done():
			return
		default:
		}
		var (
			n, oobn int
			recvErr error
		)
		ctlErr := rawConn.Read(func(fd uintptr) bool {
			n, oobn, _, _, recvErr = unix.Recvmsg(int(fd), bodyBuf, oobBuf, 0)
			if recvErr == unix.EAGAIN || recvErr == unix.EWOULDBLOCK {
				// Netpoller will call us back when data arrives.
				return false
			}
			return true
		})
		if ctlErr != nil || recvErr != nil {
			// EBADF / EINVAL after Stop → clean exit; any other error
			// treated the same because we have no logger here.
			return
		}
		if n == 0 {
			continue
		}
		evt, err := journal.UnmarshalEvent(bodyBuf[:n])
		if err != nil {
			r.dropped.Add(1)
			continue
		}
		stampFromSCM(evt, oobBuf[:oobn])
		if err := r.sink.Handle(evt); err != nil {
			r.dropped.Add(1)
			continue
		}
		r.received.Add(1)
	}
}

// stampFromSCM overwrites the trusted metadata fields on evt using the
// SCM_CREDENTIALS ancillary buffer. When the buffer is empty (SO_PASSCRED
// wasn't sent, or the client is on a non-Linux kernel) the values from
// the emitter side stand — that's the in-process fanout case where the
// emitter already stamped its own PID/UID/GID.
//
// The /proc snapshot fields (_comm, _exe, _cmdline) are refreshed from
// /proc/<pid>/ for external clients so stale rename-yourself games
// don't fool the log stream.
func stampFromSCM(evt *journal.Event, oob []byte) {
	if len(oob) == 0 {
		return
	}
	cmsgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return
	}
	for _, cm := range cmsgs {
		if cm.Header.Level != unix.SOL_SOCKET || cm.Header.Type != unix.SCM_CREDENTIALS {
			continue
		}
		if len(cm.Data) < unix.SizeofUcred {
			continue
		}
		ucred := castUcred(cm.Data)
		evt.Pid = int(ucred.Pid)
		evt.Uid = int(ucred.Uid)
		evt.Gid = int(ucred.Gid)
		if evt.Pid > 0 {
			snapshotProc(evt)
		}
	}
}

// snapshotProc reads /proc/<pid>/{comm,exe,cmdline} into the trusted
// _comm/_exe/_cmdline fields. Failures (process already gone, race
// with SIGKILL) silently leave the field empty rather than falling
// back to whatever the client claimed — trust the /proc snapshot or
// nothing, never the client's word.
func snapshotProc(evt *journal.Event) {
	pidStr := fmt.Sprintf("%d", evt.Pid)
	if b, err := os.ReadFile("/proc/" + pidStr + "/comm"); err == nil {
		evt.Comm = strings.TrimRight(string(b), "\n")
	}
	if link, err := os.Readlink("/proc/" + pidStr + "/exe"); err == nil {
		evt.Exe = link
	}
	if b, err := os.ReadFile("/proc/" + pidStr + "/cmdline"); err == nil {
		// cmdline is NUL-separated; join with space for display.
		evt.Cmdline = strings.TrimRight(strings.ReplaceAll(string(b), "\x00", " "), " ")
	}
}

// ---------- Stdout sink (debug / bootstrap) -----------------------------

// StdoutSink prints every received event as one JSON line to stdout.
// Used by `slinit-journald --dry-run` and as the default sink until 3b
// wires the JSONL file writer. Safe for concurrent use — Handle takes
// a pointer write which the runtime serializes at the OS layer.
type StdoutSink struct{}

// Handle marshals evt back to JSONL and writes it to os.Stdout. Errors
// from the write are surfaced so the receiver can bump its dropped
// counter (a broken pipe → parent process died → daemon should exit
// via SIGPIPE, which os.Stdout.Write propagates).
func (StdoutSink) Handle(evt *journal.Event) error {
	data, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(append(data, '\n'))
	return err
}

// Close is a no-op — os.Stdout is owned by the process.
func (StdoutSink) Close() error { return nil }


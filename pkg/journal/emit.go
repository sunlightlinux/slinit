package journal

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// SocketPath is the Unix SOCK_DGRAM address at which slinit-journald
// (or any consumer) receives events. Kept under /run/slinit/ so the
// path is tmpfs — no crash-recovery issue with a stale socket file
// surviving reboot.
const SocketPath = "/run/slinit/events.sock"

// Emitter routes events to (a) an in-process ring buffer that's
// always available to control-socket queries, and (b) a Unix DGRAM
// socket that slinit-journald (or any listener) receives. When no
// listener is bound to the socket, send fails with ECONNREFUSED and
// is silently dropped — this matches sd_journal's fail-open behavior
// and prevents journald crashes from blocking the daemon.
//
// The Emitter is safe for concurrent use. Slinit installs one
// process-wide instance via SetGlobal at PID-1 startup; any package
// (config, service, process, control) can then call the top-level
// Emit function without threading a *Emitter through APIs.
type Emitter struct {
	// buffer is the in-process fallback path. Always populated.
	buffer *EventBuffer

	// conn writes to /run/slinit/events.sock. When nil, only the
	// buffer path is taken. lazily-initialized on first Emit to
	// avoid a bind-time chicken-and-egg with the journald daemon.
	connMu sync.Mutex
	conn   *net.UnixConn

	// socketPath is the target address. Configurable so tests can
	// point at a fixture socket without touching /run/slinit.
	socketPath string

	// dropped counts events that couldn't be sent (marshal error,
	// oversized, socket-write failure). Exported via Stats so ops
	// can detect a broken listener.
	dropped atomic.Uint64
	sent    atomic.Uint64
}

// NewEmitter builds an emitter that pushes events into buf and
// attempts to write to socketPath. Neither is required — an emitter
// with nil buffer just publishes to the socket, and one with unbound
// socket path just fills the buffer. In practice slinit installs
// both, but tests may want one or the other.
func NewEmitter(buf *EventBuffer, socketPath string) *Emitter {
	if socketPath == "" {
		socketPath = SocketPath
	}
	return &Emitter{
		buffer:     buf,
		socketPath: socketPath,
	}
}

// Emit publishes an event through both the in-process buffer and the
// Unix socket. Timestamps and daemon-injected metadata are stamped
// here; callers should NOT pre-populate _pid / _uid / _boot_id and
// friends — Emit will overwrite them with authoritative values.
//
// For slinit-internal callers (driver transport), Emit uses the
// current process's identity for _pid/_uid/_gid because there's no
// SO_PASSCRED ancillary data on an in-process publish. For external
// clients connecting to /run/slinit/events.sock, the server-side
// receiver derives the same metadata from SCM_CREDENTIALS ancillary
// data.
//
// Returns nil on success, or an error describing why publish failed.
// A non-nil return does NOT necessarily mean the event was fully
// lost — the buffer path may have succeeded even if the socket
// write did not.
func (e *Emitter) Emit(evt *Event) error {
	if evt == nil {
		return errors.New("journal: emit nil event")
	}

	// Stamp trusted metadata authoritatively; whatever the caller had
	// set is overwritten. Do this before Validate so validation
	// reflects the final on-wire form.
	e.stampTrusted(evt)

	// Populate timestamps if the caller didn't already (some paths
	// pre-Now to preserve original event time when re-emitting from
	// a buffer).
	if evt.Ts == 0 {
		evt.Now()
	}
	// Default priority to Info if the caller left it at the zero
	// value AND didn't say "please, emerg". Distinguishing between
	// "didn't set" and "explicitly 0" requires a separate sentinel;
	// leaving 0 = emerg loses too many bug reports to be worthwhile,
	// so we treat 0 as info-by-default. Emerg callers can pass
	// PriorityEmergency explicitly and the receiver will trust it.
	// The trade-off is documented on Event.Prio.
	if evt.Prio == 0 {
		evt.Prio = PriorityInfo
	}

	if err := evt.Validate(); err != nil {
		e.dropped.Add(1)
		return fmt.Errorf("journal: emit: %w", err)
	}

	// Fan out. Buffer first (never fails), then socket (best-effort).
	if e.buffer != nil {
		e.buffer.Push(evt)
	}

	if err := e.publishToSocket(evt); err != nil {
		e.dropped.Add(1)
		// Socket-drop is expected when journald isn't running; don't
		// surface as a hard error unless the caller cares. But we do
		// return it so a caller that wants confirmation can check.
		return err
	}

	e.sent.Add(1)
	return nil
}

// stampTrusted overwrites the daemon-injected fields on evt with the
// authoritative values for the current process context.
func (e *Emitter) stampTrusted(evt *Event) {
	// _pid/_uid/_gid come from the current process for in-process
	// emits. External clients get them via SO_PASSCRED on the
	// receiver side.
	evt.Pid = os.Getpid()
	evt.Uid = os.Getuid()
	evt.Gid = os.Getgid()

	// Boot ID + machine ID + hostname come from the cache populated
	// at InitIDs time. If InitIDs was never called, these will panic
	// — that's a programming error (main should always Init before
	// any Emit).
	evt.BootID = BootID()
	evt.MachineID = MachineID()
	evt.Hostname = Hostname()

	// Default Transport = driver for in-process emits. Callers that
	// know better (kmsg reader → kernel, stdout reader → stdout) set
	// it explicitly before calling Emit; we don't overwrite a
	// non-empty value.
	if evt.Transport == "" {
		evt.Transport = TransportDriver
	}
}

// publishToSocket attempts to send the event as a JSONL datagram.
// Returns nil on success, an error on marshal failure or socket-write
// failure. A "no such file or directory" or "connection refused"
// error is normal (no listener bound) — the caller should treat it
// as a soft failure worth counting but not logging.
func (e *Emitter) publishToSocket(evt *Event) error {
	data, err := evt.MarshalJSONL()
	if err != nil {
		return err
	}

	conn, err := e.connect()
	if err != nil {
		return err
	}

	// SetWriteDeadline so a stuck listener can't block emit-side
	// callers indefinitely. 10ms is generous for a local Unix
	// socket write.
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Millisecond))

	// One datagram per event. UDP-like semantics: the receiver either
	// gets the whole packet or nothing, no partial writes to worry
	// about.
	_, err = conn.Write(data)
	return err
}

// connect returns the cached socket connection, dialing on first use
// and after ECONNREFUSED / ENOENT (journald not yet up, or was
// restarted). We keep the conn open across calls to avoid the
// per-emit connect cost on a high-traffic system.
func (e *Emitter) connect() (*net.UnixConn, error) {
	e.connMu.Lock()
	defer e.connMu.Unlock()

	if e.conn != nil {
		return e.conn, nil
	}

	addr := &net.UnixAddr{Name: e.socketPath, Net: "unixgram"}
	// DialUnix on unixgram takes a nil laddr — the kernel assigns an
	// autobind address for our end.
	conn, err := net.DialUnix("unixgram", nil, addr)
	if err != nil {
		return nil, err
	}
	e.conn = conn
	return conn, nil
}

// resetConn drops the cached socket, forcing the next Emit to dial
// again. Called by the daemon when it observes a persistent write
// failure (listener died and restarted).
func (e *Emitter) resetConn() {
	e.connMu.Lock()
	if e.conn != nil {
		_ = e.conn.Close()
		e.conn = nil
	}
	e.connMu.Unlock()
}

// Stats returns cumulative counts of successfully-sent and dropped
// events. Not authoritative for observability of buffer overrun
// (that's EventBuffer.Seq); this only tracks the socket path.
func (e *Emitter) Stats() (sent, dropped uint64) {
	return e.sent.Load(), e.dropped.Load()
}

// Close releases the cached socket connection. Safe to call
// multiple times.
func (e *Emitter) Close() error {
	e.connMu.Lock()
	defer e.connMu.Unlock()
	if e.conn != nil {
		err := e.conn.Close()
		e.conn = nil
		return err
	}
	return nil
}

// ---------- process-wide singleton --------------------------------------

// Slinit runs a single Emitter for the whole process. Rather than
// thread *Emitter through every callsite that wants to publish an
// event (pkg/config, pkg/service, pkg/process, pkg/control all
// have reasons to emit), we install a package-level default that
// SetGlobal populates at startup.
//
// Packages that don't set a global still work — Emit falls back to a
// no-op that returns nil silently. This preserves testability: a
// test can leave the global unset and just verify the code doesn't
// crash on emit calls.
var (
	globalMu sync.RWMutex
	global   *Emitter
)

// SetGlobal installs the process-wide default emitter. Typically
// called once at slinit startup after InitIDs. Passing nil clears
// the global (used by tests).
func SetGlobal(e *Emitter) {
	globalMu.Lock()
	global = e
	globalMu.Unlock()
}

// Emit publishes an event via the process-wide default emitter. If no
// global emitter has been set, the call is silently ignored so
// package code (pkg/service, pkg/config) can emit without worrying
// about init ordering.
//
// Errors from the underlying Emit are dropped here to keep call
// sites simple; use the Emitter method form when the caller cares
// about the outcome.
func Emit(evt *Event) {
	globalMu.RLock()
	e := global
	globalMu.RUnlock()
	if e == nil {
		return
	}
	_ = e.Emit(evt)
}

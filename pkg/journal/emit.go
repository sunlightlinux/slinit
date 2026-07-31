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
// always available to control-socket queries, (b) a Unix DGRAM
// socket that slinit-journald (or any listener) receives, and (c) a
// set of live in-process subscribers (used by slinit-journalctl -f
// via the CmdJournalSubscribe control opcode). When no listener is
// bound to the socket, send fails with ECONNREFUSED and is silently
// dropped — this matches sd_journal's fail-open behavior and prevents
// journald crashes from blocking the daemon.
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

	// Live in-process subscribers. Every Emit fans a *pointer* to the
	// event out to each subscriber's channel. When a channel is full
	// the emit path drops the event for that subscriber (never
	// blocks) — the counter tracks per-subscriber overrun so slow
	// readers surface in Stats() without stalling the daemon.
	subsMu sync.RWMutex
	subs   map[*subscription]struct{}
}

type subscription struct {
	ch      chan *Event
	dropped atomic.Uint64
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
		subs:       make(map[*subscription]struct{}),
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

	// Fan out. Buffer first (never fails), then subscribers (never
	// blocks — full channel drops), then socket (best-effort).
	if e.buffer != nil {
		e.buffer.Push(evt)
	}
	e.fanoutSubs(evt)

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

// fanoutSubs pushes evt to every live subscriber. Non-blocking: when
// a channel is full the event is skipped for that subscriber (dropped
// counter incremented). Uses RLock so multiple emits can fan out
// concurrently — the map is only written by Subscribe/Unsubscribe.
func (e *Emitter) fanoutSubs(evt *Event) {
	e.subsMu.RLock()
	defer e.subsMu.RUnlock()
	for s := range e.subs {
		select {
		case s.ch <- evt:
		default:
			s.dropped.Add(1)
		}
	}
}

// Subscribe registers a channel that receives every subsequent Emit.
// The returned channel is buffered with `bufferSize` slots; when the
// buffer fills, further events are silently dropped for this
// subscriber (each drop is counted internally). Zero or negative
// bufferSize is clamped to 64 — smaller than that risks storm loss
// during boot when hundreds of state transitions fire back-to-back.
//
// The returned cancel function unsubscribes and closes the channel;
// call it (typically deferred) when the subscriber is done. It is
// safe to call cancel more than once.
func (e *Emitter) Subscribe(bufferSize int) (<-chan *Event, func()) {
	if bufferSize <= 0 {
		bufferSize = 64
	}
	sub := &subscription{ch: make(chan *Event, bufferSize)}
	e.subsMu.Lock()
	e.subs[sub] = struct{}{}
	e.subsMu.Unlock()

	var cancelOnce sync.Once
	cancel := func() {
		cancelOnce.Do(func() {
			e.subsMu.Lock()
			delete(e.subs, sub)
			e.subsMu.Unlock()
			close(sub.ch)
		})
	}
	return sub.ch, cancel
}

// SubscriberCount returns the number of active subscribers. Diagnostic
// only — surfaced by tests and future stats reporting to detect
// stuck subscriptions.
func (e *Emitter) SubscriberCount() int {
	e.subsMu.RLock()
	defer e.subsMu.RUnlock()
	return len(e.subs)
}

// stampTrusted overwrites the daemon-injected fields on evt with the
// authoritative values for the current process context.
//
// Kernel-transport events are a special case: they originate in the
// kernel, not in a userspace process, so stamping slinit's own PID/
// UID/GID/Comm/Exe/Cmdline onto them would be a lie. Consumers seeing
// `_pid=1` on a kmsg event would reasonably conclude PID 1 emitted
// it — misleading. For TransportKernel we leave the userspace-
// identity fields at their zero values so renderers (which already
// omit empty fields) hide them.
func (e *Emitter) stampTrusted(evt *Event) {
	// Default Transport = driver for in-process emits. Callers that
	// know better (kmsg reader → kernel, stdout reader → stdout) set
	// it explicitly before calling Emit; we don't overwrite a
	// non-empty value. Resolved before the identity block so the
	// kernel-transport branch sees the caller's choice.
	if evt.Transport == "" {
		evt.Transport = TransportDriver
	}

	// Userspace identity: skip for kernel-origin events (see doc).
	if evt.Transport != TransportKernel {
		evt.Pid = os.Getpid()
		evt.Uid = os.Getuid()
		evt.Gid = os.Getgid()
	}

	// Boot ID + machine ID + hostname always come from the cache
	// populated at InitIDs time. If InitIDs was never called, these
	// will panic — that's a programming error (main should always
	// Init before any Emit). Kernel events still carry them; they're
	// per-boot / per-host metadata, not per-process identity.
	evt.BootID = BootID()
	evt.MachineID = MachineID()
	evt.Hostname = Hostname()
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

// Buffer exposes the emitter's in-process ring buffer so external
// consumers (control-socket handlers replying to slinit-journalctl
// queries) can Query without threading the emitter through APIs. May
// be nil when the emitter was constructed without a buffer.
func (e *Emitter) Buffer() *EventBuffer { return e.buffer }

// GlobalBuffer returns the process-wide emitter's ring buffer, or
// nil when SetGlobal has not been called or the emitter has no
// buffer. Callers (pkg/control) use this to answer JournalQuery
// requests without an import cycle back to cmd/slinit/main.go.
func GlobalBuffer() *EventBuffer {
	globalMu.RLock()
	defer globalMu.RUnlock()
	if global == nil {
		return nil
	}
	return global.buffer
}

// GlobalSubscribe registers a follower against the process-wide
// emitter, returning the receive channel and the unsubscribe
// function. When no global emitter is installed (tests, embedded
// mode) the returned channel is nil and cancel is a no-op — callers
// should check the channel for nil before selecting on it.
func GlobalSubscribe(bufferSize int) (<-chan *Event, func()) {
	globalMu.RLock()
	e := global
	globalMu.RUnlock()
	if e == nil {
		return nil, func() {}
	}
	return e.Subscribe(bufferSize)
}

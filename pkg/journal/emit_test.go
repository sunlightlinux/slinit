package journal

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// emitterFixture returns a fresh emitter wired to a temp socket + a
// listener goroutine that captures received datagrams. Callers use
// receive() to pull the next one.
type emitterFixture struct {
	t        *testing.T
	emitter  *Emitter
	buffer   *EventBuffer
	sockPath string

	listener *net.UnixConn
	recvMu   sync.Mutex
	recvBuf  [][]byte
}

func newEmitterFixture(t *testing.T) *emitterFixture {
	t.Helper()
	// InitIDs is a global — but its cache doesn't survive between
	// test runs cleanly if a prior test left it set. Reset + init
	// with a stable hostname so trusted metadata is deterministic.
	resetIDsForTest()
	if err := InitIDs("test-host"); err != nil {
		t.Skipf("InitIDs failed: %v", err)
	}

	dir := t.TempDir()
	sock := filepath.Join(dir, "events.sock")

	// Bind the listener first so DialUnix from Emit succeeds
	// immediately. We use unixgram for datagram semantics matching
	// the production socket.
	addr := &net.UnixAddr{Name: sock, Net: "unixgram"}
	conn, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		t.Fatalf("listen unixgram: %v", err)
	}

	buf := NewEventBuffer(MinBufferCap)
	fx := &emitterFixture{
		t:        t,
		emitter:  NewEmitter(buf, sock),
		buffer:   buf,
		sockPath: sock,
		listener: conn,
	}

	// Background reader — drains datagrams into recvBuf.
	go func() {
		msg := make([]byte, 64*1024)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, err := conn.Read(msg)
			if err != nil {
				return
			}
			fx.recvMu.Lock()
			cp := make([]byte, n)
			copy(cp, msg[:n])
			fx.recvBuf = append(fx.recvBuf, cp)
			fx.recvMu.Unlock()
		}
	}()

	t.Cleanup(func() {
		_ = fx.emitter.Close()
		_ = conn.Close()
		_ = os.Remove(sock)
		SetGlobal(nil)
		resetIDsForTest()
	})
	return fx
}

// receive pulls the next captured datagram, waiting up to 1s.
func (fx *emitterFixture) receive() []byte {
	fx.t.Helper()
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		fx.recvMu.Lock()
		if len(fx.recvBuf) > 0 {
			d := fx.recvBuf[0]
			fx.recvBuf = fx.recvBuf[1:]
			fx.recvMu.Unlock()
			return d
		}
		fx.recvMu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	fx.t.Fatalf("no datagram received within 1s")
	return nil
}

func TestEmitFillsBufferAndSocket(t *testing.T) {
	fx := newEmitterFixture(t)

	err := fx.emitter.Emit(&Event{
		Msg:  "hello",
		Prio: PriorityWarning,
		Unit: "test-svc",
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	// Buffer path.
	events, _ := fx.buffer.Snapshot()
	if len(events) != 1 {
		t.Fatalf("buffer: got %d events, want 1", len(events))
	}
	if events[0].Msg != "hello" || events[0].Unit != "test-svc" {
		t.Errorf("buffer event corrupt: %+v", events[0])
	}

	// Socket path.
	data := fx.receive()
	var got Event
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("received datagram not JSON: %v (raw=%q)", err, data)
	}
	if got.Msg != "hello" || got.Unit != "test-svc" {
		t.Errorf("received event corrupt: %+v", got)
	}
}

func TestEmitStampsTrustedMetadata(t *testing.T) {
	fx := newEmitterFixture(t)

	// Client tries to spoof trusted fields — they should be
	// overwritten by Emit.
	err := fx.emitter.Emit(&Event{
		Msg:      "x",
		Pid:      9999,       // should be replaced with os.Getpid()
		BootID:   "spoofed",  // should be replaced with real boot id
		Hostname: "wrong",    // should be replaced with test-host
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	events, _ := fx.buffer.Snapshot()
	if len(events) != 1 {
		t.Fatalf("buffer len=%d", len(events))
	}
	e := events[0]

	if e.Pid == 9999 {
		t.Errorf("_pid should be overwritten with real pid; got %d", e.Pid)
	}
	if e.Pid != os.Getpid() {
		t.Errorf("_pid should be os.Getpid()=%d; got %d", os.Getpid(), e.Pid)
	}
	if e.BootID == "spoofed" {
		t.Errorf("_boot_id should be overwritten; got %q", e.BootID)
	}
	if !isValidID(e.BootID) {
		t.Errorf("_boot_id malformed: %q", e.BootID)
	}
	if e.Hostname != "test-host" {
		t.Errorf("_hostname: got %q, want test-host", e.Hostname)
	}
	if e.Transport != TransportDriver {
		t.Errorf("default transport should be driver; got %q", e.Transport)
	}
}

func TestEmitPreservesExplicitTransport(t *testing.T) {
	fx := newEmitterFixture(t)

	if err := fx.emitter.Emit(&Event{
		Msg:       "kernel msg",
		Transport: TransportKernel,
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	events, _ := fx.buffer.Snapshot()
	if len(events) != 1 || events[0].Transport != TransportKernel {
		t.Errorf("explicit transport lost: %+v", events)
	}
}

func TestEmitDefaultsPriority(t *testing.T) {
	fx := newEmitterFixture(t)

	// Zero priority is ambiguous (emerg or unset?) — emit treats it
	// as unset and defaults to info to avoid drowning journal in
	// spurious emerg entries from callers that forgot to set prio.
	if err := fx.emitter.Emit(&Event{Msg: "x"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	events, _ := fx.buffer.Snapshot()
	if events[0].Prio != PriorityInfo {
		t.Errorf("default priority: got %v, want Info", events[0].Prio)
	}
}

func TestEmitNilEventReturnsError(t *testing.T) {
	fx := newEmitterFixture(t)
	if err := fx.emitter.Emit(nil); err == nil {
		t.Errorf("Emit(nil) should return an error")
	}
}

func TestEmitStatsCount(t *testing.T) {
	fx := newEmitterFixture(t)

	for i := 0; i < 5; i++ {
		_ = fx.emitter.Emit(&Event{Msg: "x"})
	}
	// Give the listener goroutine a moment to drain.
	time.Sleep(50 * time.Millisecond)

	sent, dropped := fx.emitter.Stats()
	if sent != 5 {
		t.Errorf("sent = %d, want 5", sent)
	}
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0", dropped)
	}
}

func TestEmitNoListenerFallsBack(t *testing.T) {
	// Point the emitter at a socket that nobody listens on. Emit
	// should still fill the buffer (stat: sent+0, dropped+1).
	resetIDsForTest()
	if err := InitIDs("test-host"); err != nil {
		t.Skipf("InitIDs failed: %v", err)
	}
	defer resetIDsForTest()

	buf := NewEventBuffer(MinBufferCap)
	dir := t.TempDir()
	em := NewEmitter(buf, filepath.Join(dir, "nobody-listens.sock"))
	defer em.Close()

	err := em.Emit(&Event{Msg: "orphan"})
	// Emit surfaces the socket error to the caller, but buffer path
	// should have succeeded.
	if err == nil {
		t.Errorf("expected socket-side error when no listener")
	}
	events, _ := buf.Snapshot()
	if len(events) != 1 || events[0].Msg != "orphan" {
		t.Errorf("buffer path should succeed even when socket fails; got %+v", events)
	}
}

func TestGlobalEmitNoOpWhenUnset(t *testing.T) {
	SetGlobal(nil)
	// Should not panic, should not error, should just do nothing.
	Emit(&Event{Msg: "x"})
}

func TestGlobalEmitWorksWhenSet(t *testing.T) {
	fx := newEmitterFixture(t)
	SetGlobal(fx.emitter)

	Emit(&Event{Msg: "via-global"})

	events, _ := fx.buffer.Snapshot()
	if len(events) != 1 || events[0].Msg != "via-global" {
		t.Errorf("global emit lost message: %+v", events)
	}
}

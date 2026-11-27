package journal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// lifecycleEmitter emits one event per millisecond, numbered, and
// records when each was emitted, so a test can tell how long after a
// listener appeared the first event reached it.
type lifecycleEmitter struct {
	t    *testing.T
	e    *Emitter
	n    int
	when []time.Time
}

func (l *lifecycleEmitter) emit() {
	l.when = append(l.when, time.Now())
	l.e.Emit(&Event{Msg: fmt.Sprintf("event %d", l.n), Fields: map[string]string{"SLINIT_TARGET_PID": "7"}})
	l.n++
}

// untilDelivered emits until the listener reads one, and returns the
// number of the event read and how long after since it was emitted.
func (l *lifecycleEmitter) untilDelivered(ln interface {
	SetReadDeadline(time.Time) error
	Read([]byte) (int, error)
}, since time.Time, limit time.Duration) (int, time.Duration, []byte) {
	l.t.Helper()
	buf := make([]byte, 64*1024)
	for time.Since(since) < limit {
		l.emit()
		ln.SetReadDeadline(time.Now().Add(time.Millisecond))
		if n, err := ln.Read(buf); err == nil {
			var idx int
			ev, err := UnmarshalEvent(buf[:n])
			if err != nil {
				l.t.Fatalf("listener got an undecodable datagram %q: %v", buf[:n], err)
			}
			fmt.Sscanf(ev.Msg, "event %d", &idx)
			return idx, l.when[idx].Sub(since), append([]byte(nil), buf[:n]...)
		}
	}
	l.t.Fatalf("no event reached the listener within %v", limit)
	return 0, 0, nil
}

// The socket path is an unwatched rendezvous: slinit-journald may be
// absent when PID 1 starts emitting, restart, or have its socket file
// replaced. Every event reaches the ring whatever the socket does; the
// datagram is json.Marshal of the ring's event; and a listener that
// appears is reached within the redial bounds (emitProbeInterval when
// the socket file was missing, emitRedialBackoff when it refused).
func TestEmitDeliveryAcrossListenerLifecycle(t *testing.T) {
	initIDsForStallTest(t)
	p := filepath.Join(t.TempDir(), "events.sock")
	const ringCap = 1 << 16 // holds every event this test emits
	buf := NewEventBuffer(ringCap)
	l := &lifecycleEmitter{t: t, e: NewEmitter(buf, p)}
	defer l.e.Close()
	slack := 100 * time.Millisecond

	// journald absent at start, then started.
	for i := 0; i < 20; i++ {
		l.emit()
	}
	ln := listenUnixgram(t, p)
	idx, delay, wire := l.untilDelivered(ln, time.Now(), emitProbeInterval+slack)
	t.Logf("absent at start: first delivered event %d, emitted %v after the bind", idx, delay)
	if delay > emitProbeInterval+slack {
		t.Errorf("first event after the listener appeared was emitted %v after it", delay)
	}
	evs, _ := buf.Snapshot()
	want, _ := json.Marshal(evs[idx])
	if !bytes.Equal(wire, want) {
		t.Errorf("datagram differs from the ring's event:\n got %q\nwant %q", wire, want)
	}

	// journald restarted mid-stream: old socket closed and removed,
	// a new one bound at the same path a little later.
	ln.Close()
	os.Remove(p)
	for i := 0; i < 20; i++ {
		l.emit()
		time.Sleep(time.Millisecond)
	}
	ln2 := listenUnixgram(t, p)
	idx, delay, _ = l.untilDelivered(ln2, time.Now(), emitRedialBackoff+slack)
	t.Logf("restarted: first delivered event %d, emitted %v after the bind", idx, delay)
	if delay > emitRedialBackoff+slack {
		t.Errorf("first event after the restart was emitted %v after the bind", delay)
	}

	// Socket path replaced under a live listener: the emitter stays
	// connected to the old socket until it refuses, then reaches the
	// new one.
	tmp := p + ".new"
	ln3 := listenUnixgram(t, tmp)
	if err := os.Rename(tmp, p); err != nil {
		t.Fatal(err)
	}
	idx, _, _ = l.untilDelivered(ln2, time.Now(), slack)
	t.Logf("replaced: old listener still gets event %d", idx)
	ln2.Close()
	idx, delay, _ = l.untilDelivered(ln3, time.Now(), emitRedialBackoff+slack)
	t.Logf("replaced: new listener's first event %d, emitted %v after the old one closed", idx, delay)

	// The ring has every event, in order, whatever the socket did.
	evs, seq := buf.Snapshot()
	if int(seq) != l.n || len(evs) != l.n {
		t.Fatalf("ring has seq %d, %d events; emitted %d", seq, len(evs), l.n)
	}
	for i, ev := range evs {
		if w := fmt.Sprintf("event %d", i); ev.Msg != w {
			t.Fatalf("ring slot %d holds %q, want %q", i, ev.Msg, w)
		}
	}
}

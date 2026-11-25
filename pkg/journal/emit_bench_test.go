package journal

import (
	"net"
	"path/filepath"
	"testing"
)

// benchStateEvent is the shape of the event a service state transition
// emits (pkg/service emitJournalStateEvent).
func benchStateEvent() *Event {
	return &Event{
		Unit:      "sshd",
		Prio:      PriorityInfo,
		Transport: TransportDriver,
		Msg:       `service "sshd": STARTED`,
		Fields: map[string]string{
			"SLINIT_EVENT":         "STARTED",
			"SLINIT_SERVICE_STATE": "STARTED",
			"SLINIT_INVOCATION_ID": "0123456789abcdef0123456789abcdef",
			"SLINIT_TARGET_PID":    "1234",
		},
	}
}

// benchListener binds a datagram socket that drains everything sent
// to it, as slinit-journald does.
func benchListener(b *testing.B) string {
	p := filepath.Join(b.TempDir(), "ev.sock")
	l, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: p, Net: "unixgram"})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { l.Close() })
	_ = l.SetReadBuffer(8 << 20)
	go func() {
		buf := make([]byte, 70000)
		for {
			if _, err := l.Read(buf); err != nil {
				return
			}
		}
	}()
	return p
}

// BenchmarkEmit measures one state-transition emit, which runs with
// the scheduling lock held, with and without a listener bound.
func BenchmarkEmit(b *testing.B) {
	if err := InitIDs("h"); err != nil {
		b.Skip(err)
	}
	b.Run("nolistener", func(b *testing.B) {
		e := NewEmitter(NewEventBuffer(4096), filepath.Join(b.TempDir(), "none.sock"))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = e.Emit(benchStateEvent())
		}
	})
	b.Run("listener", func(b *testing.B) {
		e := NewEmitter(NewEventBuffer(4096), benchListener(b))
		defer e.Close()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = e.Emit(benchStateEvent())
		}
	})
}

func BenchmarkStampTrusted(b *testing.B) {
	if err := InitIDs("h"); err != nil {
		b.Skip(err)
	}
	e := NewEmitter(nil, "")
	ev := benchStateEvent()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		e.stampTrusted(ev)
	}
}

func BenchmarkEventNow(b *testing.B) {
	ev := benchStateEvent()
	for i := 0; i < b.N; i++ {
		ev.Now()
	}
}

// BenchmarkConnectNoListener is connect inside the redial backoff
// after a dial found no socket file (ENOENT).
func BenchmarkConnectNoListener(b *testing.B) {
	e := NewEmitter(nil, filepath.Join(b.TempDir(), "none.sock"))
	for i := 0; i < b.N; i++ {
		_, _ = e.connect()
	}
}

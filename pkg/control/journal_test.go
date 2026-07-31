package control

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// setJournalGlobalForTest installs a fresh buffer as the journal global
// for the duration of the test and restores whatever was there before
// (typically nil in unit tests). Also clears it after the test so a
// later test starts from a clean slate.
func setJournalGlobalForTest(t *testing.T, capacity int) *journal.EventBuffer {
	t.Helper()
	buf := journal.NewEventBuffer(capacity)
	e := journal.NewEmitter(buf, "/dev/null-nope")
	journal.SetGlobal(e)
	t.Cleanup(func() {
		journal.SetGlobal(nil)
	})
	return buf
}

// pushTestEvent bypasses journal.Emit's socket/timestamp/validation
// dance so tests can seed a buffer with hand-crafted events. Ts is
// stamped so Validate would pass — the buffer itself doesn't validate,
// but consistency helps future maintainers.
func pushTestEvent(buf *journal.EventBuffer, unit string, prio journal.Priority, msg string, transport journal.Transport, ts int64) {
	buf.Push(&journal.Event{
		Ts:        ts,
		Mts:       ts,
		Unit:      unit,
		Prio:      prio,
		Msg:       msg,
		Transport: transport,
		BootID:    "0123456789abcdef0123456789abcdef",
		MachineID: "fedcba9876543210fedcba9876543210",
	})
}

// readJournalStream consumes RplyJournalEntry packets until a
// RplyJournalDone or RplyJournalErr, and returns the decoded events
// plus the terminating packet type.
func readJournalStream(t *testing.T, conn interface {
	Read(p []byte) (n int, err error)
	Write(p []byte) (n int, err error)
}) (events []*journal.Event, terminator uint8, errPayload []byte) {
	t.Helper()
	for {
		typ, payload, err := ReadPacket(conn)
		if err != nil {
			t.Fatalf("read reply: %v", err)
		}
		switch typ {
		case RplyJournalEntry:
			evt, err := journal.UnmarshalEvent(payload)
			if err != nil {
				t.Fatalf("bad entry payload: %v", err)
			}
			events = append(events, evt)
		case RplyJournalDone:
			return events, typ, nil
		case RplyJournalErr:
			return events, typ, payload
		default:
			t.Fatalf("unexpected reply type %d", typ)
		}
	}
}

func TestJournalQueryEmpty(t *testing.T) {
	setJournalGlobalForTest(t, 32)

	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	if err := WritePacket(conn, CmdJournalQuery, nil); err != nil {
		t.Fatal(err)
	}
	events, term, _ := readJournalStream(t, conn)
	if term != RplyJournalDone {
		t.Fatalf("expected Done, got %d", term)
	}
	if len(events) != 0 {
		t.Fatalf("expected no events, got %d", len(events))
	}
}

func TestJournalQueryAllEvents(t *testing.T) {
	buf := setJournalGlobalForTest(t, 32)
	pushTestEvent(buf, "cron", journal.PriorityInfo, "hello", journal.TransportDriver, 1_000_000_000)
	pushTestEvent(buf, "sshd", journal.PriorityError, "boom", journal.TransportStdout, 2_000_000_000)

	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	if err := WritePacket(conn, CmdJournalQuery, nil); err != nil {
		t.Fatal(err)
	}
	events, term, _ := readJournalStream(t, conn)
	if term != RplyJournalDone {
		t.Fatalf("expected Done, got %d", term)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Unit != "cron" || events[1].Unit != "sshd" {
		t.Fatalf("unexpected order: %q, %q", events[0].Unit, events[1].Unit)
	}
	// Chronological order preserved.
	if events[0].Ts >= events[1].Ts {
		t.Fatalf("expected chronological order")
	}
}

func TestJournalQueryFilterUnit(t *testing.T) {
	buf := setJournalGlobalForTest(t, 32)
	pushTestEvent(buf, "cron", journal.PriorityInfo, "a", journal.TransportDriver, 1)
	pushTestEvent(buf, "sshd", journal.PriorityInfo, "b", journal.TransportDriver, 2)
	pushTestEvent(buf, "cron", journal.PriorityInfo, "c", journal.TransportDriver, 3)

	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	req := JournalQueryRequest{Units: []string{"cron"}}
	payload, _ := json.Marshal(req)
	if err := WritePacket(conn, CmdJournalQuery, payload); err != nil {
		t.Fatal(err)
	}
	events, _, _ := readJournalStream(t, conn)
	if len(events) != 2 {
		t.Fatalf("expected 2 cron events, got %d", len(events))
	}
	for _, e := range events {
		if e.Unit != "cron" {
			t.Fatalf("filter leak: unit=%q", e.Unit)
		}
	}
}

func TestJournalQueryFilterPriority(t *testing.T) {
	buf := setJournalGlobalForTest(t, 32)
	pushTestEvent(buf, "a", journal.PriorityError, "err", journal.TransportDriver, 1)
	pushTestEvent(buf, "b", journal.PriorityInfo, "info", journal.TransportDriver, 2)
	pushTestEvent(buf, "c", journal.PriorityWarning, "warn", journal.TransportDriver, 3)

	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// MinPriority=err (3) keeps err/crit/alert/emerg and drops warn/info/debug.
	req := JournalQueryRequest{MinPriority: int(journal.PriorityError), PrioritySet: true}
	payload, _ := json.Marshal(req)
	if err := WritePacket(conn, CmdJournalQuery, payload); err != nil {
		t.Fatal(err)
	}
	events, _, _ := readJournalStream(t, conn)
	if len(events) != 1 {
		t.Fatalf("expected 1 err event, got %d", len(events))
	}
	if events[0].Unit != "a" {
		t.Fatalf("expected unit=a, got %q", events[0].Unit)
	}
}

func TestJournalQueryFilterTransport(t *testing.T) {
	buf := setJournalGlobalForTest(t, 32)
	pushTestEvent(buf, "a", journal.PriorityInfo, "m1", journal.TransportDriver, 1)
	pushTestEvent(buf, "b", journal.PriorityInfo, "m2", journal.TransportStdout, 2)
	pushTestEvent(buf, "c", journal.PriorityInfo, "m3", journal.TransportKernel, 3)

	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	req := JournalQueryRequest{Transports: []string{"stdout", "kernel"}}
	payload, _ := json.Marshal(req)
	if err := WritePacket(conn, CmdJournalQuery, payload); err != nil {
		t.Fatal(err)
	}
	events, _, _ := readJournalStream(t, conn)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
}

func TestJournalQueryFilterTimeRange(t *testing.T) {
	buf := setJournalGlobalForTest(t, 32)
	pushTestEvent(buf, "a", journal.PriorityInfo, "early", journal.TransportDriver, 100)
	pushTestEvent(buf, "b", journal.PriorityInfo, "mid", journal.TransportDriver, 500)
	pushTestEvent(buf, "c", journal.PriorityInfo, "late", journal.TransportDriver, 900)

	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	req := JournalQueryRequest{Since: 200, Until: 800}
	payload, _ := json.Marshal(req)
	if err := WritePacket(conn, CmdJournalQuery, payload); err != nil {
		t.Fatal(err)
	}
	events, _, _ := readJournalStream(t, conn)
	if len(events) != 1 || events[0].Msg != "mid" {
		t.Fatalf("expected 1 event 'mid', got %d events", len(events))
	}
}

func TestJournalQueryLimit(t *testing.T) {
	buf := setJournalGlobalForTest(t, 32)
	for i := int64(1); i <= 10; i++ {
		pushTestEvent(buf, "u", journal.PriorityInfo, "", journal.TransportDriver, i)
	}

	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	req := JournalQueryRequest{Limit: 3}
	payload, _ := json.Marshal(req)
	if err := WritePacket(conn, CmdJournalQuery, payload); err != nil {
		t.Fatal(err)
	}
	events, _, _ := readJournalStream(t, conn)
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	// Most-recent-kept semantics: Ts should be 8, 9, 10.
	if events[0].Ts != 8 || events[2].Ts != 10 {
		t.Fatalf("wrong tail: got %d..%d", events[0].Ts, events[2].Ts)
	}
}

func TestJournalQueryBadJSON(t *testing.T) {
	setJournalGlobalForTest(t, 32)

	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	if err := WritePacket(conn, CmdJournalQuery, []byte("not json")); err != nil {
		t.Fatal(err)
	}
	events, term, payload := readJournalStream(t, conn)
	if term != RplyJournalErr {
		t.Fatalf("expected Err, got %d", term)
	}
	if len(events) != 0 {
		t.Fatalf("expected no events on error, got %d", len(events))
	}
	if len(payload) == 0 {
		t.Fatalf("expected diagnostic payload")
	}
}

func TestJournalSubscribeBacklogThenLive(t *testing.T) {
	// Install a REAL emitter so both backlog (buffer) and live
	// (fanoutSubs) paths are exercised end-to-end.
	journal.InitIDs("testhost")
	buf := journal.NewEventBuffer(32)
	e := journal.NewEmitter(buf, "/dev/null-nope")
	journal.SetGlobal(e)
	t.Cleanup(func() { journal.SetGlobal(nil) })

	// Seed 2 backlog events (bypass Emit so we don't fan out to a
	// subscriber that doesn't exist yet).
	pushTestEvent(buf, "cron", journal.PriorityInfo, "past-1", journal.TransportDriver, 1)
	pushTestEvent(buf, "cron", journal.PriorityInfo, "past-2", journal.TransportDriver, 2)

	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	if err := WritePacket(conn, CmdJournalSubscribe, nil); err != nil {
		t.Fatal(err)
	}

	// Read backlog first.
	got := make([]string, 0, 4)
	readOne := func() string {
		typ, body, err := ReadPacket(conn)
		if err != nil {
			t.Fatal(err)
		}
		if typ != RplyJournalEntry {
			t.Fatalf("expected Entry, got %d", typ)
		}
		evt, err := journal.UnmarshalEvent(body)
		if err != nil {
			t.Fatal(err)
		}
		return evt.Msg
	}
	got = append(got, readOne(), readOne())
	if got[0] != "past-1" || got[1] != "past-2" {
		t.Fatalf("backlog wrong: %v", got)
	}

	// Now emit a live event — the subscriber should see it.
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = e.Emit(&journal.Event{Msg: "live-1", Unit: "cron", Prio: journal.PriorityInfo})
	}()
	got = append(got, readOne())
	if got[2] != "live-1" {
		t.Fatalf("live event wrong: %v", got)
	}
}

func TestJournalSubscribeFilterMatches(t *testing.T) {
	journal.InitIDs("testhost")
	buf := journal.NewEventBuffer(32)
	e := journal.NewEmitter(buf, "/dev/null-nope")
	journal.SetGlobal(e)
	t.Cleanup(func() { journal.SetGlobal(nil) })

	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	req := JournalQueryRequest{Units: []string{"sshd"}}
	payload, _ := json.Marshal(req)
	if err := WritePacket(conn, CmdJournalSubscribe, payload); err != nil {
		t.Fatal(err)
	}

	// Give the server a moment to subscribe.
	time.Sleep(50 * time.Millisecond)

	// Emit one matching + one non-matching. Only sshd should come through.
	_ = e.Emit(&journal.Event{Msg: "boom", Unit: "sshd"})
	_ = e.Emit(&journal.Event{Msg: "quiet", Unit: "cron"})

	typ, body, err := ReadPacket(conn)
	if err != nil {
		t.Fatal(err)
	}
	if typ != RplyJournalEntry {
		t.Fatalf("expected Entry, got %d", typ)
	}
	evt, _ := journal.UnmarshalEvent(body)
	if evt.Msg != "boom" || evt.Unit != "sshd" {
		t.Fatalf("filter leak: %+v", evt)
	}
}

func TestJournalQueryNoBuffer(t *testing.T) {
	// Explicitly ensure no global buffer is set.
	journal.SetGlobal(nil)

	server, sockPath := setupTestServer(t)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()

	if err := WritePacket(conn, CmdJournalQuery, nil); err != nil {
		t.Fatal(err)
	}
	_, term, payload := readJournalStream(t, conn)
	if term != RplyJournalErr {
		t.Fatalf("expected Err, got %d", term)
	}
	if len(payload) == 0 {
		t.Fatalf("expected diagnostic payload")
	}
}

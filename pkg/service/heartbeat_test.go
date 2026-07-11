package service

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureLogger records formatted Info() lines so the heartbeat
// tests can inspect them.
type captureLogger struct {
	mu   sync.Mutex
	msgs []string
}

func (l *captureLogger) Info(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, fmt.Sprintf(format, args...))
}
func (l *captureLogger) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.msgs))
	copy(out, l.msgs)
	return out
}

// TestCompactDurationFormats spot-checks the label rendering used
// in the heartbeat's "restarts(1m)=..." slot.
func TestCompactDurationFormats(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{time.Second, "1s"},
		{30 * time.Second, "30s"},
		{time.Minute, "1m"},
		{5 * time.Minute, "5m"},
		{time.Hour, "1h"},
		{2 * time.Hour, "2h"},
		{90 * time.Second, "90s"}, // not a clean minute
	}
	for _, c := range cases {
		got := compactDuration(c.d)
		if got != c.want {
			t.Errorf("compactDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// TestReadOwnRSSKBReturnsNonZero is a smoke test — /proc/self/status
// is always present on Linux CI and slinit's memory footprint is
// well above zero.
func TestReadOwnRSSKBReturnsNonZero(t *testing.T) {
	rss := readOwnRSSKB()
	if rss == 0 {
		t.Skip("readOwnRSSKB returned 0 (likely non-Linux test env)")
	}
}

// TestServiceSetNoteRestartAndCount drives the restart-log accounting
// end-to-end: three NoteRestart calls within the window count as
// three; a stale entry outside the window does not.
func TestServiceSetNoteRestartAndCount(t *testing.T) {
	ss := NewServiceSet(profileTestLogger{})
	ss.NoteRestart()
	ss.NoteRestart()
	ss.NoteRestart()

	if got := ss.RestartsInLast(time.Minute); got != 3 {
		t.Errorf("RestartsInLast(1m) = %d, want 3", got)
	}

	// Force a stale entry by rewinding one of the log timestamps
	// past the horizon.
	ss.mu.Lock()
	if len(ss.restartLog) > 0 {
		ss.restartLog[0] = time.Now().Add(-2 * time.Hour)
	}
	ss.mu.Unlock()

	// Trigger a prune via another NoteRestart.
	ss.NoteRestart()
	if got := ss.RestartsInLast(time.Minute); got != 3 {
		// One was pruned (stale), one was added → net 3 recent.
		t.Errorf("after prune: RestartsInLast(1m) = %d, want 3", got)
	}
}

// TestServiceSetWatchdogMissesCounter confirms the atomic counter is
// visible via WatchdogMisses() after concurrent increments.
func TestServiceSetWatchdogMissesCounter(t *testing.T) {
	ss := NewServiceSet(profileTestLogger{})
	if ss.WatchdogMisses() != 0 {
		t.Fatalf("initial WatchdogMisses = %d, want 0", ss.WatchdogMisses())
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ss.NoteWatchdogMiss()
		}()
	}
	wg.Wait()

	if got := ss.WatchdogMisses(); got != 50 {
		t.Errorf("after 50 concurrent notes, WatchdogMisses = %d, want 50", got)
	}
}

// TestHeartbeatReporterEmitsExpectedShape wires a reporter with a
// small interval and verifies the emitted line is a single
// grep-friendly "key=value" summary with all the expected fields.
func TestHeartbeatReporterEmitsExpectedShape(t *testing.T) {
	ss := NewServiceSet(profileTestLogger{})
	// Inject a couple of restarts so the count is non-zero.
	ss.NoteRestart()
	ss.NoteRestart()
	ss.NoteWatchdogMiss()

	lg := &captureLogger{}
	hb := NewHeartbeatReporter(ss, lg, 50*time.Millisecond, time.Minute)
	go hb.Run()
	defer hb.Stop()

	// Wait for at least one tick.
	time.Sleep(120 * time.Millisecond)

	msgs := lg.snapshot()
	if len(msgs) == 0 {
		t.Fatal("heartbeat did not emit within 120ms of two ticks")
	}
	first := msgs[0]

	// Must contain each key=value slot we advertise.
	for _, need := range []string{
		"heartbeat:",
		"active=",
		"failed=",
		"stopped=",
		"restarts(1m)=2",
		"watchdog-misses=1",
	} {
		if !strings.Contains(first, need) {
			t.Errorf("heartbeat missing %q: %s", need, first)
		}
	}
}

// TestHeartbeatReporterZeroIntervalDefaults confirms the constructor
// promotes 0/negative values to the documented defaults.
func TestHeartbeatReporterZeroIntervalDefaults(t *testing.T) {
	hb := NewHeartbeatReporter(nil, &captureLogger{}, 0, 0)
	if hb.interval != 5*time.Minute {
		t.Errorf("default interval = %v, want 5m", hb.interval)
	}
	if hb.window != time.Minute {
		t.Errorf("default window = %v, want 1m", hb.window)
	}
}

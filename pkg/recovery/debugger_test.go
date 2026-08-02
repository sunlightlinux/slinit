package recovery

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestDebugCharToAction covers the key mapping including Ctrl-B
// (shell) and Ctrl-D (continue) aliases — the whole reason we
// exposed shortcuts.
func TestDebugCharToAction(t *testing.T) {
	cases := []struct {
		in   byte
		want DebugAction
	}{
		{'c', DebugContinue},
		{'C', DebugContinue},
		{0x04, DebugContinue}, // Ctrl-D
		{'s', debugActionShell},
		{'S', debugActionShell},
		{0x02, debugActionShell}, // Ctrl-B
		{'r', DebugReboot},
		{'R', DebugReboot},
		{'p', DebugPoweroff},
		{'P', DebugPoweroff},
		{'f', DebugForceFail},
		{'F', DebugForceFail},
		{'x', DebugContinue}, // unknown → continue (safe default)
		{0x00, DebugContinue},
	}
	for _, c := range cases {
		if got := debugCharToAction(c.in); got != c.want {
			t.Errorf("debugCharToAction(%#x) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestDebugActionStringRoundtrip — public DebugAction values must
// stringify distinctly so log lines are unambiguous.
func TestDebugActionStringRoundtrip(t *testing.T) {
	seen := map[string]DebugAction{}
	for _, a := range []DebugAction{
		DebugContinue, DebugReboot, DebugPoweroff,
		DebugForceFail, DebugTimeout,
	} {
		s := a.String()
		if s == "" {
			t.Errorf("DebugAction(%d) has empty String()", int(a))
		}
		if prev, ok := seen[s]; ok {
			t.Errorf("DebugActions %v and %v both stringify to %q", prev, a, s)
		}
		seen[s] = a
	}
}

// TestRenderDebugMenuIncludesStatus — the boxed menu output must
// carry the caller-supplied status snapshot (in-progress + waiting
// + errors) so the operator sees live state, not a static template.
func TestRenderDebugMenuIncludesStatus(t *testing.T) {
	var buf bytes.Buffer
	snap := StatusSnapshot{
		Elapsed: 3421 * time.Millisecond,
		InProgress: []ServiceInfo{
			{Name: "healthcheck-demo", State: "STARTING", Note: "PID=42"},
		},
		Waiting: []ServiceInfo{
			{Name: "tty", State: "STOPPED"},
		},
		RecentErrors: []string{"disk read failed: EIO"},
	}
	renderDebugMenu(&buf, snap, 30*time.Second)
	out := buf.String()
	for _, want := range []string{
		"BOOT DEBUGGER",
		"healthcheck-demo",
		"STARTING",
		"tty",
		"disk read failed",
		"[c] / Ctrl-D",
		"[s] / Ctrl-B",
		"[f]",
		"[r]",
		"[p]",
		"Auto-continue in 30s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered menu missing %q\nfull output:\n%s", want, out)
		}
	}
}

// TestRenderDebugMenuSkipsEmptyBlocks — empty in-progress / waiting
// / errors sections must not print an empty box that pushes the
// action list off-screen. Verifies the "silently omit when empty"
// contract in renderServiceBlock / renderErrorBlock.
func TestRenderDebugMenuSkipsEmptyBlocks(t *testing.T) {
	var buf bytes.Buffer
	renderDebugMenu(&buf, StatusSnapshot{}, 60*time.Second)
	out := buf.String()
	for _, unwanted := range []string{
		"In progress",
		"Waiting on deps",
		"Recent errors",
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("empty snapshot printed header %q; expected omission\nfull output:\n%s", unwanted, out)
		}
	}
	// Action list still present.
	if !strings.Contains(out, "[c] / Ctrl-D") {
		t.Errorf("action list missing from empty-snapshot menu")
	}
}

// TestDebuggerStartRequiresConsole — passing a non-existent path
// returns an error and leaves the debugger in a not-running state
// (so the caller's "log and continue" fallback works).
func TestDebuggerStartRequiresConsole(t *testing.T) {
	d := NewDebugger(DebuggerOptions{
		ConsolePath: "/nonexistent/console/path",
		StatusFn:    func() StatusSnapshot { return StatusSnapshot{} },
	})
	if err := d.Start(); err == nil {
		t.Fatal("expected error opening bogus console path")
	}
	if d.running.Load() {
		t.Error("running flag stuck true after failed Start")
	}
	// Second Start on a not-running debugger should retry cleanly
	// (still fails, but no panic).
	if err := d.Start(); err == nil {
		t.Fatal("second Start on bogus path also expected to fail")
	}
}

// TestDebuggerStopClosesConsoleFd — Start on a regular file (fifo
// substitute for /dev/console in test) then Stop must close cleanly
// and be idempotent. Reader goroutine must exit.
func TestDebuggerStopClosesConsoleFd(t *testing.T) {
	// Use a regular file as the "console" — setRawMode will bail
	// (not a tty) and return nil, but Start still succeeds and the
	// reader goroutine spins up. Then Stop closes the fd → ReadByte
	// returns error → goroutine exits.
	tmpDir := t.TempDir()
	fakeCon := filepath.Join(tmpDir, "fake-console")
	if err := os.WriteFile(fakeCon, []byte{}, 0o600); err != nil {
		t.Fatalf("create fake console: %v", err)
	}
	d := NewDebugger(DebuggerOptions{
		ConsolePath: fakeCon,
		Timeout:     100 * time.Millisecond,
		StatusFn:    func() StatusSnapshot { return StatusSnapshot{} },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("Start on regular file: %v", err)
	}
	// Give the reader goroutine a moment to spin.
	time.Sleep(50 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		d.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return within 2s (goroutine leak?)")
	}
	// Second Stop is a no-op.
	d.Stop()
}

// TestDispatchForceFailFirstInProgress — [f] must call ForceFailFn
// with the NAME of the first in-progress service from StatusFn.
// Regression guard: if we ever change first-vs-last-vs-oldest the
// test flags it.
func TestDispatchForceFailFirstInProgress(t *testing.T) {
	var calledWith atomic.Value
	d := &Debugger{
		opts: DebuggerOptions{
			StatusFn: func() StatusSnapshot {
				return StatusSnapshot{
					InProgress: []ServiceInfo{
						{Name: "first-svc", State: "STARTING"},
						{Name: "second-svc", State: "STARTING"},
					},
				}
			},
			ForceFailFn: func(name string) error {
				calledWith.Store(name)
				return nil
			},
		},
		tty: os.Stderr, // dispatch may print — use a real Writer
	}
	d.dispatch(DebugForceFail)
	got := calledWith.Load()
	if got == nil {
		t.Fatal("ForceFailFn not called")
	}
	if got.(string) != "first-svc" {
		t.Errorf("ForceFailFn called with %q, want first-svc", got)
	}
}

// TestDispatchForceFailNoInProgressNoOp — when nothing is in
// progress, [f] must not call ForceFailFn (would panic or spuriously
// mutate).
func TestDispatchForceFailNoInProgressNoOp(t *testing.T) {
	called := false
	d := &Debugger{
		opts: DebuggerOptions{
			StatusFn: func() StatusSnapshot { return StatusSnapshot{} },
			ForceFailFn: func(_ string) error {
				called = true
				return nil
			},
		},
		tty: os.Stderr,
	}
	d.dispatch(DebugForceFail)
	if called {
		t.Error("ForceFailFn called with empty in-progress list")
	}
}

// TestTruncString covers the byte-length truncation used to fit
// service names in the box: no-op under n, "…" suffix over n,
// hard cut when n < 3.
func TestTruncString(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly20chars.....", 20, "exactly20chars....."},
		{"long-service-name-that-overflows", 20, "long-service-name-t…"},
		{"ab", 2, "ab"},
		{"tiny", 2, "ti"}, // n<3 hard cut, no ellipsis
	}
	for _, c := range cases {
		if got := truncString(c.in, c.n); got != c.want {
			t.Errorf("truncString(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

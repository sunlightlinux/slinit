package recovery

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

// TestCharToAction covers the char-to-Action mapping including
// the Ctrl-B and Ctrl-D aliases that were the whole reason we
// exposed shortcuts.
func TestCharToAction(t *testing.T) {
	cases := []struct {
		in   byte
		want Action
	}{
		{'r', ActionReboot},
		{'R', ActionReboot},
		{'p', ActionPoweroff},
		{'P', ActionPoweroff},
		{'c', ActionRetry},
		{'C', ActionRetry},
		{0x04, ActionRetry},     // Ctrl-D alias for continue
		{'s', actionShell},      // internal sentinel — recovery loops on it
		{'S', actionShell},
		{0x02, actionShell},     // Ctrl-B alias for shell
		{'x', ActionTimeout},    // unknown key → safest = timeout
		{0x00, ActionTimeout},
	}
	for _, c := range cases {
		if got := charToAction(c.in); got != c.want {
			t.Errorf("charToAction(%#x) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestReadActionWithTimeoutRespondsToLetter verifies the happy
// path: a single-char input mapped correctly via a pipe.
func TestReadActionWithTimeoutRespondsToLetter(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	go func() {
		pw.Write([]byte("r"))
		pw.Close()
	}()
	var out bytes.Buffer
	got := readActionWithTimeout(pr, &out, 2*time.Second)
	if got != ActionReboot {
		t.Fatalf("expected ActionReboot, got %v", got)
	}
}

// TestReadActionWithTimeoutSkipsWhitespace: line-buffered serial
// consoles deliver `r\n`; the reader must consume the newline
// without treating it as a separate keypress.
func TestReadActionWithTimeoutSkipsWhitespace(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	go func() {
		pw.Write([]byte("\n\n  p\n"))
		pw.Close()
	}()
	var out bytes.Buffer
	got := readActionWithTimeout(pr, &out, 2*time.Second)
	if got != ActionPoweroff {
		t.Fatalf("expected ActionPoweroff, got %v", got)
	}
}

// TestReadActionWithTimeoutTimesOut fires the safety-net path when
// no input arrives before the deadline. Keep the timeout short so
// the test runs fast.
func TestReadActionWithTimeoutTimesOut(t *testing.T) {
	// Pipe with no writer → read blocks indefinitely; we time out.
	pr, pw := io.Pipe()
	defer pw.Close()
	defer pr.Close()

	var out bytes.Buffer
	start := time.Now()
	got := readActionWithTimeout(pr, &out, 500*time.Millisecond)
	elapsed := time.Since(start)
	if got != ActionTimeout {
		t.Fatalf("expected ActionTimeout, got %v", got)
	}
	if elapsed < 400*time.Millisecond || elapsed > 2*time.Second {
		t.Errorf("timeout took %v (expected ~500ms)", elapsed)
	}
}

// TestPresentIntegration wires present() end-to-end with mock
// reader/writer. Confirms that a Ctrl-D input returns ActionRetry
// (the flow an operator uses to "retry after fixing config").
func TestPresentIntegration(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	go func() {
		// Give the menu a moment to render before feeding input.
		time.Sleep(50 * time.Millisecond)
		pw.Write([]byte{0x04}) // Ctrl-D
		pw.Close()
	}()
	var out bytes.Buffer
	got := present(pr, &out, Options{
		Timeout: 2 * time.Second,
		Errors:  []string{"test error line"},
	})
	if got != ActionRetry {
		t.Fatalf("expected ActionRetry from Ctrl-D, got %v", got)
	}
	// The rendered menu must have hit the console.
	if !strings.Contains(out.String(), "BOOT FAILURE") {
		t.Errorf("menu banner missing from output: %q", out.String())
	}
	if !strings.Contains(out.String(), "test error line") {
		t.Errorf("caller-provided error line missing from output")
	}
}

// TestPresentTruncatesLongErrors — errors longer than the box's
// 56-char inner width get "..." truncated so the border stays intact.
func TestPresentTruncatesLongErrors(t *testing.T) {
	long := strings.Repeat("A", 200)
	pr, pw := io.Pipe()
	defer pr.Close()
	go func() {
		time.Sleep(50 * time.Millisecond)
		pw.Write([]byte("r"))
	}()
	var out bytes.Buffer
	_ = present(pr, &out, Options{
		Timeout: 2 * time.Second,
		Errors:  []string{long},
	})
	// Full 200-char string mustn't be in output as-is.
	if strings.Contains(out.String(), long) {
		t.Fatal("long error line was not truncated")
	}
	// Truncated form ends in "..." somewhere.
	if !strings.Contains(out.String(), "...") {
		t.Fatal("expected truncation marker '...' in output")
	}
}

// TestActionStringRoundtrip — String() must render every Action
// value distinctly so log lines are unambiguous.
func TestActionStringRoundtrip(t *testing.T) {
	seen := map[string]Action{}
	for _, a := range []Action{ActionReboot, ActionPoweroff, ActionRetry, ActionTimeout} {
		s := a.String()
		if s == "" {
			t.Errorf("Action(%d) has empty String()", int(a))
		}
		if prev, ok := seen[s]; ok {
			t.Errorf("Actions %v and %v both stringify to %q", prev, a, s)
		}
		seen[s] = a
	}
}

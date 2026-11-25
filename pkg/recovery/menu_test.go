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
		{0x04, ActionRetry}, // Ctrl-D alias for continue
		{'s', actionShell},  // internal sentinel — recovery loops on it
		{'S', actionShell},
		{0x02, actionShell},  // Ctrl-B alias for shell
		{'x', ActionTimeout}, // unknown key → safest = timeout
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
	got := readActionWithTimeout(pr, newBox(&out), 2*time.Second)
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
	got := readActionWithTimeout(pr, newBox(&out), 2*time.Second)
	if got != ActionPoweroff {
		t.Fatalf("expected ActionPoweroff, got %v", got)
	}
}

// TestReadActionWithTimeoutEOFIsRetry — on a canonical-mode tty
// (default for /dev/console under PID 1), pressing Ctrl-D on an
// empty line delivers io.EOF, NOT the 0x04 byte. The reader must
// treat that EOF as the operator's "continue" intent — otherwise
// it flows through errCh → ActionTimeout → reboot, which is
// exactly the failure mode reported the first time this was tested
// against real QEMU. Regression guard.
func TestReadActionWithTimeoutEOFIsRetry(t *testing.T) {
	consoleEOFSpent.Store(false)
	pr, pw := io.Pipe()
	go func() {
		// Close without writing → readers see io.EOF, which is
		// what a bare Ctrl-D on canonical-mode /dev/console
		// produces at the syscall layer.
		pw.Close()
	}()
	defer pr.Close()

	var out bytes.Buffer
	got := readActionWithTimeout(pr, newBox(&out), 2*time.Second)
	if got != ActionRetry {
		t.Fatalf("EOF should map to ActionRetry (Ctrl-D semantic), got %v", got)
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
	got := readActionWithTimeout(pr, newBox(&out), 500*time.Millisecond)
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
	// The cut is marked with a one-column ellipsis. The box used to use
	// "..." here and "\u2026" in the debugger's service names; they agree
	// now, and one column of marker leaves two more for content.
	if !strings.Contains(out.String(), "\u2026") {
		t.Fatal("expected a truncation marker in the output")
	}
	// Every rendered row must be exactly as wide as the frame. This is
	// what broke before: the marker's extra bytes moved the right margin,
	// because the width was measured with len().
	for _, line := range strings.Split(out.String(), "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		if w := dispWidth(line); w != defaultBoxWidth {
			t.Errorf("row is %d columns, want %d: %q", w, defaultBoxWidth, line)
		}
	}
}

// TestActionStringRoundtrip — String() must render every Action
// value distinctly so log lines are unambiguous.
func TestWithTermDumb(t *testing.T) {
	in := []string{
		"PATH=/sbin:/bin",
		"TERM=xterm-256color", // parent had a fancy terminal
		"LINES=50",            // and a large window
		"COLUMNS=200",
		"HOME=/root",
	}
	got := withTermDumb(in)
	seen := map[string]string{}
	for _, kv := range got {
		if i := strings.IndexByte(kv, '='); i > 0 {
			seen[kv[:i]] = kv[i+1:]
		}
	}
	// Ours must win.
	if seen["TERM"] != "dumb" {
		t.Errorf("TERM = %q, want dumb", seen["TERM"])
	}
	if seen["LINES"] != "24" {
		t.Errorf("LINES = %q, want 24", seen["LINES"])
	}
	if seen["COLUMNS"] != "80" {
		t.Errorf("COLUMNS = %q, want 80", seen["COLUMNS"])
	}
	// Passthrough vars survive.
	if seen["PATH"] != "/sbin:/bin" {
		t.Errorf("PATH lost: %q", seen["PATH"])
	}
	if seen["HOME"] != "/root" {
		t.Errorf("HOME lost: %q", seen["HOME"])
	}
	// No duplicate TERM/LINES/COLUMNS.
	count := func(prefix string) int {
		n := 0
		for _, kv := range got {
			if strings.HasPrefix(kv, prefix) {
				n++
			}
		}
		return n
	}
	for _, p := range []string{"TERM=", "LINES=", "COLUMNS="} {
		if n := count(p); n != 1 {
			t.Errorf("%s appears %d times, want 1", p, n)
		}
	}
}

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

// A console that only ever returns EOF (hung up, or /dev/null bound on
// /dev/console) had every EOF taken for Ctrl-D: retry, the boot failed
// again, a new menu read EOF at once, and so on — about 700 KB of log
// in 4 s and no auto-reboot. One EOF is still a Ctrl-D; another with no
// byte read in between waits for the timeout. A byte, even the newline
// after a Ctrl-D, makes the next EOF a Ctrl-D again.
func TestReadActionWithTimeoutRepeatedEOFTimesOut(t *testing.T) {
	consoleEOFSpent.Store(false)
	t.Cleanup(func() { consoleEOFSpent.Store(false) })
	var out bytes.Buffer
	read := func(r io.Reader) (Action, time.Duration) {
		start := time.Now()
		a := readActionWithTimeout(r, newBox(&out), 400*time.Millisecond)
		return a, time.Since(start)
	}

	if got, _ := read(strings.NewReader("")); got != ActionRetry {
		t.Fatalf("first EOF: %v; want ActionRetry", got)
	}
	for i := 0; i < 2; i++ {
		got, took := read(strings.NewReader(""))
		if got != ActionTimeout || took < 350*time.Millisecond {
			t.Fatalf("EOF again with no byte since: %v after %v; want ActionTimeout after the 400ms timeout", got, took)
		}
	}

	// A key still answers the menu while EOFs are being ignored.
	if got, _ := read(&eofThen{n: 3, rest: strings.NewReader("r")}); got != ActionReboot {
		t.Fatalf("key after ignored EOFs: %v; want ActionReboot", got)
	}
	// That key re-arms Ctrl-D, as does a newline.
	if got, _ := read(strings.NewReader("")); got != ActionRetry {
		t.Fatalf("EOF after a key: %v; want ActionRetry", got)
	}
	if got, _ := read(strings.NewReader("\n")); got != ActionRetry {
		t.Fatalf("newline then EOF: %v; want ActionRetry", got)
	}
}

// eofThen returns io.EOF n times, then reads from rest.
type eofThen struct {
	n    int
	rest io.Reader
}

func (r *eofThen) Read(p []byte) (int, error) {
	if r.n > 0 {
		r.n--
		return 0, io.EOF
	}
	return r.rest.Read(p)
}

// A shutdown requested over the control socket while the menu waits
// for a key used to be ignored until the menu timed out and rebooted.
// Options.Interrupt ends the wait at once.
func TestPresentInterrupt(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()
	interrupt := make(chan struct{}, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		interrupt <- struct{}{}
	}()
	var out bytes.Buffer
	start := time.Now()
	got := present(pr, &out, Options{Timeout: 5 * time.Second, Interrupt: interrupt})
	if got != ActionInterrupted {
		t.Fatalf("got %v, want %v", got, ActionInterrupted)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("interrupt took %v to end the menu", d)
	}
}

func TestPresentCollapseInterrupt(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()
	interrupt := make(chan struct{}, 1)
	interrupt <- struct{}{}
	var out bytes.Buffer
	got := presentCollapseInterruptible(pr, &out, 5*time.Second, interrupt)
	if got != CollapseInterrupted {
		t.Fatalf("got %v, want %v", got, CollapseInterrupted)
	}
}

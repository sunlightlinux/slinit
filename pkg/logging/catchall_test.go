package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCatchAllCaptures(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "catch-all.log")

	cal, err := StartCatchAll(logPath)
	if err != nil {
		t.Fatalf("StartCatchAll: %v", err)
	}

	// Write to stdout (fd 1) — this should be captured.
	os.Stdout.WriteString("hello from stdout\n")
	os.Stderr.WriteString("hello from stderr\n")

	// Give the drain goroutine time to process.
	time.Sleep(50 * time.Millisecond)

	cal.Stop()

	// Verify log file contains both messages.
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "hello from stdout") {
		t.Errorf("log missing stdout output; got:\n%s", content)
	}
	if !strings.Contains(content, "hello from stderr") {
		t.Errorf("log missing stderr output; got:\n%s", content)
	}
}

func TestCatchAllTimestamps(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "catch-all.log")

	cal, err := StartCatchAll(logPath)
	if err != nil {
		t.Fatalf("StartCatchAll: %v", err)
	}

	os.Stdout.WriteString("timestamped line\n")
	time.Sleep(50 * time.Millisecond)
	cal.Stop()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	// Each line should have an ISO8601-style timestamp prefix.
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		t.Fatal("empty log")
	}
	// Format: "2006-01-02T15:04:05.000 ..."
	if len(lines[0]) < 24 || lines[0][4] != '-' || lines[0][10] != 'T' {
		t.Errorf("line missing timestamp prefix: %q", lines[0])
	}
}

func TestCatchAllRestoresFDs(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "catch-all.log")

	cal, err := StartCatchAll(logPath)
	if err != nil {
		t.Fatalf("StartCatchAll: %v", err)
	}

	cal.Stop()

	// After Stop(), writing to stdout should not panic or error.
	_, err = os.Stdout.WriteString("after stop\n")
	if err != nil {
		t.Errorf("write after stop: %v", err)
	}
}

func TestCatchAllConsole(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "catch-all.log")

	cal, err := StartCatchAll(logPath)
	if err != nil {
		t.Fatalf("StartCatchAll: %v", err)
	}

	// Console() should return a valid writable file.
	cons := cal.Console()
	if cons == nil {
		t.Fatal("Console() returned nil")
	}

	cal.Stop()
}

func TestCatchAllDash(t *testing.T) {
	// logPath "-" means no file, console only.
	cal, err := StartCatchAll("-")
	if err != nil {
		t.Fatalf("StartCatchAll(-): %v", err)
	}

	os.Stdout.WriteString("console only line\n")
	time.Sleep(50 * time.Millisecond)
	cal.Stop()
	// No crash = pass; just verify no file was created.
}

// TestCatchAllReattachAfterRedirect reproduces the InitPID1 scenario:
// catch-all sets up the pipe, an early log message is written, then
// some other code (setupConsole in real life) Dup2s fd 1/2 to a
// different fd. Without ReattachStdoutErr, subsequent log messages
// bypass the pipe and the early message ends up flushed late by the
// drain goroutine — visibly out-of-order timestamps in the demo log.
//
// This test verifies that after ReattachStdoutErr, fd 1/2 are bound
// back to the pipe and a subsequent write reaches the catch-all log
// (i.e. it goes through the pipe→drain path, not the bypass path).
func TestCatchAllReattachAfterRedirect(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "catch-all.log")

	cal, err := StartCatchAll(logPath)
	if err != nil {
		t.Fatalf("StartCatchAll: %v", err)
	}
	defer cal.Stop()

	// Write before the simulated redirect — must reach the log.
	os.Stdout.WriteString("before-redirect\n")

	// Simulate setupConsole's Dup2: redirect fd 1/2 to a side file.
	sidePath := filepath.Join(dir, "side.log")
	side, err := os.OpenFile(sidePath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open side: %v", err)
	}
	defer side.Close()
	origStdout := os.Stdout
	origStderr := os.Stderr
	if err := syscall.Dup2(int(side.Fd()), 1); err != nil {
		t.Fatalf("dup2 stdout to side: %v", err)
	}
	if err := syscall.Dup2(int(side.Fd()), 2); err != nil {
		t.Fatalf("dup2 stderr to side: %v", err)
	}
	os.Stdout = os.NewFile(1, "/dev/stdout")
	os.Stderr = os.NewFile(2, "/dev/stderr")

	// At this point fd 1/2 bypass the catch-all pipe.
	os.Stdout.WriteString("during-bypass\n")

	// Reattach — fd 1/2 should now go through the pipe again.
	if err := cal.ReattachStdoutErr(); err != nil {
		t.Fatalf("ReattachStdoutErr: %v", err)
	}

	os.Stdout.WriteString("after-reattach\n")

	// Drain.
	time.Sleep(80 * time.Millisecond)

	// Restore real stdio so test framework I/O still works after the test.
	os.Stdout = origStdout
	os.Stderr = origStderr

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	log := string(logBytes)
	if !strings.Contains(log, "before-redirect") {
		t.Errorf("catch-all log missing 'before-redirect':\n%s", log)
	}
	if !strings.Contains(log, "after-reattach") {
		t.Errorf("catch-all log missing 'after-reattach' (Reattach failed):\n%s", log)
	}

	// 'during-bypass' must NOT appear in the catch-all — it went to
	// the side file, proving the bypass scenario was real and the
	// reattach actually flipped the redirect back.
	if strings.Contains(log, "during-bypass") {
		t.Errorf("catch-all log unexpectedly contains 'during-bypass' — bypass scenario didn't trigger:\n%s", log)
	}
	sideBytes, _ := os.ReadFile(sidePath)
	if !strings.Contains(string(sideBytes), "during-bypass") {
		t.Errorf("side file missing 'during-bypass'; got:\n%s", sideBytes)
	}
}

func TestCatchAllStopIdempotent(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "catch-all.log")

	cal, err := StartCatchAll(logPath)
	if err != nil {
		t.Fatalf("StartCatchAll: %v", err)
	}

	// Stop multiple times should not panic.
	cal.Stop()
	cal.Stop()
	cal.Stop()
}

// drainThrough runs the drain over a pipe with the given line ending, feeds
// it, and returns what reached the console.
func drainThrough(t *testing.T, eol string, feed func(c *CatchAllLogger)) string {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	consR, consW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	c := &CatchAllLogger{pipeR: pr, pipeW: pw, console: consW, consoleEOL: eol}
	c.wg.Add(1)
	go c.drain()
	feed(c)
	pw.Close()
	c.wg.Wait()
	consW.Close()
	out, err := io.ReadAll(consR)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// A line the logger ends with "\r\n" for a terminal reaches it that way. The
// scanner strips the "\r", and the drain used to end every line with a bare
// "\n", so as PID 1 the shutdown lines staircased under sudo's raw mode.
// Every line gets it, not only the logger's: a child's too.
func TestCatchAllConsoleLineEndings(t *testing.T) {
	feed := func(c *CatchAllLogger) {
		l := New(LevelInfo)
		l.SetOutput(c.pipeW)
		l.SetConsoleIsTTY(true)
		l.SetBootConsole(true, false)
		l.SetShutdownConsole(true)
		l.ServiceStopped("sshd")
		fmt.Fprintln(c.pipeW, "a child's own line")
	}
	got := drainThrough(t, "\r\n", feed)
	if !strings.Contains(got, "[STOPPD] sshd\r\n") || !strings.Contains(got, "a child's own line\r\n") {
		t.Errorf("terminal console got %q", got)
	}
	for i := 0; i < len(got); i++ {
		if got[i] == '\n' && (i == 0 || got[i-1] != '\r') {
			t.Fatalf("a bare newline at %d reached the terminal: %q", i, got)
		}
	}
	if got := drainThrough(t, "\n", func(c *CatchAllLogger) { fmt.Fprintln(c.pipeW, "line") }); got != "line\n" {
		t.Errorf("a pipe console got %q", got)
	}
}

// What was written before the mute still reaches the console: it used to be
// overtaken, and the boot's last "[ OK ]" never showed.
func TestCatchAllMuteKeepsWhatWasWrittenBefore(t *testing.T) {
	got := drainThrough(t, "\n", func(c *CatchAllLogger) {
		fmt.Fprintln(c.pipeW, "[ OK ] getty-tty1")
		c.SetConsoleMuted(true)
		fmt.Fprintln(c.pipeW, "after the mute")
	})
	if got != "[ OK ] getty-tty1\n" {
		t.Errorf("console got %q", got)
	}
}

// A mute behind a child's unterminated output keeps that output and mutes.
func TestCatchAllMuteAfterPartialLine(t *testing.T) {
	got := drainThrough(t, "\n", func(c *CatchAllLogger) {
		fmt.Fprint(c.pipeW, "partial")
		c.SetConsoleMuted(true)
		fmt.Fprintln(c.pipeW, "after")
	})
	if got != "partial\n" {
		t.Errorf("console got %q", got)
	}
}

// A real terminal is recognised, a pipe is not.
func TestStartCatchAllDetectsATerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if _, err := unix.IoctlGetTermios(int(w.Fd()), unix.TCGETS); err == nil {
		t.Error("a pipe reads as a terminal")
	}
	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no pty here: %v", err)
	}
	defer ptmx.Close()
	if _, err := unix.IoctlGetTermios(int(ptmx.Fd()), unix.TCGETS); err != nil {
		t.Errorf("a pty does not read as a terminal: %v", err)
	}
}

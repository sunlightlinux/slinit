package logging

import (
	"bytes"
	"strings"
	"testing"
)

// A rescue menu owns /dev/console while it is up, and a log line written
// into it lands between two rows of the menu's box. Observed on a demo
// boot:
//
//	|   [s] / Ctrl-B   drop to shell                             |
//	[12:56:37] WARN: Debug menu: force-fail requested but no ...
//	|   [f]            force-fail first in-progress service      |
//
// PauseBootConsole used to mute only the compact "[ OK ] name" renderer,
// so every other level stayed free to scribble over the one screen an
// operator reads when something has already gone wrong.
//
// This is asserted here rather than in the QEMU harness on purpose: the
// interleaving needs a console slow enough for a write to still be
// draining when the next one starts, and the functional VM's console is
// a unix socket drained as fast as socat can read it. Racing it there
// proved nothing — the box came out clean even with the gate removed.
// The gate itself is what can be checked, and it is deterministic.
func TestPauseBootConsoleMutesEveryLevel(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelDebug)
	l.SetOutput(&buf)

	l.Info("before-pause")
	if !strings.Contains(buf.String(), "before-pause") {
		t.Fatalf("setup: logging is not reaching the buffer: %q", buf.String())
	}

	buf.Reset()
	l.PauseBootConsole()
	l.Info("info-during")
	l.Notice("notice-during")
	l.Warn("warn-during")
	l.Error("error-during")
	l.ServiceStarted("svc-during")
	l.ServiceFailed("svc-failed-during", false)

	if got := buf.String(); got != "" {
		t.Errorf("console wrote while a menu held it: %q", got)
	}

	l.ResumeBootConsole()
	l.Warn("after-resume")
	if !strings.Contains(buf.String(), "after-resume") {
		t.Errorf("console did not come back after resume: %q", buf.String())
	}
}

// Muting the screen must not lose the event: the record has to survive
// somewhere, or an error raised while an operator reads the rescue menu
// vanishes entirely.
func TestPauseBootConsoleStillFillsTheRingBuffer(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelDebug)
	l.SetOutput(&buf)
	rb := NewRingBuffer(4096)
	l.SetRingBuffer(rb)

	l.PauseBootConsole()
	l.Error("recorded-but-not-shown")

	if got := buf.String(); got != "" {
		t.Errorf("console should be muted, got %q", got)
	}
	if ring := string(rb.Bytes()); !strings.Contains(ring, "recorded-but-not-shown") {
		t.Errorf("the event was lost rather than deferred; ring buffer holds %q", ring)
	}
}

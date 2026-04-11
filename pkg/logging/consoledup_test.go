package logging

import (
	"bytes"
	"strings"
	"testing"
)

func TestConsoleDupReceivesCopy(t *testing.T) {
	var primary bytes.Buffer
	var dup bytes.Buffer

	logger := New(LevelInfo)
	logger.SetOutput(&primary)
	logger.SetConsoleDup(&dup)

	logger.Info("hello world")

	if !strings.Contains(primary.String(), "hello world") {
		t.Errorf("primary missing message: %q", primary.String())
	}
	if !strings.Contains(dup.String(), "hello world") {
		t.Errorf("consoleDup missing message: %q", dup.String())
	}
	// Both should have identical content.
	if primary.String() != dup.String() {
		t.Errorf("primary %q != dup %q", primary.String(), dup.String())
	}
}

func TestConsoleDupNilIsNoop(t *testing.T) {
	var primary bytes.Buffer

	logger := New(LevelInfo)
	logger.SetOutput(&primary)
	// No SetConsoleDup — should not panic.
	logger.Info("no dup")

	if !strings.Contains(primary.String(), "no dup") {
		t.Errorf("primary missing message: %q", primary.String())
	}
}

func TestConsoleDupBelowLevel(t *testing.T) {
	var primary bytes.Buffer
	var dup bytes.Buffer

	logger := New(LevelWarn)
	logger.SetOutput(&primary)
	logger.SetConsoleDup(&dup)

	// Info is below Warn level — should not appear in either.
	logger.Info("should be suppressed")

	if primary.Len() > 0 {
		t.Errorf("primary should be empty, got %q", primary.String())
	}
	if dup.Len() > 0 {
		t.Errorf("dup should be empty, got %q", dup.String())
	}
}

func TestConsoleDupMultipleMessages(t *testing.T) {
	var primary bytes.Buffer
	var dup bytes.Buffer

	logger := New(LevelDebug)
	logger.SetOutput(&primary)
	logger.SetConsoleDup(&dup)

	logger.Debug("msg1")
	logger.Info("msg2")
	logger.Error("msg3")

	for _, msg := range []string{"msg1", "msg2", "msg3"} {
		if !strings.Contains(dup.String(), msg) {
			t.Errorf("dup missing %q", msg)
		}
	}
	if primary.String() != dup.String() {
		t.Error("primary and dup diverged")
	}
}

package service

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestLogRotatorSanitizeInPlaceDefault verifies the built-in
// control-char set: < 0x20 (except \n and \t) and 0x7F all become the
// replacement byte; printable and high-bit bytes pass through.
func TestLogRotatorSanitizeInPlaceDefault(t *testing.T) {
	cfg := LogRotatorConfig{
		ServiceName:  "test",
		LogLevelMax:  -1,
		SanitizeChar: '_',
	}
	lr, err := NewLogRotator(cfg)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	in := []byte{
		'h', 'i', 0x1b, '[', '3', '1', 'm',
		'\t', 'x', '\n',
		0x00, 0x07, 0x7F, 0xC3, 0xA9, // NUL, BEL, DEL, é (UTF-8)
	}
	lr.sanitizeInPlace(in)
	want := []byte{
		'h', 'i', '_', '[', '3', '1', 'm',
		'\t', 'x', '\n',
		'_', '_', '_', 0xC3, 0xA9,
	}
	if !bytes.Equal(in, want) {
		t.Errorf("sanitize:\n got %q\nwant %q", in, want)
	}
}

// TestLogRotatorSanitizeExtra verifies -R semantics: user-supplied
// bytes are replaced in addition to the default control set.
func TestLogRotatorSanitizeExtra(t *testing.T) {
	cfg := LogRotatorConfig{
		ServiceName:   "test",
		LogLevelMax:   -1,
		SanitizeChar:  'X',
		SanitizeExtra: []byte{'|', ';'},
	}
	lr, err := NewLogRotator(cfg)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	in := []byte("foo|bar;baz\x00")
	lr.sanitizeInPlace(in)
	want := []byte("fooXbarXbazX")
	if !bytes.Equal(in, want) {
		t.Errorf("sanitize:\n got %q\nwant %q", in, want)
	}
}

// TestLogRotatorSanitizeExtraDefaultsChar checks that supplying only
// SanitizeExtra (no SanitizeChar) picks '_' as the default replacement,
// matching svlogd's behavior where -R implies -r.
func TestLogRotatorSanitizeExtraDefaultsChar(t *testing.T) {
	cfg := LogRotatorConfig{
		ServiceName:   "test",
		LogLevelMax:   -1,
		SanitizeExtra: []byte{'|'},
	}
	lr, err := NewLogRotator(cfg)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if lr.sanitizeChar != '_' {
		t.Errorf("default sanitizeChar = %q, want '_'", lr.sanitizeChar)
	}
}

// TestLogRotatorSanitizeDisabled confirms zero-config leaves bytes
// alone: sanitizeInPlace is a no-op path via the processLine gate.
func TestLogRotatorSanitizeDisabled(t *testing.T) {
	cfg := LogRotatorConfig{
		ServiceName: "test",
		LogLevelMax: -1,
	}
	lr, err := NewLogRotator(cfg)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if lr.sanitizeChar != 0 {
		t.Errorf("sanitizeChar should be 0 when neither knob is set, got %d", lr.sanitizeChar)
	}
}

// TestLogRotatorProcessLineSanitizes drives sanitization through
// processLine → file write, matching what a real service pipe does.
func TestLogRotatorProcessLineSanitizes(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "svc.log")

	cfg := LogRotatorConfig{
		FilePath:     logPath,
		FilePerms:    0600,
		FileUID:      -1,
		FileGID:      -1,
		ServiceName:  "test",
		LogLevelMax:  -1,
		SanitizeChar: '.',
	}
	lr, err := NewLogRotator(cfg)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer lr.Close()

	lr.processLine([]byte("start\x1b[31mred\x1b[0m end\n"))
	lr.mu.Lock()
	if lr.file != nil {
		lr.file.Sync()
	}
	lr.mu.Unlock()

	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	want := "start.[31mred.[0m end\n"
	if string(got) != want {
		t.Errorf("logfile contents:\n got %q\nwant %q", got, want)
	}
}

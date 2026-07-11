package service

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestLogRotatorCapLineDisabled verifies the fast path: with
// MaxLineLength=0 the helper returns the input slice unchanged.
func TestLogRotatorCapLineDisabled(t *testing.T) {
	lr, err := NewLogRotator(LogRotatorConfig{ServiceName: "t", LogLevelMax: -1})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	in := []byte("short line\n")
	got := lr.capLine(in)
	if &got[0] != &in[0] {
		t.Errorf("cap-disabled: capLine should return the input slice unchanged")
	}
}

// TestLogRotatorCapLineNoTruncation covers the boundary: a line whose
// content is exactly maxLineLen bytes long must NOT be marked.
func TestLogRotatorCapLineNoTruncation(t *testing.T) {
	lr, err := NewLogRotator(LogRotatorConfig{
		ServiceName:   "t",
		LogLevelMax:   -1,
		MaxLineLength: 16,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	// exactly 16 bytes of content + newline
	in := []byte("0123456789abcdef\n")
	got := lr.capLine(in)
	if string(got) != "0123456789abcdef\n" {
		t.Errorf("boundary: got %q, want unchanged", got)
	}
}

// TestLogRotatorCapLineTruncatesWithMarker covers the svlogd-compat
// path: content > maxLineLen gets clipped to N bytes then '+' + '\n'.
func TestLogRotatorCapLineTruncatesWithMarker(t *testing.T) {
	lr, err := NewLogRotator(LogRotatorConfig{
		ServiceName:   "t",
		LogLevelMax:   -1,
		MaxLineLength: 8,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	in := []byte("0123456789ABCDEF\n")
	got := lr.capLine(in)
	if string(got) != "01234567+\n" {
		t.Errorf("truncate: got %q, want %q", got, "01234567+\n")
	}
}

// TestLogRotatorCapLineNoTrailingNewline covers the discard-mode
// path where readLoop hands us content without a terminating '\n'
// (mid-line overflow). capLine must still emit a well-formed line.
func TestLogRotatorCapLineNoTrailingNewline(t *testing.T) {
	lr, err := NewLogRotator(LogRotatorConfig{
		ServiceName:   "t",
		LogLevelMax:   -1,
		MaxLineLength: 4,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	in := []byte("aaaaaaaa") // 8 bytes, no newline
	got := lr.capLine(in)
	if string(got) != "aaaa+\n" {
		t.Errorf("no-newline overflow: got %q, want %q", got, "aaaa+\n")
	}
}

// waitForLogFile busy-polls for the log file to reach at least
// wantSize bytes. Needed because CreatePipe/StartReader/Close race
// with the reader goroutine — a synchronous Close() the moment after
// w.Close() sometimes tears pipeR down before the reader schedules
// its Read of the buffered payload. The condition variable would
// need a larger refactor to expose; polling here keeps the test
// footprint minimal.
func waitForLogFile(t *testing.T, path string, wantSize int) []byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) >= wantSize {
			return b
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("log file %s did not reach %d bytes within timeout", path, wantSize)
	return nil
}

// TestLogRotatorReadLoopTruncatesLongLine drives the full pipe →
// readLoop → capLine → file path with a line that exceeds the cap
// but arrives with a newline (single-chunk read).
func TestLogRotatorReadLoopTruncatesLongLine(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "svc.log")

	lr, err := NewLogRotator(LogRotatorConfig{
		FilePath:      logPath,
		FilePerms:     0600,
		FileUID:       -1,
		FileGID:       -1,
		ServiceName:   "t",
		LogLevelMax:   -1,
		MaxLineLength: 16,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	w, err := lr.CreatePipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	lr.StartReader()

	// Write two lines: one short, one over the cap.
	if _, err := w.Write([]byte("short line\n")); err != nil {
		t.Fatalf("write short: %v", err)
	}
	long := bytes.Repeat([]byte("A"), 100)
	long = append(long, '\n')
	if _, err := w.Write(long); err != nil {
		t.Fatalf("write long: %v", err)
	}
	w.Close()

	want := "short line\n" + strings.Repeat("A", 16) + "+\n"
	got := waitForLogFile(t, logPath, len(want))
	lr.Close()
	if string(got) != want {
		t.Errorf("logfile contents:\n got %q\nwant %q", got, want)
	}
}

// TestLogRotatorReadLoopDiscardModeRecovers verifies the safety-net:
// a producer that emits N bytes without a '\n' triggers early truncate,
// then the reader silently discards until it finds the next newline,
// and the following line is delivered intact.
func TestLogRotatorReadLoopDiscardModeRecovers(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "svc.log")

	lr, err := NewLogRotator(LogRotatorConfig{
		FilePath:      logPath,
		FilePerms:     0600,
		FileUID:       -1,
		FileGID:       -1,
		ServiceName:   "t",
		LogLevelMax:   -1,
		MaxLineLength: 8,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	w, err := lr.CreatePipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	lr.StartReader()

	// Emit 50 bytes with no newline in the middle, then the closing
	// '\n', then a clean short line. The 50-byte run should be
	// truncated at 8 bytes with a '+' marker; the tail (bytes 9..50)
	// should be dropped by discard mode; then "ok\n" should land
	// unchanged as its own line.
	if _, err := w.Write(bytes.Repeat([]byte("X"), 50)); err != nil {
		t.Fatalf("write X: %v", err)
	}
	if _, err := w.Write([]byte("\nok\n")); err != nil {
		t.Fatalf("write ok: %v", err)
	}
	w.Close()

	want := strings.Repeat("X", 8) + "+\n" + "ok\n"
	got := waitForLogFile(t, logPath, len(want))
	lr.Close()
	if string(got) != want {
		t.Errorf("logfile contents:\n got %q\nwant %q", got, want)
	}
}

// TestLogRotatorTimestampISO8601 checks the svlogd -ttt style
// timestamp is emitted at the start of each line and preserves the
// newline at the end.
func TestLogRotatorTimestampISO8601(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "svc.log")

	lr, err := NewLogRotator(LogRotatorConfig{
		FilePath:      logPath,
		FilePerms:     0600,
		FileUID:       -1,
		FileGID:       -1,
		ServiceName:   "t",
		LogLevelMax:   -1,
		TimestampMode: "iso8601",
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer lr.Close()

	lr.processLine([]byte("hello\n"))
	lr.mu.Lock()
	if lr.file != nil {
		lr.file.Sync()
	}
	lr.mu.Unlock()

	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	line := string(got)
	// Expect: YYYY-MM-DDTHH:MM:SS.µs⁠Z hello\n  — 27-char ts + " hello\n".
	if len(line) < 30 || line[10] != 'T' || line[len(line)-6:] != "hello\n" {
		t.Errorf("iso8601: unexpected output %q", line)
	}
	if !strings.HasSuffix(strings.TrimSpace(strings.Split(line, " ")[0]), "Z") {
		t.Errorf("iso8601: expected trailing Z on timestamp, got %q", line)
	}
}

// TestLogRotatorTimestampHuman covers the svlogd -tt format:
// YYYY-MM-DD_HH:MM:SS.µs (no trailing Z, underscore between date and
// time).
func TestLogRotatorTimestampHuman(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "svc.log")

	lr, err := NewLogRotator(LogRotatorConfig{
		FilePath: logPath, FilePerms: 0600, FileUID: -1, FileGID: -1,
		ServiceName:   "t",
		LogLevelMax:   -1,
		TimestampMode: "human",
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer lr.Close()

	lr.processLine([]byte("hi\n"))
	lr.mu.Lock()
	if lr.file != nil {
		lr.file.Sync()
	}
	lr.mu.Unlock()

	got, _ := os.ReadFile(logPath)
	line := string(got)
	if len(line) < 26 || line[10] != '_' || !strings.HasSuffix(line, "hi\n") {
		t.Errorf("human: unexpected output %q", line)
	}
}

// TestLogRotatorTimestampTAI64N verifies the tai64n token shape:
// '@' followed by 24 hex digits (16 for seconds, 8 for nanos).
func TestLogRotatorTimestampTAI64N(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "svc.log")

	lr, err := NewLogRotator(LogRotatorConfig{
		FilePath: logPath, FilePerms: 0600, FileUID: -1, FileGID: -1,
		ServiceName:   "t",
		LogLevelMax:   -1,
		TimestampMode: "tai64n",
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer lr.Close()

	lr.processLine([]byte("x\n"))
	lr.mu.Lock()
	if lr.file != nil {
		lr.file.Sync()
	}
	lr.mu.Unlock()

	got, _ := os.ReadFile(logPath)
	line := string(got)
	if len(line) < 27 || line[0] != '@' || !strings.HasSuffix(line, "x\n") {
		t.Errorf("tai64n: unexpected output %q", line)
	}
	// Chars 1..24 must be hex.
	for i := 1; i <= 24; i++ {
		c := line[i]
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			t.Errorf("tai64n: non-hex byte %q at position %d in %q", c, i, line)
		}
	}
}

// TestLogRotatorLinePrefix covers svlogd's `p<prefix>` config: a fixed
// string precedes each line's content. Auto-adds trailing space when
// operator omitted it.
func TestLogRotatorLinePrefix(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "svc.log")

	lr, err := NewLogRotator(LogRotatorConfig{
		FilePath: logPath, FilePerms: 0600, FileUID: -1, FileGID: -1,
		ServiceName: "t",
		LogLevelMax: -1,
		LinePrefix:  "host01",
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer lr.Close()

	lr.processLine([]byte("hello\n"))
	lr.mu.Lock()
	if lr.file != nil {
		lr.file.Sync()
	}
	lr.mu.Unlock()

	got, _ := os.ReadFile(logPath)
	if string(got) != "host01 hello\n" {
		t.Errorf("prefix: got %q, want %q", got, "host01 hello\n")
	}
}

// TestLogRotatorTimestampPlusPrefix confirms the two decorations
// compose as [timestamp] [prefix] content.
func TestLogRotatorTimestampPlusPrefix(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "svc.log")

	lr, err := NewLogRotator(LogRotatorConfig{
		FilePath: logPath, FilePerms: 0600, FileUID: -1, FileGID: -1,
		ServiceName:   "t",
		LogLevelMax:   -1,
		TimestampMode: "iso8601",
		LinePrefix:    "host01",
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer lr.Close()

	lr.processLine([]byte("hello\n"))
	lr.mu.Lock()
	if lr.file != nil {
		lr.file.Sync()
	}
	lr.mu.Unlock()

	got, _ := os.ReadFile(logPath)
	line := string(got)
	parts := strings.SplitN(line, " ", 3)
	if len(parts) != 3 || parts[1] != "host01" || parts[2] != "hello\n" {
		t.Errorf("compose: got %q, want <ts> host01 hello\\n", line)
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

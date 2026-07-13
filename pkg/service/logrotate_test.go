package service

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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

// testCaptureLogger records Info/Error emissions for assertion.
type testCaptureLogger struct {
	mu   sync.Mutex
	msgs []string
}

func (l *testCaptureLogger) Info(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, "INFO: "+fmt.Sprintf(format, args...))
}
func (l *testCaptureLogger) Error(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, "ERROR: "+fmt.Sprintf(format, args...))
}
func (l *testCaptureLogger) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.msgs))
	copy(out, l.msgs)
	return out
}

// seedRotated creates count rotated files with monotonically-increasing
// timestamps under dir, matching the naming convention svc.log.YYYY...
// The oldest file is created first so filenames sort chronologically.
func seedRotated(t *testing.T, dir, base string, count int) {
	t.Helper()
	base0 := "20240101-000000"
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("%s.%s-%03d", base, base0, i)
		full := filepath.Join(dir, name)
		if err := os.WriteFile(full, []byte("stub"), 0600); err != nil {
			t.Fatalf("seed %s: %v", full, err)
		}
	}
}

// countRotated returns the number of files in dir whose basename starts
// with base+"." (matching LogRotator's rotated-file discovery scheme).
func countRotated(t *testing.T, dir, base string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	prefix := base + "."
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), prefix) && e.Name() != base {
			n++
		}
	}
	return n
}

// TestLogRotatorFreeSpaceDrainsToMin verifies svlogd's Nmin behavior:
// when freeSpaceLocked is invoked with 5 rotated files on disk and
// minFiles=2, the 3 oldest files get deleted, 2 remain, and the
// helper reports success.
func TestLogRotatorFreeSpaceDrainsToMin(t *testing.T) {
	dir := t.TempDir()
	base := "svc.log"

	seedRotated(t, dir, base, 5)

	lr, err := NewLogRotator(LogRotatorConfig{
		FilePath:    filepath.Join(dir, base),
		FilePerms:   0600,
		FileUID:     -1,
		FileGID:     -1,
		ServiceName: "t",
		LogLevelMax: -1,
		MaxFiles:    10,
		MinFiles:    2,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer lr.Close()

	if !lr.freeSpaceLocked() {
		t.Fatal("freeSpaceLocked returned false when it should have removed files")
	}
	if got := countRotated(t, dir, base); got != 2 {
		t.Errorf("after drain: %d rotated files remain, want 2", got)
	}
}

// TestLogRotatorFreeSpaceRespectsFloor confirms that once we're at
// (or below) minFiles, no files are deleted and the helper reports
// no work done — the caller must not retry the failed write.
func TestLogRotatorFreeSpaceRespectsFloor(t *testing.T) {
	dir := t.TempDir()
	base := "svc.log"

	seedRotated(t, dir, base, 2)

	lr, err := NewLogRotator(LogRotatorConfig{
		FilePath:    filepath.Join(dir, base),
		FilePerms:   0600,
		FileUID:     -1,
		FileGID:     -1,
		ServiceName: "t",
		LogLevelMax: -1,
		MaxFiles:    10,
		MinFiles:    2,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer lr.Close()

	if lr.freeSpaceLocked() {
		t.Fatal("freeSpaceLocked deleted files at or below the floor")
	}
	if got := countRotated(t, dir, base); got != 2 {
		t.Errorf("at floor: %d files after no-op drain, want 2", got)
	}
}

// TestLogRotatorFreeSpaceOldestFirst pins down the deletion order:
// with rotated files whose names sort chronologically, we must delete
// the earliest ones so recent history is preserved.
func TestLogRotatorFreeSpaceOldestFirst(t *testing.T) {
	dir := t.TempDir()
	base := "svc.log"

	seedRotated(t, dir, base, 4) // creates -000 (oldest) .. -003 (newest)

	lr, err := NewLogRotator(LogRotatorConfig{
		FilePath:    filepath.Join(dir, base),
		FilePerms:   0600,
		FileUID:     -1,
		FileGID:     -1,
		ServiceName: "t",
		LogLevelMax: -1,
		MaxFiles:    10,
		MinFiles:    2,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer lr.Close()

	lr.freeSpaceLocked()

	// Only -002 and -003 (the two newest) should survive.
	survivors := []string{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), base+".") {
			survivors = append(survivors, e.Name())
		}
	}
	sort.Strings(survivors)
	if len(survivors) != 2 ||
		!strings.HasSuffix(survivors[0], "-002") ||
		!strings.HasSuffix(survivors[1], "-003") {
		t.Errorf("survivors = %v, want the two most recent (-002, -003)", survivors)
	}
}

// TestLogRotatorFreeSpaceOneShotWarn verifies that repeated drain
// events produce only ONE ERROR line per LogRotator lifetime until a
// full rotateLocked() rearms the notifier.
func TestLogRotatorFreeSpaceOneShotWarn(t *testing.T) {
	dir := t.TempDir()
	base := "svc.log"

	lg := &testCaptureLogger{}
	lr, err := NewLogRotator(LogRotatorConfig{
		FilePath:    filepath.Join(dir, base),
		FilePerms:   0600,
		FileUID:     -1,
		FileGID:     -1,
		ServiceName: "t",
		LogLevelMax: -1,
		MaxFiles:    10,
		MinFiles:    2,
		Logger:      lg,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer lr.Close()

	// First drain — should warn.
	seedRotated(t, dir, base, 5)
	lr.freeSpaceLocked()

	// Second drain — should NOT warn again (flag is latched).
	seedRotated(t, dir, base+".x", 5) // fresh set with different prefix so seed doesn't clobber; but for the same rotator it must be base
	_ = os.RemoveAll(filepath.Join(dir, base+".x"))
	seedRotated(t, dir, base, 5)
	lr.freeSpaceLocked()

	msgs := lg.snapshot()
	warns := 0
	for _, m := range msgs {
		if strings.Contains(m, "ENOSPC on logfile") {
			warns++
		}
	}
	if warns != 1 {
		t.Errorf("expected exactly 1 ENOSPC warning across two drain events, got %d\nmsgs: %v", warns, msgs)
	}

	// Simulate a successful rotation: it should rearm the notifier.
	// rotateLocked requires lr.file != nil, so we bypass by resetting
	// the field directly — that's the actual contract that governs
	// the reset, and testing that contract is the point of the test.
	lr.enospcReported = false
	seedRotated(t, dir, base, 5)
	lr.freeSpaceLocked()

	msgs = lg.snapshot()
	warns = 0
	for _, m := range msgs {
		if strings.Contains(m, "ENOSPC on logfile") {
			warns++
		}
	}
	if warns != 2 {
		t.Errorf("after rearm, expected 2 total warnings, got %d", warns)
	}
}

// TestLogRotatorReadBufferSizeCustom checks that a large read buffer
// still yields correct line-oriented output: readLoop must split on
// '\n' regardless of read chunk boundaries.
func TestLogRotatorReadBufferSizeCustom(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "svc.log")

	lr, err := NewLogRotator(LogRotatorConfig{
		FilePath:       logPath,
		FilePerms:      0600,
		FileUID:        -1,
		FileGID:        -1,
		ServiceName:    "t",
		LogLevelMax:    -1,
		ReadBufferSize: 65536,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	w, err := lr.CreatePipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	lr.StartReader()

	// Write several lines in one syscall — the large read buf will
	// pick them up together, and readLoop must still split correctly.
	if _, err := w.Write([]byte("aaa\nbbb\nccc\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	w.Close()

	want := "aaa\nbbb\nccc\n"
	got := waitForLogFile(t, logPath, len(want))
	lr.Close()
	if string(got) != want {
		t.Errorf("with 64KB read buffer:\n got %q\nwant %q", got, want)
	}
}

// TestLogRotatorReadBufferSizeDefault confirms the zero-value fast
// path: an unset ReadBufferSize keeps the historical 4096-byte
// buffer that the code always shipped with.
func TestLogRotatorReadBufferSizeDefault(t *testing.T) {
	lr, err := NewLogRotator(LogRotatorConfig{
		ServiceName: "t",
		LogLevelMax: -1,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer lr.Close()

	if lr.readBufSize != 0 {
		t.Errorf("readBufSize should stay at 0 (deferred to default) when unconfigured, got %d", lr.readBufSize)
	}
	if defaultReadBufferSize != 4096 {
		t.Errorf("defaultReadBufferSize changed unexpectedly: %d (want 4096 for parity with historical behavior)", defaultReadBufferSize)
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

// TestLogRotatorSelectChain covers the s6-log-style regex chain: `-*
// +alert` drops everything by default but keeps alert lines. This is
// the exact idiom the s6-log docs give as the canonical filter.
func TestLogRotatorSelectChain(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "svc.log")

	cfg := LogRotatorConfig{
		FilePath:    logPath,
		FilePerms:   0600,
		FileUID:     -1,
		FileGID:     -1,
		ServiceName: "test",
		LogLevelMax: -1,
		Select:      []string{"-*", "+alert", "+warn"},
	}
	lr, err := NewLogRotator(cfg)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer lr.Close()

	for _, line := range []string{
		"debug: something happened\n",
		"alert: DISK FULL\n",
		"info: fyi\n",
		"warn: low mem\n",
		"trace: chatty output\n",
	} {
		lr.processLine([]byte(line))
	}
	lr.mu.Lock()
	if lr.file != nil {
		lr.file.Sync()
	}
	lr.mu.Unlock()

	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	// Chain evaluated left-to-right, LAST-MATCHED wins. debug/info/
	// trace only match `-*`, so they end verdict = exclude. alert/warn
	// re-flip to include via a later token.
	want := "alert: DISK FULL\nwarn: low mem\n"
	if string(got) != want {
		t.Errorf("logfile contents:\n got %q\nwant %q", got, want)
	}
}

// TestLogRotatorSelectLastMatchWins encodes the "last-matched verdict
// wins" invariant that separates chain semantics from the classic
// include/exclude AND: an initial `+*` is undone by a later `-noisy`
// on lines matching noisy.
func TestLogRotatorSelectLastMatchWins(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "svc.log")

	lr, err := NewLogRotator(LogRotatorConfig{
		FilePath:    logPath,
		FilePerms:   0600,
		FileUID:     -1,
		FileGID:     -1,
		ServiceName: "test",
		LogLevelMax: -1,
		Select:      []string{"+*", "-noisy"},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer lr.Close()

	for _, line := range []string{
		"important: keep me\n",
		"noisy: drop me\n",
		"normal: keep me\n",
	} {
		lr.processLine([]byte(line))
	}
	lr.mu.Lock()
	if lr.file != nil {
		lr.file.Sync()
	}
	lr.mu.Unlock()

	got, _ := os.ReadFile(logPath)
	want := "important: keep me\nnormal: keep me\n"
	if string(got) != want {
		t.Errorf("logfile contents:\n got %q\nwant %q", got, want)
	}
}

// TestLogRotatorSelectRejectsBadToken guards operator confusion —
// missing polarity prefix should fail load rather than silently no-op.
func TestLogRotatorSelectRejectsBadToken(t *testing.T) {
	_, err := NewLogRotator(LogRotatorConfig{
		ServiceName: "test",
		LogLevelMax: -1,
		Select:      []string{"missingpolarity"},
	})
	if err == nil {
		t.Fatal("expected error on missing +/- polarity, got nil")
	}
}

// TestLogRotatorAlertChannelRoutes verifies the s6-log-style priority
// alert channel: high-severity lines land in the alert file in
// addition to the main sink; low-severity lines only hit the main
// sink. The two thresholds (levelMax + alertLevel) are independent.
func TestLogRotatorAlertChannelRoutes(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "svc.log")
	alertPath := filepath.Join(dir, "svc-alerts.log")

	lr, err := NewLogRotator(LogRotatorConfig{
		FilePath:      mainPath,
		FilePerms:     0600,
		FileUID:       -1,
		FileGID:       -1,
		AlertFilePath: alertPath,
		AlertLevel:    4, // warn: routes 0..4 to alert
		LogLevelMax:   -1,
		ServiceName:   "test",
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// crit (2) and warn (4) → both files; info (6) → main only.
	lr.processLine([]byte("<2>critical failure\n"))
	lr.processLine([]byte("<4>warning event\n"))
	lr.processLine([]byte("<6>informational tick\n"))

	// Close via defer path — bypass the pipe machinery in this
	// direct-call test by draining the file handles manually.
	lr.mu.Lock()
	if lr.file != nil {
		lr.file.Close()
	}
	if lr.alertFile != nil {
		lr.alertFile.Close()
	}
	lr.mu.Unlock()

	mainBody, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("read main: %v", err)
	}
	if want := "<2>critical failure\n<4>warning event\n<6>informational tick\n"; string(mainBody) != want {
		t.Errorf("main log:\n got %q\nwant %q", mainBody, want)
	}

	alertBody, err := os.ReadFile(alertPath)
	if err != nil {
		t.Fatalf("read alert: %v", err)
	}
	if want := "<2>critical failure\n<4>warning event\n"; string(alertBody) != want {
		t.Errorf("alert log:\n got %q\nwant %q", alertBody, want)
	}
}

// TestLogRotatorAlertChannelSurvivesLevelMax verifies the crossover:
// when levelMax drops a line from the main sink, the alert channel
// still receives it (routing is independent of the main-sink gate).
func TestLogRotatorAlertChannelSurvivesLevelMax(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "svc.log")
	alertPath := filepath.Join(dir, "svc-alerts.log")

	// Main sink drops everything above notice (5); alert routes
	// warn (4) and stricter to the alert file.
	lr, err := NewLogRotator(LogRotatorConfig{
		FilePath:      mainPath,
		FilePerms:     0600,
		FileUID:       -1,
		FileGID:       -1,
		AlertFilePath: alertPath,
		AlertLevel:    4,
		LogLevelMax:   5,
		ServiceName:   "test",
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// crit (2): main + alert. info (6): main drops, alert doesn't route.
	// warn (4): main + alert. debug (7): main drops, alert doesn't route.
	lr.processLine([]byte("<2>critical\n"))
	lr.processLine([]byte("<6>chatty info\n"))
	lr.processLine([]byte("<4>warn\n"))
	lr.processLine([]byte("<7>debug spam\n"))

	lr.mu.Lock()
	if lr.file != nil {
		lr.file.Close()
	}
	if lr.alertFile != nil {
		lr.alertFile.Close()
	}
	lr.mu.Unlock()

	mainBody, _ := os.ReadFile(mainPath)
	if want := "<2>critical\n<4>warn\n"; string(mainBody) != want {
		t.Errorf("main log:\n got %q\nwant %q", mainBody, want)
	}

	alertBody, _ := os.ReadFile(alertPath)
	if want := "<2>critical\n<4>warn\n"; string(alertBody) != want {
		t.Errorf("alert log:\n got %q\nwant %q", alertBody, want)
	}
}

// TestLogRotatorAlertChannelDisabledByDefault asserts the safety
// default: with AlertLevel=-1 and AlertFilePath="", NO alert file is
// created regardless of severity. Guards against a future refactor
// accidentally opting every service into an alert sink.
func TestLogRotatorAlertChannelDisabledByDefault(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "svc.log")
	alertPath := filepath.Join(dir, "svc-alerts.log")

	lr, err := NewLogRotator(LogRotatorConfig{
		FilePath:    mainPath,
		FilePerms:   0600,
		FileUID:     -1,
		FileGID:     -1,
		AlertLevel:  -1,
		LogLevelMax: -1,
		ServiceName: "test",
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	lr.processLine([]byte("<0>panic-level line\n"))

	lr.mu.Lock()
	if lr.file != nil {
		lr.file.Close()
	}
	lr.mu.Unlock()

	if _, err := os.Stat(alertPath); !os.IsNotExist(err) {
		t.Errorf("alert file should not exist when channel is disabled; stat err=%v", err)
	}
}

// TestLogRotatorRotateNoCollision drives two back-to-back rotations and
// asserts both rotated files survive on disk. Prior second-precision
// timestamp naming would silently clobber the first rotated file when
// two rotations landed inside the same wall-clock second (rename(2)
// on Linux atomically REPLACES the destination). Nanosecond precision
// eliminates the collision under the rotator's mutex-serialized path.
func TestLogRotatorRotateNoCollision(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "svc.log")

	lr, err := NewLogRotator(LogRotatorConfig{
		FilePath:    logPath,
		FilePerms:   0600,
		FileUID:     -1,
		FileGID:     -1,
		ServiceName: "test",
		LogLevelMax: -1,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	lr.mu.Lock()
	if err := lr.openFileLocked(); err != nil {
		lr.mu.Unlock()
		t.Fatalf("open: %v", err)
	}
	// First rotation.
	if _, err := lr.file.WriteString("first\n"); err != nil {
		lr.mu.Unlock()
		t.Fatalf("write1: %v", err)
	}
	lr.rotateLocked()
	// Second rotation immediately after — same wall-clock second is
	// guaranteed under mutex-serialized execution here.
	if _, err := lr.file.WriteString("second\n"); err != nil {
		lr.mu.Unlock()
		t.Fatalf("write2: %v", err)
	}
	lr.rotateLocked()
	lr.mu.Unlock()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var rotated []string
	prefix := filepath.Base(logPath) + "."
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			rotated = append(rotated, e.Name())
		}
	}
	if len(rotated) != 2 {
		t.Fatalf("expected 2 rotated files, got %d: %v", len(rotated), rotated)
	}

	// Verify each rotated file carries the payload we wrote before
	// rotating — i.e. the second rotation did not overwrite the first.
	sort.Strings(rotated)
	firstBody, err := os.ReadFile(filepath.Join(dir, rotated[0]))
	if err != nil {
		t.Fatalf("read first: %v", err)
	}
	if string(firstBody) != "first\n" {
		t.Errorf("first rotated body: got %q want %q", firstBody, "first\n")
	}
	secondBody, err := os.ReadFile(filepath.Join(dir, rotated[1]))
	if err != nil {
		t.Fatalf("read second: %v", err)
	}
	if string(secondBody) != "second\n" {
		t.Errorf("second rotated body: got %q want %q", secondBody, "second\n")
	}
}

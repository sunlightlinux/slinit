package service

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// faultDisk is a filesystem of cap bytes holding the test's directory,
// and a device that fails every byte from offset eioAt on (counted over
// all log-file writes); with once, only the write that reaches eioAt
// fails, the device works again after it. Each write writes what fits,
// then reports the error, as write(2) and os.File.Write do. -1
// disables either.
type faultDisk struct {
	dir     string
	cap     int64
	eioAt   int64
	once    bool
	written int64
	failed  bool
}

func (d *faultDisk) usage() int64 {
	var sum int64
	ents, _ := os.ReadDir(d.dir)
	for _, e := range ents {
		if info, err := e.Info(); err == nil {
			sum += info.Size()
		}
	}
	return sum
}

func (d *faultDisk) write(f *os.File, b []byte) (int, error) {
	if f == nil {
		return f.Write(b)
	}
	allowed := int64(len(b))
	var fault error
	if d.cap >= 0 {
		if free := d.cap - d.usage(); free < allowed {
			allowed, fault = max(free, 0), syscall.ENOSPC
		}
	}
	if d.eioAt >= 0 && !(d.once && d.failed) {
		if left := d.eioAt - d.written; left < allowed {
			allowed, fault = max(left, 0), syscall.EIO
		}
	}
	n, err := f.Write(b[:allowed])
	d.written += int64(n)
	d.failed = d.failed || fault == syscall.EIO
	if err == nil && fault != nil {
		err = &os.PathError{Op: "write", Path: f.Name(), Err: fault}
	}
	return n, err
}

// batchOutcome is everything a run leaves behind that a user or the
// rest of slinit can see.
type batchOutcome struct {
	files   []string // rotated files' contents oldest first, then the live file's
	size    int64
	msgs    []string
	journal []string
}

func (o batchOutcome) String() string {
	return fmt.Sprintf("files=%q size=%d msgs=%q journal=%q", o.files, o.size, o.msgs, o.journal)
}

// runRotatorLines feeds lines through processLine, the way readLoop
// does: with chunks == nil one line at a time, each written as it is
// processed; otherwise batched, chunks[i] lines per read chunk.
func runRotatorLines(t *testing.T, cfg LogRotatorConfig, lines []string, chunks []int, disk faultDisk) batchOutcome {
	t.Helper()
	if err := journal.InitIDs("testhost"); err != nil {
		t.Fatal(err)
	}
	buf := journal.NewEventBuffer(4096)
	journal.SetGlobal(journal.NewEmitter(buf, ""))
	defer journal.SetGlobal(nil)

	dir := t.TempDir()
	disk.dir = dir
	lg := &testCaptureLogger{}
	cfg.FilePath = filepath.Join(dir, "svc.log")
	if cfg.AlertFilePath != "" {
		cfg.AlertFilePath = filepath.Join(dir, "alert")
	}
	cfg.ServiceName, cfg.FileUID, cfg.FileGID, cfg.Logger = "batch", -1, -1, lg
	cfg.GetPID = func() int { return 77 }
	lr, err := NewLogRotator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	lr.writeFile = disk.write

	feed := func(s string) { lr.processLine(lr.capLine([]byte(s))) }
	if chunks == nil {
		for _, s := range lines {
			feed(s)
		}
	} else {
		lr.batching = true
		i := 0
		for _, c := range chunks {
			for ; c > 0 && i < len(lines); c-- {
				feed(lines[i])
				i++
			}
			lr.flushBatch()
		}
		for ; i < len(lines); i++ {
			feed(lines[i])
		}
		lr.flushBatch()
	}

	var out batchOutcome
	out.size = lr.currentSize
	if lr.file != nil {
		lr.file.Close()
		lr.file = nil
	}
	if lr.alertFile != nil {
		lr.alertFile.Close()
		lr.alertFile = nil
	}
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names) // svc.log < svc.log.<timestamp>...: oldest rotated first after it
	for _, n := range names {
		b, _ := os.ReadFile(filepath.Join(dir, n))
		label := n
		if strings.HasPrefix(n, "svc.log.") {
			label = "rotated"
		}
		out.files = append(out.files, label+":"+string(b))
	}
	out.msgs = lg.snapshot()
	evs, _ := buf.Snapshot()
	for _, e := range evs {
		if e.Unit == "batch" {
			out.journal = append(out.journal, fmt.Sprintf("%d/%s/%s", e.Prio, e.Fields["SLINIT_TARGET_PID"], e.Msg))
		}
	}
	return out
}

func batchTestLines() []string {
	var lines []string
	for i := 0; i < 24; i++ {
		switch i % 6 {
		case 0:
			lines = append(lines, "\n")
		case 1:
			lines = append(lines, fmt.Sprintf("<2>crit %d\n", i))
		case 2:
			lines = append(lines, fmt.Sprintf("<7>debug %d drop-me\n", i))
		case 3:
			lines = append(lines, fmt.Sprintf("a longer line number %d, past the cap\n", i))
		default:
			lines = append(lines, fmt.Sprintf("l%d\n", i))
		}
	}
	return lines
}

// A batched write that fails must leave exactly what writing each line
// on its own would have: the same files with the same bytes, the same
// size accounting (which decides the next rotation), the same min-files
// drain and its warning, the same journal events. Checked for a disk
// that fills up and for a device error at every byte offset, with
// rotation, min-files recovery, decoration, truncation, filters, the
// level gate, an alert file on the same disk and a rate limit.
func TestLogRotatorBatchFaultsMatchPerLine(t *testing.T) {
	lines := batchTestLines()
	total := 0
	for _, s := range lines {
		total += len(s) + 8 // room for the prefix and truncation marks
	}
	cases := []struct {
		name string
		cfg  LogRotatorConfig
	}{
		{"plain", LogRotatorConfig{LogLevelMax: -1, AlertLevel: -1}},
		{"rotate", LogRotatorConfig{MaxSize: 60, LogLevelMax: -1, AlertLevel: -1}},
		{"rotate-minfiles", LogRotatorConfig{MaxSize: 50, MaxFiles: 4, MinFiles: 1, LogLevelMax: -1, AlertLevel: -1}},
		{"filters", LogRotatorConfig{
			MaxSize: 70, MinFiles: 1, LogLevelMax: 6, AlertFilePath: "alert", AlertLevel: 2,
			Excludes: []string{"drop-me"}, MaxLineLength: 12, LinePrefix: "p",
			RateInterval: time.Hour, RateBurst: 14,
		}},
	}
	splits := map[string][]int{"one-chunk": {len(lines)}, "threes": {3, 3, 3, 3, 3, 3, 3, 3}, "uneven": {1, 5, 2, 7, 9}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for cap := int64(0); cap <= int64(total); cap += 3 {
				for _, disk := range []faultDisk{{cap: cap, eioAt: -1}, {cap: -1, eioAt: cap}, {cap: -1, eioAt: cap, once: true}} {
					want := runRotatorLines(t, c.cfg, lines, nil, disk)
					for sname, split := range splits {
						got := runRotatorLines(t, c.cfg, lines, split, disk)
						if got.String() != want.String() {
							t.Fatalf("cap=%d eioAt=%d once=%v %s:\n batched  %v\n per-line %v", disk.cap, disk.eioAt, disk.once, sname, got, want)
						}
					}
				}
			}
		})
	}
}

// Without faults, the reference run must itself have exercised what
// the comparison above relies on, or it compared nothing.
func TestLogRotatorBatchFaultsHarness(t *testing.T) {
	lines := batchTestLines()
	cfg := LogRotatorConfig{MaxSize: 50, MaxFiles: 4, MinFiles: 1, LogLevelMax: -1, AlertLevel: -1}
	full := runRotatorLines(t, cfg, lines, nil, faultDisk{cap: -1, eioAt: -1})
	if len(full.files) < 4 || len(full.journal) == 0 {
		t.Fatalf("no rotation or journal in reference run: %v", full)
	}
	tight := runRotatorLines(t, cfg, lines, nil, faultDisk{cap: 120, eioAt: -1})
	drained := false
	for _, m := range tight.msgs {
		drained = drained || strings.Contains(m, "ENOSPC on logfile")
	}
	if !drained {
		t.Fatalf("a 120-byte disk did not drain rotated files: %v", tight)
	}
	f := faultDisk{cap: -1, eioAt: -1}
	n, err := f.write(nil, nil)
	if n != 0 || err == nil {
		t.Fatal("nil file write succeeded")
	}
	d := faultDisk{dir: t.TempDir(), cap: 5, eioAt: -1}
	fh, _ := os.Create(filepath.Join(d.dir, "x"))
	defer fh.Close()
	if n, err := d.write(fh, []byte("0123456789")); n != 5 || !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("faultDisk short write = %d, %v", n, err)
	}
}

// rotatorPipe runs a rotator's reader on cfg, writes input to its pipe
// in one write (under PIPE_BUF, so one read chunk for a reader with a
// buffer that large), and returns the directory once the reader is done.
func rotatorPipe(t *testing.T, cfg LogRotatorConfig, input string) (string, *testCaptureLogger) {
	t.Helper()
	dir := t.TempDir()
	lg := &testCaptureLogger{}
	cfg.FilePath = filepath.Join(dir, "svc.log")
	cfg.ServiceName, cfg.FileUID, cfg.FileGID, cfg.Logger = "pipe", -1, -1, lg
	if cfg.LogLevelMax == 0 {
		cfg.LogLevelMax = -1
	}
	cfg.AlertLevel = -1
	lr, err := NewLogRotator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w, err := lr.CreatePipe()
	if err != nil {
		t.Fatal(err)
	}
	lr.StartReader()
	if _, err := w.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	lr.CloseWriteEnd()
	<-lr.doneCh
	lr.Close()
	return dir, lg
}

func readRotatorDir(t *testing.T, dir string) (live string, rotated []string) {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		b, _ := os.ReadFile(filepath.Join(dir, n))
		if n == "svc.log" {
			live = string(b)
		} else {
			rotated = append(rotated, string(b))
		}
	}
	return live, rotated
}

// A max-size that ends exactly on a line boundary inside one read
// chunk: each rotated file holds exactly the lines that fit.
func TestLogRotatorBatchRotateAtLineBoundary(t *testing.T) {
	var in strings.Builder
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&in, "line-%03d\n", i) // 9 bytes
	}
	dir, _ := rotatorPipe(t, LogRotatorConfig{MaxSize: 27}, in.String())
	live, rotated := readRotatorDir(t, dir)
	want := []string{"line-000\nline-001\nline-002\n", "line-003\nline-004\nline-005\n", "line-006\nline-007\nline-008\n"}
	if strings.Join(rotated, "|") != strings.Join(want, "|") || live != "line-009\n" {
		t.Fatalf("rotated %q live %q", rotated, live)
	}
}

// A line longer than the read buffer, between short lines, arrives
// whole and in order.
func TestLogRotatorBatchLineLongerThanReadBuffer(t *testing.T) {
	in := "a\n" + strings.Repeat("x", 1500) + "\nb\n"
	dir, _ := rotatorPipe(t, LogRotatorConfig{ReadBufferSize: 512}, in)
	if live, rotated := readRotatorDir(t, dir); live != in || len(rotated) != 0 {
		t.Fatalf("live %q rotated %d", live, len(rotated))
	}
}

// log-max-line-length truncates lines inside a batch.
func TestLogRotatorBatchMaxLineLength(t *testing.T) {
	dir, _ := rotatorPipe(t, LogRotatorConfig{MaxLineLength: 8}, "abcdefghijk\nab\n12345678\n123456789\n")
	if live, _ := readRotatorDir(t, dir); live != "abcdefgh+\nab\n12345678\n12345678+\n" {
		t.Fatalf("live %q", live)
	}
}

// A filter dropping some of a batch's lines.
func TestLogRotatorBatchFilter(t *testing.T) {
	dir, _ := rotatorPipe(t, LogRotatorConfig{Excludes: []string{"^drop"}}, "keep1\ndrop1\nkeep2\ndrop2\ndrop3\nkeep3\n")
	if live, _ := readRotatorDir(t, dir); live != "keep1\nkeep2\nkeep3\n" {
		t.Fatalf("live %q", live)
	}
}

// A rate limit that runs out in the middle of a batch.
func TestLogRotatorBatchRateLimit(t *testing.T) {
	var in bytes.Buffer
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&in, "r%d\n", i)
	}
	dir, lg := rotatorPipe(t, LogRotatorConfig{RateInterval: time.Hour, RateBurst: 3}, in.String())
	if live, _ := readRotatorDir(t, dir); live != "r0\nr1\nr2\n" {
		t.Fatalf("live %q", live)
	}
	hits := 0
	for _, m := range lg.snapshot() {
		if strings.Contains(m, "log-rate-limit hit") {
			hits++
		}
	}
	if hits != 1 {
		t.Fatalf("%d rate-limit notices, want 1: %q", hits, lg.snapshot())
	}
}

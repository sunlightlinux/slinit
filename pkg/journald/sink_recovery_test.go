package journald

import (
	"bytes"
	"encoding/json"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journalbin"
)

// limitFileSize models a full disk: with RLIMIT_FSIZE lowered, a write
// past n bytes fails with EFBIG, the same path ENOSPC takes. The
// returned func gives the space back.
func limitFileSize(t *testing.T, n uint64) (restore func()) {
	t.Helper()
	signal.Ignore(syscall.SIGXFSZ)
	var old syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: n, Max: old.Max}); err != nil {
		t.Skipf("cannot lower RLIMIT_FSIZE: %v", err)
	}
	done := false
	restore = func() {
		if done {
			return
		}
		done = true
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old); err != nil {
			t.Fatal(err)
		}
		signal.Reset(syscall.SIGXFSZ)
	}
	t.Cleanup(restore)
	return restore
}

// unwritableDir makes dir refuse creates and renames until the returned
// func runs. Root ignores the mode, so the test is skipped there.
func unwritableDir(t *testing.T, dir string) (restore func()) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	restore = func() { os.Chmod(dir, 0o755) }
	t.Cleanup(restore)
	return restore
}

// checkJSONLFiles fails the test if any .jsonl in dir has a line that is
// not a whole JSON object, or does not end in a newline.
func checkJSONLFiles(t *testing.T, dir string) (lines int) {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if len(b) > 0 && b[len(b)-1] != '\n' {
			t.Errorf("%s ends in a partial line", e.Name())
		}
		for _, l := range bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n")) {
			if len(l) == 0 {
				continue
			}
			var ev journal.Event
			if err := json.Unmarshal(l, &ev); err != nil {
				t.Errorf("%s: bad line %.60q: %v", e.Name(), l, err)
			}
			lines++
		}
	}
	return lines
}

// bufio's error is sticky: after one failed write on a full disk every
// later event, Flush and Rotate failed with it, long after space came
// back (ok=0 of 10), and the file ended in a partial line.
func TestFileSinkRecoversAfterWriteError(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileSinkWithOptions(dir, FileSinkOptions{FsyncEvery: 1, MaxSize: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	restore := limitFileSize(t, 2048)
	fails := 0
	for i := 0; i < 20; i++ {
		if s.Handle(journalEvent(i, "b")) != nil {
			fails++
		}
	}
	restore()
	if fails == 0 {
		t.Fatal("full disk did not fail any write; the test proves nothing")
	}
	written, errs := s.Stats()
	if written+errs != 20 {
		t.Errorf("written %d + errors %d != 20 events", written, errs)
	}

	for i := 100; i < 110; i++ {
		if err := s.Handle(journalEvent(i, "b")); err != nil {
			t.Fatalf("event after space came back: %v", err)
		}
	}
	if err := s.Rotate(); err != nil {
		t.Fatalf("Rotate after recovery: %v", err)
	}
	for i := 200; i < 205; i++ {
		if err := s.Handle(journalEvent(i, "b")); err != nil {
			t.Fatalf("event after Rotate: %v", err)
		}
	}
	s.Close()
	written, _ = s.Stats()
	if n := checkJSONLFiles(t, dir); uint64(n) != written {
		t.Errorf("%d lines on disk, sink counted %d written", n, written)
	}
}

// A rotation whose rename failed closed the file and never reopened it:
// "sink closed" for every event after, until a restart.
func TestFileSinkRecoversAfterFailedRotation(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileSink(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Handle(journalEvent(1, "b"))
	restore := unwritableDir(t, dir)
	if err := s.Rotate(); err == nil {
		t.Fatal("rotate in an unwritable dir succeeded; the test proves nothing")
	}
	// The rename failed but the current file is still there to write.
	if err := s.Handle(journalEvent(2, "b")); err != nil {
		t.Errorf("event after a failed rotation: %v", err)
	}
	restore()
	if err := s.Rotate(); err != nil {
		t.Fatalf("Rotate once the dir is writable: %v", err)
	}
	if err := s.Handle(journalEvent(3, "b")); err != nil {
		t.Errorf("event after the rotation: %v", err)
	}
	s.Close()
	if n := checkJSONLFiles(t, dir); n != 3 {
		t.Errorf("%d lines on disk, want 3", n)
	}
}

// The binary sink dropped its writer before renaming and reopening, and
// a failure of either left it nil for good: ok=0 of 5 after the
// directory was writable again, and an explicit Rotate did not help.
func TestBinarySinkRecoversAfterFailedRotation(t *testing.T) {
	dir := t.TempDir()
	s, err := NewBinarySink(BinarySinkOptions{Dir: dir, FsyncEvery: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 3; i++ {
		if err := s.Handle(journalEvent(i, "")); err != nil {
			t.Fatal(err)
		}
	}
	restore := unwritableDir(t, dir)
	if err := s.Rotate(); err == nil {
		t.Fatal("rotate in an unwritable dir succeeded; the test proves nothing")
	}
	restore()
	for i := 10; i < 15; i++ {
		if err := s.Handle(journalEvent(i, "")); err != nil {
			t.Fatalf("event after the dir is writable again: %v", err)
		}
	}
	if err := s.Rotate(); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if err := s.Handle(journalEvent(99, "")); err != nil {
		t.Fatalf("event after Rotate: %v", err)
	}
	s.Close()
	if n := countBinaryEntries(t, dir); n != 9 {
		t.Errorf("%d entries on disk, want 9", n)
	}
}

// The file being written was removed under the sink (a vacuum that took
// it for an old one). Events went to the unlinked inode, and the next
// rotation failed its rename and closed the sink for good.
func TestBinarySinkActiveFileRemoved(t *testing.T) {
	dir := t.TempDir()
	s, err := NewBinarySink(BinarySinkOptions{Dir: dir, FsyncEvery: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Handle(journalEvent(1, ""))
	if err := os.Remove(s.CurrentPath()); err != nil {
		t.Fatal(err)
	}
	// This one lands in the removed file; the sink notices after it.
	s.Handle(journalEvent(2, ""))
	if _, err := os.Stat(s.CurrentPath()); err != nil {
		t.Fatalf("no new file under the current name: %v", err)
	}
	if err := s.Handle(journalEvent(3, "")); err != nil {
		t.Fatalf("event after the file was removed: %v", err)
	}
	if err := s.Rotate(); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if err := s.Handle(journalEvent(4, "")); err != nil {
		t.Fatalf("event after Rotate: %v", err)
	}
	s.Close()
	if n := countBinaryEntries(t, dir); n != 2 {
		t.Errorf("%d entries on disk, want 2 (events 3 and 4)", n)
	}
}

func countBinaryEntries(t *testing.T, dir string) int {
	t.Helper()
	mr, err := journalbin.OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	n := 0
	if err := mr.Iter(func(*journal.Event) bool { n++; return true }); err != nil {
		t.Fatal(err)
	}
	return n
}

// A start on a full disk left the day's file with hash tables and no
// header (4096 bytes); every later start failed with "bad magic" and
// the daemon exited for the rest of the UTC day.
func TestBinarySinkStartsAfterFailedCreate(t *testing.T) {
	dir := t.TempDir()
	restore := limitFileSize(t, 4096)
	if _, err := NewBinarySink(BinarySinkOptions{Dir: dir}); err == nil {
		t.Fatal("create on a full disk succeeded; the test proves nothing")
	}
	restore()
	s, err := NewBinarySink(BinarySinkOptions{Dir: dir, FsyncEvery: 1})
	if err != nil {
		t.Fatalf("start after space came back: %v", err)
	}
	if err := s.Handle(journalEvent(1, "")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if n := countBinaryEntries(t, dir); n != 1 {
		t.Errorf("%d entries, want 1", n)
	}
}

// The same file left by an older writer: kept under a .corrupt name, not
// deleted, and a new file started.
func TestBinarySinkMovesHeaderlessFileAside(t *testing.T) {
	for name, junk := range map[string][]byte{
		"zeroes": make([]byte, 4096),
		"short":  []byte("SLJR"),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, binaryCurrentFileName(time.Now().UTC()))
			if err := os.WriteFile(path, junk, 0o640); err != nil {
				t.Fatal(err)
			}
			s, err := NewBinarySink(BinarySinkOptions{Dir: dir, FsyncEvery: 1})
			if err != nil {
				t.Fatalf("start with a headerless day file: %v", err)
			}
			if err := s.Handle(journalEvent(1, "")); err != nil {
				t.Fatal(err)
			}
			s.Close()
			kept, _ := filepath.Glob(path + ".*.corrupt")
			if len(kept) != 1 {
				t.Fatalf("headerless file not kept aside: %v", kept)
			}
			if b, _ := os.ReadFile(kept[0]); !bytes.Equal(b, junk) {
				t.Errorf("kept file changed")
			}
			// Readers do not trip over it.
			if n := countBinaryEntries(t, dir); n != 1 {
				t.Errorf("%d entries, want 1", n)
			}
		})
	}
}

// A file with a valid magic that this version cannot read (a newer
// format) is not set aside.
func TestBinarySinkLeavesUnreadableJournalAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, binaryCurrentFileName(time.Now().UTC()))
	hdr := make([]byte, journalbin.HeaderSize)
	copy(hdr, journalbin.Magic[:])
	hdr[12] = 0x80 // an incompat flag this version does not know
	if err := os.WriteFile(path, hdr, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := NewBinarySink(BinarySinkOptions{Dir: dir}); err == nil {
		t.Fatal("opened a journal with unknown incompat flags")
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, hdr) {
		t.Error("unreadable journal was moved or changed")
	}
	if kept, _ := filepath.Glob(path + ".*.corrupt"); len(kept) != 0 {
		t.Errorf("set aside: %v", kept)
	}
}

// A rotation on a full disk: the new file cannot be created. Once space
// is back the sink resumes, where it used to stay closed.
func TestBinarySinkRecoversAfterFailedReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := NewBinarySink(BinarySinkOptions{Dir: dir, FsyncEvery: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Handle(journalEvent(1, ""))
	restore := limitFileSize(t, 4096)
	if err := s.Rotate(); err == nil {
		t.Fatal("rotation on a full disk succeeded; the test proves nothing")
	}
	restore()
	s.mu.Lock()
	s.retryAt = time.Time{} // skip the 1 s wait before the next attempt
	s.mu.Unlock()
	for i := 0; i < 5; i++ {
		if err := s.Handle(journalEvent(10+i, "")); err != nil {
			t.Fatalf("event after space came back: %v", err)
		}
	}
	s.Close()
	if n := countBinaryEntries(t, dir); n != 6 {
		t.Errorf("%d entries, want 6", n)
	}
}

// checkIdxMatches fails the test unless the idx of the jsonl at path has
// one record per line, each pointing at the start of its line.
func checkIdxMatches(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var starts []int64
	for off := 0; off < len(b); {
		starts = append(starts, int64(off))
		n := bytes.IndexByte(b[off:], '\n')
		if n < 0 {
			break
		}
		off += n + 1
	}
	r, err := OpenIdx(idxPath(path))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Len() != int64(len(starts)) {
		t.Fatalf("idx has %d records for %d lines", r.Len(), len(starts))
	}
	for i := range starts {
		rec, err := r.At(int64(i))
		if err != nil {
			t.Fatal(err)
		}
		if rec.Offset != starts[i] {
			t.Errorf("idx record %d points at %d, line starts at %d", i, rec.Offset, starts[i])
		}
	}
}

// A batch cut short by a full disk was truncated back to the last fsync,
// deleting the events that reached the file whole: 32 events, a 1000-byte
// limit and FsyncEvery 32 kept 0 lines where 3 had been written in full.
func TestFileSinkShortWriteKeepsWholeLines(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileSinkWithOptions(dir, FileSinkOptions{FsyncEvery: 32, MaxSize: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const limit = 1000
	whole, size := 0, 0
	evs := make([]*journal.Event, 32)
	for i := range evs {
		evs[i] = journalEvent(i, "b")
		b, _ := json.Marshal(evs[i])
		if size += len(b) + 1; size <= limit {
			whole++
		}
	}
	restore := limitFileSize(t, limit)
	for _, ev := range evs {
		s.Handle(ev)
	}
	restore()
	written, errs := s.Stats()
	if written != uint64(whole) || errs != uint64(32-whole) {
		t.Errorf("written %d, errors %d; want %d and %d", written, errs, whole, 32-whole)
	}
	// The sink carries on, and the idx indexes the kept lines and the
	// new ones, not the events that were dropped.
	for i := 100; i < 103; i++ {
		if err := s.Handle(journalEvent(i, "b")); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	if n := checkJSONLFiles(t, dir); n != whole+3 {
		t.Errorf("%d lines on disk, want %d", n, whole+3)
	}
	checkIdxMatches(t, s.CurrentPath())
}

// An fsync error after the batch was written in full truncated the whole
// batch away; nothing in the file is partial, so nothing is cut.
func TestFileSinkFsyncErrorKeepsWrittenEvents(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileSinkWithOptions(dir, FileSinkOptions{FsyncEvery: 4, MaxSize: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	failed := false
	s.fsync = func(f *os.File) error {
		if !failed && strings.HasSuffix(f.Name(), ".jsonl") {
			failed = true
			return syscall.EIO
		}
		return f.Sync()
	}
	for i := 0; i < 4; i++ {
		s.Handle(journalEvent(i, "b"))
	}
	if !failed {
		t.Fatal("fsync never ran; the test proves nothing")
	}
	if w, e := s.Stats(); w != 4 || e != 0 {
		t.Errorf("written %d, errors %d; want 4 and 0", w, e)
	}
	for i := 4; i < 8; i++ {
		if err := s.Handle(journalEvent(i, "b")); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	if n := checkJSONLFiles(t, dir); n != 8 {
		t.Errorf("%d lines on disk, want 8", n)
	}
	checkIdxMatches(t, s.CurrentPath())
}

// A failed idx write is cut back to the last whole record: a partial one
// left behind shifted every record after it.
func TestFileSinkIdxWriteErrorCutsBack(t *testing.T) {
	dir := t.TempDir()
	jsonl := filepath.Join(dir, currentFileName(time.Now().UTC()))
	if err := os.WriteFile(jsonl, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	// An idx larger than the lines will be, so the idx crosses the
	// limit first, half a record past it.
	const pre = 250 * IdxRecordSize
	if err := os.WriteFile(idxPath(jsonl), make([]byte, pre), 0o640); err != nil {
		t.Fatal(err)
	}
	s, err := NewFileSinkWithOptions(dir, FileSinkOptions{FsyncEvery: 1, MaxSize: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	restore := limitFileSize(t, pre+IdxRecordSize/2)
	if err := s.Handle(journalEvent(1, "b")); err == nil {
		t.Fatal("idx write past the limit succeeded; the test proves nothing")
	}
	restore()
	if st, _ := os.Stat(idxPath(jsonl)); st.Size() != pre {
		t.Errorf("idx is %d bytes after the failed write, want %d", st.Size(), pre)
	}
	if err := s.Handle(journalEvent(2, "b")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if st, _ := os.Stat(idxPath(jsonl)); st.Size() != pre+2*IdxRecordSize {
		t.Errorf("idx is %d bytes, want %d (both events indexed once)", st.Size(), pre+2*IdxRecordSize)
	}
	if w, e := s.Stats(); w != 2 || e != 0 {
		t.Errorf("written %d, errors %d; want 2 and 0", w, e)
	}
}

// A rotation whose flush failed (here the idx fsync) ran again on every
// event, each one paying the flush and its fsyncs, and counted events
// that were written as write errors.
func TestFileSinkFailedRotationFlushWaits(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileSinkWithOptions(dir, FileSinkOptions{FsyncEvery: 100, MaxSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	s.now = func() time.Time { return now }
	syncs := 0
	s.fsync = func(f *os.File) error {
		syncs++
		if strings.HasSuffix(f.Name(), ".idx") {
			return syscall.EIO
		}
		return f.Sync()
	}
	if err := s.Handle(journalEvent(0, "b")); err == nil {
		t.Fatal("rotation with a failing idx fsync succeeded; the test proves nothing")
	}
	first := syncs
	for i := 1; i < 5; i++ {
		if err := s.Handle(journalEvent(i, "b")); err != nil {
			t.Errorf("event %d within a second of the failed rotation: %v", i, err)
		}
	}
	if syncs != first {
		t.Errorf("%d fsyncs after the failed rotation, want none until the retry", syncs-first)
	}
	if w, e := s.Stats(); w != 5 || e != 0 {
		t.Errorf("written %d, errors %d; want 5 and 0", w, e)
	}
	s.fsync = (*os.File).Sync
	now = now.Add(reopenInterval)
	if err := s.Handle(journalEvent(5, "b")); err != nil {
		t.Fatal(err)
	}
	if s.Rotations() != 1 {
		t.Errorf("%d rotations once the wait was over, want 1", s.Rotations())
	}
}

// An automatic rotation that failed was attempted again by every event
// (closing and reopening the writer each time) instead of once a second.
func TestBinarySinkFailedRotationWaits(t *testing.T) {
	dir := t.TempDir()
	s, err := NewBinarySink(BinarySinkOptions{Dir: dir, FsyncEvery: 1, MaxSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	unwritableDir(t, dir)
	if err := s.Handle(journalEvent(0, "")); err == nil {
		t.Fatal("rotation in an unwritable dir succeeded; the test proves nothing")
	}
	for i := 1; i < 4; i++ {
		if err := s.Handle(journalEvent(i, "")); err != nil {
			t.Errorf("event %d within a second of the failed rotation: %v", i, err)
		}
	}
}

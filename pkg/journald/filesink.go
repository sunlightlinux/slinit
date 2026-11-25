package journald

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// DefaultJournalDir is where slinit-journald persists JSONL files by
// default. /var/log/slinit-journal/ keeps them separate from
// slinit-cranial system logs, mirrors systemd's /var/log/journal/
// convention, and stays under /var so retention outlives reboots.
const DefaultJournalDir = "/var/log/slinit-journal"

// DefaultVolatileDir is the fallback used when DefaultJournalDir is
// not writable (missing /var partition, EROFS, ENOSPC, container
// without a persistent mount). /run is tmpfs so entries evaporate on
// reboot — better than silently dropping every log line, and matches
// systemd-journald's `Storage=auto` fallback to /run/log/journal.
const DefaultVolatileDir = "/run/slinit-journal"

// DefaultFsyncEvery is how many events land between forced fsyncs.
// 32 balances two extremes: fsyncing per event costs ~ms and
// serialises writes on rotational media; never fsyncing risks
// losing minutes of logs on power loss. 32 keeps steady-state
// worst-case loss under a second at typical slinit emit rates.
const DefaultFsyncEvery = 32

// DefaultMaxSize is the size cap that triggers rotation. 128 MiB
// matches the plan in project_journal_pipeline.md — small enough
// that a corrupt tail costs at most one file's worth of history,
// big enough that busy systems don't rotate every few minutes.
const DefaultMaxSize int64 = 128 << 20

// DefaultMaxAge is the age cap that triggers rotation. 24h means a
// day of logs lives in one file (with same-day intra-file suffix
// on size-triggered rotates), matching operator expectations from
// journalctl's default retention slicing.
const DefaultMaxAge = 24 * time.Hour

// FileSink writes each event as one JSONL line to a dated file
// under `dir`. The file name convention matches Phase 3d's rotation
// scheme (YYYY-MM-DD.jsonl) so 3d only has to handle the intra-day
// re-open case (`.NN` suffix on size trigger).
//
// Safe for concurrent Handle calls — bufio.Writer + mutex serialize
// writes so events land in strict arrival order.
type FileSink struct {
	dir        string
	fsyncEvery int
	// maxSize / maxAge are rotation triggers. Zero means "disabled"
	// on that dimension — callers passing 0 for both get an
	// append-forever sink.
	maxSize int64
	maxAge  time.Duration
	// rotatedHook fires after a successful rotation. Vacuum (3e)
	// wires here so pruning happens synchronously with rotation
	// rather than needing its own timer. Both paths are passed so
	// the hook doesn't have to call back into the sink (which
	// would deadlock — the hook fires with s.mu held).
	rotatedHook func(rotatedPath, currentPath string)

	// now is the clock the age trigger reads; time.Now outside tests.
	now func() time.Time

	mu       sync.Mutex
	f        *os.File
	bw       *bufio.Writer
	curPath  string
	openedAt time.Time

	// idxF is the companion .idx file (see idx.go). Writes are
	// batched: entries queue in pendingIdx until the next flush, so
	// idx and jsonl stay consistent on disk (the idx never points
	// past the end of the jsonl).
	idxF       *os.File
	pendingIdx []IdxRecord
	// totalWritten counts bytes handed to the JSONL bufio (whether
	// flushed to fd yet or not) — the pending idx entries reference
	// these offsets, which become file offsets on the next Flush.
	totalWritten int64

	// durable / idxDurable are the jsonl and idx sizes known to end on
	// a whole line / record. A failed write is cut back no further than
	// the last whole line it left (discardLocked), so a full disk leaves
	// no partial line behind and loses no line that reached the file.
	durable    int64
	idxDurable int64

	// fsync is (*os.File).Sync; tests replace it to fail.
	fsync func(*os.File) error

	// closed is set by Close: only then is a missing file final. A file
	// lost to a failed rotation or reopen is reopened by a later event,
	// at most once per reopenInterval; retryAt is when the next attempt
	// (or the next automatic rotation, after one failed) may run.
	closed  bool
	retryAt time.Time

	unflushed int
	written   atomic.Uint64
	writeErrs atomic.Uint64
	rotations atomic.Uint64
}

// reopenInterval bounds how often a sink retries opening its file, or an
// automatic rotation, after a failure: often enough to resume within a
// second of space coming back, not so often that every event of a busy
// system pays for an open that is bound to fail.
const reopenInterval = time.Second

// FileSinkOptions configures a FileSink. Any zero-valued field takes
// the corresponding Default*. Kept as a struct (not positional args)
// so 3e/3f can add vacuum + LZ4 options without churning the
// constructor signature at every callsite.
type FileSinkOptions struct {
	FsyncEvery int
	MaxSize    int64
	MaxAge     time.Duration
	// RotatedHook fires after a successful rotation with the path
	// of the file that was closed and renamed, plus the new
	// current path. Vacuum (3e) uses this to prune old files
	// right after a rotation without calling back into the sink
	// (that would deadlock — the hook fires with the sink's
	// internal mutex held).
	RotatedHook func(rotatedPath, currentPath string)
}

// NewFileSink opens (creating if needed) a JSONL file under dir named
// after today's UTC date. Returns an error if dir cannot be created
// or the file cannot be opened for append.
//
// fsyncEvery ≤ 0 selects DefaultFsyncEvery. For rotation tuning use
// NewFileSinkWithOptions.
func NewFileSink(dir string, fsyncEvery int) (*FileSink, error) {
	return NewFileSinkWithOptions(dir, FileSinkOptions{FsyncEvery: fsyncEvery})
}

// NewFileSinkWithOptions is the full constructor. Callers that don't
// need to override rotation limits use NewFileSink instead.
func NewFileSinkWithOptions(dir string, opts FileSinkOptions) (*FileSink, error) {
	if dir == "" {
		dir = DefaultJournalDir
	}
	if opts.FsyncEvery <= 0 {
		opts.FsyncEvery = DefaultFsyncEvery
	}
	if opts.MaxSize == 0 {
		opts.MaxSize = DefaultMaxSize
	}
	if opts.MaxAge == 0 {
		opts.MaxAge = DefaultMaxAge
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("journald: mkdir %s: %w", dir, err)
	}
	s := &FileSink{
		dir:         dir,
		fsyncEvery:  opts.FsyncEvery,
		maxSize:     opts.MaxSize,
		maxAge:      opts.MaxAge,
		rotatedHook: opts.RotatedHook,
		now:         time.Now,
		fsync:       (*os.File).Sync,
	}
	if err := s.openCurrent(); err != nil {
		return nil, err
	}
	return s, nil
}

// CurrentPath returns the path of the file receiving writes. Exposed
// for tests and for a future `slinit-journald --status` reporter.
func (s *FileSink) CurrentPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.curPath
}

// Stats returns cumulative (written, writeErrs) counters. Diagnostic;
// the receiver already bumps its own dropped counter on Handle error,
// so writeErrs here surfaces where the drop originated (sink vs
// upstream).
func (s *FileSink) Stats() (written, writeErrs uint64) {
	return s.written.Load(), s.writeErrs.Load()
}

// Handle serializes evt to JSONL, buffers the write, and fsyncs
// every fsyncEvery events. Errors from the write are surfaced so the
// receiver can count them; a write failure does NOT block subsequent
// events: the events it lost are counted in writeErrs, the file is cut
// back to its last complete line, and the next event tries again.
func (s *FileSink) Handle(evt *journal.Event) error {
	data, err := json.Marshal(evt)
	if err != nil {
		s.writeErrs.Add(1)
		return err
	}
	// Append newline in one Write so we don't split an event across
	// two bufio flushes (would let a partial-line reader see a
	// broken JSONL entry mid-Handle).
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bw == nil {
		if err := s.reopenLocked(); err != nil {
			s.writeErrs.Add(1)
			return err
		}
	}
	startOff := s.totalWritten
	if _, err := s.bw.Write(data); err != nil {
		// bufio's error is sticky: without the discard every later
		// event failed too, long after space came back.
		s.discardLocked()
		s.writeErrs.Add(1)
		return err
	}
	s.totalWritten += int64(len(data))
	s.pendingIdx = append(s.pendingIdx, IdxRecord{
		TsUsec: evt.Ts / 1000, // ns → us
		Offset: startOff,
	})
	s.unflushed++
	s.written.Add(1)
	if s.unflushed >= s.fsyncEvery {
		// A failed flush counts the events it lost itself.
		if err := s.flushLocked(); err != nil {
			return err
		}
	}
	if s.shouldRotateLocked() {
		// The event is in the file whatever the rotation did, and a
		// flush that lost events has counted them: not a write error.
		if err := s.rotateLocked(); err != nil {
			return err
		}
	}
	return nil
}

// Rotations returns the cumulative number of rotations performed.
// Diagnostic; useful in tests to assert rotation actually fired.
func (s *FileSink) Rotations() uint64 { return s.rotations.Load() }

// shouldRotateLocked returns true when the current file exceeds
// either configured limit. maxSize/maxAge == 0 means "disabled" on
// that dimension. Caller must hold s.mu.
func (s *FileSink) shouldRotateLocked() bool {
	if s.f == nil || s.now().Before(s.retryAt) {
		return false
	}
	if s.maxSize > 0 && s.totalWritten >= s.maxSize {
		return true
	}
	if s.maxAge > 0 && !s.openedAt.IsZero() && s.now().Sub(s.openedAt) >= s.maxAge {
		return true
	}
	return false
}

// rotateLocked closes the current jsonl+idx, renames both with a
// nanosecond suffix (`<basename>.<open_unix_ns>.jsonl` and the
// matching `.jsonl.idx`), and opens fresh files under the original
// name. The nanosecond suffix guarantees uniqueness even under
// pathological rotate-every-event pressure.
//
// After a successful rotation the rotatedHook (if set) fires with
// the ROTATED path (not the new current). Vacuum uses this to prune
// old files on rotation without a separate timer.
//
// Caller must hold s.mu.
func (s *FileSink) rotateLocked() error {
	if s.f == nil {
		return nil
	}
	if err := s.flushLocked(); err != nil {
		// Without the wait every later event ran the flush and its
		// fsyncs again, only to fail the same way.
		s.retryAt = s.now().Add(reopenInterval)
		return err
	}
	oldPath := s.curPath
	oldIdx := idxPath(oldPath)
	closeErr := s.f.Close()
	if s.idxF != nil {
		_ = s.idxF.Close()
	}
	s.f = nil
	s.bw = nil
	s.idxF = nil

	// Whatever fails below, a file is reopened before returning (or by
	// a later event): a failed rename used to leave the sink without
	// one, and every event after it failed with "sink closed".

	// Rename to <basename-without-.jsonl>.<open_unix_ns>.jsonl so
	// operators can sort chronologically and grep by date.
	openNs := s.openedAt.UnixNano()
	base := oldPath[:len(oldPath)-len(".jsonl")]
	newJsonl := fmt.Sprintf("%s.%d.jsonl", base, openNs)
	newIdx := idxPath(newJsonl)
	var rotErr error
	renamed := false
	if err := os.Rename(oldPath, newJsonl); err != nil {
		rotErr = fmt.Errorf("journald: rotate rename jsonl: %w", err)
	} else {
		renamed = true
		// idx may not exist yet if the sink took zero events since
		// open; ignore ENOENT so a fresh-file rotation still succeeds.
		if err := os.Rename(oldIdx, newIdx); err != nil && !os.IsNotExist(err) {
			// The new current file must not inherit the old index;
			// a missing one is rebuilt from the rotated file.
			_ = os.Remove(oldIdx)
			rotErr = fmt.Errorf("journald: rotate rename idx: %w", err)
		}
		s.rotations.Add(1)
	}
	if rotErr == nil && closeErr != nil {
		rotErr = closeErr
	}

	if err := s.openCurrent(); err != nil {
		s.retryAt = s.now().Add(reopenInterval)
		return err
	}
	if rotErr != nil {
		s.retryAt = s.now().Add(reopenInterval)
	}
	if renamed && s.rotatedHook != nil {
		s.rotatedHook(newJsonl, s.curPath)
	}
	return rotErr
}

// Flush forces a bufio flush + fsync. Used by tests and by any future
// rotation trigger that needs the current file on disk before renaming.
func (s *FileSink) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushLocked()
}

// Rotate forces an immediate rotation regardless of size/age triggers.
// Called from operator-triggered maintenance (SIGUSR2 in the daemon,
// `slinit-journalctl --rotate`). Returns nil on no-op (file already
// zero-length or sink closed) so the operator doesn't see a scary
// error for the "nothing to rotate yet" case.
func (s *FileSink) Rotate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		if s.closed {
			return nil
		}
		// Lost its file to an earlier failure: an operator's rotate
		// is the moment to try again, whatever the rate limit says.
		return s.openCurrent()
	}
	return s.rotateLocked()
}

// Close flushes any pending writes, fsyncs, and closes the file. After
// Close, further Handle calls return an error rather than silently
// re-opening — the daemon's Stop path is the only legitimate way in.
func (s *FileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.bw == nil {
		return nil
	}
	err := s.flushLocked()
	if s.f == nil {
		// The failed flush let go of the file (discardLocked).
		return err
	}
	if err2 := s.f.Close(); err == nil && err2 != nil {
		err = err2
	}
	if s.idxF != nil {
		if err2 := s.idxF.Close(); err == nil && err2 != nil {
			err = err2
		}
	}
	s.f = nil
	s.bw = nil
	s.idxF = nil
	return err
}

// openCurrent opens today's JSONL file for append plus its .idx
// companion, updating curPath / f / bw / idxF / totalWritten /
// unflushed. Caller must hold s.mu (or be in construction before any
// Handle can race).
//
// totalWritten seeds from the current jsonl size so idx entries
// written in this session use correct file offsets when appending to
// a pre-existing file (daemon restart after crash).
func (s *FileSink) openCurrent() error {
	path := filepath.Join(s.dir, currentFileName(time.Now().UTC()))
	// O_RDWR rather than O_WRONLY: the last byte is read below.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0640)
	if err != nil {
		return fmt.Errorf("journald: open %s: %w", path, err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("journald: stat %s: %w", path, err)
	}
	size := st.Size()
	// A crash mid-write leaves the file ending in part of a line.
	// Appending to it glued the first new event onto that fragment,
	// and readers, which skip a line that does not parse, dropped the
	// event with it. End the fragment first so it stays a bad line of
	// its own.
	if size > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, size-1); err != nil {
			_ = f.Close()
			return fmt.Errorf("journald: read %s: %w", path, err)
		}
		if last[0] != '\n' {
			if _, err := f.Write([]byte{'\n'}); err != nil {
				_ = f.Truncate(size)
				_ = f.Close()
				return fmt.Errorf("journald: end torn line in %s: %w", path, err)
			}
			size++
		}
	}
	idxF, err := os.OpenFile(idxPath(path), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0640)
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("journald: open idx %s: %w", idxPath(path), err)
	}
	idxSt, err := idxF.Stat()
	if err != nil {
		_ = f.Close()
		_ = idxF.Close()
		return fmt.Errorf("journald: stat idx %s: %w", idxPath(path), err)
	}
	s.f = f
	// 64 KiB matches a typical page/disk sector cluster; small enough
	// to bound crash-window loss between fsyncs, big enough to
	// amortise write syscalls under a burst.
	s.bw = bufio.NewWriterSize(f, 64*1024)
	s.curPath = path
	s.idxF = idxF
	s.totalWritten = size
	s.durable = size
	s.idxDurable = idxSt.Size()
	s.openedAt = s.now()
	s.unflushed = 0
	s.pendingIdx = s.pendingIdx[:0]
	return nil
}

// flushLocked flushes the bufio buffer to fd, fsyncs, then writes
// pending idx entries and fsyncs the idx too. Caller must hold s.mu.
//
// idx is written AFTER jsonl fsync so the on-disk invariant holds:
// every idx offset points at a valid (already flushed) jsonl line.
// A crash between the two fsyncs at worst loses the trailing idx
// entries, which RebuildIdx can regenerate — never corruption.
//
// On a failed jsonl write the unflushed events are dropped and counted
// (discardLocked); on a failed idx write the idx is cut back and its
// entries kept for the next flush, since the lines they index are
// already on disk.
func (s *FileSink) flushLocked() error {
	if s.bw == nil {
		return nil
	}
	if err := s.bw.Flush(); err != nil {
		s.discardLocked()
		return err
	}
	if err := s.fsync(s.f); err != nil {
		s.discardLocked()
		return err
	}
	s.durable = s.totalWritten
	s.unflushed = 0
	if len(s.pendingIdx) > 0 && s.idxF != nil {
		buf := make([]byte, IdxRecordSize*len(s.pendingIdx))
		out := buf
		for _, rec := range s.pendingIdx {
			out = encodeIdxRecord(out, rec)
		}
		if _, err := s.idxF.Write(buf); err != nil {
			_ = s.idxF.Truncate(s.idxDurable)
			return err
		}
		if err := s.fsync(s.idxF); err != nil {
			_ = s.idxF.Truncate(s.idxDurable)
			return err
		}
		s.idxDurable += int64(len(buf))
		s.pendingIdx = s.pendingIdx[:0]
	}
	return nil
}

// discardLocked recovers from a failed jsonl write or fsync. bufio.Writer
// keeps its first error and fails every later Write and Flush with it,
// so the sink used to stay broken after a full disk had room again (and
// so did Rotate, which flushes first), with a partial line left at the
// end of the file. The file is cut back to the last whole line it holds,
// the events past it are dropped and counted as write errors, and the
// writer starts clean, so the next event is a fresh attempt. Events that
// reached the file whole are kept, even when the fsync after them failed:
// cutting back to the last successful fsync deleted a batch that was
// written in full. Caller holds s.mu.
func (s *FileSink) discardLocked() {
	size := int64(-1)
	keepTo := s.durable
	if st, err := s.f.Stat(); err == nil {
		size = st.Size()
		keepTo = lastLineEnd(size, s.durable, s.totalWritten, s.pendingIdx)
	}
	var lost uint64
	keep := s.pendingIdx[:0]
	for _, rec := range s.pendingIdx {
		if rec.Offset < keepTo {
			// A whole line in the file, or one already durable
			// whose idx write failed: indexed by the next flush.
			keep = append(keep, rec)
		} else {
			lost++
		}
	}
	if lost > 0 {
		s.written.Add(^(lost - 1))
		s.writeErrs.Add(lost)
	}
	s.unflushed = 0
	s.pendingIdx = keep
	s.totalWritten = keepTo
	s.durable = keepTo
	if size == keepTo {
		// Every byte in the file is a whole line (an fsync failed
		// after complete writes): nothing to cut.
		s.bw.Reset(s.f)
		return
	}
	if err := s.f.Truncate(keepTo); err != nil {
		// Cannot cut the partial line off: let go of the file and
		// reopen it later, which starts on a fresh line.
		_ = s.f.Close()
		if s.idxF != nil {
			_ = s.idxF.Close()
		}
		s.f, s.bw, s.idxF = nil, nil, nil
		s.retryAt = s.now().Add(reopenInterval)
		return
	}
	s.bw.Reset(s.f)
}

// lastLineEnd returns the largest line boundary at or below size: the
// durable size, the start of a pending (not yet fsynced) line, or the
// end of the last one, totalWritten. Pending records are in offset
// order; those below durable belong to lines already durable.
func lastLineEnd(size, durable, totalWritten int64, pending []IdxRecord) int64 {
	if totalWritten <= size {
		return totalWritten
	}
	end := durable
	for _, rec := range pending {
		if rec.Offset > size {
			break
		}
		if rec.Offset > end {
			end = rec.Offset
		}
	}
	return end
}

// reopenLocked opens the current file again for a sink that lost it to
// a failed rotation or write, at most once per reopenInterval. Caller
// holds s.mu.
func (s *FileSink) reopenLocked() error {
	if s.closed {
		return errors.New("journald: sink closed")
	}
	now := s.now()
	if now.Before(s.retryAt) {
		return errors.New("journald: sink has no file, retrying")
	}
	s.retryAt = now.Add(reopenInterval)
	return s.openCurrent()
}

// currentFileName returns "YYYY-MM-DD.jsonl" for the given time.
// Broken out so tests can hand a fixed clock and get deterministic
// names.
func currentFileName(t time.Time) string {
	return fmt.Sprintf("%04d-%02d-%02d.jsonl", t.Year(), int(t.Month()), t.Day())
}

package journald

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journalbin"
)

// BinarySink implements Sink by appending events to a rotating
// pkg/journalbin.Writer. FSS sealing is optional (BinarySinkOptions.
// FSSKey). Rotation reuses the same size/age triggers as FileSink so
// operators only have one policy to tune.
//
// The v1 sink does not compress rotated files — see the note in
// pkg/journalbin.compress-related design. Adding gzip on rotate is a
// follow-up mirroring pkg/journald.CompressingRotationHook.
type BinarySink struct {
	dir        string
	fsyncEvery int
	maxSize    int64
	maxAge     time.Duration
	fssKey     *journalbin.FSSKey
	tagEvery   int
	bootID     string
	machineID  string
	rotatedHook func(rotatedPath, currentPath string)

	mu       sync.Mutex
	w        *journalbin.Writer
	curPath  string
	openedAt time.Time

	written   atomic.Uint64
	writeErrs atomic.Uint64
	rotations atomic.Uint64
}

// BinarySinkOptions collects the constructor parameters. Kept as a
// struct so adding new knobs (compression, custom rotation trigger)
// doesn't churn every callsite.
type BinarySinkOptions struct {
	Dir         string
	FsyncEvery  int
	MaxSize     int64
	MaxAge      time.Duration
	// FSSKey enables sealing when non-nil. Loaded from the operator's
	// journal-key file (see cmd/slinit-journald and
	// pkg/journalbin.NewFSSKey).
	FSSKey *journalbin.FSSKey
	// TagEvery — entries between forced TAGs. Zero picks the
	// journalbin default.
	TagEvery int
	// BootID / MachineID: 32-hex strings same as journal.BootID().
	// Empty is tolerated.
	BootID    string
	MachineID string
	// RotatedHook fires after a successful rotation. Same wiring as
	// FileSink's RotatedHook.
	RotatedHook func(rotatedPath, currentPath string)
}

// NewBinarySink opens (or creates) the daily binary journal file
// under dir. Rotation/vacuum defaults mirror FileSink so operators
// see the same behaviour switching --format.
func NewBinarySink(opts BinarySinkOptions) (*BinarySink, error) {
	if opts.Dir == "" {
		opts.Dir = DefaultJournalDir
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
	if err := os.MkdirAll(opts.Dir, 0755); err != nil {
		return nil, fmt.Errorf("journald: mkdir %s: %w", opts.Dir, err)
	}
	s := &BinarySink{
		dir:         opts.Dir,
		fsyncEvery:  opts.FsyncEvery,
		maxSize:     opts.MaxSize,
		maxAge:      opts.MaxAge,
		fssKey:      opts.FSSKey,
		tagEvery:    opts.TagEvery,
		bootID:      opts.BootID,
		machineID:   opts.MachineID,
		rotatedHook: opts.RotatedHook,
	}
	if err := s.openCurrent(); err != nil {
		return nil, err
	}
	return s, nil
}

// CurrentPath returns the on-disk path of the file receiving writes.
func (s *BinarySink) CurrentPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.curPath
}

// Stats returns cumulative (written, writeErrs) counters. Rotations
// tracked separately via Rotations().
func (s *BinarySink) Stats() (written, writeErrs uint64) {
	return s.written.Load(), s.writeErrs.Load()
}

// Rotations returns the cumulative rotation count. Diagnostic.
func (s *BinarySink) Rotations() uint64 { return s.rotations.Load() }

// Handle appends the event to the current binary journal file. On
// success bumps written; on failure bumps writeErrs and returns the
// error so the receiver's dropped counter reflects it.
//
// Rotation triggers post-write: if the file grew past maxSize OR is
// older than maxAge, we swap to a new file (linking the old one to
// a timestamp-suffixed name), then fire the RotatedHook.
func (s *BinarySink) Handle(evt *journal.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w == nil {
		s.writeErrs.Add(1)
		return fmt.Errorf("journald: binary sink closed")
	}
	if _, err := s.w.Append(evt); err != nil {
		s.writeErrs.Add(1)
		return err
	}
	s.written.Add(1)
	// Poll rotation triggers. Cheap: stat is one syscall per event —
	// same overhead FileSink pays. A future optimisation could batch
	// this every N appends.
	if err := s.maybeRotateLocked(); err != nil {
		s.writeErrs.Add(1)
		return err
	}
	return nil
}

// Close flushes the current writer and marks the sink shut. After
// Close, further Handle calls return an error.
func (s *BinarySink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w == nil {
		return nil
	}
	err := s.w.Close()
	s.w = nil
	return err
}

// openCurrent creates/opens today's file. Caller holds s.mu (or is
// running in the constructor before any Handle can race).
func (s *BinarySink) openCurrent() error {
	name := binaryCurrentFileName(time.Now().UTC())
	path := filepath.Join(s.dir, name)
	w, err := journalbin.NewWriterWithOptions(path, journalbin.WriterOptions{
		BootID:    s.bootID,
		MachineID: s.machineID,
		FSSKey:    s.fssKey,
		TagEvery:  s.tagEvery,
	})
	if err != nil {
		return err
	}
	s.w = w
	s.curPath = path
	s.openedAt = time.Now()
	return nil
}

// maybeRotateLocked checks the size + age triggers and rotates if
// either fires. Rename convention mirrors FileSink: current file
// becomes `<base>.<open_unix_ns>.journal` so operators see the same
// pattern across formats.
func (s *BinarySink) maybeRotateLocked() error {
	st, err := os.Stat(s.curPath)
	if err != nil {
		return nil // stat failed — skip trigger this cycle
	}
	sizeTrig := s.maxSize > 0 && st.Size() >= s.maxSize
	ageTrig := s.maxAge > 0 && !s.openedAt.IsZero() && time.Since(s.openedAt) >= s.maxAge
	if !sizeTrig && !ageTrig {
		return nil
	}

	oldPath := s.curPath
	openNs := s.openedAt.UnixNano()
	if err := s.w.Close(); err != nil {
		return err
	}
	s.w = nil
	base := oldPath[:len(oldPath)-len(".journal")]
	newPath := fmt.Sprintf("%s.%d.journal", base, openNs)
	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("journald: binary rotate rename: %w", err)
	}
	s.rotations.Add(1)
	if err := s.openCurrent(); err != nil {
		return err
	}
	if s.rotatedHook != nil {
		s.rotatedHook(newPath, s.curPath)
	}
	return nil
}

// binaryCurrentFileName mirrors currentFileName but with the .journal
// extension. Kept separate so JSONL and binary layouts co-exist in
// the same directory without name collision.
func binaryCurrentFileName(t time.Time) string {
	return fmt.Sprintf("%04d-%02d-%02d.journal", t.Year(), int(t.Month()), t.Day())
}

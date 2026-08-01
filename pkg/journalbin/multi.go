package journalbin

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// MultiReader merges entries across every .journal file in a
// directory into one time-ordered iterator. Used by slinit-journalctl
// when reading the persistent journal directory (default
// /var/log/slinit-journal/). All sub-readers are opened at
// construction and closed together by Close().
//
// gzip-compressed rotated files (.journal.gz) are OUT of scope for
// B2c — random-access reads on a compressed stream require
// decompress-to-memory, which is a follow-up. The current Phase C
// rotation infrastructure only gzips JSONL files; binary files stay
// uncompressed on disk in v1.
type MultiReader struct {
	readers []*Reader
}

// OpenDir opens every `*.journal` (case-sensitive) file directly
// under dir as a Reader. Non-journal files are ignored. Returns an
// error if dir cannot be read or if any candidate has a bad magic —
// silently skipping bad files would hide corruption.
func OpenDir(dir string) (*MultiReader, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("journalbin: read dir %s: %w", dir, err)
	}
	mr := &MultiReader{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".journal") {
			continue
		}
		full := filepath.Join(dir, name)
		r, err := OpenReader(full)
		if err != nil {
			mr.Close()
			return nil, err
		}
		mr.readers = append(mr.readers, r)
	}
	return mr, nil
}

// Readers exposes the underlying sub-readers for callers that need
// per-file access (e.g. `--file` diagnostics that name a specific
// journal). Order is directory-scan order — not sorted.
func (m *MultiReader) Readers() []*Reader { return m.readers }

// Close releases every sub-reader. Idempotent.
func (m *MultiReader) Close() error {
	var firstErr error
	for _, r := range m.readers {
		if err := r.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	m.readers = nil
	return firstErr
}

// mergedEntry pairs an entry offset with its reader for merge-sort.
// realtime is stashed inline so the sort doesn't re-read it per
// comparison.
type mergedEntry struct {
	realtime uint64
	reader   *Reader
	offset   uint64
}

// Iter emits every entry across every open reader in strictly
// non-decreasing realtime order. Ties are broken by file open
// order — a stable enough tiebreaker for operator eyes (two events
// at the same microsecond in different files is rare and the
// display order is arbitrary either way).
//
// Loads every offset up-front (sort-in-memory). For typical
// slinit-journal directories with a handful of files each holding
// thousands of entries this is a few MB of pointers — trivial. A
// heap-based streaming merge would beat this once directories grow
// to hundreds of files; deferred until the workload demands it.
func (m *MultiReader) Iter(fn func(*journal.Event) bool) error {
	var all []mergedEntry
	for _, r := range m.readers {
		offs, err := r.EntryOffsets()
		if err != nil {
			return err
		}
		for _, off := range offs {
			rt, err := r.entryRealtimeAt(off)
			if err != nil {
				return err
			}
			all = append(all, mergedEntry{realtime: rt, reader: r, offset: off})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].realtime < all[j].realtime })

	for _, me := range all {
		evt, err := me.reader.ReadEntryAt(me.offset)
		if err != nil {
			return err
		}
		if !fn(evt) {
			return nil
		}
	}
	return nil
}

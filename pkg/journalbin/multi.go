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

// mergedEntry pairs an entry offset with its reader for merge-sort,
// with the fields the sort compares stashed inline so it doesn't
// re-read them per comparison.
type mergedEntry struct {
	realtime  uint64
	monotonic uint64
	boot      [16]byte
	reader    *Reader
	offset    uint64
}

// Iter emits every entry across every open reader, a boot at a time
// in the order the boots started, and within a boot in monotonic
// order. Ties are broken by file open order.
//
// The order used to be wall-clock time alone. The wall clock can step
// back during a boot — chronyd correcting an RTC that ran ahead, or
// someone setting the time — and the entries after such a step were
// then shown before the boot they belong to, interleaved with an
// earlier boot. A boot's start is the wall-clock time of its earliest
// entry by monotonic time, which comes before any step; within a boot
// the monotonic clock never goes back. systemd's journalctl orders
// entries of one boot by monotonic time for the same reason.
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
			rt, mono, boot, err := r.entryKeyAt(off)
			if err != nil {
				return err
			}
			all = append(all, mergedEntry{realtime: rt, monotonic: mono, boot: boot, reader: r, offset: off})
		}
	}
	// Each boot's start: the wall-clock time of its earliest entry.
	type first struct{ mono, rt uint64 }
	start := map[[16]byte]first{}
	for _, e := range all {
		if f, ok := start[e.boot]; !ok || e.monotonic < f.mono {
			start[e.boot] = first{e.monotonic, e.realtime}
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.boot != b.boot {
			sa, sb := start[a.boot].rt, start[b.boot].rt
			if sa != sb {
				return sa < sb
			}
			return string(a.boot[:]) < string(b.boot[:])
		}
		if a.monotonic != b.monotonic {
			return a.monotonic < b.monotonic
		}
		return a.realtime < b.realtime
	})

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

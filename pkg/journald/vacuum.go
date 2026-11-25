package journald

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Default vacuum caps per project_journal_pipeline.md Phase 3e. Zero
// means "disabled" on that dimension — callers can pass any subset.
const (
	// DefaultMaxFiles bounds the count of rotated .jsonl files
	// (idx companions don't count separately). 100 files at
	// DefaultMaxSize each caps disk to ~12.5 GiB before
	// DefaultMaxTotalSize kicks in first at typical event sizes.
	DefaultMaxFiles = 100
	// DefaultMaxTotalSize bounds the sum of rotated .jsonl sizes.
	// 4 GiB matches the plan; comfortably fits an active journal
	// on any bare-metal /var partition without hogging it.
	DefaultMaxTotalSize int64 = 4 << 30
	// DefaultVacuumMaxAge deletes files older than 30 days.
	// (VacuumMaxAge name-scoped to avoid confusion with the
	// per-file rotation MaxAge in FileSinkOptions.)
	DefaultVacuumMaxAge = 30 * 24 * time.Hour
)

// VacuumOptions configures what Vacuum prunes. Zero on any field
// disables that dimension so operators can tune one lever without
// resetting the others.
type VacuumOptions struct {
	MaxFiles     int
	MaxTotalSize int64
	MaxAge       time.Duration
	// Suffixes limits the file extensions Vacuum considers. Empty
	// selects the JSONL layout, [".jsonl", ".jsonl.gz"]: rotated JSONL
	// files are gzipped by default, and with ".jsonl" alone none of
	// them was ever pruned. Binary journal callers pass [".journal"]; a
	// bilingual tool that manages both wires all three.
	Suffixes []string
	// KeepFree, when positive, prunes further (oldest first) until the
	// filesystem holding dir has at least this many bytes available.
	KeepFree int64
}

// Bounds systemd puts on the caps it derives from a filesystem's size
// (journal_default_metrics in sd-journal/journal-file.c).
const (
	runtimeMaxUseLower   int64 = 1 << 20 // MAX_USE_LOWER
	runtimeMaxUseUpper   int64 = 4 << 30 // MAX_USE_UPPER
	runtimeKeepFreeUpper int64 = 4 << 30 // KEEP_FREE_UPPER
	runtimeKeepFreeNoFS  int64 = 1 << 20 // DEFAULT_KEEP_FREE
)

// statfsSize reports a filesystem's total and available bytes. A
// variable so tests can stand in a small filesystem.
var statfsSize = func(dir string) (size, avail int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0, err
	}
	return int64(st.Blocks) * st.Frsize, int64(st.Bavail) * st.Frsize, nil
}

// RuntimeVacuumOptions scales opts to the filesystem holding dir, for
// the volatile journal on tmpfs: the total size is capped at 10% of the
// filesystem (between 1 MiB and 4 GiB) and 5% of it is kept free (at
// most 4 GiB). These are systemd's RuntimeMaxUse= and RuntimeKeepFree=
// defaults as journal_default_metrics computes them; a static cap sized
// for /var would let the journal fill /run.
func RuntimeVacuumOptions(dir string, opts VacuumOptions) VacuumOptions {
	maxUse, keepFree := runtimeMaxUseLower, runtimeKeepFreeNoFS
	if size, _, err := statfsSize(dir); err == nil && size > 0 {
		maxUse = min(max(size/10, runtimeMaxUseLower), runtimeMaxUseUpper)
		keepFree = min(size/20, runtimeKeepFreeUpper)
	}
	if opts.MaxTotalSize <= 0 || opts.MaxTotalSize > maxUse {
		opts.MaxTotalSize = maxUse
	}
	if opts.KeepFree < keepFree {
		opts.KeepFree = keepFree
	}
	return opts
}

// Vacuum enumerates .jsonl files under dir, sorts them oldest-first
// by mtime, then deletes candidates until every configured cap is
// satisfied. Each deletion drops both the .jsonl and its .jsonl.idx
// companion (if present). Files whose full path matches any entry in
// excludePaths are protected — the caller passes the sink's current
// active file to prevent an accidental delete of the file being
// written to right now.
//
// Returns the count of jsonl files removed and the first error (if
// any) encountered — errors on individual removes are logged into
// firstErr but do NOT abort the sweep, so a single locked file
// doesn't block pruning of other candidates.
func Vacuum(dir string, opts VacuumOptions, excludePaths ...string) (removed int, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("journald: vacuum: read dir %s: %w", dir, err)
	}
	// Build the candidate list with size+mtime for sort.
	type cand struct {
		path  string
		size  int64
		mtime time.Time
	}
	excl := make(map[string]struct{}, len(excludePaths))
	for _, p := range excludePaths {
		if p != "" {
			excl[p] = struct{}{}
		}
	}
	suffixes := opts.Suffixes
	if len(suffixes) == 0 {
		suffixes = []string{".jsonl", ".jsonl.gz"}
	}
	var candidates []cand
	for _, e := range entries {
		name := e.Name()
		// Match any configured suffix. .idx / .gz companions are
		// removed via removeJournalFile side-effects when their
		// parent is deleted.
		matched := false
		for _, sfx := range suffixes {
			if strings.HasSuffix(name, sfx) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		full := filepath.Join(dir, name)
		if _, ok := excl[full]; ok {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		candidates = append(candidates, cand{
			path:  full,
			size:  info.Size(),
			mtime: info.ModTime(),
		})
	}
	// Oldest first — deletion iterates from the head until caps met.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].mtime.Before(candidates[j].mtime)
	})

	// Running tallies to check against the caps.
	totalSize := int64(0)
	for _, c := range candidates {
		totalSize += c.size
	}
	fileCount := len(candidates)
	ageCutoff := time.Now().Add(-opts.MaxAge)
	byPath := make(map[string]int, len(candidates))
	for i, c := range candidates {
		byPath[c.path] = i
	}

	var firstErr error
	deleteOne := func(idx int) {
		c := candidates[idx]
		if e := removeJournalFile(c.path); e != nil {
			if firstErr == nil {
				firstErr = e
			}
			return
		}
		removed++
		fileCount--
		totalSize -= c.size
		// removeJournalFile also took x.jsonl.gz with x.jsonl (a
		// compression whose original was not removed); if that was a
		// candidate too, it is gone now.
		if j, ok := byPath[c.path+compressedSuffix]; ok && candidates[j].path != "" {
			fileCount--
			totalSize -= candidates[j].size
			candidates[j].path = ""
		}
	}

	// Age sweep first — a very old file is unconditionally removed
	// (unless MaxAge is disabled), even if we're under file/size
	// caps. That keeps deep history from lingering when traffic
	// dropped mid-retention window.
	if opts.MaxAge > 0 {
		for i := range candidates {
			if candidates[i].path == "" {
				continue // already removed
			}
			if candidates[i].mtime.Before(ageCutoff) {
				deleteOne(i)
				candidates[i].path = ""
			}
		}
	}
	// File-count sweep.
	if opts.MaxFiles > 0 {
		for i := range candidates {
			if candidates[i].path == "" {
				continue
			}
			if fileCount <= opts.MaxFiles {
				break
			}
			deleteOne(i)
			candidates[i].path = ""
		}
	}
	// Size sweep.
	if opts.MaxTotalSize > 0 {
		for i := range candidates {
			if candidates[i].path == "" {
				continue
			}
			if totalSize <= opts.MaxTotalSize {
				break
			}
			deleteOne(i)
			candidates[i].path = ""
		}
	}
	// Free-space sweep: each removal frees about its size.
	if opts.KeepFree > 0 {
		if _, avail, serr := statfsSize(dir); serr == nil {
			before := totalSize
			for i := range candidates {
				if candidates[i].path == "" {
					continue
				}
				if avail+(before-totalSize) >= opts.KeepFree {
					break
				}
				deleteOne(i)
				candidates[i].path = ""
			}
		}
	}
	return removed, firstErr
}

// removeJournalFile deletes a journal file and any companion — .idx
// for both JSONL and binary layouts, .gz for compressed JSONL. A
// missing companion is not an error (may not exist for a same-second
// rotation with zero events or a JSONL file that wasn't compressed).
// A missing primary IS an error — the caller was told it existed by
// ReadDir.
func removeJournalFile(primaryPath string) error {
	if err := os.Remove(primaryPath); err != nil {
		return err
	}
	// jsonl+idx wrote alongside .jsonl in Phase 3c; binary+idx wrote
	// alongside .journal in Phase B2b. idxPath appends ".idx" in both
	// cases so the same helper suits both layouts. A compressed file
	// keeps the index of the file it was made from: x.jsonl.idx for
	// x.jsonl.gz.
	_ = os.Remove(idxPath(strings.TrimSuffix(primaryPath, compressedSuffix)))
	// Compressed JSONL companion.
	if !isCompressed(primaryPath) {
		_ = os.Remove(primaryPath + compressedSuffix)
	}
	return nil
}

// VacuumingHook returns a RotatedHook that runs Vacuum on the dir
// after every rotation, excluding the sink's currently-active file.
// The current path is passed directly as an argument by the FileSink
// (no callback that could deadlock).
//
// Wire it into FileSinkOptions.RotatedHook — Vacuum then runs
// synchronously with rotation, so there's no separate timer or
// goroutine to manage.
func VacuumingHook(dir string, opts VacuumOptions) func(rotatedPath, currentPath string) {
	return func(rotatedPath, currentPath string) {
		_, _ = Vacuum(dir, opts, currentPath)
	}
}

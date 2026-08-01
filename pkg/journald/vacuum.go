package journald

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	// selects [".jsonl"] for backward compatibility with the Phase 3
	// callers. Binary journal callers pass [".journal"]; a bilingual
	// tool that manages both wires [".jsonl", ".journal"].
	Suffixes []string
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
		suffixes = []string{".jsonl"}
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
	// cases so the same helper suits both layouts.
	_ = os.Remove(primaryPath + ".idx")
	// Compressed JSONL companion.
	_ = os.Remove(primaryPath + ".gz")
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

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
	var candidates []cand
	for _, e := range entries {
		name := e.Name()
		// Only rotated .jsonl files (not .idx, not compressed .lz4
		// yet — 3f wires that later). Also skip the excluded
		// current file.
		if !strings.HasSuffix(name, ".jsonl") {
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

// removeJournalFile deletes a jsonl file and its .idx companion.
// A missing .idx is not an error (may not exist for a same-second
// rotation that took zero events). A missing .jsonl IS an error —
// the caller was told it existed by ReadDir.
func removeJournalFile(jsonlPath string) error {
	if err := os.Remove(jsonlPath); err != nil {
		return err
	}
	_ = os.Remove(idxPath(jsonlPath))
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

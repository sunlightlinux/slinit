package journald

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// JournalFileSuffixes is the set of extensions Migrate treats as
// journal artefacts: JSONL (Phase C), gzipped rotated JSONL, and the
// binary Phase B format. Sidecar .idx files are picked up implicitly
// via the base rename.
var JournalFileSuffixes = []string{".jsonl", ".jsonl.gz", ".journal"}

// ProbeWritable checks whether dir can be used as a persistent
// journal directory: MkdirAll succeeds (parent chain exists / can be
// created), and a probe write completes. Removes the probe so a
// subsequent live sink open finds no stale file.
//
// Returned error kinds:
//   - permission / read-only fs → non-nil, sentinel-style message the
//     caller can log as "primary still unwritable"
//   - unknown → wrapped %w so os.Is* predicates keep working
func ProbeWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	probe, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return fmt.Errorf("write probe %s: %w", dir, err)
	}
	name := probe.Name()
	probe.Close()
	if err := os.Remove(name); err != nil {
		// Cleanup failure is not a hard error — the probe file is
		// harmless. Surface via log but don't fail the caller.
		return nil
	}
	return nil
}

// Migrate moves every journal artefact under src into dst. Attempts
// os.Rename first (single-fs fast path); falls back to copy+remove
// when cross-device (common when src is tmpfs and dst is on-disk).
// Returns the number of successfully moved files plus the first
// error, if any — partial success is normal here since we want to
// migrate as many files as possible even if one fails.
//
// The current-day file (`YYYY-MM-DD.jsonl` / `.journal`) is
// deliberately NOT excluded — the caller is expected to have closed
// the active sink before calling Migrate so nothing races the move.
func Migrate(src, dst string) (moved int, firstErr error) {
	entries, err := os.ReadDir(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Nothing to migrate — src never existed. Not an error.
			return 0, nil
		}
		return 0, fmt.Errorf("read %s: %w", src, err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0, fmt.Errorf("mkdir %s: %w", dst, err)
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !isJournalArtefact(name) {
			continue
		}
		srcPath := filepath.Join(src, name)
		dstPath := filepath.Join(dst, name)
		if err := moveFile(srcPath, dstPath); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("move %s: %w", name, err)
			}
			continue
		}
		moved++
	}
	return moved, firstErr
}

// isJournalArtefact reports whether name matches any suffix in
// JournalFileSuffixes OR is a sidecar .idx for a JSONL file (paired
// with the base name so they migrate together).
func isJournalArtefact(name string) bool {
	if strings.HasSuffix(name, ".jsonl.idx") {
		return true
	}
	for _, s := range JournalFileSuffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// moveFile tries a rename (same fs) then falls back to copy+remove
// (cross-device). Rename preserves inode + timestamps atomically;
// copy+remove leaves a small window where the source still exists
// but caller has already logged the move — acceptable for a
// migration op that runs while the daemon isn't writing.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// Rename failed — try copy+remove. If copy also fails, don't
	// remove: leave the source intact so the operator can retry.
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fi.Mode())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return os.Remove(src)
}

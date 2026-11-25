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
// a hard link first (single-fs fast path); falls back to copy+remove
// when cross-device (common when src is tmpfs and dst is on-disk).
// Returns the number of successfully moved files plus the first
// error, if any — partial success is normal here since we want to
// migrate as many files as possible even if one fails.
//
// The current-day file (`YYYY-MM-DD.jsonl` / `.journal`) is
// deliberately NOT excluded — the caller is expected to have closed
// the active sink before calling Migrate so nothing races the move.
//
// Nothing in dst is overwritten: a file of the same name already there
// is rotated aside first (see migrateOne).
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
	present := make(map[string]bool, len(entries))
	for _, ent := range entries {
		present[ent.Name()] = true
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !isJournalArtefact(name) {
			continue
		}
		// A sidecar index travels with its journal file below, so it
		// can never be paired with another file's contents. One whose
		// journal file is gone is moved on its own, if nothing is
		// in its way.
		if strings.HasSuffix(name, idxSuffix) {
			base := strings.TrimSuffix(name, idxSuffix)
			if present[base] {
				continue
			}
			if _, err := os.Lstat(filepath.Join(dst, name)); err == nil {
				continue
			}
		}
		srcPath := filepath.Join(src, name)
		dstPath := filepath.Join(dst, name)
		if err := migrateOne(srcPath, dstPath); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("move %s: %w", name, err)
			}
			continue
		}
		moved++
		if !strings.HasSuffix(name, idxSuffix) && present[name+idxSuffix] {
			if err := moveFile(idxPath(srcPath), idxPath(dstPath)); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("move %s: %w", name+idxSuffix, err)
				}
				continue
			}
			moved++
		}
	}
	return moved, firstErr
}

// migrateOne moves one journal file into dst without overwriting
// anything there.
//
// The same name can exist on both sides: a boot that persisted
// 2026-11-25.jsonl, then a reboot the same UTC day whose early events
// went to the volatile directory under that name. Migrate used to rename
// the volatile file over the persistent one, and the earlier boot's
// journal for the day was gone. The file already in dst is now
// rotated aside first, under the name a rotation gives it
// (<date>.<ns><ext>, with its .idx), so readers, which take rotated
// files before the current one, still see it before the file moved in.
func migrateOne(srcPath, dstPath string) error {
	if _, err := os.Lstat(dstPath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return moveFile(srcPath, dstPath)
	}
	aside, err := asideName(dstPath)
	if err != nil {
		return err
	}
	if err := os.Rename(dstPath, aside); err != nil {
		return fmt.Errorf("rotate existing %s aside: %w", filepath.Base(dstPath), err)
	}
	if err := os.Rename(idxPath(dstPath), idxPath(aside)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("rotate existing %s aside: %w", filepath.Base(idxPath(dstPath)), err)
	}
	return moveFile(srcPath, dstPath)
}

// asideName returns a free rotated name for an existing journal file:
// <base>.<ns><ext>, ns being the file's modification time (when it was
// last written, so it sorts before anything a later boot rotated), and
// counted up past any name already taken.
func asideName(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	ext := ""
	for _, s := range JournalFileSuffixes {
		if strings.HasSuffix(path, s) && len(s) > len(ext) {
			ext = s
		}
	}
	base := strings.TrimSuffix(path, ext)
	ns := fi.ModTime().UnixNano()
	for i := 0; i < 1000; i++ {
		cand := fmt.Sprintf("%s.%d%s", base, ns+int64(i), ext)
		_, err := os.Lstat(cand)
		if errors.Is(err, os.ErrNotExist) {
			if _, err := os.Lstat(idxPath(cand)); errors.Is(err, os.ErrNotExist) {
				return cand, nil
			}
		}
	}
	return "", fmt.Errorf("no free rotated name for %s", filepath.Base(path))
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

// moveFile tries a hard link (same fs) then falls back to copy+remove
// (cross-device). A link keeps the inode + timestamps;
// copy+remove leaves a small window where the source still exists
// but caller has already logged the move — acceptable for a
// migration op that runs while the daemon isn't writing.
//
// Neither path replaces an existing dst: the fast path is link+unlink
// rather than rename (which would silently replace it), and the copy
// creates dst with O_EXCL.
func moveFile(src, dst string) error {
	err := os.Link(src, dst)
	if err == nil {
		return os.Remove(src)
	}
	if errors.Is(err, os.ErrExist) {
		return err
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
	// O_EXCL: the caller made sure dst is free; never truncate a file
	// that appeared there since.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode())
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

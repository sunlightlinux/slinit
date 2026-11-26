// Package atomicfile writes a whole file into place for the
// generators (the slinit-*-convert tools and slinit-init-maker), which
// run as root into directories such as /etc/slinit.d.
//
// os.WriteFile there opened the destination with O_TRUNC and followed
// whatever was at that name: a symlink out/foo -> ../victim truncated
// and rewrote the victim, and an existing service file was replaced
// without a word. A fixed "dst.tmp" scratch name had the same problem
// one step earlier. Write never opens the destination name: the data
// goes to a fresh O_EXCL temp file in the destination directory, which
// is then renamed over the name, and rename replaces a directory entry
// without following it.
package atomicfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ErrExists reports a destination that is already there and was not
// replaced; errors.Is(err, fs.ErrExist) holds for it too. The errors
// Write returns do not repeat the path: every caller prefixes it.
var ErrExists error = existsError{}

type existsError struct{}

func (existsError) Error() string        { return "already exists; use --force to replace it" }
func (existsError) Is(target error) bool { return target == fs.ErrExist }

// ErrSymlink reports a destination that is a symbolic link. It is
// refused even when overwriting: replacing the link with a file is not
// what someone who pointed it elsewhere asked for, and writing through
// it is how a planted link reaches a file outside the directory.
var ErrSymlink = errors.New("is a symbolic link; refusing to write through or replace it")

// renameNoReplace is renameat2(RENAME_NOREPLACE); a var so tests can
// take the fallback a filesystem without it gets.
var renameNoReplace = func(from, to string) error {
	return unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
}

// Write puts data at dst with the given permission bits. The data is
// written to a temp file created in dst's directory, synced, chmodded
// and renamed into place, so dst is either the old file or the whole
// new one, never a partial write. A symlink or a directory at dst is
// refused. An existing dst is refused unless overwrite is set; without
// overwrite the final step is renameat2(RENAME_NOREPLACE), so a file
// that appears at dst meanwhile is not replaced either.
func Write(dst string, data []byte, mode fs.FileMode, overwrite bool) (err error) {
	if err := checkDest(dst, overwrite); err != nil {
		return err
	}
	dir, base := filepath.Split(dst)
	if dir == "" {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			os.Remove(tmpName)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Chmod(mode.Perm()); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = install(tmpName, dst, overwrite); err != nil {
		return err
	}
	if d, derr := os.Open(dir); derr == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// checkDest refuses a symlink or a directory at dst, and any existing
// entry when overwrite is false.
func checkDest(dst string, overwrite bool) error {
	fi, err := os.Lstat(dst)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return ErrSymlink
	case fi.IsDir():
		return errors.New("is a directory")
	case !overwrite:
		return ErrExists
	}
	return nil
}

// install renames tmp to dst. Without overwrite it must not replace an
// entry that appeared after checkDest; renameat2(RENAME_NOREPLACE)
// does that atomically, and where the filesystem lacks it, link(2)
// does: it fails on any existing name, a dangling symlink included.
func install(tmp, dst string, overwrite bool) error {
	if overwrite {
		// A symlink planted after checkDest is replaced, never
		// followed: rename acts on the directory entry.
		return os.Rename(tmp, dst)
	}
	err := renameNoReplace(tmp, dst)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.EEXIST) {
		return ErrExists
	}
	if !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOSYS) {
		return err
	}
	if err := os.Link(tmp, dst); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrExists
		}
		return err
	}
	os.Remove(tmp) // dst holds the data; a stray temp name is cosmetic
	return nil
}

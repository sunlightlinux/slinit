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
//
// The file Write leaves behind looks like the one os.WriteFile would
// have: a new file gets the requested mode less the process umask (the
// kernel applies it when the temp file is created; the mode is never
// widened afterwards), and a file that is replaced keeps its permission
// bits and, where the caller may set them (as root), its owner and
// group. Rename installs a new inode, so whatever only the old inode
// carried does not follow: a hard link to the old file keeps the old
// content, and extended attributes and ACLs are not copied. That is
// inherent to replacing a file atomically.
package atomicfile

import (
	"errors"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

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
// written to a temp file created in dst's directory, synced and renamed
// into place, so dst is either the old file or the whole new one, never
// a partial write. A symlink or a directory at dst is refused. An
// existing dst is refused unless overwrite is set; without overwrite
// the final step is renameat2(RENAME_NOREPLACE), so a file that appears
// at dst meanwhile is not replaced either.
//
// A new dst gets mode less the umask, as os.WriteFile gives it. An
// existing dst that is replaced keeps its owner, group and permission
// bits, as os.WriteFile (which truncates in place) keeps them; the
// only bits mode adds are execute bits for a class that may already
// read the file, so a generated script asked for at 0755 still runs.
func Write(dst string, data []byte, mode fs.FileMode, overwrite bool) (err error) {
	old, err := checkDest(dst, overwrite)
	if err != nil {
		return err
	}
	dir, base := filepath.Split(dst)
	if dir == "" {
		dir = "."
	}
	createMode := mode.Perm()
	if old != nil {
		// Never more open than either: the final bits are set
		// before any data is written.
		createMode &= old.Mode().Perm()
	}
	tmp, err := createTemp(dir, "."+base+".tmp-", createMode)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			os.Remove(tmpName)
		}
	}()
	if old != nil {
		if err = keepAttrs(tmp, old, mode); err != nil {
			tmp.Close()
			return err
		}
	}
	if _, err = tmp.Write(data); err != nil {
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

// createTemp is os.CreateTemp with a creation mode: the file is made
// O_CREATE|O_EXCL with perm, so the kernel applies the umask. Reading
// the umask instead (umask(2) set and restore) races with every other
// thread that creates a file meanwhile.
func createTemp(dir, prefix string, perm fs.FileMode) (*os.File, error) {
	for try := 0; ; try++ {
		name := filepath.Join(dir, prefix+strconv.FormatUint(uint64(rand.Uint32()), 10))
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
		if errors.Is(err, fs.ErrExist) && try < 10000 {
			continue
		}
		return f, err
	}
}

// keepAttrs gives the temp file the owner, group and permission bits of
// the file it replaces. The owner is set first (chown clears set-id
// bits). Changing the owner needs privilege; without it the file ends
// up owned by the caller, as any new file would.
func keepAttrs(tmp *os.File, old fs.FileInfo, mode fs.FileMode) error {
	if st, ok := old.Sys().(*syscall.Stat_t); ok {
		uid, gid := int(st.Uid), int(st.Gid)
		if uid != os.Geteuid() || gid != os.Getegid() {
			if err := tmp.Chown(uid, gid); err != nil && !errors.Is(err, fs.ErrPermission) {
				return err
			}
		}
	}
	perm := old.Mode().Perm()
	// Execute bits mode asks for, only where that class can read.
	perm |= mode.Perm() & 0o111 & (perm >> 2)
	return tmp.Chmod(perm)
}

// checkDest refuses a symlink or a directory at dst, and any existing
// entry when overwrite is false. It returns the entry that will be
// replaced, or nil when there is none.
func checkDest(dst string, overwrite bool) (fs.FileInfo, error) {
	fi, err := os.Lstat(dst)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return nil, ErrSymlink
	case fi.IsDir():
		return nil, errors.New("is a directory")
	case !overwrite:
		return nil, ErrExists
	case !fi.Mode().IsRegular():
		// A fifo or device keeps nothing worth copying.
		return nil, nil
	}
	return fi, nil
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

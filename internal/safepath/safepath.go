// Package safepath resolves directories for a privileged caller (tmpfiles,
// checkpath) without letting a less-privileged user steer it with a
// symlink. Resolution walks one component at a time, each opened with
// O_PATH|O_NOFOLLOW relative to the directory before it, and the caller
// gets back a descriptor to work relative to (mkdirat, unlinkat, openat
// with O_NOFOLLOW), so nothing is looked up by path again afterwards.
//
// The symlink rule is systemd's CHASE_SAFE (src/basic/chase.c) plus the
// owner check of fs.protected_symlinks:
//
//   - Every step from a directory to an entry in it (directory or
//     symlink), and from a symlink to "/" when its target is absolute, is
//     an unsafe transition if the thing stepped from is owned by a
//     non-root user and the thing stepped to has a different owner. An
//     unsafe transition is refused. So a user can redirect a lookup only
//     within what that user owns, never back into root's files.
//   - A symlink is followed only if it is owned by root or by the owner
//     of the directory that holds it. A user's link in root's /tmp is
//     refused.
//
// Root-owned links in root-owned directories, such as /var/run -> /run or
// the /sys/class symlinks, are followed as before.
package safepath

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// maxFollow bounds symlink expansions, as the kernel's 40 does.
const maxFollow = 32

// HardlinksProtected reports whether fs.protected_hardlinks is on. With
// it on, a user cannot hard-link a file they could not already write, so
// a hard link in their directory is harmless; with it off, a link to
// /etc/shadow would have root change /etc/shadow. Unreadable counts as
// off, as in systemd. Tests replace it.
var HardlinksProtected = func() bool {
	b, err := os.ReadFile("/proc/sys/fs/protected_hardlinks")
	return err == nil && strings.TrimSpace(string(b)) == "1"
}

// OpenDir opens dir as an O_PATH directory descriptor under the rule above.
func OpenDir(dir string) (int, error) {
	return chase(dir, false, 0)
}

// OpenParent opens path's parent directory as OpenDir does and returns it
// with the final component, which the caller opens or creates relative to
// the descriptor without following it.
func OpenParent(path string) (int, string, error) {
	return openParent(path, false, 0)
}

// MkdirParent is OpenParent that creates missing parents with perm.
func MkdirParent(path string, perm uint32) (int, string, error) {
	return openParent(path, true, perm)
}

func openParent(path string, create bool, perm uint32) (int, string, error) {
	name := filepath.Base(path)
	if name == "/" || name == "." || name == ".." {
		return -1, "", &os.PathError{Op: "open parent", Path: path, Err: unix.EINVAL}
	}
	fd, err := chase(filepath.Dir(path), create, perm)
	return fd, name, err
}

func chase(path string, create bool, perm uint32) (int, error) {
	fail := func(err error) (int, error) {
		return -1, &os.PathError{Op: "resolve", Path: path, Err: err}
	}
	start := "."
	if filepath.IsAbs(path) {
		start = "/"
	}
	fd, err := unix.Open(start, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fail(err)
	}
	var cur unix.Stat_t
	if err := unix.Fstat(fd, &cur); err != nil {
		unix.Close(fd)
		return fail(err)
	}
	todo := strings.Split(path, "/")
	follows := 0
	for len(todo) > 0 {
		comp := todo[0]
		todo = todo[1:]
		if comp == "" || comp == "." {
			continue
		}
		child, err := unix.Openat(fd, comp, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err == unix.ENOENT && create && comp != ".." {
			if err = unix.Mkdirat(fd, comp, perm); err == nil || err == unix.EEXIST {
				child, err = unix.Openat(fd, comp, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			}
		}
		if err != nil {
			unix.Close(fd)
			return fail(err)
		}
		var st unix.Stat_t
		if err := unix.Fstat(child, &st); err != nil {
			unix.Close(child)
			unix.Close(fd)
			return fail(err)
		}
		if unsafeTransition(cur.Uid, st.Uid) {
			unix.Close(child)
			unix.Close(fd)
			return fail(fmt.Errorf("unsafe transition at %q (owner %d to %d)", comp, cur.Uid, st.Uid))
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			unix.Close(fd)
			fd, cur = child, st
		case unix.S_IFLNK:
			unix.Close(child)
			if st.Uid != 0 && st.Uid != cur.Uid {
				unix.Close(fd)
				return fail(fmt.Errorf("refusing symlink %q owned by %d in a directory owned by %d", comp, st.Uid, cur.Uid))
			}
			if follows++; follows > maxFollow {
				unix.Close(fd)
				return fail(unix.ELOOP)
			}
			target, err := readlinkat(fd, comp)
			if err != nil {
				unix.Close(fd)
				return fail(err)
			}
			if filepath.IsAbs(target) {
				unix.Close(fd)
				if fd, err = unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0); err != nil {
					return fail(err)
				}
				var root unix.Stat_t
				if err := unix.Fstat(fd, &root); err != nil {
					unix.Close(fd)
					return fail(err)
				}
				if unsafeTransition(st.Uid, root.Uid) {
					unix.Close(fd)
					return fail(fmt.Errorf("unsafe transition: symlink %q owned by %d points to %q", comp, st.Uid, target))
				}
				cur = root
			}
			// A relative target resolves from the directory holding
			// the link, which stays cur, as in systemd.
			todo = append(strings.Split(target, "/"), todo...)
		default:
			unix.Close(child)
			unix.Close(fd)
			return fail(unix.ENOTDIR)
		}
	}
	return fd, nil
}

// unsafeTransition is systemd's uid_unsafe_transition: stepping from
// something root owns is always fine, otherwise the owner must not change.
func unsafeTransition(from, to uint32) bool {
	return from != 0 && from != to
}

func readlinkat(dirfd int, name string) (string, error) {
	buf := make([]byte, unix.PathMax)
	n, err := unix.Readlinkat(dirfd, name, buf)
	if err != nil {
		return "", err
	}
	if n == 0 || n >= len(buf) {
		return "", unix.EINVAL
	}
	return string(buf[:n]), nil
}

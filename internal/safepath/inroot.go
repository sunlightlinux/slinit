package safepath

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// openat2 is unix.Openat2; a var so tests can take the fallback walk.
var openat2 = unix.Openat2

// OpenInRoot opens path as if root were "/": "..", and absolute symlinks
// and their targets, resolve inside root and never reach the host's
// files. A container's or an image's tree is someone else's, and its
// /run/slinit.socket -> /run/slinit.socket otherwise named the host's.
// It is openat2's RESOLVE_IN_ROOT, magic links refused; where the
// kernel lacks openat2 (before 5.6), a component walk that resolves the
// same way. flags are open(2) flags, O_CREAT not among them; O_CLOEXEC
// is added. The caller owns the returned descriptor.
func OpenInRoot(root, path string, flags int) (int, error) {
	rfd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, &os.PathError{Op: "open", Path: root, Err: err}
	}
	defer unix.Close(rfd)
	fd, err := openat2(rfd, path, &unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC),
		Resolve: unix.RESOLVE_IN_ROOT | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err == unix.ENOSYS {
		fd, err = walkInRoot(rfd, path, flags)
	}
	if err != nil {
		return -1, &os.PathError{Op: "open in root", Path: filepath.Join(root, path), Err: err}
	}
	return fd, nil
}

// walkInRoot resolves path under rfd one component at a time, as
// RESOLVE_IN_ROOT does: ".." at the root stays there, and an absolute
// symlink target starts again from the root. The result is reopened
// with flags through its /proc/self/fd link.
func walkInRoot(rfd int, path string, flags int) (int, error) {
	root, err := unix.Dup(rfd)
	if err != nil {
		return -1, err
	}
	stack := []int{root} // stack[i] is the directory at depth i
	defer func() {
		for _, fd := range stack {
			unix.Close(fd)
		}
	}()
	todo := strings.Split(path, "/")
	follows := 0
	for len(todo) > 0 {
		comp := todo[0]
		todo = todo[1:]
		switch comp {
		case "", ".":
			continue
		case "..":
			if len(stack) > 1 {
				unix.Close(stack[len(stack)-1])
				stack = stack[:len(stack)-1]
			}
			continue
		}
		child, err := unix.Openat(stack[len(stack)-1], comp, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return -1, err
		}
		var st unix.Stat_t
		if err := unix.Fstat(child, &st); err != nil {
			unix.Close(child)
			return -1, err
		}
		if st.Mode&unix.S_IFMT == unix.S_IFLNK {
			unix.Close(child)
			if follows++; follows > maxFollow {
				return -1, unix.ELOOP
			}
			target, err := readlinkat(stack[len(stack)-1], comp)
			if err != nil {
				return -1, err
			}
			if filepath.IsAbs(target) {
				for _, fd := range stack[1:] {
					unix.Close(fd)
				}
				stack = stack[:1]
			}
			todo = append(strings.Split(target, "/"), todo...)
			continue
		}
		stack = append(stack, child)
	}
	last := stack[len(stack)-1]
	if flags&unix.O_PATH != 0 && flags&unix.O_DIRECTORY == 0 {
		return unix.Dup(last)
	}
	return unix.Open("/proc/self/fd/"+strconv.Itoa(last), flags|unix.O_CLOEXEC, 0)
}

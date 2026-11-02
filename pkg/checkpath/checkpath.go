// Package checkpath creates or verifies a filesystem path with a specified
// type (file, directory, or named pipe), mode, and ownership. It is the
// slinit equivalent of OpenRC's checkpath(8) utility, intended for use from
// service pre-start commands:
//
//	pre-start: /usr/bin/slinit-checkpath -d -m 0755 -o app:app /var/run/myapp
//
// Behavior is intentionally conservative:
//   - Parent directories are not created recursively. Missing parents
//     produce a clear error, mirroring OpenRC.
//   - Symlinks are not followed on the final path component (O_NOFOLLOW),
//     and the component is resolved once: every check and change goes
//     through the descriptor, so the entry cannot be swapped in between.
//     Callers that need symlink traversal can resolve the path themselves.
//   - Mode and owner are corrected only when the path already exists; they
//     are set as part of creation otherwise.
package checkpath

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// PathType identifies what kind of inode the caller wants at the target.
type PathType int

const (
	// TypeUnknown means "check writable only, do not create"; used with
	// the -W flag. It is an error to pair it with create/truncate flags.
	TypeUnknown PathType = iota
	// TypeFile ensures a regular file exists.
	TypeFile
	// TypeDir ensures a directory exists.
	TypeDir
	// TypeFifo ensures a named pipe (FIFO) exists.
	TypeFifo
)

// Owner resolves to a uid/gid pair. A negative value means "leave unchanged".
type Owner struct {
	UID int
	GID int
}

// Spec describes one checkpath operation.
type Spec struct {
	Path     string
	Type     PathType
	Mode     os.FileMode // 0 means "leave mode unchanged"
	Owner    Owner       // UID/GID -1 each means no chown
	Truncate bool        // -D / -F: empty the dir / truncate the file
	Writable bool        // -W: just check that the path is writable
}

// Result describes what changed, if anything. Zero value means "no-op".
type Result struct {
	Created   bool
	ChMod     bool
	ChOwn     bool
	Truncated bool
}

// Apply executes the given spec. Returns a descriptive error on any failure;
// the caller may chain multiple calls and aggregate the first error.
func Apply(spec Spec) (Result, error) {
	var res Result
	if spec.Path == "" {
		return res, errors.New("checkpath: empty path")
	}

	if spec.Writable {
		// OpenRC semantics: if the path already exists and is writable,
		// short-circuit success. Otherwise, if a type was specified, fall
		// through to the normal create/verify path; if no type was
		// specified we cannot create, so a missing/unwritable path is a
		// hard error.
		if err := unix.Access(spec.Path, unix.W_OK); err == nil {
			return res, nil
		} else if !errors.Is(err, unix.ENOENT) || spec.Type == TypeUnknown {
			return res, fmt.Errorf("checkpath: %s: not writable: %w", spec.Path, err)
		}
	}

	// Resolve the final component ONCE, to an O_PATH descriptor that does
	// not follow a symlink, and do every check and change through it.
	// This used to Lstat the path and then Truncate, Chmod and Chown it
	// by name: between the check and each change, whoever owns the parent
	// (the documented use hands paths like /var/run/myapp to a service
	// user) could swap the entry for a symlink, and root would change the
	// target. -D emptied a directory by name the same way, so the swap
	// could make root delete another directory's contents.
	fd, openErr := openNoFollow(spec.Path)
	switch {
	case openErr == nil:
	case errors.Is(openErr, unix.ENOENT):
		if err := createPath(spec.Path, spec.Type, spec.Mode); err != nil {
			return res, err
		}
		res.Created = true
		// The new inode is opened the same way: the window between
		// creating it and correcting its owner is the same window.
		if fd, openErr = openNoFollow(spec.Path); openErr != nil {
			return res, fmt.Errorf("checkpath: %s: open after create: %w", spec.Path, openErr)
		}
	default:
		return res, fmt.Errorf("checkpath: %s: stat: %w", spec.Path, openErr)
	}
	defer unix.Close(fd)
	if afterOpenHook != nil {
		afterOpenHook()
	}

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return res, fmt.Errorf("checkpath: %s: stat: %w", spec.Path, err)
	}
	// A symlink opened with O_PATH|O_NOFOLLOW is the link itself, which
	// verifyType refuses, as it refused an Lstat'd one.
	if err := verifyType(spec.Path, fileMode(st.Mode), spec.Type); err != nil {
		return res, err
	}
	// /proc/self/fd/N names the opened inode, whatever the path names now.
	inode := "/proc/self/fd/" + strconv.Itoa(fd)

	// Truncation is only meaningful for files and directories.
	if spec.Truncate {
		switch spec.Type {
		case TypeFile:
			f, err := os.OpenFile(inode, os.O_WRONLY|os.O_TRUNC, 0)
			if err != nil {
				return res, fmt.Errorf("checkpath: %s: truncate: %w", spec.Path, err)
			}
			f.Close()
			res.Truncated = true
		case TypeDir:
			if err := emptyDirectory(spec.Path, inode); err != nil {
				return res, err
			}
			res.Truncated = true
		}
	}

	// Mode correction: honour exactly the caller's requested bits
	// (permission bits only — never sticky/setuid unless asked).
	if spec.Mode != 0 {
		current := fileMode(st.Mode).Perm()
		if current != (spec.Mode & os.ModePerm) {
			if err := os.Chmod(inode, spec.Mode&os.ModePerm); err != nil {
				return res, fmt.Errorf("checkpath: %s: chmod: %w", spec.Path, err)
			}
			res.ChMod = true
		}
	}

	// Owner correction: only touch whichever of uid/gid the caller asked
	// for (-1 means leave alone), and only when it differs.
	if spec.Owner.UID >= 0 || spec.Owner.GID >= 0 {
		curUID, curGID := int(st.Uid), int(st.Gid)
		wantUID, wantGID := spec.Owner.UID, spec.Owner.GID
		if wantUID < 0 {
			wantUID = curUID
		}
		if wantGID < 0 {
			wantGID = curGID
		}
		if wantUID != curUID || wantGID != curGID {
			if err := os.Chown(inode, wantUID, wantGID); err != nil {
				return res, fmt.Errorf("checkpath: %s: chown: %w", spec.Path, err)
			}
			res.ChOwn = true
		}
	}

	return res, nil
}

// afterOpenHook, when set, runs right after the target is opened: tests
// use it to swap the path in exactly the window this code must survive.
var afterOpenHook func()

// openNoFollow opens path's final component without following it.
func openNoFollow(path string) (int, error) {
	return unix.Open(path, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

// fileMode converts a stat(2) st_mode to the os.FileMode bits verifyType
// and the mode check read.
func fileMode(m uint32) os.FileMode {
	mode := os.FileMode(m & 0o777)
	switch m & unix.S_IFMT {
	case unix.S_IFDIR:
		mode |= os.ModeDir
	case unix.S_IFLNK:
		mode |= os.ModeSymlink
	case unix.S_IFIFO:
		mode |= os.ModeNamedPipe
	case unix.S_IFREG:
	default:
		mode |= os.ModeIrregular
	}
	return mode
}

func verifyType(path string, mode os.FileMode, want PathType) error {
	// Refuse symlinks at the final component first — we will not silently
	// chown/chmod a link target that a caller did not explicitly resolve.
	// This check must precede the type checks because a symlink also
	// fails IsRegular() and IsDir() and would otherwise produce a
	// confusing "not a X" error.
	if mode&os.ModeSymlink != 0 {
		return fmt.Errorf("checkpath: %s: refusing to operate on a symbolic link", path)
	}
	switch want {
	case TypeFile:
		if !mode.IsRegular() {
			return fmt.Errorf("checkpath: %s: exists but is not a regular file", path)
		}
	case TypeDir:
		if !mode.IsDir() {
			return fmt.Errorf("checkpath: %s: exists but is not a directory", path)
		}
	case TypeFifo:
		if mode&os.ModeNamedPipe == 0 {
			return fmt.Errorf("checkpath: %s: exists but is not a named pipe", path)
		}
	case TypeUnknown:
		// No type constraint.
	}
	return nil
}

func createPath(path string, typ PathType, mode os.FileMode) error {
	perm := mode & os.ModePerm
	switch typ {
	case TypeFile:
		if perm == 0 {
			perm = 0o644
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, perm)
		if err != nil {
			return fmt.Errorf("checkpath: %s: create file: %w", path, err)
		}
		f.Close()
	case TypeDir:
		if perm == 0 {
			perm = 0o755
		}
		if err := os.Mkdir(path, perm); err != nil {
			return fmt.Errorf("checkpath: %s: mkdir: %w", path, err)
		}
	case TypeFifo:
		if perm == 0 {
			perm = 0o644
		}
		if err := unix.Mkfifo(path, uint32(perm)); err != nil {
			return fmt.Errorf("checkpath: %s: mkfifo: %w", path, err)
		}
	case TypeUnknown:
		return fmt.Errorf("checkpath: %s: cannot create: no type specified", path)
	}
	return nil
}

// emptyDirectory removes everything inside the directory opened as inode
// (a /proc/self/fd path), through an os.Root on it: nothing it removes
// can be outside that directory, whatever happens to path meanwhile.
func emptyDirectory(path, inode string) error {
	root, err := os.OpenRoot(inode)
	if err != nil {
		return fmt.Errorf("checkpath: %s: open: %w", path, err)
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("checkpath: %s: open: %w", path, err)
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return fmt.Errorf("checkpath: %s: read: %w", path, err)
	}
	for _, e := range entries {
		if err := root.RemoveAll(e.Name()); err != nil {
			return fmt.Errorf("checkpath: %s: remove %s: %w", path, e.Name(), err)
		}
	}
	return nil
}

// ParseMode parses an octal mode string such as "0755", "755", or "0o644".
func ParseMode(s string) (os.FileMode, error) {
	s = strings.TrimPrefix(s, "0o")
	if s == "" {
		return 0, fmt.Errorf("checkpath: empty mode")
	}
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("checkpath: invalid mode %q: %w", s, err)
	}
	return os.FileMode(n), nil
}

// ParseOwner parses a user[:group] spec where each component may be a name
// (resolved via /etc/passwd or /etc/group) or a numeric id. An omitted user
// or group leaves that side as -1 (no change). Empty input returns
// {-1, -1}, i.e. no chown.
func ParseOwner(s string) (Owner, error) {
	own := Owner{UID: -1, GID: -1}
	if s == "" {
		return own, nil
	}
	user, group, _ := strings.Cut(s, ":")
	if user != "" {
		uid, err := lookupUID(user)
		if err != nil {
			return own, fmt.Errorf("checkpath: user %q: %w", user, err)
		}
		own.UID = uid
	}
	if group != "" {
		gid, err := lookupGID(group)
		if err != nil {
			return own, fmt.Errorf("checkpath: group %q: %w", group, err)
		}
		own.GID = gid
	}
	return own, nil
}

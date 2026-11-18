package logging

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// OpenAppend opens path for appending, creating it with perm if nothing is
// there — but never through a dangling symlink.
//
// A plain O_CREAT follows a symlink whose target does not exist yet and
// creates the target, wherever it points. Whoever can plant that symlink
// in the log's directory then chooses where slinit, often as root, creates
// a file. systemd refuses this case with ELOOP (fs-util, 381d85eda5) for
// StandardOutput=file: and the other files it creates, and so does this.
//
// A symlink whose target exists is followed, as before: an administrator
// may point a log at another file deliberately. Only creation is refused.
func OpenAppend(path string, perm os.FileMode) (*os.File, error) {
	const flags = os.O_WRONLY | os.O_APPEND
	// Two attempts are enough for the one race that loops: the file
	// appearing between the open that found nothing and the create.
	for attempt := 0; attempt < 3; attempt++ {
		f, err := os.OpenFile(path, flags, 0)
		if err == nil || !errors.Is(err, fs.ErrNotExist) {
			return f, err
		}
		// ENOENT: either nothing is there, or a symlink to nothing.
		// O_EXCL never follows a final symlink, so this creates a file
		// only where there is none.
		f, err = os.OpenFile(path, flags|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, perm)
		if err == nil || !errors.Is(err, fs.ErrExist) {
			return f, err
		}
		if fi, lerr := os.Lstat(path); lerr == nil && fi.Mode()&fs.ModeSymlink != 0 {
			if _, serr := os.Stat(path); errors.Is(serr, fs.ErrNotExist) {
				return nil, &fs.PathError{Op: "open", Path: path, Err: syscall.ELOOP}
			}
		}
		// Created by someone else in between: open it on the next round.
	}
	return nil, &fs.PathError{Op: "open", Path: path, Err: syscall.EAGAIN}
}

package process

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// PIDResult represents the outcome of reading a PID file.
// ErrPIDFileEmpty reports a pid file that exists but has no content yet —
// the normal intermediate state while a daemon is writing it. Callers tell
// it apart from unparseable content, which is a real error rather than
// something that will resolve itself.
var ErrPIDFileEmpty = errors.New("PID file is empty")

type PIDResult int

const (
	// PIDResultOK means the PID was read successfully and the process exists.
	PIDResultOK PIDResult = iota
	// PIDResultFailed means the PID file could not be read or parsed.
	PIDResultFailed
	// PIDResultTerminated means the PID was valid but the process no longer exists.
	PIDResultTerminated
)

// ReadPIDFile reads a process ID from the given file path.
// It validates that the PID is a positive integer and checks if the process
// is still alive using kill(pid, 0).
//
// O_NOFOLLOW: services often write their pid-file into world-writable
// locations (/tmp, /var/run when shared across users). Without it a local
// user could pre-create the path as a symlink to /proc/1/status etc. and
// trick slinit into reading the wrong PID — which then drives signal
// targets and "process alive?" decisions.
//
// O_NONBLOCK plus the regular-file check: this runs under the service
// set's lock, and opening a FIFO left at the path would block PID 1
// until someone opened the other end.
func ReadPIDFile(path string) (int, PIDResult, error) {
	pid, result, _, err := ReadPIDFileOwner(path)
	return pid, result, err
}

// ReadPIDFileOwner is ReadPIDFile, also returning the uid that owns the
// file read (from an Fstat of the open file, so it is the file read).
// Whoever owns it chose the pid.
func ReadPIDFileOwner(path string) (int, PIDResult, uint32, error) {
	var owner uint32
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return 0, PIDResultFailed, owner, fmt.Errorf("reading PID file: %w", err)
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return 0, PIDResultFailed, owner, fmt.Errorf("reading PID file: %w", err)
	} else if !fi.Mode().IsRegular() {
		return 0, PIDResultFailed, owner, fmt.Errorf("reading PID file %s: not a regular file (%s)", path, fi.Mode().Type())
	} else if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		owner = st.Uid
	}
	// Cap the read so a hostile/junk file can't drive an unbounded
	// allocation. A real PID file is < 64 bytes; 4 KiB is generous.
	data, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return 0, PIDResultFailed, owner, fmt.Errorf("reading PID file: %w", err)
	}

	content := strings.TrimSpace(string(data))
	if content == "" {
		return 0, PIDResultFailed, owner, ErrPIDFileEmpty
	}

	// PID file may contain PID on first line followed by other data
	if idx := strings.IndexByte(content, '\n'); idx >= 0 {
		content = content[:idx]
	}

	pid, err := strconv.Atoi(strings.TrimSpace(content))
	if err != nil {
		return 0, PIDResultFailed, owner, fmt.Errorf("invalid PID in file: %w", err)
	}

	if pid <= 0 {
		return 0, PIDResultFailed, owner, fmt.Errorf("invalid PID value: %d", pid)
	}
	// Init and slinit itself are never a service's daemon, and the pid
	// read here is what stop signals: a file naming 1 (or us) would turn
	// a service stop into a SIGTERM to PID 1, which is a reboot request.
	// systemd refuses the same two.
	if pid == 1 || pid == os.Getpid() {
		return 0, PIDResultFailed, owner, fmt.Errorf("refusing PID %d from PID file: "+
			"it is init or the service manager itself", pid)
	}

	// Check if process exists
	err = syscall.Kill(pid, 0)
	if err == nil {
		return pid, PIDResultOK, owner, nil
	}

	if errors.Is(err, syscall.ESRCH) {
		return pid, PIDResultTerminated, owner, nil
	}

	// EPERM means the process exists but we don't have permission to signal it
	if errors.Is(err, syscall.EPERM) {
		return pid, PIDResultOK, owner, nil
	}

	return pid, PIDResultFailed, owner, fmt.Errorf("checking process %d: %w", pid, err)
}

// PIDFileInsecure says why a pidfile cannot be trusted to name the
// process to signal, or "" if it can. Whoever can write it picks the
// pid: refused when world-writable, and — with nothing else matched —
// when someone other than root or us owns it or, through a group
// write bit, can write it. The checks are Debian start-stop-daemon's
// (dpkg 1.19.3); st must come from an Fstat of the open file, so the
// file checked is the file read.
func PIDFileInsecure(st *syscall.Stat_t, uid, gid int, pidfileOnly bool) string {
	if st.Mode&0o002 != 0 {
		return "matching on world-writable"
	}
	if pidfileOnly &&
		((st.Uid != uint32(uid) && st.Uid != 0) ||
			(st.Gid != uint32(gid) && st.Gid != 0 && st.Mode&0o020 != 0)) {
		return "matching only on non-root"
	}
	return ""
}

package process

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// openConfiguredFile opens the path in e per the systemd OpenFile=
// semantics. Options is a comma-separated list; recognised tokens:
//
//	read-only   — O_RDONLY (default is O_RDWR|O_CREATE).
//	append      — O_APPEND (with default write flags).
//	truncate    — O_TRUNC on open (with default write flags).
//	graceful    — treat "open failed" as non-fatal: the parent
//	              substitutes /dev/null so ExtraFiles keeps the fd
//	              slot stable. Systemd's exact semantics.
//
// Unknown tokens are refused so a typo surfaces at start time rather
// than silently downgrading protection.
func openConfiguredFile(e OpenFileEntry) (*os.File, error) {
	flags := os.O_RDWR | os.O_CREATE
	graceful := false
	for _, tok := range splitOpenFileOptions(e.Options) {
		switch tok {
		case "":
			continue
		case "read-only":
			flags = os.O_RDONLY
		case "append":
			flags = (flags &^ (os.O_RDWR | os.O_RDONLY)) | os.O_WRONLY | os.O_APPEND | os.O_CREATE
		case "truncate":
			flags = (flags &^ os.O_APPEND) | os.O_TRUNC
		case "graceful":
			graceful = true
		default:
			return nil, fmt.Errorf("unknown option %q (accepted: read-only, append, truncate, graceful)", tok)
		}
	}
	// O_NONBLOCK so a FIFO at the path cannot block PID 1 in open()
	// waiting for its other end; FIFOs and sockets are legitimate
	// targets, so they are not refused. The service gets the fd back
	// in blocking mode, as a plain open would have given it.
	fd, err := unix.Open(e.Path, flags|unix.O_NONBLOCK|unix.O_CLOEXEC, 0o644)
	if err == nil {
		if err = unix.SetNonblock(fd, false); err != nil {
			unix.Close(fd)
		}
	}
	if err != nil {
		if graceful {
			return os.Open("/dev/null")
		}
		return nil, &os.PathError{Op: "open", Path: e.Path, Err: err}
	}
	return os.NewFile(uintptr(fd), e.Path), nil
}

func splitOpenFileOptions(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

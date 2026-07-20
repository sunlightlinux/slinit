package process

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// setupTTY opens p.TTYPath (O_RDWR|O_NOCTTY) and applies every knob
// the operator requested: VT_DISALLOCATE for /dev/ttyN, vhangup(),
// terminal reset (ESC c), TIOCSWINSZ. Returns the opened fd so the
// caller can wire it as stdin/stdout/stderr; nil is returned when
// TTYPath is unset (no work to do).
//
// Ordering is load-bearing:
//   1. VT_DISALLOCATE first — the ioctl works on /dev/tty0 (parent
//      of every VT); disallocation FREES the VT number, and the
//      subsequent open reallocates a fresh one with clean state.
//   2. Open the TTY (O_RDWR|O_NOCTTY so we don't accidentally
//      steal it as controlling terminal — the caller does that
//      later via Setctty).
//   3. vhangup() drops any prior session on the fd. Must happen
//      AFTER open (needs a valid fd for the calling task).
//   4. Reset (ESC c) — after vhangup so the reset lands on the
//      fresh state, not on a hanging-up terminal.
//   5. WinSize — after reset (reset would otherwise clobber it).
func setupTTY(p ExecParams) (*os.File, error) {
	if p.TTYPath == "" {
		return nil, nil
	}
	if p.TTYVTDisallocate {
		vtDisallocate(p.TTYPath)
	}
	fd, err := os.OpenFile(p.TTYPath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	if p.TTYVHangup {
		// vhangup(2) applies to the calling task's controlling
		// terminal; on a task that has none, it operates on the
		// tty referenced by the currently-open fd. Best-effort:
		// unprivileged callers may hit EPERM, and the operator
		// asked for it in configuration — surface the error only
		// if it prevents the rest of the setup.
		_, _, _ = unix.Syscall(unix.SYS_VHANGUP, 0, 0, 0)
	}
	if p.TTYReset {
		// ESC c = RIS (Reset to Initial State) — full terminal
		// reset per ECMA-48. Ignored errors are fine: on a device
		// that doesn't understand the sequence, the bytes are
		// discarded harmlessly.
		_, _ = fd.Write([]byte("\033c"))
	}
	if p.TTYColumns > 0 && p.TTYRows > 0 {
		var ws struct {
			Row, Col, XPixel, YPixel uint16
		}
		ws.Row = p.TTYRows
		ws.Col = p.TTYColumns
		_, _, _ = unix.Syscall(unix.SYS_IOCTL, fd.Fd(),
			uintptr(unix.TIOCSWINSZ), uintptr(unsafe.Pointer(&ws)))
	}
	return fd, nil
}

// vtDisallocate: for a virtual-terminal path like /dev/tty3, extract
// N=3 and ioctl(/dev/tty0, VT_DISALLOCATE, N). No-op on non-VT paths
// (serial, pty, generic tty). ioctl errors are ignored — the operator
// asked to try, worst case the terminal keeps its prior state.
func vtDisallocate(path string) {
	const vtDisallocateIoctl = 0x5608
	numStr := strings.TrimPrefix(path, "/dev/tty")
	if numStr == path { // no /dev/tty prefix
		return
	}
	n, err := strconv.Atoi(numStr)
	if err != nil || n < 1 || n > 63 {
		return
	}
	// /dev/tty0 is the "current" VT and accepts the ioctl on behalf
	// of any allocated N. Fall back silently on open failure.
	f, err := os.OpenFile("/dev/tty0", syscall.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return
	}
	defer f.Close()
	_, _, _ = unix.Syscall(unix.SYS_IOCTL, f.Fd(),
		uintptr(vtDisallocateIoctl), uintptr(n))
}

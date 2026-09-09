package process

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// consoleActivePath is the kernel's list of active console tty names,
// space-separated, in kernel-priority order. The LAST entry is the
// console /dev/console redirects to and where oops messages land —
// that's the one we pick when resolving the "@console" tty-path
// sentinel. Overridable at package level so tests can point at a
// fixture instead of the real sysfs entry.
var consoleActivePath = "/sys/class/tty/console/active"

// resolveConsolePath expands the finit-parity "@console" sentinel to
// the /dev/tty* path the kernel currently uses as its primary
// console. Returns an error when sysfs is unmounted or the file is
// empty (kernel booted with `console=null` or every console blacklist-
// ed) — no silent fallback because a getty pointed at the wrong tty
// is worse than a service that fails loudly and can be fixed.
func resolveConsolePath() (string, error) {
	data, err := os.ReadFile(consoleActivePath)
	if err != nil {
		return "", fmt.Errorf("@console: read %s: %w", consoleActivePath, err)
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "", fmt.Errorf("@console: %s is empty — kernel reports no active console", consoleActivePath)
	}
	// Kernel doc: "the console at the end of the list is the console
	// where the kernel oops messages go" — matches /dev/console's
	// redirect target and what an operator running a login prompt on
	// "the console" would expect.
	return "/dev/" + fields[len(fields)-1], nil
}

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
	ttyPath := p.TTYPath
	if ttyPath == "@console" {
		resolved, err := resolveConsolePath()
		if err != nil {
			return nil, err
		}
		ttyPath = resolved
	}
	if p.TTYVTDisallocate {
		vtDisallocate(ttyPath)
	}
	fd, err := os.OpenFile(ttyPath, os.O_RDWR|syscall.O_NOCTTY, 0)
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

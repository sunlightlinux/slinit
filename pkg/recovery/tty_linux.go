package recovery

import (
	"os"

	"golang.org/x/sys/unix"
)

// setRawMode puts fd into a cbreak-style tty mode: canonical
// (line-buffered) input off, echo off, signal characters (Ctrl-C,
// Ctrl-Z) still delivered. Every keypress is then delivered as a
// single byte from ReadByte without needing Enter — which is what
// the rescue menu wants (`r`, `s`, Ctrl-B, Ctrl-D all fire
// immediately, no confirm-with-Enter dance).
//
// Returns the ORIGINAL termios so the caller can restore before
// forking a shell (shells want canonical mode) and re-set raw
// afterwards. When the fd isn't a tty (test pipes, redirected
// stdin) both calls return nil and no-op.
func setRawMode(f *os.File) *unix.Termios {
	orig, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	if err != nil {
		return nil // not a tty — leave untouched
	}
	raw := *orig
	// Disable canonical input processing (deliver chars as they
	// arrive, not line-by-line) and echo (don't clutter the
	// menu with typed keys — the menu re-renders on state changes
	// so echo would just noise up the box).
	//
	// Keep ISIG so Ctrl-C still terminates slinit — an operator
	// stuck in the menu on a truly wedged system needs an escape
	// hatch, and slinit's parent (kernel) will restart PID 1
	// however it likes.
	raw.Lflag &^= unix.ICANON | unix.ECHO
	// VMIN=1, VTIME=0: read blocks until at least one byte
	// arrives, no interbyte timeout. Suits our
	// "wait-for-keypress-then-act" model.
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(int(f.Fd()), unix.TCSETS, &raw); err != nil {
		return nil
	}
	// Drop any bytes already queued in the tty input buffer BEFORE
	// the menu starts reading. Rescue-menu context: at boot the
	// console has probably accumulated stray input (kernel-boot
	// noise on serial lines, an operator's `\n`s while watching the
	// boot, QEMU test-agent chatter, etc). Without this flush, our
	// first ReadByte returns one of those stale bytes immediately,
	// charToAction maps it to some action, Present returns fast,
	// and main's retry loop cycles the menu without the operator
	// ever touching a key.
	flushInput(f)
	return orig
}

// restoreTermios reverses setRawMode. Safe to call with a nil
// orig (no-op) — that matches the "not a tty" bail-out path in
// setRawMode.
func restoreTermios(f *os.File, orig *unix.Termios) {
	if orig == nil {
		return
	}
	_ = unix.IoctlSetTermios(int(f.Fd()), unix.TCSETS, orig)
}

// SeedWinsize is the exported form of seedWinsize for callers outside
// pkg/recovery (cmd/slinit's rescue + debug-shell exec paths hit the
// same serial-console-no-winsize trap as the recovery menu does).
func SeedWinsize(f *os.File) { seedWinsize(f) }

// FlushInput is the exported form of flushInput for the same reason.
func FlushInput(f *os.File) { flushInput(f) }

// seedWinsize sets a plausible 24x80 window size on the tty when
// the kernel has none recorded (both dims 0). Rescue-menu context:
// serial-console setups (`-serial mon:stdio` under QEMU, agetty on
// ttyS0, etc.) don't get a WINCH from the host emulator, so
// TIOCGWINSZ returns zeros. Shells with line-editing (busybox ash's
// libbb/lineedit is the concrete offender) then fall back to
// sending `ESC[6n` cursor-position queries on every prompt render,
// and the host terminal's `ESC[row;colR` replies show up as stray
// input like `[38;5R` / `[45R` at the shell — treated as commands.
// Setting a sane winsize once suppresses the query loop.
//
// Called with the same fd the shell will read from. Zero-valued
// winsize is treated as "unset" (matches what shells check); if the
// admin already set dimensions via `stty rows N cols M` or agetty
// -w, we leave the caller-chosen size alone.
func seedWinsize(f *os.File) {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return
	}
	if ws.Row != 0 || ws.Col != 0 {
		return
	}
	seed := &unix.Winsize{Row: 24, Col: 80}
	_ = unix.IoctlSetWinsize(int(f.Fd()), unix.TIOCSWINSZ, seed)
}

// flushInput drops whatever bytes have already been queued on the
// tty's input buffer. Wanted before handing the tty off to another
// consumer (a fork'd shell, a re-entered menu) so it starts on a
// clean slate.
//
// Concrete rescue-menu case: the menu emits ANSI writes to draw its
// box (\r\x1b[2K\n, etc.); many terminal emulators reply to
// terminal-side queries — cursor-position reports (\x1b[6n → \x1b
// [row;colR), device-attributes reports (\x1b[c → \x1b[?…c),
// keyboard-encoding reports — spontaneously or in response to
// sequences that look adjacent to a query. Those replies land in
// the tty's input buffer. Without a flush, the very first bytes the
// exec'd /bin/sh reads are those replies, which the shell tries to
// interpret as commands (`[5R: not found`, `[40: not found`, etc.).
//
// tcflush(TCIFLUSH) drains only the input side, matching what
// setRawMode already does at menu entry. No-op on non-tty fds.
func flushInput(f *os.File) {
	_ = unix.IoctlSetInt(int(f.Fd()), unix.TCFLSH, unix.TCIFLUSH)
}

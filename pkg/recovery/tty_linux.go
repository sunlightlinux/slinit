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
	// ever touching a key. tcflush(TCIFLUSH) is the standard
	// clean-slate primitive for this.
	_ = unix.IoctlSetInt(int(f.Fd()), unix.TCFLSH, unix.TCIFLUSH)
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

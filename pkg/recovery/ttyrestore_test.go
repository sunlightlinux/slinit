//go:build linux

package recovery

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// openPty returns the slave side of a new pty, as a terminal to set modes on.
func openPty(t *testing.T) (*os.File, string) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pty here: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/dev/pts/%d", n)
	s, err := os.OpenFile(path, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("cannot open %s: %v", path, err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func lflag(t *testing.T, f *os.File) uint32 {
	t.Helper()
	cur, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	return cur.Lflag
}

// With nobody else on the terminal, the raw mode is undone: left raw, a shell
// opened on the console would show nothing typed.
func TestRestoreIfStillRawRestores(t *testing.T) {
	s, _ := openPty(t)
	before := lflag(t, s)
	orig := setRawMode(s)
	if lflag(t, s)&unix.ECHO != 0 {
		t.Fatal("setup: echo still on in raw mode")
	}
	restoreIfStillRaw(s, orig)
	if got := lflag(t, s); got != before {
		t.Errorf("lflag = %#x after restore, want %#x", got, before)
	}
}

// Stopping the debugger leaves alone a mode something else set meanwhile.
// agetty clears every local flag; restoring over that turned echo and
// canonical input back on under it, and its login name came out twice.
func TestDebuggerStopLeavesAgettysModeAlone(t *testing.T) {
	s, path := openPty(t)
	d := NewDebugger(DebuggerOptions{ConsolePath: path})
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	cur, err := unix.IoctlGetTermios(int(s.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	cur.Lflag = 0 // what agetty's termio_init leaves
	if err := unix.IoctlSetTermios(int(s.Fd()), unix.TCSETS, cur); err != nil {
		t.Fatal(err)
	}
	d.Stop()
	if got := lflag(t, s); got != 0 {
		t.Errorf("lflag = %#x after Stop, want agetty's 0", got)
	}
}

// And the debugger left alone restores, end to end through Stop.
func TestDebuggerStopRestoresWhenAlone(t *testing.T) {
	s, path := openPty(t)
	before := lflag(t, s)
	d := NewDebugger(DebuggerOptions{ConsolePath: path})
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	d.Stop()
	if got := lflag(t, s); got != before {
		t.Errorf("lflag = %#x after Stop, want %#x", got, before)
	}
}

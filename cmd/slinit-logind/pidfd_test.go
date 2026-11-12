package main

import (
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

func TestPidFromPidfd(t *testing.T) {
	self, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		t.Skipf("no pidfd_open: %v", err)
	}
	defer unix.Close(self)
	if pid, err := pidFromPidfd(self); err != nil || pid != uint32(os.Getpid()) {
		t.Fatalf("own pidfd: pid %d, err %v; want %d", pid, err, os.Getpid())
	}

	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	if _, err := pidFromPidfd(int(r.Fd())); err == nil {
		t.Error("a pipe was taken for a pidfd")
	}

	// A process that has exited reads as Pid: -1 and cannot lead a session.
	cmd := exec.Command("/bin/true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	gone, err := unix.PidfdOpen(cmd.Process.Pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(gone)
	_ = cmd.Wait()
	if _, err := pidFromPidfd(gone); err == nil {
		t.Error("the pidfd of a reaped process was accepted")
	}
}

// fdOpen reports whether fd is still open in this process.
func fdOpen(fd int) bool {
	_, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	return err == nil
}

func TestCreateSessionWithPIDFDRefuses(t *testing.T) {
	m := usersBus(t)
	// A live child, so that only the check under test can refuse it.
	child := exec.Command("/bin/sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	call := func(sender dbus.Sender, flags uint64, pid int) (*dbus.Error, bool) {
		fd, err := unix.PidfdOpen(pid, 0)
		if err != nil {
			t.Skipf("no pidfd_open: %v", err)
		}
		_, _, _, _, _, _, _, _, derr := m.CreateSessionWithPIDFD(sender, 1000, dbus.UnixFD(fd),
			"login", "tty", "user", "", "seat0", 1, "tty1", "", false, "", "", flags, nil)
		return derr, fdOpen(fd)
	}
	if err, open := call(alice, 0, child.Process.Pid); !denied(err) || open {
		t.Errorf("non-root: err %v, fd still open %v; want AccessDenied and the fd closed", err, open)
	}
	if err, open := call(root, 1, child.Process.Pid); err == nil || err.Name != "org.freedesktop.DBus.Error.InvalidArgs" || open {
		t.Errorf("flags=1: err %v, fd still open %v; want InvalidArgs and the fd closed", err, open)
	}
	// The pidfd names this process, which is logind itself here.
	if err, _ := call(root, 0, os.Getpid()); err == nil || err.Name != "org.freedesktop.DBus.Error.InvalidArgs" {
		t.Errorf("leader = logind itself: err %v, want InvalidArgs", err)
	}
}

// The placeholder is one eventfd for every session: a per-call fd could
// never be closed, as godbus does not dup a reply fd.
func TestSessionFDIsOneEventfd(t *testing.T) {
	a, b := sessionFD(), sessionFD()
	if a < 0 || a != b {
		t.Fatalf("sessionFD = %d then %d, want one valid fd", a, b)
	}
	link, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(int(a)))
	if err != nil || link != "anon_inode:[eventfd]" {
		t.Fatalf("sessionFD is %q (%v), want an eventfd", link, err)
	}
}

func TestSeatFromTTY(t *testing.T) {
	cases := []struct {
		seat, tty string
		vtnr      uint32
		wantSeat  string
		wantTTY   string
		wantVT    uint32
		wantErr   bool
	}{
		{"", "tty1", 0, "seat0", "tty1", 1, false},
		{"", "/dev/tty7", 0, "seat0", "tty7", 7, false},
		{"seat0", "tty2", 2, "seat0", "tty2", 2, false},
		{"", "console", 0, "seat0", "console", 0, false},
		{"", "pts/0", 0, "", "pts/0", 0, false},
		{"", "ttyS0", 0, "", "ttyS0", 0, false},
		{"", "", 0, "", "", 0, false},
		{"seat1", "tty1", 0, "", "", 0, true},
		{"", "tty3", 4, "", "", 0, true},
	}
	for _, c := range cases {
		seat, tty, vt, err := seatFromTTY(c.seat, c.tty, c.vtnr)
		if (err != nil) != c.wantErr {
			t.Errorf("%q/%q/%d: err %v", c.seat, c.tty, c.vtnr, err)
			continue
		}
		if !c.wantErr && (seat != c.wantSeat || tty != c.wantTTY || vt != c.wantVT) {
			t.Errorf("%q/%q/%d = %q/%q/%d, want %q/%q/%d", c.seat, c.tty, c.vtnr, seat, tty, vt, c.wantSeat, c.wantTTY, c.wantVT)
		}
	}
}

// Linux-specific syscall wrappers for session lifecycle. Kept in a
// build-tagged file so a future BSD port can supply equivalents
// without editing session.go.
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// nonblock is unix.O_NONBLOCK — imported inline so session.go's fifo
// open doesn't have to reach into unix directly.
const nonblock = unix.O_NONBLOCK

// makeFifo creates a FIFO node at path. Wraps unix.Mkfifo — the
// stdlib doesn't expose one.
func makeFifo(path string, mode uint32) error {
	return unix.Mkfifo(path, mode)
}

// killPID sends the given signal to the process. Broken out so
// session.KillSession's cgroup-procs loop stays platform-portable.
func killPID(pid int, sig int32) error {
	return unix.Kill(pid, unix.Signal(sig))
}

// devPathForMajorMinor returns the /dev path for a character or block
// device identified by major:minor. Used by Session.TakeDevice to
// resolve the compositor's request (typically DRM 226:0 for the
// primary card + input devices) into a filesystem path.
//
// Reads /sys/dev/{char,block}/<major>:<minor>/uevent for the DEVNAME
// entry — the canonical map exposed by udev + kernel.
func devPathForMajorMinor(major, minor uint32) (string, error) {
	for _, kind := range []string{"char", "block"} {
		p := fmt.Sprintf("/sys/dev/%s/%d:%d/uevent", kind, major, minor)
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "DEVNAME=") {
				return "/dev/" + strings.TrimPrefix(line, "DEVNAME="), nil
			}
		}
	}
	return "", fmt.Errorf("device %d:%d not found in /sys/dev", major, minor)
}

// openDevice opens a device node the way logind does before handing it
// to a compositor: read/write, close-on-exec, no controlling tty, and
// non-blocking so a read on an idle evdev node can't wedge the daemon.
func openDevice(path string) (*os.File, error) {
	return os.OpenFile(path,
		os.O_RDWR|unix.O_CLOEXEC|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
}

// DRM master ioctls. _IO('d', 0x1e) and _IO('d', 0x1f) — no argument,
// no payload, so the request numbers are just the direction-less
// encoding of type 'd' (0x64) and the two sequence numbers.
const (
	drmIoctlSetMaster  = 0x641e
	drmIoctlDropMaster = 0x641f
)

// drmSetMaster claims DRM master on fd.
//
// A compositor cannot modeset without it. The kernel hands master to
// whoever opens the node first when nobody holds it, which means our
// own open() may already have taken it — but relying on that is the
// race elogind's comment calls out, so we ask explicitly.
//
// EBUSY means another client still holds master and is on its way out
// (the common case is the previous session's compositor during a
// handover), so retry briefly rather than failing the TakeDevice call.
// Attempt count and delay match elogind's DRM_SET_MASTER_RETRY_*.
func drmSetMaster(fd uintptr) error {
	for i := 0; ; i++ {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, drmIoctlSetMaster, 0)
		if errno == 0 {
			return nil
		}
		if errno != unix.EBUSY || i >= 20 {
			return errno
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// drmDropMaster releases DRM master, letting the next session's
// compositor claim it.
func drmDropMaster(fd uintptr) error {
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, drmIoctlDropMaster, 0); errno != 0 {
		return errno
	}
	return nil
}

// isDRMNode reports whether a /dev path is a DRM node, which is the
// only device class that needs the master dance.
func isDRMNode(path string) bool {
	return strings.HasPrefix(path, "/dev/dri/")
}

// waitForPidExit blocks until the process exits, then returns nil.
// Returns an error if the pid can't be watched at all.
//
// pidfd is the only race-free way to do this: a pid can be recycled
// between a /proc check and a kill, and polling /proc would both miss
// short-lived leaders and cost a wakeup per session per interval.
// POLLIN on a pidfd fires exactly once, when the process dies.
func waitForPidExit(pid uint32) error {
	fd, err := unix.PidfdOpen(int(pid), 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)

	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		_, err := unix.Poll(fds, -1)
		if err == unix.EINTR {
			continue
		}
		return err
	}
}

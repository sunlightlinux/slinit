// Linux-specific syscall wrappers for session lifecycle. Kept in a
// build-tagged file so a future BSD port can supply equivalents
// without editing session.go.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

// sessionFD is what CreateSession returns in the slot that used to carry
// the session FIFO. Since systemd v258 logind tracks a session by its
// leader's pidfd and hands back "something" there, a fresh eventfd per
// call. One shared eventfd does the same job: godbus does not dup a reply
// fd, so a per-call one could never be closed, and pam_systemd keeps the
// fd only when it is a FIFO (talking to an older logind), so nobody reads
// this one.
var sessionFD = sync.OnceValue(func() dbus.UnixFD {
	fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC)
	if err != nil {
		return -1
	}
	return dbus.UnixFD(fd)
})

// pidFromPidfd reads the pid a pidfd refers to from /proc/self/fdinfo.
// The kernel reports -1 once the process has exited and 0 when it lives
// in a pid namespace we cannot see; neither can lead a session.
func pidFromPidfd(fd int) (uint32, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", fd))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		v, ok := strings.CutPrefix(line, "Pid:")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, err
		}
		if n <= 0 {
			return 0, fmt.Errorf("pidfd refers to no visible process (Pid: %d)", n)
		}
		return uint32(n), nil
	}
	return 0, fmt.Errorf("fd %d is not a pidfd", fd)
}

// killPID sends the given signal to the process. Broken out so
// session.KillSession's cgroup-procs loop stays platform-portable.
func killPID(pid int, sig int32) error {
	return unix.Kill(pid, unix.Signal(sig))
}

// killLeader sends sig to the session's leader, and only if the pid
// still names a process of the session.
//
// The record holds a bare pid. Once the leader has exited — and before
// the reaper drops the session — the kernel may hand that pid to
// another process, and the session owner, who may call KillSession,
// would have root signal it. So the process is pinned with a pidfd,
// its cgroup is checked against the session's scope, and the signal
// goes through the pidfd: if the pinned process has died in between,
// the send fails instead of reaching whoever got the pid next. systemd
// holds the leader as a pidref for the same reason.
func killLeader(rec SessionRecord, sig int32) error {
	if !scopeUnderCgroupFS(rec.Scope) {
		return fmt.Errorf("session has no cgroup scope")
	}
	pidfd, err := unix.PidfdOpen(int(rec.LeaderPID), 0)
	if err != nil {
		return err
	}
	defer unix.Close(pidfd)
	want := strings.TrimPrefix(filepath.Clean(rec.Scope), "/sys/fs/cgroup")
	if got := procCgroup(rec.LeaderPID); got != want {
		return fmt.Errorf("pid %d is not in the session (cgroup %q)", rec.LeaderPID, got)
	}
	return unix.PidfdSendSignal(pidfd, unix.Signal(sig), nil, 0)
}

// sysRoot is where sysfs is mounted; a variable so the tests can build
// a device tree of their own.
var sysRoot = "/sys"

// devPathForMajorMinor returns the /dev path for a character device
// identified by major:minor. Used by Session.TakeDevice to resolve the
// compositor's request (typically DRM 226:0 for the primary card + input
// devices) into a filesystem path.
//
// Only the devices a session may be handed resolve, as in systemd's
// session_device_verify: character devices of subsystem drm named
// card*, input named event* and hidraw named hidraw*. Anything else is
// an error, or a session's owner could ask for /dev/mem or a disk and be
// handed it open read/write.
//
// Reads /sys/dev/char/<major>:<minor>/uevent for the DEVNAME entry —
// the canonical map exposed by udev + kernel.
func devPathForMajorMinor(major, minor uint32) (string, error) {
	dir, err := filepath.EvalSymlinks(fmt.Sprintf("%s/dev/char/%d:%d", sysRoot, major, minor))
	if err != nil {
		return "", fmt.Errorf("character device %d:%d not found in /sys/dev/char", major, minor)
	}
	sub, err := filepath.EvalSymlinks(filepath.Join(dir, "subsystem"))
	if err != nil {
		return "", fmt.Errorf("device %d:%d has no subsystem", major, minor)
	}
	subsystem, name := filepath.Base(sub), filepath.Base(dir)
	switch {
	case subsystem == "drm" && strings.HasPrefix(name, "card"),
		subsystem == "input" && strings.HasPrefix(name, "event"),
		subsystem == "hidraw" && strings.HasPrefix(name, "hidraw"):
	default:
		return "", fmt.Errorf("device %d:%d (%s %s) is not a session device", major, minor, subsystem, name)
	}
	b, err := os.ReadFile(filepath.Join(dir, "uevent"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "DEVNAME=") {
			return "/dev/" + strings.TrimPrefix(line, "DEVNAME="), nil
		}
	}
	return "", fmt.Errorf("device %d:%d has no device node", major, minor)
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
	f, err := openPidWatch(pid)
	if err != nil {
		return err
	}
	defer f.Close()
	return waitPidWatch(f)
}

// openPidWatch opens a pidfd on pid for waitPidWatch, as an *os.File in
// the Go netpoller. A wait on it parks the goroutine, not an OS thread:
// one thread per session, parked in poll(2), is a task counted against
// RLIMIT_NPROC and pids.max, and when those run out the Go runtime's next
// pthread_create fails and logind aborts. Closing the file ends the wait.
func openPidWatch(pid uint32) (*os.File, error) {
	fd, err := unix.PidfdOpen(int(pid), unix.PIDFD_NONBLOCK)
	if err == unix.EINVAL {
		// PIDFD_NONBLOCK is 5.10; the flag only matters to waitid.
		fd, err = unix.PidfdOpen(int(pid), 0)
		if err == nil {
			err = unix.SetNonblock(fd, true)
			if err != nil {
				unix.Close(fd)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "pidfd"), nil
}

// waitPidWatch blocks until the watched process exits (nil) or f is
// closed (os.ErrClosed).
func waitPidWatch(f *os.File) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	exited := func(fd uintptr) bool {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		for {
			n, err := unix.Poll(fds, 0)
			if err != unix.EINTR {
				return err == nil && n > 0
			}
		}
	}
	// ErrNoDeadline: the fd is not in the netpoller (pidfds are not
	// pollable before 5.3), and Read would not wait. Wait in poll(2), the
	// thread held, as before.
	if f.SetReadDeadline(time.Time{}) != nil {
		var werr error
		cerr := rc.Control(func(fd uintptr) {
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			for {
				_, werr = unix.Poll(fds, -1)
				if werr != unix.EINTR {
					return
				}
			}
		})
		if cerr != nil {
			return cerr
		}
		return werr
	}
	return rc.Read(exited)
}

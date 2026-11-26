package process

import (
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Waiting for a child without holding an OS thread.
//
// cmd.Wait() blocks in waitid(2) on the child's pidfd, and a goroutine
// blocked in a system call keeps its OS thread for as long as the call
// lasts — for a service, its whole life. A 2000-service boot held about
// a thousand threads that way, and every one of them is a task the
// kernel counts against RLIMIT_NPROC and a cgroup's pids.max. When that
// limit is close, the fork that fails is handled as a failed start, but
// the next thread the Go runtime needs fails too, and Go cannot survive
// that: slinit aborted with "pthread_create failed", which as PID 1 is
// a kernel panic. The defence is to need as few threads as possible, so
// the thread count must not grow with the number of children.
//
// A pidfd becomes readable when its process exits, and a readable fd can
// be waited for in the Go netpoller, which parks the goroutine and not a
// thread. Once it is readable the child is a zombie (or already reaped by
// PID 1's reaper), so the waitid that collects it returns at once.

// exitWatch is the pidfd of one started child, registered with the
// netpoller. The child's os.Process has been released, so this is the
// only handle slinit holds on it: the fd count per child stays at one.
type exitWatch struct {
	f *os.File
}

// newExitWatch takes the pidfd that cmd.Start filled in through
// SysProcAttr.PidFD and, if the kernel lets it be polled and waited on,
// returns a watch for it and releases cmd.Process. Returns nil when it
// cannot (no pidfd: kernel < 5.2; not pollable: < 5.3; no waitid
// P_PIDFD: < 5.4), having closed the pidfd; the caller then waits with
// cmd.Wait() as before.
//
// cmd must have been started by exec.Command, not CommandContext: with
// the process released, a context's Cancel could no longer kill it.
func newExitWatch(process *os.Process, pidfd int) *exitWatch {
	if pidfd < 0 {
		return nil
	}
	// Does waitid know P_PIDFD? WNOWAIT leaves the child to be collected
	// later. ECHILD is a yes too: the child is already gone, reaped by
	// PID 1's reaper, which is the race the exit router exists for.
	var si siginfoChild
	if err := waitidPidfd(pidfd, &si, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT); err != nil && err != syscall.ECHILD {
		unix.Close(pidfd)
		return nil
	}
	if err := unix.SetNonblock(pidfd, true); err != nil {
		unix.Close(pidfd)
		return nil
	}
	f := os.NewFile(uintptr(pidfd), "pidfd")
	// SetReadDeadline is how os admits whether the fd went into the
	// netpoller: ErrNoDeadline means it did not, and a wait on it would
	// block a thread after all.
	if f == nil || f.SetReadDeadline(time.Time{}) != nil {
		if f != nil {
			f.Close()
		}
		return nil
	}
	// The os.Process holds a pidfd of its own, dup'ed from this one.
	// Releasing it closes that copy; without this every child would cost
	// two descriptors, and PID 1 starts with a hard limit of 4096.
	process.Release()
	return &exitWatch{f: f}
}

// wait blocks, in the netpoller, until the child has exited, then reaps
// it. known is false when there was no status to collect — PID 1's
// reaper got there first and the status is on its way through the exit
// router — and the caller must not read a zero status as a clean exit.
func (w *exitWatch) wait() (status syscall.WaitStatus, known bool) {
	defer w.f.Close()
	rc, _ := w.f.SyscallConn() // cannot fail for an open *os.File
	// Read calls the func, and on false parks until the fd turns
	// readable and calls it again. Should the poller fail us after all,
	// the waitid below still waits for the child to exit: correct, just
	// with a thread held.
	_ = rc.Read(func(fd uintptr) bool { return pidfdReadable(int(fd)) })
	var si siginfoChild
	var werr error
	ctlErr := rc.Control(func(fd uintptr) {
		// The fd is non-blocking, so waitid answers EAGAIN rather than
		// waiting if the child has not exited; after a successful Read
		// that cannot happen, but block rather than guess if it does.
		for {
			werr = waitidPidfd(int(fd), &si, unix.WEXITED)
			if werr != syscall.EAGAIN {
				return
			}
			pidfdPollBlocking(int(fd))
		}
	})
	if ctlErr != nil || werr != nil {
		return 0, false
	}
	return si.waitStatus(), true
}

// pidfdReadable reports whether the pidfd's process has exited.
func pidfdReadable(fd int) bool {
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(fds, 0)
		if err == syscall.EINTR {
			continue
		}
		return err == nil && n > 0
	}
}

// pidfdPollBlocking waits, holding the thread, until fd is readable.
func pidfdPollBlocking(fd int) {
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		if _, err := unix.Poll(fds, -1); err != syscall.EINTR {
			return
		}
	}
}

// waitidPidfd is waitid(P_PIDFD, fd, si, options), retried on EINTR.
// x/sys's Siginfo does not expose the child fields, hence the raw call.
func waitidPidfd(fd int, si *siginfoChild, options int) error {
	for {
		_, _, e := syscall.Syscall6(syscall.SYS_WAITID, unix.P_PIDFD, uintptr(fd),
			uintptr(unsafe.Pointer(si)), uintptr(options), 0, 0)
		if e == syscall.EINTR {
			continue
		}
		if e != 0 {
			return e
		}
		return nil
	}
}

const is64bit = ^uint(0) >> 63 // 1 on 64-bit hosts, 0 on 32-bit ones

// siginfoChild is siginfo_t as waitid fills it for a child, laid out as
// Go's own internal/syscall/unix.SiginfoChild.
type siginfoChild struct {
	Signo     int32
	ErrnoCode [2]int32       // si_errno, si_code; swapped on MIPS
	_         [is64bit]int32 // padding on 64-bit hosts only
	Pid       int32
	Uid       uint32
	Status    int32
	_         [128 - (6+is64bit)*4]byte
}

// si_code values for SIGCHLD (include/uapi/asm-generic/siginfo.h).
const (
	cldExited    = 1
	cldKilled    = 2
	cldDumped    = 3
	cldTrapped   = 4
	cldStopped   = 5
	cldContinued = 6
)

var codeIndex = func() int {
	if strings.HasPrefix(runtime.GOARCH, "mips") {
		return 0
	}
	return 1
}()

// waitStatus converts the siginfo to the syscall.WaitStatus wait4 would
// have produced, as os does in pidfdWait.
func (s *siginfoChild) waitStatus() syscall.WaitStatus {
	switch s.ErrnoCode[codeIndex] {
	case cldExited:
		return syscall.WaitStatus(s.Status << 8)
	case cldDumped:
		return syscall.WaitStatus(s.Status) | 0x80
	case cldKilled:
		return syscall.WaitStatus(s.Status)
	case cldTrapped, cldStopped:
		return syscall.WaitStatus(s.Status<<8) | 0x7f
	case cldContinued:
		return 0xffff
	}
	return 0
}

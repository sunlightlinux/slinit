package service

import (
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// pollParked is unix.Poll(fds, -1) that waits in the Go netpoller
// instead of in poll(2). A goroutine in poll(2) holds an OS thread for
// as long as it waits — for an on-demand service, until its first
// client — and every such thread is a task the kernel counts against
// RLIMIT_NPROC and pids.max. When those run out the Go runtime's next
// pthread_create fails, and slinit aborts.
//
// The fds go into an epoll instance of their own, and the epoll fd,
// which is readable while any of them is ready, is what the netpoller
// waits on. The fds themselves are not touched: they are not made
// non-blocking (a socket shares that flag with the service that
// inherits it) and not registered with the runtime.
//
// Only for fds whose readiness a poll does not consume. A PSI trigger
// reports each event to one poll only, and to answer whether an epoll
// fd is ready the kernel polls its members (fs/eventpoll.c), so the
// nested check could take the event before epoll_wait sees it. Not
// measured; psiwatch keeps poll(2).
//
// Where the epoll route is not available it falls back to poll(2).
// Revents are filled in as poll(2) would; EINTR is retried.
func pollParked(fds []unix.PollFd) error {
	for i := range fds {
		fds[i].Revents = 0
	}
	if ok, err := epollParked(fds); ok {
		return err
	}
	for {
		_, err := unix.Poll(fds, -1)
		if err != syscall.EINTR {
			return err
		}
	}
}

// epollParked does pollParked's work; ok is false when it could not set
// up the epoll instance, and poll(2) must do it instead.
func epollParked(fds []unix.PollFd) (ok bool, err error) {
	epfd, err := unix.EpollCreate1(unix.EPOLL_CLOEXEC)
	if err != nil {
		return false, nil
	}
	for i, p := range fds {
		// POLLIN, POLLPRI, POLLOUT, POLLERR and POLLHUP have the same
		// values as their EPOLL counterparts. The index rides in the
		// event's data, so the answer maps back to fds[i].
		ev := unix.EpollEvent{Events: uint32(uint16(p.Events)), Fd: int32(i)}
		if unix.EpollCtl(epfd, unix.EPOLL_CTL_ADD, int(p.Fd), &ev) != nil {
			unix.Close(epfd)
			return false, nil
		}
	}
	if unix.SetNonblock(epfd, true) != nil {
		unix.Close(epfd)
		return false, nil
	}
	f := os.NewFile(uintptr(epfd), "epoll")
	defer f.Close()
	// ErrNoDeadline: not in the netpoller, and a Read would block a
	// thread after all.
	if f.SetReadDeadline(time.Time{}) != nil {
		return false, nil
	}
	rc, err := f.SyscallConn()
	if err != nil {
		return false, nil
	}
	evs := make([]unix.EpollEvent, len(fds))
	var waitErr error
	rerr := rc.Read(func(fd uintptr) bool {
		n, err := unix.EpollWait(int(fd), evs, 0)
		for err == syscall.EINTR {
			n, err = unix.EpollWait(int(fd), evs, 0)
		}
		if err != nil {
			waitErr = err
			return true
		}
		for _, ev := range evs[:n] {
			fds[ev.Fd].Revents = int16(ev.Events)
		}
		return n > 0
	})
	if rerr != nil {
		return true, rerr
	}
	return true, waitErr
}

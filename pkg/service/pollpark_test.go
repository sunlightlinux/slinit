package service

import (
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// pollParked must answer as poll(2) would: Revents on the fd that is
// ready and on no other, a hang-up reported, and the fds left blocking.
func TestPollParkedReportsLikePoll(t *testing.T) {
	var a, b [2]int
	if err := unix.Pipe2(a[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(a[0])
	if err := unix.Pipe2(b[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(b[0])
	defer unix.Close(b[1])

	fds := []unix.PollFd{
		{Fd: int32(b[0]), Events: unix.POLLIN},
		{Fd: int32(a[0]), Events: unix.POLLIN},
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		unix.Write(b[1], []byte{1})
	}()
	if err := pollParked(fds); err != nil {
		t.Fatal(err)
	}
	if fds[0].Revents&unix.POLLIN == 0 || fds[1].Revents != 0 {
		t.Fatalf("revents = %#x, %#x; want POLLIN on the written pipe only", fds[0].Revents, fds[1].Revents)
	}

	unix.Close(a[1]) // the wake-up the watchers use: EOF on the read end
	if err := pollParked(fds); err != nil {
		t.Fatal(err)
	}
	if fds[1].Revents&unix.POLLHUP == 0 {
		t.Fatalf("revents on the closed pipe = %#x, want POLLHUP", fds[1].Revents)
	}

	for _, fd := range []int{a[0], b[0]} {
		if fl, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0); err != nil || fl&unix.O_NONBLOCK != 0 {
			t.Errorf("fd %d flags %#x (%v): pollParked made it non-blocking", fd, fl, err)
		}
	}
}

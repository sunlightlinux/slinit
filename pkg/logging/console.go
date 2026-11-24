package logging

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// consoleStallTimeout is how long a write waits on a console that takes
// no output at all before giving up on it. A slow but working line takes
// bytes continuously (even 300 baud does thirty a second), so only a
// console that has stopped — Ctrl-S or Scroll Lock on a VT, XOFF on a
// serial line — goes this long without taking one.
var consoleStallTimeout = time.Second

var errConsoleStalled = errors.New("console is not taking output")

// Console is a console device opened for writing that cannot hold up its
// writer. The service state machine writes its "[ OK ] name" lines with
// the scheduling lock held, so a console that stopped taking output used
// to stop PID 1 at its next transition, for as long as the console stayed
// stopped.
//
// The device is opened non-blocking, on its own open file description, so
// nothing else that shares the device is affected. On a console that takes
// its output, Write writes exactly what it is given, as a blocking write
// would. One that takes nothing for consoleStallTimeout is given up on:
// what it would not take is dropped, later writes try once without waiting,
// and the first one that gets through is preceded by a line saying how many
// bytes were lost.
type Console struct {
	mu      sync.Mutex
	fd      int
	stalled bool   // gave up waiting; do not wait again until a write gets through
	partial bool   // the last drop cut a line short
	dropped uint64 // bytes dropped since the console last took output
}

// OpenConsole opens a console device (e.g. /dev/console, /dev/tty0) for
// writing; see Console.
func OpenConsole(path string) (*Console, error) {
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return &Console{fd: fd}, nil
}

// Write writes p to the console. It reports p as written when the console
// is blocked and p was dropped: a short write would stop an io.MultiWriter
// from reaching the other consoles.
func (c *Console) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fd < 0 {
		return 0, os.ErrClosed
	}
	buf, note := p, 0
	if c.dropped > 0 {
		lead := ""
		if c.partial {
			lead = "\r\n"
		}
		s := fmt.Sprintf("%sslinit: the console was blocked; %d bytes of output to it were dropped\r\n",
			lead, c.dropped)
		note = len(s)
		buf = append([]byte(s), p...)
	}
	n, err := c.write(buf)
	switch {
	case err == nil:
		c.dropped, c.partial = 0, false
		return len(p), nil
	case err == errConsoleStalled:
		if n < note {
			c.dropped += uint64(len(p))
		} else {
			c.dropped = uint64(len(buf) - n)
		}
		if n > 0 {
			c.partial = true
		}
		return len(p), nil
	default:
		return max(0, n-note), err
	}
}

// write writes buf, waiting while the console takes output and for up to
// consoleStallTimeout while it takes none — not at all once it is known
// to be stalled.
func (c *Console) write(buf []byte) (int, error) {
	n := 0
	var deadline time.Time
	for n < len(buf) {
		m, err := unix.Write(c.fd, buf[n:])
		if m > 0 {
			n += m
			c.stalled = false
			deadline = time.Time{}
			continue
		}
		switch err {
		case unix.EINTR:
			continue
		case unix.EAGAIN:
			if c.stalled {
				return n, errConsoleStalled
			}
			if deadline.IsZero() {
				deadline = time.Now().Add(consoleStallTimeout)
			} else if time.Now().After(deadline) {
				c.stalled = true
				return n, errConsoleStalled
			}
			// A tty reports POLLOUT only once its buffer is nearly
			// empty, so wake up every 10 ms to try anyway.
			_, _ = unix.Poll([]unix.PollFd{{Fd: int32(c.fd), Events: unix.POLLOUT}}, 10)
		case nil:
			return n, io.ErrShortWrite
		default:
			return n, err
		}
	}
	return n, nil
}

// Close closes the console.
func (c *Console) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fd < 0 {
		return os.ErrClosed
	}
	err := unix.Close(c.fd)
	c.fd = -1
	return err
}

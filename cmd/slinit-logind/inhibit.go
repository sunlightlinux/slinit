package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Inhibitor locks, the login1 half of making suspend safe.
//
// A client calls Inhibit(what, who, why, mode) and gets a file
// descriptor back. The lock lasts until that descriptor is closed, which
// is the whole protocol — there is no Release method. Two modes:
//
//   - "block" refuses the operation outright. A video player holds one
//     so the machine does not suspend mid-film.
//   - "delay" asks for time before the operation. A screen locker holds
//     one, and on PrepareForSleep(true) it locks the screen and then
//     closes the fd to say it is done. This is why closure has to be
//     detected per-suspend rather than per-session: the locker re-takes
//     the lock after every wake.
//
// Until now Inhibit() returned a pipe whose write end was closed
// immediately, so the fd was at EOF before the client saw it. That
// satisfied code which checks the call succeeded and nothing else: no
// registry, no enforcement, and ListInhibitors always empty.

// inhibitWhats are the operation classes systemd defines. Unknown
// tokens are rejected rather than silently ignored, so a typo in a
// client surfaces instead of producing a lock that guards nothing.
var inhibitWhats = map[string]struct{}{
	"shutdown": {}, "sleep": {}, "idle": {},
	"handle-power-key": {}, "handle-suspend-key": {},
	"handle-hibernate-key": {}, "handle-lid-switch": {},
	"handle-reboot-key": {},
}

// fdSendGrace is how long our copy of the descriptor handed to the
// client stays open after the method returns.
//
// godbus sends reply descriptors with syscall.UnixRights, taking the raw
// numbers at send time and leaving ownership with us — sd-bus closes
// them for you, godbus does not. So our copy has to be closed, or the
// pipe never reaches EOF and the lock never releases, and there is no
// callback that says "the reply has gone out". The reply is written
// microseconds after the method returns; two seconds is generous.
//
// Closing too early would hand the client a descriptor that is already
// at EOF, which releases the lock immediately — the behaviour this file
// replaces, so the degenerate case is no worse than the status quo
// rather than a hang.
const fdSendGrace = 2 * time.Second

// inhibitDelayMax caps how long a sleep waits for delay locks to clear.
// systemd's InhibitDelayMaxSec default is 5s and screen lockers are
// written against it, so matching the number means a locker that expects
// to have five seconds gets five seconds.
const inhibitDelayMax = 5 * time.Second

type inhibitor struct {
	ID   uint32
	What string // colon-separated list, e.g. "sleep:idle"
	Who  string
	Why  string
	Mode string // "block" or "delay"
	UID  uint32
	PID  uint32

	// ourEnd is the read end of the pipe. The client holds the write
	// end; when it closes, poll reports POLLHUP here.
	ourEnd *os.File
}

// blocks reports whether this lock covers the given operation class.
func (in *inhibitor) blocks(what string) bool {
	for _, tok := range strings.Split(in.What, ":") {
		if tok == what {
			return true
		}
	}
	return false
}

type inhibitRegistry struct {
	mu   sync.Mutex
	next uint32
	m    map[uint32]*inhibitor
}

func newInhibitRegistry() *inhibitRegistry {
	return &inhibitRegistry{m: map[uint32]*inhibitor{}}
}

// add registers a lock and returns the descriptor to hand the client.
// The caller passes it straight back over D-Bus; this function owns
// closing our own copy once the grace has elapsed.
func (r *inhibitRegistry) add(what, who, why, mode string, uid, pid uint32) (*os.File, *inhibitor, error) {
	for _, tok := range strings.Split(what, ":") {
		if tok == "" {
			continue
		}
		if _, ok := inhibitWhats[tok]; !ok {
			return nil, nil, fmt.Errorf("unknown inhibitor class %q", tok)
		}
	}
	if mode != "block" && mode != "delay" {
		return nil, nil, fmt.Errorf("mode must be block or delay, got %q", mode)
	}

	ourEnd, clientEnd, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}

	r.mu.Lock()
	r.next++
	in := &inhibitor{
		ID: r.next, What: what, Who: who, Why: why, Mode: mode,
		UID: uid, PID: pid, ourEnd: ourEnd,
	}
	r.m[in.ID] = in
	r.mu.Unlock()

	// Watch for the client letting go.
	go r.waitRelease(in)
	// And drop our copy of what the client now owns.
	time.AfterFunc(fdSendGrace, func() { _ = clientEnd.Close() })

	return clientEnd, in, nil
}

// waitRelease blocks until the client closes its end, then deregisters.
// A client that writes into the pipe instead of closing it is not
// releasing anything, so bytes are read and discarded; only a hangup or
// an error ends the lock.
func (r *inhibitRegistry) waitRelease(in *inhibitor) {
	fd := int(in.ourEnd.Fd())
	buf := make([]byte, 64)
	for {
		pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN | unix.POLLHUP}}
		if _, err := unix.Poll(pfd, -1); err != nil {
			if err == unix.EINTR {
				continue
			}
			break
		}
		if pfd[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			break
		}
		if pfd[0].Revents&unix.POLLIN != 0 {
			n, err := unix.Read(fd, buf)
			if n == 0 || err != nil {
				break
			}
			// Data, not a release. Keep waiting.
		}
	}
	r.remove(in.ID)
}

func (r *inhibitRegistry) remove(id uint32) {
	r.mu.Lock()
	in := r.m[id]
	delete(r.m, id)
	r.mu.Unlock()
	if in != nil {
		_ = in.ourEnd.Close()
	}
}

// list returns the current locks, ordered by id so output is stable.
func (r *inhibitRegistry) list() []*inhibitor {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*inhibitor, 0, len(r.m))
	for _, in := range r.m {
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// blockedBy returns the first block-mode lock covering `what`, or nil.
// First rather than all: the error message names one holder, which is
// what an operator needs to go and close.
func (r *inhibitRegistry) blockedBy(what string) *inhibitor {
	for _, in := range r.list() {
		if in.Mode == "block" && in.blocks(what) {
			return in
		}
	}
	return nil
}

// delayCount counts delay-mode locks covering `what` — how many holders
// still have to let go before the operation may proceed.
func (r *inhibitRegistry) delayCount(what string) int {
	n := 0
	for _, in := range r.list() {
		if in.Mode == "delay" && in.blocks(what) {
			n++
		}
	}
	return n
}

// waitForDelays blocks until every delay lock on `what` is released or
// `max` elapses, reporting whether the wait timed out.
//
// Timing out and proceeding is deliberate: a locker that crashed while
// holding a delay lock must not keep a laptop awake with its lid shut.
// systemd makes the same choice with InhibitDelayMaxSec.
func (r *inhibitRegistry) waitForDelays(what string, max time.Duration) (timedOut bool) {
	deadline := time.Now().Add(max)
	for r.delayCount(what) > 0 {
		if !time.Now().Before(deadline) {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

// inhibitedClasses returns the colon-separated list of classes locked in
// the given mode, which is the shape the BlockInhibited and
// DelayInhibited properties take.
func (r *inhibitRegistry) inhibitedClasses(mode string) string {
	seen := map[string]bool{}
	for _, in := range r.list() {
		if in.Mode != mode {
			continue
		}
		for _, tok := range strings.Split(in.What, ":") {
			if tok != "" {
				seen[tok] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for tok := range seen {
		out = append(out, tok)
	}
	sort.Strings(out)
	return strings.Join(out, ":")
}

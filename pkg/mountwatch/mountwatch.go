// Package mountwatch provides mount-based activation for slinit: start a
// service when a filesystem is mounted. This is launchd's StartOnMount.
//
// WHY NOT INOTIFY. pkg/pathwatch cannot do this. A mount creates no
// directory entry, so inotify produces no event for it — measured, not
// assumed: an IN_ALL_EVENTS watch on a directory reports nothing at all
// when a filesystem is mounted over it, and the watch stays attached to
// the now-hidden underlying inode. inotify has IN_UNMOUNT and no
// counterpart for mounting.
//
// The mechanism that does work is poll(2) on /proc/self/mountinfo, which
// reports POLLPRI|POLLERR when the mount table changes. It behaves well
// for this: each poll blocks until the next change rather than
// re-asserting, so the loop is not a busy wait. That was worth measuring
// too — a level-triggered reading of the same call would have returned
// immediately forever and burnt a core inside PID 1.
package mountwatch

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// mountInfoPath is the mount table this watcher follows.
const mountInfoPath = "/proc/self/mountinfo"

// pollInterval bounds how long a mount can go unnoticed when the kernel
// does not deliver POLLPRI, and is also what makes Close responsive.
const pollInterval = time.Second

// AnyMount is the path registration that fires on every new mount,
// whatever it is: launchd's StartOnMount is a boolean with exactly that
// meaning.
const AnyMount = "*"

// Logger is the subset of slinit's logger this package needs.
type Logger interface {
	Info(format string, args ...interface{})
	Warn(format string, args ...interface{})
	Debug(format string, args ...interface{})
}

type registration struct {
	path string
	fn   func()
}

// Watcher multiplexes one /proc/self/mountinfo poll across many service
// registrations.
type Watcher struct {
	logger Logger
	f      *os.File

	// stop is closed by Close to unblock Run. The poll wakes on its own
	// timeout rather than on this, which is why the timeout exists at
	// all; shutdown would otherwise wait for the next mount on the
	// system, which may never come.
	stop     chan struct{}
	stopOnce sync.Once

	// wake is signalled by the first Add. Until then Run does not poll
	// at all — see Run for why that matters.
	wake     chan struct{}
	wakeOnce sync.Once

	// done is closed by the poll loop when it exits, so Close can wait
	// for it to stop touching w.f before closing the file. Closing an fd
	// another thread is polling is not merely untidy: the number can be
	// reused by the next open and the poll then watches something else.
	done chan struct{}

	// onRead, when set, is called on each mount-table read. Test seam
	// for the idle-cost guarantee; nil in production.
	onRead func()

	mu    sync.Mutex
	regs  map[string]*registration
	known map[string]bool // mountpoints present at the last read
}

// New opens the mount table. The caller must Run it and Close it.
func New(logger Logger) (*Watcher, error) {
	f, err := os.Open(mountInfoPath)
	if err != nil {
		return nil, fmt.Errorf("opening the mount table: %w", err)
	}
	w := &Watcher{
		logger: logger,
		f:      f,
		stop:   make(chan struct{}),
		wake:   make(chan struct{}),
		done:   make(chan struct{}),
		regs:   make(map[string]*registration),
	}
	// Seed the known set, so the first poll reports what changed rather
	// than treating every filesystem already mounted at boot as news.
	known, err := w.readMountpoints()
	if err != nil {
		f.Close()
		return nil, err
	}
	w.known = known

	// The loop is started here rather than by the caller so the file has
	// exactly one owner. It costs nothing until a service registers.
	go w.run()
	return w, nil
}

// Add registers fn to run when path becomes a mountpoint. path may be
// AnyMount to fire on every mount.
//
// For a concrete path that is ALREADY mounted, fn fires immediately:
// "start when /mnt/data is mounted" is a statement about the state the
// service needs, and a service loaded after the mount landed would
// otherwise wait for an unmount-remount cycle that never comes. The
// AnyMount form does not fire on Add, because there is no "already" for
// it — every mount is the event.
func (w *Watcher) Add(path string, fn func()) error {
	if path == "" {
		return fmt.Errorf("empty mount path")
	}
	if path != AnyMount && !strings.HasPrefix(path, "/") {
		return fmt.Errorf("mount path %q is not absolute", path)
	}

	// Checked against a fresh read, not the set seeded at New: a path
	// mounted between startup and this registration is mounted now, and
	// that is the question being asked.
	already := false
	if path != AnyMount {
		if current, err := w.readMountpoints(); err == nil {
			already = current[path]
		}
	}

	w.mu.Lock()
	w.regs[path] = &registration{path: path, fn: fn}
	w.mu.Unlock()

	w.wakeOnce.Do(func() { close(w.wake) })

	if already {
		w.logger.Debug("mountwatch: %s is already mounted, firing on arm", path)
		fn()
	}
	return nil
}

// Remove drops a registration.
func (w *Watcher) Remove(path string) {
	w.mu.Lock()
	delete(w.regs, path)
	w.mu.Unlock()
}

// run polls the mount table until Close. Started by New.
func (w *Watcher) run() {
	defer close(w.done)
	// Idle cost has to be zero. Until some service declares
	// start-on-mount there is nothing to notice, and a once-per-second
	// wakeup in PID 1 on every system that never uses the directive
	// would be a regression nobody asked for — path activation sets the
	// bar here, since an inotify read costs literally nothing while it
	// waits. So this blocks until the first Add.
	select {
	case <-w.wake:
	case <-w.stop:
		return
	}

	// Re-seed now that watching has actually begun. Filesystems the boot
	// sequence mounted between New and the first registration are not
	// news: without this the wildcard form fires for /dev/pts and
	// friends on every boot.
	if known, err := w.readMountpoints(); err == nil {
		w.mu.Lock()
		w.known = known
		w.mu.Unlock()
	}

	for {
		select {
		case <-w.stop:
			return
		default:
		}

		// POLLIN is deliberately NOT requested. mounts_poll() always
		// returns EPOLLIN|EPOLLRDNORM and adds EPOLLERR|EPOLLPRI only
		// when the namespace's mount-event counter has moved, so asking
		// for POLLIN makes poll return instantly, every time: measured
		// at 23,289 iterations in one second inside the test VM, which
		// is a core burnt in PID 1 for nothing.
		fds := []unix.PollFd{{
			Fd:     int32(w.f.Fd()),
			Events: unix.POLLPRI | unix.POLLERR,
		}}
		n, err := unix.Poll(fds, int(pollInterval.Milliseconds()))
		if err != nil && err != unix.EINTR {
			w.logger.Warn("mountwatch: poll failed, falling back to the "+
				"interval re-read only: %v", err)
		}
		_ = n

		// The table is diffed on EVERY wake, including a bare timeout,
		// and that is the load-bearing part of this loop — poll is only
		// here to make the common case fast.
		//
		// The reason is measured, not defensive. In the test VM, POLLPRI
		// arrives for the first couple of mount-table changes after the
		// fd is opened and then stops arriving entirely: the loop stayed
		// healthy (timeouts kept ticking, the fd stayed valid) while a
		// tmpfs mount that /proc/1/mountinfo plainly showed produced no
		// notification at all. A watcher that trusted the notification
		// would simply stop noticing mounts, with nothing in any log to
		// say so — the exact failure this trigger exists to avoid.
		//
		// Re-reading costs an open and a few KB of parsing per second,
		// which is not worth optimising away for a correctness guarantee.
		w.dispatch()
	}
}

// dispatch re-reads the table and fires the registrations whose
// mountpoint has just appeared.
func (w *Watcher) dispatch() {
	current, err := w.readMountpoints()
	if err != nil {
		w.logger.Warn("mountwatch: re-reading the mount table: %v", err)
		return
	}

	w.mu.Lock()
	var fire []func()
	var appeared []string
	for mp := range current {
		if !w.known[mp] {
			appeared = append(appeared, mp)
		}
	}
	w.known = current
	if len(appeared) > 0 {
		for _, mp := range appeared {
			if reg, ok := w.regs[mp]; ok {
				fire = append(fire, reg.fn)
			}
		}
		if reg, ok := w.regs[AnyMount]; ok {
			fire = append(fire, reg.fn)
		}
	}
	w.mu.Unlock()

	// Callbacks run outside the lock: one of them starting a service
	// must not be able to block the next mount event behind this one.
	for _, fn := range fire {
		fn()
	}
}

// readMountpoints returns the set of mount points in the table.
//
// It OPENS THE FILE FRESH every time rather than rewinding the fd used
// for poll. That is not a style choice. /proc/self/mountinfo is a
// seq_file, and seeking it back to zero does not reliably regenerate
// its contents: measured inside the test VM, a watcher that seeked and
// re-read reported the same 7 mount points before and after a tmpfs
// mount that /proc/1/mountinfo plainly showed. Nothing ever looked
// newly mounted, so nothing ever fired, and there was no error anywhere
// to say so.
//
// Field 5 is the mount point and it is octal-escaped by the kernel for
// space, tab, newline and backslash, so a path with a space in it would
// otherwise never match a registration.
func (w *Watcher) readMountpoints() (map[string]bool, error) {
	if w.onRead != nil {
		w.onRead()
	}
	f, err := os.Open(mountInfoPath)
	if err != nil {
		return nil, fmt.Errorf("opening the mount table: %w", err)
	}
	defer f.Close()

	out := make(map[string]bool)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 {
			continue
		}
		out[unescapeMountPath(fields[4])] = true
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading the mount table: %w", err)
	}
	return out, nil
}

// unescapeMountPath reverses the kernel's octal escaping in
// /proc/*/mountinfo. Only four characters are ever escaped there, so
// this is not a general octal decoder.
func unescapeMountPath(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			switch s[i+1 : i+4] {
			case "040":
				b.WriteByte(' ')
				i += 3
				continue
			case "011":
				b.WriteByte('\t')
				i += 3
				continue
			case "012":
				b.WriteByte('\n')
				i += 3
				continue
			case "134":
				b.WriteByte('\\')
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Close stops Run and releases the mount table.
func (w *Watcher) Close() {
	w.stopOnce.Do(func() { close(w.stop) })
	<-w.done
	w.f.Close()
}

// Watching reports whether any service has registered, i.e. whether the
// poll loop is doing anything at all. Exists for the test that holds the
// idle-cost promise.
func (w *Watcher) Watching() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.regs) > 0
}

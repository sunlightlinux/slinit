// Package pathwatch provides inotify-based path activation for slinit
// services. A service can declare one of the start-on-path-* stanzas to
// be started when an external filesystem condition is met (file appears,
// file changes, directory becomes non-empty). The watcher fires the
// service's start callback once per arming; the caller is expected to
// re-arm the watch when the service becomes inactive again, mirroring
// systemd path-unit one-shot semantics.
package pathwatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Trigger selects the filesystem condition that activates the service.
type Trigger int

const (
	// TriggerNone means no trigger configured (zero value).
	TriggerNone Trigger = 0
	// TriggerExists fires when the path exists. If the path already exists
	// at arm time, fires immediately; otherwise the parent directory is
	// watched for the basename to appear.
	TriggerExists Trigger = 1
	// TriggerChanged fires when the path is written and closed (file) or
	// has entries created/removed/renamed (directory).
	TriggerChanged Trigger = 2
	// TriggerModified fires on every write to the path (or to entries
	// inside a watched directory).
	TriggerModified Trigger = 3
	// TriggerDirNotEmpty fires when the directory contains at least one
	// entry. Fires immediately if already non-empty.
	TriggerDirNotEmpty Trigger = 4
)

// String returns the stanza name for a Trigger.
func (t Trigger) String() string {
	switch t {
	case TriggerExists:
		return "start-on-path-exists"
	case TriggerChanged:
		return "start-on-path-changed"
	case TriggerModified:
		return "start-on-path-modified"
	case TriggerDirNotEmpty:
		return "start-on-directory-not-empty"
	default:
		return "none"
	}
}

// Logger is the minimum interface pathwatch needs.
type Logger interface {
	Info(format string, args ...interface{})
	Warn(format string, args ...interface{})
	Error(format string, args ...interface{})
}

type entry struct {
	path    string  // user-supplied absolute path
	parent  string  // parent dir being watched (for "appear" mode)
	base    string  // basename filter for parent-dir watches
	trigger Trigger
	fn      func()

	// State protected by Watcher.mu.
	wd       int32 // current inotify watch descriptor (-1 if none)
	fired    bool  // one-shot: ignore further events until Rearm
	watching string // path currently registered with inotify (parent or path itself)
}

// Watcher multiplexes a single inotify fd across many service registrations.
type Watcher struct {
	fd     int
	logger Logger

	mu      sync.Mutex
	entries map[string]*entry // path → entry (callers key by path)
	byWd    map[int32]*entry  // wd → entry

	quit chan struct{}
	done chan struct{}
}

// New creates a Watcher with its own inotify fd. The caller must invoke
// Run() in a goroutine and Close() at shutdown.
func New(logger Logger) (*Watcher, error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, fmt.Errorf("inotify_init1: %w", err)
	}
	return &Watcher{
		fd:      fd,
		logger:  logger,
		entries: make(map[string]*entry),
		byWd:    make(map[int32]*entry),
		quit:    make(chan struct{}),
		done:    make(chan struct{}),
	}, nil
}

// Add registers a service callback for the given path and trigger. The
// callback may be invoked from the watcher's Run goroutine (or
// synchronously, for triggers that match at arm time). It is the
// caller's responsibility to make fn goroutine-safe.
//
// Returns an error if the configuration is unusable (relative path,
// missing parent for "appear" mode, missing path for in-place modes).
// The error is non-fatal: the caller should log it and continue.
func (w *Watcher) Add(path string, trigger Trigger, fn func()) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("path must be absolute: %q", path)
	}
	if trigger < TriggerExists || trigger > TriggerDirNotEmpty {
		return fmt.Errorf("invalid trigger %d", trigger)
	}

	e := &entry{
		path:    path,
		parent:  filepath.Dir(path),
		base:    filepath.Base(path),
		trigger: trigger,
		fn:      fn,
		wd:      -1,
	}

	w.mu.Lock()
	if existing, ok := w.entries[path]; ok {
		w.mu.Unlock()
		_ = existing
		return fmt.Errorf("path %q already registered", path)
	}
	w.entries[path] = e
	w.mu.Unlock()

	return w.arm(e)
}

// Rearm clears the one-shot fired flag and re-evaluates the trigger so
// the service can be activated again. Called when the service becomes
// inactive (BecomingInactive / EventStopped). Safe to call concurrently.
func (w *Watcher) Rearm(path string) error {
	w.mu.Lock()
	e, ok := w.entries[path]
	if !ok {
		w.mu.Unlock()
		return fmt.Errorf("path %q not registered", path)
	}
	if !e.fired {
		w.mu.Unlock()
		return nil // already armed
	}
	e.fired = false
	// Drop any old watch — arm() will install a fresh one matching current state.
	if e.wd >= 0 {
		_, _ = unix.InotifyRmWatch(w.fd, uint32(e.wd))
		delete(w.byWd, e.wd)
		e.wd = -1
		e.watching = ""
	}
	w.mu.Unlock()
	return w.arm(e)
}

// Remove cancels a registration and drops its inotify watch.
func (w *Watcher) Remove(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	e, ok := w.entries[path]
	if !ok {
		return
	}
	if e.wd >= 0 {
		_, _ = unix.InotifyRmWatch(w.fd, uint32(e.wd))
		delete(w.byWd, e.wd)
	}
	delete(w.entries, path)
}

// arm installs the inotify watch appropriate for the entry's current
// trigger and filesystem state, firing synchronously if the condition
// already holds. Always called outside w.mu.
func (w *Watcher) arm(e *entry) error {
	switch e.trigger {
	case TriggerExists:
		if _, err := os.Stat(e.path); err == nil {
			w.fire(e)
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			w.logger.Warn("pathwatch: stat %q: %v", e.path, err)
		}
		// Watch parent for IN_CREATE | IN_MOVED_TO.
		return w.addWatch(e, e.parent, unix.IN_CREATE|unix.IN_MOVED_TO)

	case TriggerChanged, TriggerModified:
		info, err := os.Stat(e.path)
		if err != nil {
			return fmt.Errorf("%s requires existing path: %w", e.trigger, err)
		}
		mask := uint32(unix.IN_CLOSE_WRITE | unix.IN_CREATE | unix.IN_DELETE | unix.IN_MOVED_TO | unix.IN_MOVED_FROM)
		if e.trigger == TriggerModified {
			mask |= unix.IN_MODIFY
		}
		_ = info
		return w.addWatch(e, e.path, mask)

	case TriggerDirNotEmpty:
		info, err := os.Stat(e.path)
		if err != nil {
			return fmt.Errorf("%s requires existing directory: %w", e.trigger, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s: %q is not a directory", e.trigger, e.path)
		}
		empty, err := dirIsEmpty(e.path)
		if err != nil {
			return fmt.Errorf("%s: read %q: %w", e.trigger, e.path, err)
		}
		if !empty {
			w.fire(e)
			return nil
		}
		return w.addWatch(e, e.path, unix.IN_CREATE|unix.IN_MOVED_TO)
	}
	return fmt.Errorf("unhandled trigger %v", e.trigger)
}

// addWatch installs an inotify watch on `target` with `mask` and records
// the wd in the entry. target may be the path itself or its parent.
func (w *Watcher) addWatch(e *entry, target string, mask uint32) error {
	wd, err := unix.InotifyAddWatch(w.fd, target, mask)
	if err != nil {
		return fmt.Errorf("inotify_add_watch %q: %w", target, err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	// If another arm() raced us (unlikely — arm is serialized per entry by
	// the caller flow), drop the duplicate.
	if e.wd >= 0 && e.wd != int32(wd) {
		_, _ = unix.InotifyRmWatch(w.fd, uint32(e.wd))
		delete(w.byWd, e.wd)
	}
	e.wd = int32(wd)
	e.watching = target
	w.byWd[int32(wd)] = e
	return nil
}

// fire invokes the entry's callback under w.mu so the fired flag flips
// atomically with the call. The callback runs synchronously — service
// callers must not block (slinit's StartService returns immediately
// after queuing the start).
func (w *Watcher) fire(e *entry) {
	w.mu.Lock()
	if e.fired {
		w.mu.Unlock()
		return
	}
	e.fired = true
	fn := e.fn
	w.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// Run reads inotify events in a loop and dispatches to entries. Returns
// when Close() is called or the inotify fd is shut down.
func (w *Watcher) Run() {
	defer close(w.done)
	var buf [16 * 1024]byte
	for {
		select {
		case <-w.quit:
			return
		default:
		}
		n, err := readWithPoll(w.fd, buf[:], w.quit)
		if err != nil {
			if errors.Is(err, errQuit) {
				return
			}
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			w.logger.Error("pathwatch: read inotify: %v", err)
			return
		}
		w.dispatch(buf[:n])
	}
}

// dispatch parses inotify events out of buf and notifies matching entries.
func (w *Watcher) dispatch(buf []byte) {
	for off := 0; off+unix.SizeofInotifyEvent <= len(buf); {
		raw := (*unix.InotifyEvent)(unsafe.Pointer(&buf[off]))
		nameStart := off + unix.SizeofInotifyEvent
		nameEnd := nameStart + int(raw.Len)
		if nameEnd > len(buf) {
			break
		}
		name := ""
		if raw.Len > 0 {
			// name is NUL-terminated, possibly NUL-padded.
			b := buf[nameStart:nameEnd]
			for i, c := range b {
				if c == 0 {
					b = b[:i]
					break
				}
			}
			name = string(b)
		}

		w.mu.Lock()
		e, ok := w.byWd[raw.Wd]
		if !ok || e == nil {
			w.mu.Unlock()
			off = nameEnd
			continue
		}

		// IN_IGNORED means the kernel removed the watch (target was
		// deleted, or InotifyRmWatch was called). Drop our record.
		if raw.Mask&unix.IN_IGNORED != 0 {
			delete(w.byWd, raw.Wd)
			e.wd = -1
			e.watching = ""
			w.mu.Unlock()
			off = nameEnd
			continue
		}

		match := w.eventMatches(e, raw.Mask, name)
		w.mu.Unlock()

		if match {
			w.fire(e)
		}
		off = nameEnd
	}
}

// eventMatches decides whether `mask`/`name` on `e`'s current watch
// represents the configured trigger condition. Caller holds w.mu.
func (w *Watcher) eventMatches(e *entry, mask uint32, name string) bool {
	if e.fired {
		return false
	}
	switch e.trigger {
	case TriggerExists:
		// Watching parent dir for basename appearance.
		return (mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0) && name == e.base
	case TriggerChanged:
		// Watching path directly. For directories, name is the child
		// entry; for files, name is empty.
		return mask&(unix.IN_CLOSE_WRITE|unix.IN_CREATE|unix.IN_DELETE|
			unix.IN_MOVED_TO|unix.IN_MOVED_FROM) != 0
	case TriggerModified:
		return mask&(unix.IN_CLOSE_WRITE|unix.IN_MODIFY|unix.IN_CREATE|unix.IN_DELETE|
			unix.IN_MOVED_TO|unix.IN_MOVED_FROM) != 0
	case TriggerDirNotEmpty:
		return mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0
	}
	return false
}

// Close stops the Run goroutine and releases the inotify fd. Safe to
// call multiple times.
func (w *Watcher) Close() {
	select {
	case <-w.quit:
		return
	default:
		close(w.quit)
	}
	// Closing the fd unblocks readWithPoll's poll syscall.
	_ = unix.Close(w.fd)
	<-w.done
}

// errQuit is a sentinel from readWithPoll when quit was signalled.
var errQuit = errors.New("pathwatch: quit")

// readWithPoll reads from fd, returning errQuit if quit closes first.
// Uses ppoll to avoid a busy loop and to react to Close() promptly.
func readWithPoll(fd int, buf []byte, quit <-chan struct{}) (int, error) {
	for {
		select {
		case <-quit:
			return 0, errQuit
		default:
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		// 200ms timeout so we re-check quit periodically even if fd is silent.
		_, err := unix.Poll(fds, 200)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return 0, err
		}
		if fds[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) == 0 {
			continue
		}
		n, err := unix.Read(fd, buf)
		if err != nil {
			if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
				continue
			}
			if errors.Is(err, syscall.EBADF) {
				return 0, errQuit
			}
			return 0, err
		}
		return n, nil
	}
}

// dirIsEmpty returns true if dir has no entries (excluding . and ..).
func dirIsEmpty(dir string) (bool, error) {
	f, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return true, nil
		}
		// os.ReadDirnames returns io.EOF when truly empty.
		if err.Error() == "EOF" {
			return true, nil
		}
		// Fall through: empty if names is empty.
	}
	return len(names) == 0, nil
}


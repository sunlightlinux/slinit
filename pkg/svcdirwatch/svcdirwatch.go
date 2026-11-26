// Package svcdirwatch watches slinit's service-description directories
// with inotify and dispatches load / unload / modify callbacks when a
// service file appears, disappears, or is rewritten in place. Inspired
// by runsvdir's inotify-based rescan (runit 2.3.1+), but shaped to fit
// dinit-style single-file service descriptions rather than runit's
// per-service subdirectories.
//
// Concurrency model: Run() reads inotify events on its own goroutine
// and invokes callbacks synchronously. Callbacks typically forward
// into ServiceSet.LoadService / UnloadService, which take their own
// locks — no dispatch-through-eventloop indirection is needed.
package svcdirwatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Logger is the minimum interface svcdirwatch needs.
type Logger interface {
	Info(format string, args ...interface{})
	Warn(format string, args ...interface{})
	Error(format string, args ...interface{})
}

// Handler receives filesystem-driven service-description events. All
// callbacks fire from the Watcher's Run goroutine (after debounce);
// implementations must be goroutine-safe with respect to each other
// and any external control-socket work.
//
// Any callback may be left nil to opt out of that event class.
type Handler struct {
	// Appeared fires when a new service file is created (or renamed
	// into a watched dir). Typically implemented as
	// serviceSet.LoadService(name) — idempotent if already loaded.
	// The service is loaded but NOT auto-started; explicit start is
	// still required. This matches dinit's explicit-start model
	// rather than runsv's "present ⇒ running" semantics.
	Appeared func(name string)

	// Disappeared fires when a service file is removed (or renamed
	// out) and no watched directory still provides the name. Handler is expected to no-op if the service is currently
	// running (only unload when STOPPED); UnloadService requires
	// STOPPED state.
	Disappeared func(name string)

	// Modified fires when an existing service file is rewritten
	// in place. May be nil — slinit's "(modified since loaded)"
	// marker (checked on next `status`/`start`) already surfaces
	// this to the operator without a forced reload.
	Modified func(name string)
}

// Options controls filtering and debouncing behaviour.
type Options struct {
	// DebounceInterval collapses rapid multi-event bursts (write,
	// close, rename) into a single dispatch per file. Zero uses
	// 300ms — long enough for editor-save patterns, short enough to
	// stay reactive under interactive use.
	DebounceInterval time.Duration
}

// pending is one debounced burst of events on a file awaiting dispatch.
// What it dispatches is decided when it fires, from what is on disk
// then, not from the events: an editor save is a rename-away and a
// fresh write, and a burst read as "disappear wins" reported a file
// that is still there as removed.
type pending struct {
	timer    *time.Timer
	wasKnown bool // the file existed before the burst began
}

// Watcher multiplexes a single inotify fd across N services-dirs.
type Watcher struct {
	fd      int
	wakeFd  int // eventfd Close writes to, so Run's poll needs no timeout
	logger  Logger
	handler Handler
	opts    Options

	mu      sync.Mutex
	byWd    map[int32]string    // inotify wd → dir path
	pending map[string]*pending // "dir/name" key → debounced event
	known   map[string]struct{} // "dir/name" for files present at Add()
	// lost holds the dirs whose watch the kernel dropped (the dir was
	// removed); Run re-adds them every rewatchInterval, so one that is
	// created again is watched again.
	lost        map[string]struct{}
	lastRewatch time.Time
	quit        chan struct{}
	done        chan struct{}
}

// New creates a Watcher with its own inotify fd. The caller must
// invoke Run() in a goroutine and Close() at shutdown. `opts` may be
// the zero value for defaults.
func New(logger Logger, handler Handler, opts Options) (*Watcher, error) {
	if opts.DebounceInterval == 0 {
		opts.DebounceInterval = 300 * time.Millisecond
	}
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, fmt.Errorf("inotify_init1: %w", err)
	}
	wakeFd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("eventfd: %w", err)
	}
	return &Watcher{
		fd:      fd,
		wakeFd:  wakeFd,
		logger:  logger,
		handler: handler,
		opts:    opts,
		byWd:    make(map[int32]string),
		pending: make(map[string]*pending),
		known:   make(map[string]struct{}),
		lost:    make(map[string]struct{}),
		quit:    make(chan struct{}),
		done:    make(chan struct{}),
	}, nil
}

// AddDir registers a services-dir to watch. Existing entries in the
// dir are recorded (so subsequent MODIFY events fire Modified, not
// Appeared) but their callbacks are NOT invoked here — the caller has
// already loaded them via the normal boot path. Non-directories and
// missing paths return an error, but the caller can treat that as
// non-fatal (fall through to the polling default).
func (w *Watcher) AddDir(dir string) error {
	dir = filepath.Clean(dir)
	fi, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat %q: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%q is not a directory", dir)
	}

	wd, err := w.addWatch(dir)
	if err != nil {
		return err
	}

	w.mu.Lock()
	w.byWd[int32(wd)] = dir
	// Snapshot existing entries so we can distinguish first
	// appearance from in-place modification.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if !isServiceFile(name) {
			continue
		}
		if !e.Type().IsRegular() {
			continue
		}
		w.known[key(dir, name)] = struct{}{}
	}
	w.mu.Unlock()
	return nil
}

// addWatch adds the inotify watch for dir.
func (w *Watcher) addWatch(dir string) (int, error) {
	// Watch for names entering / leaving. IN_MODIFY isn't strictly
	// needed for the appear/disappear game — IN_CLOSE_WRITE covers
	// the "editor closed the file" case for both new and existing
	// files, and IN_MOVED_TO / IN_MOVED_FROM cover atomic rename.
	// IN_DELETE_SELF lets us react if someone rms the entire dir.
	mask := uint32(unix.IN_CREATE | unix.IN_CLOSE_WRITE |
		unix.IN_MOVED_TO | unix.IN_MOVED_FROM |
		unix.IN_DELETE | unix.IN_DELETE_SELF |
		unix.IN_EXCL_UNLINK | unix.IN_ONLYDIR)
	wd, err := unix.InotifyAddWatch(w.fd, dir, mask)
	if err != nil {
		return 0, fmt.Errorf("inotify_add_watch %q: %w", dir, err)
	}
	return wd, nil
}

// rewatchInterval is how often Run retries the watch on a removed dir.
const rewatchInterval = time.Second

// rewatchLost re-adds the watch on each removed dir that exists again,
// and rescans it: files created before the watch was back sent no event.
func (w *Watcher) rewatchLost() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.lost) == 0 || time.Since(w.lastRewatch) < rewatchInterval {
		return
	}
	w.lastRewatch = time.Now()
	for dir := range w.lost {
		wd, err := w.addWatch(dir)
		if err != nil {
			continue
		}
		delete(w.lost, dir)
		w.byWd[int32(wd)] = dir
		w.logger.Info("svcdirwatch: services-dir %q is back, watching it again", dir)
		w.rescanLocked(dir)
	}
}

// rescanLocked compares dir with what the watcher believes is in it and
// schedules a burst for every name that differs; fire works out which
// event each one is. Used when events were lost. Caller holds w.mu.
func (w *Watcher) rescanLocked(dir string) {
	entries, _ := os.ReadDir(dir)
	onDisk := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !isServiceFile(name) || e.IsDir() {
			continue
		}
		onDisk[name] = struct{}{}
		if _, ok := w.known[key(dir, name)]; !ok {
			w.scheduleLocked(dir, name)
		}
	}
	prefix := dir + "/"
	for k := range w.known {
		name, ok := strings.CutPrefix(k, prefix)
		if !ok || strings.Contains(name, "/") {
			continue
		}
		if _, ok := onDisk[name]; !ok {
			w.scheduleLocked(dir, name)
		}
	}
}

// scheduleLocked arms (or re-arms) the debounce timer for dir/name.
// Caller holds w.mu.
func (w *Watcher) scheduleLocked(dir, name string) {
	k := key(dir, name)
	p, ok := w.pending[k]
	if ok {
		p.timer.Stop()
	} else {
		_, was := w.known[k]
		p = &pending{wasKnown: was}
		w.pending[k] = p
	}
	p.timer = time.AfterFunc(w.opts.DebounceInterval, func() {
		w.fire(dir, name)
	})
}

// Run reads inotify events in a loop and dispatches to the handler.
// Returns when Close() is called or the inotify fd is shut down.
func (w *Watcher) Run() {
	defer close(w.done)
	var buf [16 * 1024]byte
	for {
		// Wake up on a timer only while a removed dir is waiting to
		// come back; otherwise sleep until an event or Close.
		timeout := -1
		w.mu.Lock()
		if len(w.lost) > 0 {
			timeout = int(rewatchInterval / time.Millisecond)
		}
		w.mu.Unlock()
		n, err := readWithPoll(w.fd, w.wakeFd, buf[:], timeout)
		if err != nil {
			if errors.Is(err, errQuit) {
				return
			}
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			w.logger.Error("svcdirwatch: read inotify: %v", err)
			return
		}
		w.dispatch(buf[:n])
		w.rewatchLost()
	}
}

// dispatch parses inotify events and schedules debounced callbacks.
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
			b := buf[nameStart:nameEnd]
			for i, c := range b {
				if c == 0 {
					b = b[:i]
					break
				}
			}
			name = string(b)
		}
		off = nameEnd

		w.mu.Lock()
		// The kernel's queue filled and events were dropped: what was
		// read says nothing about what is in the dirs now, so compare
		// every dir with what is known.
		if raw.Mask&unix.IN_Q_OVERFLOW != 0 {
			w.logger.Warn("svcdirwatch: inotify queue overflowed, rescanning")
			for _, d := range w.byWd {
				w.rescanLocked(d)
			}
			w.mu.Unlock()
			continue
		}

		dir, ok := w.byWd[raw.Wd]
		if !ok {
			w.mu.Unlock()
			continue
		}

		// Watched dir itself was removed.
		if raw.Mask&unix.IN_DELETE_SELF != 0 {
			w.logger.Warn("svcdirwatch: services-dir %q was removed", dir)
			delete(w.byWd, raw.Wd)
			w.lost[dir] = struct{}{}
			w.mu.Unlock()
			continue
		}

		// IN_IGNORED means kernel dropped the watch.
		if raw.Mask&unix.IN_IGNORED != 0 {
			delete(w.byWd, raw.Wd)
			w.lost[dir] = struct{}{}
			w.mu.Unlock()
			continue
		}

		if name == "" || !isServiceFile(name) {
			w.mu.Unlock()
			continue
		}

		// Directory-child events targeting a dir (not a file) are
		// filtered out — dinit-format descriptions are single files,
		// and overlay `.d/` dirs are separately consumed by the
		// loader. IN_ISDIR is set on both IN_CREATE and IN_DELETE for
		// directories.
		if raw.Mask&unix.IN_ISDIR != 0 {
			w.mu.Unlock()
			continue
		}

		if raw.Mask&(unix.IN_CREATE|unix.IN_MOVED_TO|unix.IN_CLOSE_WRITE|
			unix.IN_DELETE|unix.IN_MOVED_FROM) == 0 {
			w.mu.Unlock()
			continue
		}

		// Debounce: reset (or arm) the per-file timer; fire decides
		// what the burst amounted to.
		w.scheduleLocked(dir, name)
		w.mu.Unlock()
	}
}

// fire runs a debounced burst's callback, then clears it. The file
// present now is Appeared if it was not there before the burst and
// Modified if it was; a file absent now is Disappeared, unless another
// watched directory still provides the name — the handler gets only
// the name, and would unload the copy that is still there — in which
// case the service's description changed: Modified.
func (w *Watcher) fire(dir, name string) {
	w.mu.Lock()
	k := key(dir, name)
	p, ok := w.pending[k]
	if !ok {
		w.mu.Unlock()
		return
	}
	delete(w.pending, k)
	handler := w.handler

	var evk kind
	if present(dir, name) {
		w.known[k] = struct{}{}
		evk = kindAppeared
		if p.wasKnown {
			evk = kindModified
		}
	} else {
		delete(w.known, k)
		evk = kindDisappeared
		for _, d := range w.byWd {
			if d != dir && present(d, name) {
				evk = kindModified
				break
			}
		}
	}
	w.mu.Unlock()

	switch evk {
	case kindAppeared:
		if handler.Appeared != nil {
			handler.Appeared(name)
		}
	case kindDisappeared:
		if handler.Disappeared != nil {
			handler.Disappeared(name)
		}
	case kindModified:
		if handler.Modified != nil {
			handler.Modified(name)
		}
	}
}

// Close stops the Run goroutine and releases the inotify fd. Any
// still-pending debounced events are dropped without firing. Safe to
// call multiple times.
func (w *Watcher) Close() {
	select {
	case <-w.quit:
		return
	default:
		close(w.quit)
	}
	w.mu.Lock()
	for _, p := range w.pending {
		p.timer.Stop()
	}
	w.pending = map[string]*pending{}
	w.mu.Unlock()
	// Wake Run's poll. The fds are closed only once Run has returned:
	// closed under it, Run could poll or read a number the process had
	// already reused for another file.
	_, _ = unix.Write(w.wakeFd, []byte{1, 0, 0, 0, 0, 0, 0, 0})
	<-w.done
	_ = unix.Close(w.fd)
	_ = unix.Close(w.wakeFd)
}

// isServiceFile filters out obvious non-service entries:
//   - dotfiles (.foo, .git)
//   - editor backups (foo~, foo.swp, foo.swx, foo.swo, foo.tmp, foo.new)
//   - obvious ordering / conf.d artefacts we don't want to treat as
//     services (foo.d treated as overlay by the loader; leave it alone)
func isServiceFile(name string) bool {
	if name == "" || name[0] == '.' {
		return false
	}
	if strings.HasSuffix(name, "~") ||
		strings.HasSuffix(name, ".swp") ||
		strings.HasSuffix(name, ".swx") ||
		strings.HasSuffix(name, ".swo") ||
		strings.HasSuffix(name, ".tmp") ||
		strings.HasSuffix(name, ".new") ||
		strings.HasSuffix(name, ".bak") ||
		strings.HasSuffix(name, ".d") {
		return false
	}
	return true
}

// kind is what a debounced burst amounted to.
type kind uint8

const (
	kindAppeared kind = iota
	kindDisappeared
	kindModified
)

// present reports whether dir holds a service file called name.
func present(dir, name string) bool {
	fi, err := os.Stat(filepath.Join(dir, name))
	return err == nil && !fi.IsDir()
}

func key(dir, name string) string {
	return dir + "/" + name
}

// errQuit is a sentinel from readWithPoll when Close woke it.
var errQuit = errors.New("svcdirwatch: quit")

// readWithPoll reads from fd, returning errQuit once wakeFd is
// signalled. A poll timeout (timeout ms; -1 waits for ever) returns
// (0, nil), so Run gets to retry lost dirs.
func readWithPoll(fd, wakeFd int, buf []byte, timeout int) (int, error) {
	for {
		fds := []unix.PollFd{
			{Fd: int32(fd), Events: unix.POLLIN},
			{Fd: int32(wakeFd), Events: unix.POLLIN},
		}
		ready, err := unix.Poll(fds, timeout)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return 0, err
		}
		if ready == 0 {
			return 0, nil
		}
		if fds[1].Revents != 0 {
			return 0, errQuit
		}
		if fds[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) == 0 {
			continue
		}
		n, err := unix.Read(fd, buf)
		if err != nil {
			if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
				continue
			}
			return 0, err
		}
		return n, nil
	}
}

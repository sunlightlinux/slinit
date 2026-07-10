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
	// out). Handler is expected to no-op if the service is currently
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

// event kind carried through debounce.
type kind uint8

const (
	kindAppeared kind = iota
	kindDisappeared
	kindModified
)

// pending is one debounced event awaiting dispatch.
type pending struct {
	timer *time.Timer
	last  kind
}

// Watcher multiplexes a single inotify fd across N services-dirs.
type Watcher struct {
	fd      int
	logger  Logger
	handler Handler
	opts    Options

	mu       sync.Mutex
	byWd     map[int32]string   // inotify wd → dir path
	pending  map[string]*pending // "dir/name" key → debounced event
	known    map[string]struct{} // "dir/name" for files present at Add()
	quit     chan struct{}
	done     chan struct{}
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
	return &Watcher{
		fd:      fd,
		logger:  logger,
		handler: handler,
		opts:    opts,
		byWd:    make(map[int32]string),
		pending: make(map[string]*pending),
		known:   make(map[string]struct{}),
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

	// Watch for names entering / leaving. IN_MODIFY isn't strictly
	// needed for the appear/disappear game — IN_CLOSE_WRITE covers
	// the "editor closed the file" case for both new and existing
	// files, and IN_MOVED_TO / IN_MOVED_FROM cover atomic rename.
	// IN_DELETE_SELF lets us react if someone rms the entire dir.
	mask := uint32(unix.IN_CREATE | unix.IN_CLOSE_WRITE |
		unix.IN_MOVED_TO | unix.IN_MOVED_FROM |
		unix.IN_DELETE | unix.IN_DELETE_SELF |
		unix.IN_EXCL_UNLINK)
	wd, err := unix.InotifyAddWatch(w.fd, dir, mask)
	if err != nil {
		return fmt.Errorf("inotify_add_watch %q: %w", dir, err)
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

// Run reads inotify events in a loop and dispatches to the handler.
// Returns when Close() is called or the inotify fd is shut down.
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
			w.logger.Error("svcdirwatch: read inotify: %v", err)
			return
		}
		w.dispatch(buf[:n])
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
		dir, ok := w.byWd[raw.Wd]
		if !ok {
			w.mu.Unlock()
			continue
		}

		// Watched dir itself was removed.
		if raw.Mask&unix.IN_DELETE_SELF != 0 {
			w.logger.Warn("svcdirwatch: services-dir %q was removed", dir)
			delete(w.byWd, raw.Wd)
			w.mu.Unlock()
			continue
		}

		// IN_IGNORED means kernel dropped the watch.
		if raw.Mask&unix.IN_IGNORED != 0 {
			delete(w.byWd, raw.Wd)
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

		k := key(dir, name)
		var evk kind
		switch {
		case raw.Mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0:
			// Both CREATE and MOVED_TO can arrive for a new file;
			// CLOSE_WRITE will follow for CREATE, but MOVED_TO is a
			// complete rename so no CLOSE_WRITE trails it. Decide by
			// whether we've seen this name before.
			if _, seen := w.known[k]; seen {
				evk = kindModified
			} else {
				evk = kindAppeared
				w.known[k] = struct{}{}
			}
		case raw.Mask&unix.IN_CLOSE_WRITE != 0:
			// Second close after CREATE → still first appearance
			// (CREATE already primed 'known'). For a name we hadn't
			// seen at Add-time and hadn't been CREATE-tracked, this
			// covers `cp foo /etc/slinit.d/svc` where CLOSE_WRITE
			// arrives without a prior CREATE we processed
			// (races between AddDir's snapshot and the first write).
			if _, seen := w.known[k]; seen {
				evk = kindModified
			} else {
				evk = kindAppeared
				w.known[k] = struct{}{}
			}
		case raw.Mask&(unix.IN_DELETE|unix.IN_MOVED_FROM) != 0:
			delete(w.known, k)
			evk = kindDisappeared
		default:
			w.mu.Unlock()
			continue
		}

		// Debounce: reset (or arm) the per-file timer with the
		// latest event kind. Coalescing rules:
		// - Appeared+Modified → Appeared (load implies fresh parse).
		// - Appeared+Disappeared → Disappeared (final winner).
		// - Modified+Modified   → Modified.
		p, ok := w.pending[k]
		if ok {
			p.timer.Stop()
			// Coalesce: disappear always wins; appeared beats modified.
			if evk == kindDisappeared || p.last == kindDisappeared {
				p.last = kindDisappeared
			} else if p.last == kindAppeared || evk == kindAppeared {
				p.last = kindAppeared
			} else {
				p.last = kindModified
			}
		} else {
			p = &pending{last: evk}
			w.pending[k] = p
		}
		dirCopy, nameCopy := dir, name
		p.timer = time.AfterFunc(w.opts.DebounceInterval, func() {
			w.fire(dirCopy, nameCopy)
		})
		w.mu.Unlock()
	}
}

// fire runs a debounced pending event's callback, then clears it.
func (w *Watcher) fire(dir, name string) {
	w.mu.Lock()
	k := key(dir, name)
	p, ok := w.pending[k]
	if !ok {
		w.mu.Unlock()
		return
	}
	evk := p.last
	delete(w.pending, k)
	handler := w.handler
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
	_ = unix.Close(w.fd)
	<-w.done
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

func key(dir, name string) string {
	return dir + "/" + name
}

// errQuit is a sentinel from readWithPoll when quit was signalled.
var errQuit = errors.New("svcdirwatch: quit")

// readWithPoll reads from fd, returning errQuit if quit closes first.
func readWithPoll(fd int, buf []byte, quit <-chan struct{}) (int, error) {
	for {
		select {
		case <-quit:
			return 0, errQuit
		default:
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
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

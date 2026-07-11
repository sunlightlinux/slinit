package shutdown

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// SentinelAction identifies which sentinel-file action fired.
// The names mirror runit's /etc/runit/{stopit,reboot} plus an added
// poweroff (symmetric with slinit's shutdown surface).
type SentinelAction string

const (
	// SentinelHalt = stopit + x.
	SentinelHalt SentinelAction = "halt"
	// SentinelReboot = reboot + x.
	SentinelReboot SentinelAction = "reboot"
	// SentinelPoweroff = poweroff + x. New in slinit; symmetric extension.
	SentinelPoweroff SentinelAction = "poweroff"
)

// Recognized filenames. Anything else in the directory is ignored.
const (
	fileStopit   = "stopit"
	fileReboot   = "reboot"
	filePoweroff = "poweroff"
)

// SentinelHandler is invoked when a recognized sentinel event fires.
// The audit fields (Requestor, MTime, Path) are captured at trigger
// time before the file is unlinked, so a compliance log can record
// who/what/when even after the file is gone.
type SentinelHandler struct {
	// OnShutdown fires for stopit/reboot/poweroff. The audit struct
	// captures owner + timestamp for compliance reporting.
	OnShutdown func(action SentinelAction, audit SentinelAudit)
}

// SentinelAudit is the forensic evidence captured for one event.
type SentinelAudit struct {
	Path      string
	Requestor uint32    // file owner UID
	MTime     time.Time // file mtime (when the operator flipped +x)
	Source    string    // "inotify" or "initial-scan"
}

// SentinelLogger is the subset of the daemon logger the watcher uses.
type SentinelLogger interface {
	Info(string, ...interface{})
	Warn(string, ...interface{})
	Error(string, ...interface{})
}

// SentinelWatcher is a small inotify-based watcher on a single
// directory that fires SentinelHandler callbacks whenever a
// recognized filename receives IN_ATTRIB / IN_CLOSE_WRITE /
// IN_CREATE / IN_MOVED_TO. Only executable files trigger; a plain
// touch is ignored, matching runit's "chmod +x" convention.
type SentinelWatcher struct {
	dir        string
	fd         int
	logger     SentinelLogger
	handler    SentinelHandler
	quit       chan struct{}
	done       chan struct{}
	once       sync.Once
	runStarted atomic.Bool // set true when Run() enters its loop
}

// NewSentinelWatcher installs the inotify watch on dir. The
// directory is created (0700) if it does not exist — sentinel files
// are privileged control-plane input, so tight defaults protect
// against a non-root user planting a stopit file.
func NewSentinelWatcher(dir string, logger SentinelLogger, handler SentinelHandler) (*SentinelWatcher, error) {
	if dir == "" {
		return nil, errors.New("sentinel: dir is empty")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("sentinel: create %s: %w", dir, err)
	}

	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, fmt.Errorf("sentinel: inotify_init1: %w", err)
	}
	// Watch for both "file created and closed for writing" (touch +
	// chmod pattern) and IN_ATTRIB (chmod on a pre-existing file).
	// IN_MOVED_TO catches the atomic "install -m 755 /dev/null $f"
	// pattern DBAs are trained to use.
	mask := uint32(unix.IN_CREATE | unix.IN_CLOSE_WRITE | unix.IN_MOVED_TO |
		unix.IN_ATTRIB | unix.IN_EXCL_UNLINK)
	if _, err := unix.InotifyAddWatch(fd, dir, mask); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("sentinel: watch %s: %w", dir, err)
	}

	return &SentinelWatcher{
		dir:     dir,
		fd:      fd,
		logger:  logger,
		handler: handler,
		quit:    make(chan struct{}),
		done:    make(chan struct{}),
	}, nil
}

// InitialScan honors sentinel files that were placed before slinit
// started — e.g. an admin created "reboot" while slinit was down,
// and the daemon should honor that request on next boot. Only files
// with +x mode fire; the rest are silently ignored.
func (w *SentinelWatcher) InitialScan() {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		w.check(e.Name(), "initial-scan")
	}
}

// Run drains the inotify queue in the caller's goroutine. Returns
// when Close() is called or on a fatal inotify error. Safe to call
// only once.
func (w *SentinelWatcher) Run() {
	w.runStarted.Store(true)
	defer close(w.done)
	buf := make([]byte, 4096)
	for {
		n, err := w.readEvents(buf)
		if err != nil {
			if errors.Is(err, errSentinelQuit) {
				return
			}
			w.logger.Warn("sentinel: read error: %v", err)
			return
		}
		w.dispatch(buf[:n])
	}
}

// Close stops the watcher. When Run() was started, blocks until it
// returns; when it was not, only closes the inotify fd. Safe to
// call from an unrelated goroutine.
func (w *SentinelWatcher) Close() {
	w.once.Do(func() {
		close(w.quit)
	})
	unix.Close(w.fd)
	if w.runStarted.Load() {
		<-w.done
	}
}

var errSentinelQuit = errors.New("sentinel: quit")

// readEvents blocks until inotify data is available OR the quit
// channel closes. Uses a poll timeout of 200 ms so shutdown is prompt
// without busy-waiting.
func (w *SentinelWatcher) readEvents(buf []byte) (int, error) {
	for {
		select {
		case <-w.quit:
			return 0, errSentinelQuit
		default:
		}
		fds := []unix.PollFd{{Fd: int32(w.fd), Events: unix.POLLIN}}
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
		n, err := unix.Read(w.fd, buf)
		if err != nil {
			if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
				continue
			}
			if errors.Is(err, syscall.EBADF) {
				return 0, errSentinelQuit
			}
			return 0, err
		}
		return n, nil
	}
}

// dispatch walks the raw inotify buffer, decoding one event at a
// time and invoking check() for each. Follows the standard
// InotifyEvent + name layout described in inotify(7).
func (w *SentinelWatcher) dispatch(buf []byte) {
	const evtSize = int(unsafe.Sizeof(unix.InotifyEvent{}))
	off := 0
	for off+evtSize <= len(buf) {
		evt := (*unix.InotifyEvent)(unsafe.Pointer(&buf[off]))
		nameLen := int(evt.Len)
		nameStart := off + evtSize
		nameEnd := nameStart + nameLen
		if nameEnd > len(buf) {
			return
		}
		if evt.Mask&unix.IN_ISDIR == 0 && nameLen > 0 {
			// Strip trailing NUL(s) from the padded name.
			nameBytes := buf[nameStart:nameEnd]
			for i, b := range nameBytes {
				if b == 0 {
					nameBytes = nameBytes[:i]
					break
				}
			}
			w.check(string(nameBytes), "inotify")
		}
		off = nameEnd
	}
}

// check inspects one filename. If it's a recognized sentinel AND the
// file is present + executable, the corresponding audit record is
// logged, the handler is invoked, and the file is unlinked so it
// doesn't fire again after the daemon restarts.
func (w *SentinelWatcher) check(name, source string) {
	var action SentinelAction
	switch name {
	case fileStopit:
		action = SentinelHalt
	case fileReboot:
		action = SentinelReboot
	case filePoweroff:
		action = SentinelPoweroff
	default:
		return
	}

	path := filepath.Join(w.dir, name)
	info, err := os.Stat(path)
	if err != nil {
		// Race: file was removed between the inotify event and stat.
		// That's fine — nothing to trigger, nothing to log.
		return
	}
	if info.Mode()&0111 == 0 {
		// Recognized name but not executable → operator staged the
		// file without arming it. Silent — this is the standard
		// runit workflow (touch + chmod +x as two steps).
		return
	}

	st, _ := info.Sys().(*syscall.Stat_t)
	audit := SentinelAudit{
		Path:   path,
		MTime:  info.ModTime(),
		Source: source,
	}
	if st != nil {
		audit.Requestor = st.Uid
	}

	// Audit line first — even if the handler / unlink fails, the
	// compliance evidence is durable in the daemon log.
	w.logger.Info("sentinel: %s requested via %s (uid=%d, mtime=%s, source=%s)",
		action, path, audit.Requestor, audit.MTime.Format(time.RFC3339), source)

	// Unlink BEFORE firing the handler: if the handler is
	// synchronous and initiates shutdown, we might not get another
	// chance to remove the file, and leaving it in place would
	// re-fire on next boot.
	if err := os.Remove(path); err != nil {
		w.logger.Warn("sentinel: failed to unlink %s: %v", path, err)
	}

	if w.handler.OnShutdown != nil {
		w.handler.OnShutdown(action, audit)
	}
}

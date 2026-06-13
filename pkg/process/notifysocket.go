package process

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// notifySocketRoot is where slinit creates per-service Unix datagram
// sockets the child can use as $NOTIFY_SOCKET. Matches systemd's
// /run/systemd/notify layout (one socket per unit, distinct path).
const notifySocketRoot = "/run/slinit/notify"

// NotifySocketHandler is invoked by the listener for each successfully
// parsed sd_notify message. fds is non-nil only when the message had
// SCM_RIGHTS attached (the FDSTORE=1 case).
type NotifySocketHandler interface {
	OnNotify(msg NotifyMessage, fds []*os.File)
}

// NotifySocketListener owns a Unix datagram socket dedicated to one
// service. The child writes sd_notify protocol packets to it; the
// parent's goroutine routes them to the configured handler. Stop()
// closes the socket, drops the on-disk path, and waits for the
// goroutine.
type NotifySocketListener struct {
	path string
	conn *net.UnixConn

	wg     sync.WaitGroup
	stopCh chan struct{}
}

// NewNotifySocketListener creates a Unix datagram socket at
// /run/slinit/notify/<service>.sock with mode 0600 (only writable by
// the service's run-as user once chowned). The returned listener is
// inactive until Start is called.
func NewNotifySocketListener(serviceName string, uid, gid uint32) (*NotifySocketListener, error) {
	if err := os.MkdirAll(notifySocketRoot, 0755); err != nil {
		return nil, fmt.Errorf("notify socket dir: %w", err)
	}
	path := filepath.Join(notifySocketRoot, serviceName+".sock")
	// Remove any stale socket from a prior run.
	_ = os.Remove(path)
	addr := &net.UnixAddr{Name: path, Net: "unixgram"}
	conn, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		return nil, fmt.Errorf("notify socket bind %s: %w", path, err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		conn.Close()
		os.Remove(path)
		return nil, fmt.Errorf("notify socket chmod: %w", err)
	}
	if err := os.Chown(path, int(uid), int(gid)); err != nil && !os.IsPermission(err) {
		conn.Close()
		os.Remove(path)
		return nil, fmt.Errorf("notify socket chown: %w", err)
	}
	return &NotifySocketListener{
		path:   path,
		conn:   conn,
		stopCh: make(chan struct{}),
	}, nil
}

// Path returns the on-disk socket path; the caller exports it to the
// child as $NOTIFY_SOCKET.
func (l *NotifySocketListener) Path() string { return l.path }

// Start launches the reader goroutine. Safe to call once.
func (l *NotifySocketListener) Start(handler NotifySocketHandler) {
	l.wg.Add(1)
	go l.readLoop(handler)
}

// Stop tells the reader goroutine to exit, closes the socket, removes
// the on-disk path, and waits for the goroutine. Idempotent.
func (l *NotifySocketListener) Stop() {
	select {
	case <-l.stopCh:
		return // already stopping
	default:
		close(l.stopCh)
	}
	if l.conn != nil {
		l.conn.Close()
	}
	_ = os.Remove(l.path)
	l.wg.Wait()
}

func (l *NotifySocketListener) readLoop(handler NotifySocketHandler) {
	defer l.wg.Done()
	// Recvmsg via raw fd because net.UnixConn does not expose SCM_RIGHTS.
	rawConn, err := l.conn.SyscallConn()
	if err != nil {
		return
	}
	bodyBuf := make([]byte, 4096)
	oobBuf := make([]byte, unix.CmsgSpace(32*4))

	for {
		select {
		case <-l.stopCh:
			return
		default:
		}
		var (
			n, oobn int
			recvErr error
		)
		ctlErr := rawConn.Read(func(fd uintptr) bool {
			n, oobn, _, _, recvErr = unix.Recvmsg(int(fd), bodyBuf, oobBuf, 0)
			// Return true so the runtime doesn't retry on EAGAIN we
			// never produced.
			return true
		})
		if ctlErr != nil || recvErr != nil {
			// EBADF / EINVAL — socket closed by Stop().
			return
		}
		msg := ParseNotifyMessage(bodyBuf[:n])
		var fds []*os.File
		if oobn > 0 {
			if cmsgs, err := unix.ParseSocketControlMessage(oobBuf[:oobn]); err == nil {
				for _, cm := range cmsgs {
					if cm.Header.Level != unix.SOL_SOCKET || cm.Header.Type != unix.SCM_RIGHTS {
						continue
					}
					rights, err := unix.ParseUnixRights(&cm)
					if err != nil {
						continue
					}
					for _, fd := range rights {
						fds = append(fds, os.NewFile(uintptr(fd), fmt.Sprintf("fdstore-%d", fd)))
					}
				}
			}
		}
		if handler != nil {
			handler.OnNotify(msg, fds)
		}
	}
}

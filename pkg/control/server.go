package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/persist"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// listenUnixRestricted creates a Unix socket at path with mode 0600. The
// umask is tightened to 0177 around bind() so the socket file is never
// briefly created with a permissive mode (the os.Chmod that we issue
// afterwards is kept as belt-and-suspenders for filesystems where umask
// is honored differently).
func listenUnixRestricted(path string) (net.Listener, error) {
	old := syscall.Umask(0177)
	listener, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

// Server listens on a Unix domain socket and handles control connections.
type Server struct {
	services *service.ServiceSet
	listener net.Listener
	sockPath string
	logger   *logging.Logger
	conns    map[*Connection]struct{}
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup

	// acceptWg tracks only the current acceptLoop goroutine so that
	// Reopen() can wait for the old loop to exit before starting a new one.
	acceptWg sync.WaitGroup

	// stopAccept is closed to signal the current acceptLoop to exit.
	// Replaced on each Reopen() call.
	stopAccept chan struct{}

	// ShutdownFunc is called when a shutdown command is received. The
	// flags carry the operator's haste (see ShutdownFlagKill /
	// ShutdownFlagFast); zero is the ordinary graceful teardown.
	ShutdownFunc func(service.ShutdownType, uint8)

	// WallFunc is an optional hook invoked when a shutdown is scheduled
	// or cancelled. The delay argument is the time until execution
	// (0 means "immediate" or "cancelled" depending on cancelled).
	// message is the operator-supplied text (may be empty).
	// main.go wires this to shutdown.WallShutdownNoticeMsg.
	WallFunc func(st service.ShutdownType, delay time.Duration, cancelled bool, message string)

	// WallReminderFunc is called by the reminder timers at 5m/2m/1m
	// before a scheduled shutdown fires, so logged-in users see the
	// same countdown they'd expect from sysvinit/systemd shutdown.
	// remaining is the time left until deadline; message is the
	// operator-supplied text (may be empty).
	WallReminderFunc func(st service.ShutdownType, remaining time.Duration, message string)

	// WallNoticeFunc broadcasts an arbitrary wall message without
	// scheduling anything. Wired to shutdown.Wall — powers the
	// LSB-shutdown-style `-k` warning-only mode.
	WallNoticeFunc func(message string)

	// SwitchRootFunc is called when a CmdSwitchRoot request passes
	// its precheck. Returning nil means the switch committed
	// (execute never returns; the connection dies with the process);
	// returning a non-nil error means the actual switch failed after
	// the ACK went out, and the client learns via connection reset.
	// Nil hook = switch-root disabled (returns Precheck-style error
	// to the client so slinit outside initramfs doesn't accidentally
	// pretend to support it).
	SwitchRootFunc func(newroot, newinit string) error

	// SwitchRootReady, when set, is asked before a CmdSwitchRoot is
	// ACKed; a non-nil error is sent to the client as RplyBadReq and
	// the switch is not attempted.
	SwitchRootReady func() error

	// SuspendFunc writes the requested power state to
	// /sys/power/state. Blocks until wake (for "mem", "freeze",
	// "standby") or failure. Nil hook = suspend disabled (returns
	// error to client). Wired in cmd/slinit main; the actual sysfs
	// write is in pkg/shutdown so tests can substitute a mock.
	SuspendFunc func(state string) error

	// Scheduled shutdown state.
	scheduledMu        sync.Mutex
	scheduledTimer     *time.Timer
	scheduledReminders []*time.Timer
	scheduledType      service.ShutdownType
	scheduledDeadline  time.Time // zero means no scheduled shutdown
	scheduledMessage   string

	// NologinPath, when set, is created NologinLead before a scheduled
	// shutdown (at once when the shutdown is closer than that), so
	// pam_nologin refuses new logins, as systemd-logind, sysvinit's
	// shutdown and finit do. It is removed when the shutdown is
	// cancelled or replaced, and before a scheduled soft-reboot runs,
	// since /run survives one. A file that was already there is the
	// administrator's and is never removed. Empty disables it.
	NologinPath  string
	nologinTimer *time.Timer
	nologinOwned bool

	// PinStore, when Enabled(), records pin transitions to disk so a
	// `stop --pin` on a service stays effective across a reboot. Nil
	// (or a store built with an empty dir) is a valid no-op — every
	// call site invokes it unconditionally.
	Pins *persist.PinStore
}

// ErrSocketActive means another process answers on the control socket:
// an instance of slinit is already running there.
var ErrSocketActive = errors.New("control socket is already active (another instance already running?)")

// SocketActive reports whether something answers on the Unix socket at
// path. A stale file left by an instance that died refuses the
// connection and does not count.
//
// There is no atomic way to ask whether a socket file is stale, so this
// can race with an instance starting at the same moment -- dinit, which
// makes the same check, accepts the same window. What it does stop is
// the common case, and the severe one: starting a second system manager
// beside a running one.
func SocketActive(path string) bool {
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// NewServer creates a new control socket server.
func NewServer(services *service.ServiceSet, sockPath string, logger *logging.Logger) *Server {
	return &Server{
		services: services,
		sockPath: sockPath,
		logger:   logger,
		conns:    make(map[*Connection]struct{}),
	}
}

// Start binds the Unix socket and begins accepting connections.
func (s *Server) Start(ctx context.Context) error {
	// The context first: a failed bind (no room on /run) still leaves a
	// server that pass-cs-fd connections and a SIGUSR1 Reopen use, and
	// their connections read it.
	s.ctx, s.cancel = context.WithCancel(ctx)

	// Never take over a live socket. Removing it would leave the instance
	// that owns it running but unreachable: its listener stays open on a
	// file that no longer has a name, so slinitctl -- shutdown included --
	// can no longer find it.
	if SocketActive(s.sockPath) {
		return fmt.Errorf("%s: %w", s.sockPath, ErrSocketActive)
	}

	// Remove stale socket file if it exists
	if err := os.Remove(s.sockPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	listener, err := listenUnixRestricted(s.sockPath)
	if err != nil {
		return err
	}

	s.listener = listener
	s.stopAccept = make(chan struct{})

	s.wg.Add(1)
	s.acceptWg.Add(1)
	go s.acceptLoop(s.listener, s.stopAccept)

	s.logger.Info("Control socket listening on %s", s.sockPath)
	return nil
}

// Stop closes the listener and all active connections.
func (s *Server) Stop() error {
	if s.cancel != nil {
		s.cancel()
	}

	// Signal acceptLoop to stop
	s.mu.Lock()
	if s.stopAccept != nil {
		close(s.stopAccept)
		s.stopAccept = nil
	}
	s.mu.Unlock()

	var err error
	if s.listener != nil {
		err = s.listener.Close()
	}
	// Remove the socket file now, not after the connections below have
	// finished: a caller that bounds Stop (exitFlushed) gives up on a
	// connection stuck in a hook, and the file must not outlive that.
	// net's UnixListener unlinks on Close as well; this does not rely
	// on it.
	os.Remove(s.sockPath)

	// Collect connections under lock, close outside to avoid holding lock during I/O
	s.mu.Lock()
	connList := make([]*Connection, 0, len(s.conns))
	for conn := range s.conns {
		connList = append(connList, conn)
	}
	s.mu.Unlock()
	for _, conn := range connList {
		conn.close()
	}

	s.wg.Wait()

	s.logger.Info("Control socket stopped")
	return err
}

func (s *Server) acceptLoop(listener net.Listener, stopCh chan struct{}) {
	defer s.wg.Done()
	defer s.acceptWg.Done()

	var acceptDelay time.Duration
	const maxAcceptDelay = 1 * time.Second

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-s.ctx.Done():
				return
			case <-stopCh:
				return
			default:
				s.logger.Error("Control socket accept error: %v", err)
				// Backoff on transient errors (e.g. EMFILE/ENFILE) to
				// prevent a tight busy-loop that starves the system.
				if acceptDelay == 0 {
					acceptDelay = 5 * time.Millisecond
				} else {
					acceptDelay *= 2
				}
				if acceptDelay > maxAcceptDelay {
					acceptDelay = maxAcceptDelay
				}
				time.Sleep(acceptDelay)
				continue
			}
		}
		acceptDelay = 0 // reset on successful accept

		c := newConnection(s, conn)
		s.mu.Lock()
		s.conns[c] = struct{}{}
		s.mu.Unlock()

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			c.serve()
			s.mu.Lock()
			delete(s.conns, c)
			s.mu.Unlock()
		}()
	}
}

// removeConnection is called when a connection is closed.
func (s *Server) removeConnection(c *Connection) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

// Reopen closes and re-opens the control socket. This is called on
// SIGUSR1 to recover from situations where the socket was unavailable
// (e.g. filesystem was read-only during early boot).
func (s *Server) Reopen() error {
	// Signal the old acceptLoop to stop
	s.mu.Lock()
	if s.stopAccept != nil {
		close(s.stopAccept)
	}
	s.mu.Unlock()

	// Close existing listener so Accept() unblocks
	if s.listener != nil {
		s.listener.Close()
	}

	// Wait for the old acceptLoop goroutine to finish before starting a new one
	s.acceptWg.Wait()

	// Our own listener is closed, so anything still answering at the path
	// is another instance's socket. Leave it alone, as Start does.
	if SocketActive(s.sockPath) {
		return fmt.Errorf("%s: %w", s.sockPath, ErrSocketActive)
	}

	// Remove stale socket file
	os.Remove(s.sockPath)

	listener, err := listenUnixRestricted(s.sockPath)
	if err != nil {
		return err
	}

	s.listener = listener
	stopCh := make(chan struct{})
	s.mu.Lock()
	s.stopAccept = stopCh
	s.mu.Unlock()

	s.acceptWg.Add(1)
	s.wg.Add(1)
	go s.acceptLoop(listener, stopCh)

	s.logger.Info("Control socket re-opened on %s", s.sockPath)
	return nil
}

// connList returns the open connections.
func (s *Server) connList() []*Connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Connection, 0, len(s.conns))
	for c := range s.conns {
		out = append(out, c)
	}
	return out
}

// heldElsewhere reports whether a connection other than c holds a
// handle to svc.
func (s *Server) heldElsewhere(c *Connection, svc service.Service) bool {
	for _, o := range s.connList() {
		if o == c {
			continue
		}
		if _, ok := o.findHandle(svc); ok && !o.isClosing() {
			return true
		}
	}
	return false
}

// HeldByClient reports whether any control connection holds a handle
// to svc. Unloading a service a client holds leaves that client's
// handle on a detached object (see handleUnloadService), so callers
// outside this package that unload must check it too.
func (s *Server) HeldByClient(svc service.Service) bool {
	return s.heldElsewhere(nil, svc)
}

// replaceService moves every connection's handles from old to new,
// after a reload replaced old in the set.
func (s *Server) replaceService(old, new service.Service) {
	for _, c := range s.connList() {
		c.swapService(old, new)
	}
}

// HandlePassCSFD handles a pre-connected control socket (from pass-cs-fd).
// It spawns a goroutine to serve commands on the connection, just like a
// normal control client.
func (s *Server) HandlePassCSFD(conn net.Conn) {
	c := newConnection(s, conn)
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		c.serve()
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
	}()
}

// NologinLead is how long before a scheduled shutdown new logins are
// refused: five minutes, as in systemd and sysvinit.
const NologinLead = 5 * time.Minute

// ScheduleShutdown schedules a shutdown to occur after the given delay.
// If a shutdown is already scheduled, it is replaced. delay <= 0 triggers
// an immediate shutdown. message is an operator-supplied blurb appended
// to the wall broadcasts (empty is fine).
func (s *Server) ScheduleShutdown(st service.ShutdownType, delay time.Duration, message string) {
	s.scheduledMu.Lock()
	defer s.scheduledMu.Unlock()

	// Cancel existing timer(s) if any.
	s.clearScheduledLocked()

	if delay <= 0 {
		// Immediate shutdown.
		s.scheduledDeadline = time.Time{}
		if s.ShutdownFunc != nil {
			s.ShutdownFunc(st, 0)
		}
		return
	}

	s.scheduledType = st
	s.scheduledDeadline = time.Now().Add(delay)
	s.scheduledMessage = message
	s.logger.Notice("Shutdown (%s) scheduled in %v (at %s)",
		shutdownTypeName(st), delay, s.scheduledDeadline.Format("15:04:05"))

	if s.WallFunc != nil {
		s.WallFunc(st, delay, false, message)
	}
	s.armNologinLocked(st, delay, message)

	// LSB-shutdown-style reminder cadence: fire wall broadcasts at
	// 5m/2m/1m before the deadline so operators (and their users) see
	// the countdown they expect from sysvinit / systemd. Skip any
	// milestone that wouldn't fit in the remaining window.
	if s.WallReminderFunc != nil {
		for _, r := range []time.Duration{5 * time.Minute, 2 * time.Minute, 1 * time.Minute} {
			if delay > r {
				remaining := r
				stCap := st
				msgCap := message
				timer := time.AfterFunc(delay-r, func() {
					s.WallReminderFunc(stCap, remaining, msgCap)
				})
				s.scheduledReminders = append(s.scheduledReminders, timer)
			}
		}
	}

	s.scheduledTimer = time.AfterFunc(delay, func() {
		s.scheduledMu.Lock()
		s.scheduledDeadline = time.Time{}
		s.scheduledTimer = nil
		s.scheduledMessage = ""
		if st == service.ShutdownSoftReboot {
			// /run is kept across a soft-reboot; a nologin left in it
			// would refuse every login on the system that comes up.
			s.removeNologinLocked()
		}
		s.scheduledMu.Unlock()

		s.logger.Notice("Scheduled shutdown (%s) executing now", shutdownTypeName(st))
		if s.ShutdownFunc != nil {
			// A scheduled shutdown is always the graceful one: the
			// haste flags belong to an immediate request.
			s.ShutdownFunc(st, 0)
		}
	})
}

// clearScheduledLocked stops the main timer + every reminder and
// resets the reminder list. Must be called with scheduledMu held.
// The main deadline/message are cleared by the caller.
func (s *Server) clearScheduledLocked() {
	if s.scheduledTimer != nil {
		s.scheduledTimer.Stop()
		s.scheduledTimer = nil
	}
	for _, t := range s.scheduledReminders {
		t.Stop()
	}
	s.scheduledReminders = nil
	if s.nologinTimer != nil {
		s.nologinTimer.Stop()
		s.nologinTimer = nil
	}
	s.removeNologinLocked()
}

// armNologinLocked creates the nologin file now, or arms a timer for
// NologinLead before the deadline. Caller holds scheduledMu.
func (s *Server) armNologinLocked(st service.ShutdownType, delay time.Duration, message string) {
	if s.NologinPath == "" {
		return
	}
	deadline := s.scheduledDeadline
	if delay <= NologinLead {
		s.writeNologinLocked(st, deadline, message)
		return
	}
	s.nologinTimer = time.AfterFunc(delay-NologinLead, func() {
		s.scheduledMu.Lock()
		defer s.scheduledMu.Unlock()
		// A timer that fired as it was being stopped must not act for
		// a shutdown that has since been cancelled or replaced.
		if s.scheduledDeadline.Equal(deadline) && !deadline.IsZero() {
			s.writeNologinLocked(st, deadline, message)
		}
	})
}

func (s *Server) writeNologinLocked(st service.ShutdownType, deadline time.Time, message string) {
	f, err := os.OpenFile(s.NologinPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if !os.IsExist(err) {
			s.logger.Error("Cannot create %s to refuse logins: %v", s.NologinPath, err)
		}
		return
	}
	text := fmt.Sprintf("The system is going down for %s at %s.\n",
		shutdownTypeName(st), deadline.Format("Mon 2006-01-02 15:04:05 MST"))
	if message != "" {
		text += "\n" + message + "\n"
	}
	_, werr := f.WriteString(text)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		s.logger.Error("Writing %s: %v", s.NologinPath, werr)
	}
	s.nologinOwned = true
	s.logger.Notice("New logins refused until the %s (%s)", shutdownTypeName(st), s.NologinPath)
}

func (s *Server) removeNologinLocked() {
	if !s.nologinOwned {
		return
	}
	s.nologinOwned = false
	if err := os.Remove(s.NologinPath); err != nil && !os.IsNotExist(err) {
		s.logger.Error("Cannot remove %s: %v", s.NologinPath, err)
	}
}

// CancelShutdown cancels a pending scheduled shutdown.
// Returns true if a shutdown was cancelled, false if none was pending.
func (s *Server) CancelShutdown() bool {
	s.scheduledMu.Lock()
	defer s.scheduledMu.Unlock()

	if s.scheduledTimer == nil {
		return false
	}

	cancelledType := s.scheduledType
	cancelledMessage := s.scheduledMessage
	s.clearScheduledLocked()
	s.scheduledDeadline = time.Time{}
	s.scheduledMessage = ""
	s.logger.Notice("Scheduled shutdown cancelled")

	if s.WallFunc != nil {
		s.WallFunc(cancelledType, 0, true, cancelledMessage)
	}
	return true
}

// ScheduledShutdownInfo returns the pending shutdown type and time remaining.
// If no shutdown is scheduled, remaining is 0 and ok is false.
func (s *Server) ScheduledShutdownInfo() (st service.ShutdownType, remaining time.Duration, ok bool) {
	s.scheduledMu.Lock()
	defer s.scheduledMu.Unlock()

	if s.scheduledTimer == nil || s.scheduledDeadline.IsZero() {
		return 0, 0, false
	}

	rem := time.Until(s.scheduledDeadline)
	if rem < 0 {
		rem = 0
	}
	return s.scheduledType, rem, true
}

func shutdownTypeName(st service.ShutdownType) string {
	switch st {
	case service.ShutdownHalt:
		return "halt"
	case service.ShutdownPoweroff:
		return "poweroff"
	case service.ShutdownReboot:
		return "reboot"
	case service.ShutdownKexec:
		return "kexec"
	case service.ShutdownSoftReboot:
		return "softreboot"
	default:
		return "unknown"
	}
}

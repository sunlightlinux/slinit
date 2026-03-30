package control

import (
	"context"
	"net"
	"os"
	"sync"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

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

	// ShutdownFunc is called when a shutdown command is received.
	ShutdownFunc func(service.ShutdownType)
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
	// Remove stale socket file if it exists
	if err := os.Remove(s.sockPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	listener, err := net.Listen("unix", s.sockPath)
	if err != nil {
		return err
	}

	// Set socket permissions (owner only)
	if err := os.Chmod(s.sockPath, 0600); err != nil {
		listener.Close()
		return err
	}

	s.listener = listener
	s.ctx, s.cancel = context.WithCancel(ctx)
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

	// Clean up socket file
	os.Remove(s.sockPath)

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

	// Remove stale socket file
	os.Remove(s.sockPath)

	listener, err := net.Listen("unix", s.sockPath)
	if err != nil {
		return err
	}

	if err := os.Chmod(s.sockPath, 0600); err != nil {
		listener.Close()
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

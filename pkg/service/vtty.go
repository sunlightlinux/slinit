package service

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// PTY ioctl constants (Linux amd64/arm64).
const (
	ioctlTIOCGPTN   = 0x80045430 // get pts number
	ioctlTIOCSPTLCK = 0x40045431 // lock/unlock pts
)

const (
	defaultScrollback = 64 * 1024 // 64 KB ring buffer
	vttyBufSize       = 4096      // read buffer size
)

// vttyWriteDeadline bounds how long a client may take to accept data
// before it is dropped.
//
// Without it a single wedged client — one that has stopped reading, been
// SIGSTOPped, or simply filled its socket buffer — blocks the pump loop
// in Write, so the PTY master stops being drained, the service attached
// to this tty blocks on its own console output, and no further client can
// connect because the accept loop is blocked sending scrollback. One
// inattentive viewer should not be able to freeze the service it is
// watching.
//
// Five seconds to match the control socket's own write budget: both are
// "a healthy reader is never near this" numbers, and a viewer that drops
// out is recoverable by reattaching, where a stalled service is not.
// `var` so tests can shorten it.
var vttyWriteDeadline = 5 * time.Second

// writeClient sends to a client under the deadline. A timeout is returned
// as an error like any other, so the caller's existing "drop the client"
// path handles a wedged reader without a second branch.
func writeClient(conn net.Conn, data []byte) error {
	if tc, ok := conn.(interface{ SetWriteDeadline(time.Time) error }); ok {
		_ = tc.SetWriteDeadline(time.Now().Add(vttyWriteDeadline))
	}
	_, err := conn.Write(data)
	return err
}

// VirtualTTY manages a pseudo-terminal for a service, allowing
// screen-like attach/detach of client sessions.
//
// Architecture:
//   - openPTY() allocates a master/slave pair via /dev/ptmx
//   - The slave path is passed to the child as stdin/stdout/stderr
//   - A reader goroutine on the master stores output in a ring buffer
//     and forwards it to all attached clients
//   - A per-service Unix socket accepts client connections for attach
//   - Clients receive the scrollback buffer on connect, then live output
//   - Client input is forwarded to the master (→ child's stdin)
type VirtualTTY struct {
	mu sync.Mutex

	// PTY master/slave
	master    *os.File
	masterFd  int // cached fd for race-free goroutine access
	slavePath string

	// Ring buffer for scrollback
	ring      []byte
	ringSize  int
	ringStart int // index of oldest byte
	ringLen   int // number of valid bytes

	// Connected clients
	clients map[int]*vttyClient
	nextID  int

	// Unix socket for attach
	listener net.Listener
	sockPath string

	// Lifecycle
	stopCh chan struct{}
	doneCh chan struct{} // closed when reader goroutine exits
	closed bool

	serviceName string
}

type vttyClient struct {
	id   int
	conn net.Conn
	done chan struct{} // closed when input forwarder exits
}

// VTTYSocketDir returns where vtty attach sockets live: /run/slinit for
// a system instance; for a user instance $XDG_RUNTIME_DIR/slinit, or
// ~/.slinit when XDG_RUNTIME_DIR is unset. slinit creates the sockets
// there and `slinitctl attach` looks there, so both must use this.
func VTTYSocketDir(user bool) string {
	if !user {
		return "/run/slinit"
	}
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		return filepath.Join(rt, "slinit")
	}
	return filepath.Join(os.Getenv("HOME"), ".slinit")
}

// OpenVirtualTTY allocates a PTY, creates the attach socket, and starts
// the reader goroutine. Returns the slave path for the child process.
func OpenVirtualTTY(serviceName string, scrollback int, sockDir string) (*VirtualTTY, string, error) {
	if scrollback <= 0 {
		scrollback = defaultScrollback
	}

	master, slavePath, err := openPTY()
	if err != nil {
		return nil, "", fmt.Errorf("vtty: failed to open pty: %w", err)
	}

	// Create socket directory if needed
	if sockDir != "" {
		os.MkdirAll(sockDir, 0755)
	}

	sockPath := filepath.Join(sockDir, fmt.Sprintf("vtty-%s.sock", serviceName))
	// Remove stale socket
	os.Remove(sockPath)

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		master.Close()
		return nil, "", fmt.Errorf("vtty: failed to create socket %s: %w", sockPath, err)
	}
	os.Chmod(sockPath, 0660)

	vt := &VirtualTTY{
		master:      master,
		masterFd:    int(master.Fd()), // cache fd before goroutines start
		slavePath:   slavePath,
		ring:        make([]byte, scrollback),
		ringSize:    scrollback,
		clients:     make(map[int]*vttyClient),
		listener:    listener,
		sockPath:    sockPath,
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
		serviceName: serviceName,
	}

	go vt.acceptLoop()
	go vt.readLoop()

	return vt, slavePath, nil
}

// SlavePath returns the path to the slave PTY device.
func (vt *VirtualTTY) SlavePath() string {
	return vt.slavePath
}

// SocketPath returns the path to the attach Unix socket.
func (vt *VirtualTTY) SocketPath() string {
	return vt.sockPath
}

// Master returns the master file for the PTY.
func (vt *VirtualTTY) Master() *os.File {
	return vt.master
}

// Close shuts down the VirtualTTY: stops goroutines, disconnects clients,
// closes the PTY master and removes the socket file.
func (vt *VirtualTTY) Close() {
	vt.mu.Lock()
	if vt.closed {
		vt.mu.Unlock()
		return
	}
	vt.closed = true
	vt.mu.Unlock()

	close(vt.stopCh)
	vt.listener.Close()
	// Set O_NONBLOCK before closing to unblock readLoop's blocking Read
	syscall.SetNonblock(vt.masterFd, true)
	vt.master.Close()

	// Wait for reader goroutine
	<-vt.doneCh

	// Disconnect all clients
	vt.mu.Lock()
	for _, c := range vt.clients {
		c.conn.Close()
		<-c.done
	}
	vt.clients = nil
	vt.mu.Unlock()

	os.Remove(vt.sockPath)
}

// Scrollback returns the current ring buffer contents.
func (vt *VirtualTTY) Scrollback() []byte {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	return vt.ringSnapshot()
}

// ClientCount returns the number of attached clients.
func (vt *VirtualTTY) ClientCount() int {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	return len(vt.clients)
}

// --- internal ---

// openPTY allocates a pseudo-terminal pair via /dev/ptmx.
func openPTY() (master *os.File, slavePath string, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, "", err
	}

	// Get pts number
	var ptsNum uint32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(),
		uintptr(ioctlTIOCGPTN), uintptr(unsafe.Pointer(&ptsNum)))
	if errno != 0 {
		master.Close()
		return nil, "", fmt.Errorf("TIOCGPTN: %v", errno)
	}

	// Unlock pts
	var unlock int32
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, master.Fd(),
		uintptr(ioctlTIOCSPTLCK), uintptr(unsafe.Pointer(&unlock)))
	if errno != 0 {
		master.Close()
		return nil, "", fmt.Errorf("TIOCSPTLCK: %v", errno)
	}

	slavePath = "/dev/pts/" + strconv.Itoa(int(ptsNum))
	return master, slavePath, nil
}

// readResult holds data read from the PTY master.
type readResult struct {
	data []byte
	err  error
	// buf is the whole buffer data points into. The consumer returns it
	// to the free list when it has finished, which is what stops the
	// reader from overwriting bytes that are still in use.
	buf []byte
}

// vttyReadQueue is how many reads may be in flight between the PTY reader
// and the consumer.
//
// There is one buffer per queue slot plus one for the read in progress.
// The previous code queued four results through two alternating buffers,
// so a consumer that fell a single iteration behind had its bytes
// overwritten underneath it — torn console output and a corrupted
// scrollback ring, and a data race the race detector flags in ringWrite.
// Two buffers can only ever be correct with a queue of one.
const vttyReadQueue = 4

// readLoop reads from the PTY master in a separate goroutine and
// dispatches data to the ring buffer and clients. Stops when the
// master fd is closed (read returns error) or stopCh is signaled.
func (vt *VirtualTTY) readLoop() {
	defer close(vt.doneCh)

	dataCh := make(chan readResult, vttyReadQueue)

	// Buffers are owned rather than shared: the reader takes one off free,
	// fills it, and only gets it back once the consumer has finished with
	// it. Still no allocation per read — the same buffers recycle — but a
	// buffer in flight can no longer be written to. When every buffer is
	// out the reader blocks, which is ordinary back-pressure: the PTY
	// stops being drained and the service blocks on its console exactly
	// as it would against a terminal nobody is reading. Bounded, because
	// the consumer drops a client that exceeds vttyWriteDeadline.
	free := make(chan []byte, vttyReadQueue+1)
	for i := 0; i < vttyReadQueue+1; i++ {
		free <- make([]byte, vttyBufSize)
	}

	// Use cached fd to avoid race with Close() on the os.File
	fd := vt.masterFd

	// Background reader goroutine — does blocking reads on the PTY master fd.
	// Exits when the fd is closed (returns EIO or EBADF).
	go func() {
		defer close(dataCh)
		for {
			var buf []byte
			select {
			case buf = <-free:
			case <-vt.stopCh:
				return
			}
			n, err := syscall.Read(fd, buf)
			if n > 0 {
				dataCh <- readResult{data: buf[:n], buf: buf}
			} else {
				free <- buf
			}
			if err != nil {
				if err == syscall.EINTR || err == syscall.EAGAIN {
					continue
				}
				dataCh <- readResult{err: err}
				return
			}
		}
	}()

	// Reusable client snapshot slice — avoids allocation per read
	var clientsBuf []*vttyClient

	for {
		select {
		case <-vt.stopCh:
			return
		case res, ok := <-dataCh:
			if !ok || res.err != nil {
				return
			}

			vt.mu.Lock()
			vt.ringWrite(res.data)
			// Reuse snapshot slice
			clientsBuf = clientsBuf[:0]
			for _, c := range vt.clients {
				clientsBuf = append(clientsBuf, c)
			}
			vt.mu.Unlock()

			for _, c := range clientsBuf {
				if werr := writeClient(c.conn, res.data); werr != nil {
					vt.removeClient(c.id)
				}
			}

			// Done with the bytes: hand the buffer back. Non-blocking
			// because free is sized for every buffer that exists, so this
			// can never be the thing that wedges the loop.
			if res.buf != nil {
				select {
				case free <- res.buf:
				default:
				}
			}
		}
	}
}

// acceptLoop accepts client connections on the Unix socket.
func (vt *VirtualTTY) acceptLoop() {
	for {
		conn, err := vt.listener.Accept()
		if err != nil {
			select {
			case <-vt.stopCh:
				return
			default:
				continue
			}
		}
		vt.addClient(conn)
	}
}

// addClient registers a new client, sends scrollback, and starts input forwarding.
func (vt *VirtualTTY) addClient(conn net.Conn) {
	vt.mu.Lock()
	if vt.closed {
		vt.mu.Unlock()
		conn.Close()
		return
	}

	id := vt.nextID
	vt.nextID++
	c := &vttyClient{
		id:   id,
		conn: conn,
		done: make(chan struct{}),
	}
	vt.clients[id] = c

	// Send scrollback buffer
	scrollback := vt.ringSnapshot()
	vt.mu.Unlock()

	// Deadline here too: this runs on the accept loop, so a client that
	// connects and never reads would otherwise stop every later client
	// from being accepted at all.
	if len(scrollback) > 0 {
		if err := writeClient(conn, scrollback); err != nil {
			vt.removeClient(id)
			return
		}
	}

	// Start input forwarder: client → master
	go vt.forwardInput(c)
}

// removeClient disconnects and removes a client.
func (vt *VirtualTTY) removeClient(id int) {
	vt.mu.Lock()
	c, ok := vt.clients[id]
	if !ok {
		vt.mu.Unlock()
		return
	}
	delete(vt.clients, id)
	vt.mu.Unlock()

	c.conn.Close()
	<-c.done
}

// forwardInput reads from client and writes to PTY master.
func (vt *VirtualTTY) forwardInput(c *vttyClient) {
	defer close(c.done)
	io.Copy(vt.master, c.conn)
}

// ringWrite appends data to the ring buffer, overwriting oldest data if full.
// Uses vectorized copy (1-2 memcpy calls) instead of byte-by-byte iteration.
func (vt *VirtualTTY) ringWrite(data []byte) {
	dLen := len(data)
	if dLen == 0 {
		return
	}

	// If data is larger than ring, only keep the tail
	if dLen >= vt.ringSize {
		data = data[dLen-vt.ringSize:]
		dLen = vt.ringSize
		copy(vt.ring, data)
		vt.ringStart = 0
		vt.ringLen = vt.ringSize
		return
	}

	// Write position
	writePos := (vt.ringStart + vt.ringLen) % vt.ringSize
	if vt.ringLen >= vt.ringSize {
		// Buffer is full — we'll overwrite from ringStart
		writePos = vt.ringStart
	}

	// Copy in up to 2 segments (wrap-around)
	firstChunk := vt.ringSize - writePos
	if firstChunk >= dLen {
		copy(vt.ring[writePos:], data)
	} else {
		copy(vt.ring[writePos:], data[:firstChunk])
		copy(vt.ring, data[firstChunk:])
	}

	// Update ring pointers
	if vt.ringLen+dLen <= vt.ringSize {
		// No overflow
		vt.ringLen += dLen
	} else {
		// Overflow — advance start past overwritten data
		overflow := (vt.ringLen + dLen) - vt.ringSize
		vt.ringStart = (vt.ringStart + overflow) % vt.ringSize
		vt.ringLen = vt.ringSize
	}
}

// ringSnapshot returns a copy of the current ring buffer contents.
func (vt *VirtualTTY) ringSnapshot() []byte {
	if vt.ringLen == 0 {
		return nil
	}
	out := make([]byte, vt.ringLen)
	start := vt.ringStart
	if start+vt.ringLen <= vt.ringSize {
		copy(out, vt.ring[start:start+vt.ringLen])
	} else {
		first := vt.ringSize - start
		copy(out, vt.ring[start:])
		copy(out[first:], vt.ring[:vt.ringLen-first])
	}
	return out
}

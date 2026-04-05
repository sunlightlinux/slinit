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
	syscall.SetNonblock(int(vt.master.Fd()), true)
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
}

// readLoop reads from the PTY master in a separate goroutine and
// dispatches data to the ring buffer and clients. Stops when the
// master fd is closed (read returns error) or stopCh is signaled.
func (vt *VirtualTTY) readLoop() {
	defer close(vt.doneCh)

	dataCh := make(chan readResult, 4)

	// Background reader goroutine — does blocking reads on the PTY master fd.
	// Exits when the fd is closed (returns EIO or EBADF).
	go func() {
		defer close(dataCh)
		fd := int(vt.master.Fd())
		buf := make([]byte, vttyBufSize)
		for {
			n, err := syscall.Read(fd, buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				dataCh <- readResult{data: data}
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
			clients := make([]*vttyClient, 0, len(vt.clients))
			for _, c := range vt.clients {
				clients = append(clients, c)
			}
			vt.mu.Unlock()

			for _, c := range clients {
				_, werr := c.conn.Write(res.data)
				if werr != nil {
					vt.removeClient(c.id)
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

	if len(scrollback) > 0 {
		conn.Write(scrollback)
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
func (vt *VirtualTTY) ringWrite(data []byte) {
	for _, b := range data {
		idx := (vt.ringStart + vt.ringLen) % vt.ringSize
		vt.ring[idx] = b
		if vt.ringLen < vt.ringSize {
			vt.ringLen++
		} else {
			vt.ringStart = (vt.ringStart + 1) % vt.ringSize
		}
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

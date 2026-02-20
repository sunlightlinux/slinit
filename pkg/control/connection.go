package control

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"syscall"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// Connection represents a single control client connection.
type Connection struct {
	server     *Server
	conn       net.Conn
	handles    map[uint32]service.Service
	nextHandle uint32
}

func newConnection(server *Server, conn net.Conn) *Connection {
	return &Connection{
		server:     server,
		conn:       conn,
		handles:    make(map[uint32]service.Service),
		nextHandle: 1,
	}
}

func (c *Connection) close() {
	c.conn.Close()
}

func (c *Connection) allocHandle(svc service.Service) uint32 {
	// Check if this service already has a handle
	for h, s := range c.handles {
		if s == svc {
			return h
		}
	}
	h := c.nextHandle
	c.nextHandle++
	c.handles[h] = svc
	return h
}

func (c *Connection) getService(handle uint32) service.Service {
	return c.handles[handle]
}

func (c *Connection) serve() {
	defer c.close()

	for {
		select {
		case <-c.server.ctx.Done():
			return
		default:
		}

		cmd, payload, err := ReadPacket(c.conn)
		if err != nil {
			if err != io.EOF {
				c.server.logger.Debug("Control connection read error: %v", err)
			}
			return
		}

		if err := c.dispatch(cmd, payload); err != nil {
			c.server.logger.Debug("Control command dispatch error: %v", err)
			return
		}
	}
}

func (c *Connection) dispatch(cmd uint8, payload []byte) error {
	switch cmd {
	case CmdQueryVersion:
		return c.handleQueryVersion()
	case CmdFindService:
		return c.handleFindService(payload)
	case CmdLoadService:
		return c.handleLoadService(payload)
	case CmdStartService:
		return c.handleStartService(payload)
	case CmdStopService:
		return c.handleStopService(payload)
	case CmdListServices:
		return c.handleListServices()
	case CmdServiceStatus:
		return c.handleServiceStatus(payload)
	case CmdShutdown:
		return c.handleShutdown(payload)
	case CmdCloseHandle:
		return c.handleCloseHandle(payload)
	case CmdSetTrigger:
		return c.handleSetTrigger(payload)
	case CmdSignal:
		return c.handleSignal(payload)
	case CmdUnpinService:
		return c.handleUnpinService(payload)
	default:
		return WritePacket(c.conn, RplyBadReq, nil)
	}
}

// --- Command handlers ---

func (c *Connection) handleQueryVersion() error {
	payload := make([]byte, 2)
	binary.LittleEndian.PutUint16(payload, ProtocolVersion)
	return WritePacket(c.conn, RplyCPVersion, payload)
}

func (c *Connection) handleFindService(payload []byte) error {
	name, _, err := DecodeServiceName(payload)
	if err != nil {
		return WritePacket(c.conn, RplyBadReq, nil)
	}

	svc := c.server.services.FindService(name, false)
	if svc == nil {
		return WritePacket(c.conn, RplyNoService, nil)
	}

	handle := c.allocHandle(svc)
	reply := make([]byte, 6)
	reply[0] = uint8(svc.State())
	binary.LittleEndian.PutUint32(reply[1:], handle)
	reply[5] = uint8(svc.TargetState())
	return WritePacket(c.conn, RplyServiceRecord, reply)
}

func (c *Connection) handleLoadService(payload []byte) error {
	name, _, err := DecodeServiceName(payload)
	if err != nil {
		return WritePacket(c.conn, RplyBadReq, nil)
	}

	svc, err := c.server.services.LoadService(name)
	if err != nil {
		return WritePacket(c.conn, RplyNoService, nil)
	}

	handle := c.allocHandle(svc)
	reply := make([]byte, 6)
	reply[0] = uint8(svc.State())
	binary.LittleEndian.PutUint32(reply[1:], handle)
	reply[5] = uint8(svc.TargetState())
	return WritePacket(c.conn, RplyServiceRecord, reply)
}

func (c *Connection) handleStartService(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return WritePacket(c.conn, RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return WritePacket(c.conn, RplyBadReq, nil)
	}

	if c.server.services.IsShuttingDown() {
		return WritePacket(c.conn, RplyShuttingDown, nil)
	}

	if svc.State() == service.StateStarted {
		return WritePacket(c.conn, RplyAlreadySS, nil)
	}

	c.server.services.StartService(svc)
	return WritePacket(c.conn, RplyACK, nil)
}

func (c *Connection) handleStopService(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return WritePacket(c.conn, RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return WritePacket(c.conn, RplyBadReq, nil)
	}

	if svc.State() == service.StateStopped {
		return WritePacket(c.conn, RplyAlreadySS, nil)
	}

	c.server.services.StopService(svc)
	return WritePacket(c.conn, RplyACK, nil)
}

func (c *Connection) handleListServices() error {
	services := c.server.services.ListServices()
	for _, svc := range services {
		info := EncodeSvcInfo(svc)
		if err := WritePacket(c.conn, RplySvcInfo, info); err != nil {
			return err
		}
	}
	return WritePacket(c.conn, RplyListDone, nil)
}

func (c *Connection) handleServiceStatus(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return WritePacket(c.conn, RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return WritePacket(c.conn, RplyBadReq, nil)
	}

	status := EncodeServiceStatus(svc)
	return WritePacket(c.conn, RplyServiceStatus, status)
}

func (c *Connection) handleShutdown(payload []byte) error {
	if len(payload) < 1 {
		return WritePacket(c.conn, RplyBadReq, nil)
	}

	shutType := service.ShutdownType(payload[0])
	if c.server.ShutdownFunc != nil {
		c.server.ShutdownFunc(shutType)
	}
	return WritePacket(c.conn, RplyACK, nil)
}

func (c *Connection) handleCloseHandle(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return WritePacket(c.conn, RplyBadReq, nil)
	}

	delete(c.handles, handle)
	return WritePacket(c.conn, RplyACK, nil)
}

func (c *Connection) handleSetTrigger(payload []byte) error {
	// Format: handle(4) + triggerValue(1)
	if len(payload) < 5 {
		return WritePacket(c.conn, RplyBadReq, nil)
	}

	handle := binary.LittleEndian.Uint32(payload)
	triggerVal := payload[4] != 0

	svc := c.getService(handle)
	if svc == nil {
		return WritePacket(c.conn, RplyBadReq, nil)
	}

	// Check if it's a triggered service
	triggered, ok := svc.(*service.TriggeredService)
	if !ok {
		return WritePacket(c.conn, RplyNAK, nil)
	}

	triggered.SetTrigger(triggerVal)
	c.server.services.ProcessQueues()
	return WritePacket(c.conn, RplyACK, nil)
}

func (c *Connection) handleSignal(payload []byte) error {
	// Format: handle(4) + signal(4)
	if len(payload) < 8 {
		return WritePacket(c.conn, RplyBadReq, nil)
	}

	handle := binary.LittleEndian.Uint32(payload)
	sigNum := int(binary.LittleEndian.Uint32(payload[4:]))

	svc := c.getService(handle)
	if svc == nil {
		return WritePacket(c.conn, RplyBadReq, nil)
	}

	pid := svc.PID()
	if pid <= 0 {
		return WritePacket(c.conn, RplySignalNoPID, nil)
	}

	sig := syscall.Signal(sigNum)
	if err := syscall.Kill(pid, sig); err != nil {
		return WritePacket(c.conn, RplySignalErr, []byte(fmt.Sprintf("%v", err)))
	}
	return WritePacket(c.conn, RplyACK, nil)
}

func (c *Connection) handleUnpinService(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return WritePacket(c.conn, RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return WritePacket(c.conn, RplyBadReq, nil)
	}

	svc.Unpin()
	c.server.services.ProcessQueues()
	return WritePacket(c.conn, RplyACK, nil)
}

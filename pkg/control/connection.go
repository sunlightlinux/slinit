package control

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// Connection represents a single control client connection.
// It implements service.ServiceListener and service.EnvListener to receive
// push notifications about service state changes and environment changes.
type Connection struct {
	server     *Server
	conn       net.Conn
	handles    map[uint32]service.Service
	nextHandle uint32
	listenEnv  bool // true if client subscribed to env events
	writeMu    sync.Mutex // serializes all writes to conn
	closeOnce  sync.Once
	closed     bool
}

func newConnection(server *Server, conn net.Conn) *Connection {
	return &Connection{
		server:     server,
		conn:       conn,
		handles:    make(map[uint32]service.Service),
		nextHandle: 1,
	}
}

// writePacket writes a packet to the connection, protected by writeMu.
func (c *Connection) writePacket(pktType uint8, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return fmt.Errorf("connection closed")
	}
	return WritePacket(c.conn, pktType, payload)
}

func (c *Connection) close() {
	c.closeOnce.Do(func() {
		c.writeMu.Lock()
		c.closed = true
		c.writeMu.Unlock()
		// Unregister as listener from all services
		for _, svc := range c.handles {
			svc.Record().RemoveListener(c)
		}
		// Unregister env listener
		if c.listenEnv {
			c.server.services.RemoveEnvListener(c)
		}
		c.conn.Close()
	})
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
	// Auto-subscribe as listener for service events
	svc.Record().AddListener(c)
	return h
}

func (c *Connection) getService(handle uint32) service.Service {
	return c.handles[handle]
}

// findHandle returns the handle for a given service, or 0 if not found.
func (c *Connection) findHandle(svc service.Service) uint32 {
	for h, s := range c.handles {
		if s == svc {
			return h
		}
	}
	return 0
}

// ServiceEvent implements service.ServiceListener.
// Called from service state machine goroutines when state changes occur.
func (c *Connection) ServiceEvent(svc service.Service, event service.ServiceEvent) {
	handle := c.findHandle(svc)
	if handle == 0 {
		return
	}
	payload := EncodeServiceEvent(handle, uint8(event), svc)
	c.writePacket(InfoServiceEvent, payload) //nolint: errcheck
}

// EnvEvent implements service.EnvListener.
// Called when the global environment changes.
func (c *Connection) EnvEvent(varString string, override bool) {
	payload := EncodeEnvEvent(varString, override)
	c.writePacket(InfoEnvEvent, payload) //nolint: errcheck
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
	case CmdWakeService:
		return c.handleWakeService(payload)
	case CmdStopService:
		return c.handleStopService(payload)
	case CmdReleaseService:
		return c.handleReleaseService(payload)
	case CmdListServices:
		return c.handleListServices()
	case CmdBootTime:
		return c.handleBootTime()
	case CmdCatLog:
		return c.handleCatLog(payload)
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
	case CmdReloadService:
		return c.handleReloadService(payload)
	case CmdUnloadService:
		return c.handleUnloadService(payload)
	case CmdSetEnv:
		return c.handleSetEnv(payload)
	case CmdGetAllEnv:
		return c.handleGetAllEnv(payload)
	case CmdAddDep:
		return c.handleAddDep(payload)
	case CmdRmDep:
		return c.handleRmDep(payload)
	case CmdEnableService:
		return c.handleEnableService(payload)
	case CmdDisableService:
		return c.handleDisableService(payload)
	case CmdQueryServiceName:
		return c.handleQueryServiceName(payload)
	case CmdQueryServiceDscDir:
		return c.handleQueryServiceDscDir()
	case CmdListenEnv:
		return c.handleListenEnv()
	default:
		return c.writePacket(RplyBadReq, nil)
	}
}

// --- Command handlers ---

func (c *Connection) handleQueryVersion() error {
	payload := make([]byte, 4)
	binary.LittleEndian.PutUint16(payload[0:], MinCompatVersion)
	binary.LittleEndian.PutUint16(payload[2:], CPVersion)
	return c.writePacket(RplyCPVersion, payload)
}

func (c *Connection) handleFindService(payload []byte) error {
	name, _, err := DecodeServiceName(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.server.services.FindService(name, false)
	if svc == nil {
		return c.writePacket(RplyNoService, nil)
	}

	handle := c.allocHandle(svc)
	reply := make([]byte, 6)
	reply[0] = uint8(svc.State())
	binary.LittleEndian.PutUint32(reply[1:], handle)
	reply[5] = uint8(svc.TargetState())
	return c.writePacket(RplyServiceRecord, reply)
}

func (c *Connection) handleLoadService(payload []byte) error {
	name, _, err := DecodeServiceName(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc, err := c.server.services.LoadService(name)
	if err != nil {
		return c.writePacket(RplyNoService, nil)
	}

	handle := c.allocHandle(svc)
	reply := make([]byte, 6)
	reply[0] = uint8(svc.State())
	binary.LittleEndian.PutUint32(reply[1:], handle)
	reply[5] = uint8(svc.TargetState())
	return c.writePacket(RplyServiceRecord, reply)
}

func (c *Connection) handleStartService(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	// Optional flags byte after handle
	var flags uint8
	if len(payload) >= 5 {
		flags = payload[4]
	}
	pin := flags&0x01 != 0

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	if c.server.services.IsShuttingDown() {
		return c.writePacket(RplyShuttingDown, nil)
	}

	if svc.State() == service.StateStarted {
		return c.writePacket(RplyAlreadySS, nil)
	}

	c.server.services.StartService(svc)
	if pin {
		svc.PinStart()
	}
	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handleWakeService(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	if c.server.services.IsShuttingDown() {
		return c.writePacket(RplyShuttingDown, nil)
	}

	if svc.State() == service.StateStarted {
		return c.writePacket(RplyAlreadySS, nil)
	}

	if svc.Record().IsStopPinned() {
		return c.writePacket(RplyNAK, nil)
	}

	if !c.server.services.WakeService(svc) {
		return c.writePacket(RplyNAK, nil)
	}
	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handleStopService(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	// Optional flags byte after handle
	var flags uint8
	if len(payload) >= 5 {
		flags = payload[4]
	}
	pin := flags&0x01 != 0
	force := flags&0x02 != 0

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	if svc.State() == service.StateStopped {
		return c.writePacket(RplyAlreadySS, nil)
	}

	if force {
		c.server.services.ForceStopService(svc)
	} else {
		c.server.services.StopService(svc)
	}
	if pin {
		svc.PinStop()
	}
	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handleReleaseService(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	if svc.State() == service.StateStopped {
		return c.writePacket(RplyAlreadySS, nil)
	}

	svc.Stop(false) // release: remove explicit activation, stop only if unrequired
	c.server.services.ProcessQueues()
	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handleListServices() error {
	services := c.server.services.ListServices()
	for _, svc := range services {
		info := EncodeSvcInfo(svc)
		if err := c.writePacket(RplySvcInfo, info); err != nil {
			return err
		}
	}
	return c.writePacket(RplyListDone, nil)
}

func (c *Connection) handleServiceStatus(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	status := EncodeServiceStatus(svc)
	return c.writePacket(RplyServiceStatus, status)
}

func (c *Connection) handleShutdown(payload []byte) error {
	if len(payload) < 1 {
		return c.writePacket(RplyBadReq, nil)
	}

	shutType := service.ShutdownType(payload[0])
	if c.server.ShutdownFunc != nil {
		c.server.ShutdownFunc(shutType)
	}
	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handleCloseHandle(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	// Unregister as listener before removing handle
	if svc := c.handles[handle]; svc != nil {
		svc.Record().RemoveListener(c)
	}
	delete(c.handles, handle)
	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handleSetTrigger(payload []byte) error {
	// Format: handle(4) + triggerValue(1)
	if len(payload) < 5 {
		return c.writePacket(RplyBadReq, nil)
	}

	handle := binary.LittleEndian.Uint32(payload)
	triggerVal := payload[4] != 0

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	// Check if it's a triggered service
	triggered, ok := svc.(*service.TriggeredService)
	if !ok {
		return c.writePacket(RplyNAK, nil)
	}

	triggered.SetTrigger(triggerVal)
	c.server.services.ProcessQueues()
	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handleSignal(payload []byte) error {
	// Format: handle(4) + signal(4)
	if len(payload) < 8 {
		return c.writePacket(RplyBadReq, nil)
	}

	handle := binary.LittleEndian.Uint32(payload)
	sigNum := int(binary.LittleEndian.Uint32(payload[4:]))

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	pid := svc.PID()
	if pid <= 0 {
		return c.writePacket(RplySignalNoPID, nil)
	}

	sig := syscall.Signal(sigNum)
	if err := syscall.Kill(pid, sig); err != nil {
		return c.writePacket(RplySignalErr, []byte(fmt.Sprintf("%v", err)))
	}
	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handleUnpinService(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc.Unpin()
	c.server.services.ProcessQueues()
	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handleBootTime() error {
	ss := c.server.services

	info := BootTimeInfo{
		KernelUptimeNs: int64(ss.KernelUptime()),
		BootSvcName:    ss.BootServiceName(),
	}
	if !ss.BootStartTime().IsZero() {
		info.BootStartNs = ss.BootStartTime().UnixNano()
	}
	if !ss.BootReadyTime().IsZero() {
		info.BootReadyNs = ss.BootReadyTime().UnixNano()
	}

	for _, svc := range ss.ListServices() {
		entry := BootTimeEntry{
			Name:    svc.Name(),
			State:   svc.State(),
			SvcType: svc.Type(),
			PID:     int32(svc.PID()),
		}
		dur := svc.Record().StartupDuration()
		if dur > 0 {
			entry.StartupNs = int64(dur)
		}
		info.Services = append(info.Services, entry)
	}

	payload := EncodeBootTime(info)
	return c.writePacket(RplyBootTime, payload)
}

func (c *Connection) handleCatLog(payload []byte) error {
	flags, handle, err := DecodeCatLogRequest(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	if svc.GetLogType() != service.LogToBuffer {
		return c.writePacket(RplyNAK, nil)
	}

	logBuf := svc.GetLogBuffer()
	if logBuf == nil {
		return c.writePacket(RplyNAK, nil)
	}

	var data []byte
	if flags&CatLogFlagClear != 0 {
		data = logBuf.GetBufferAndClear()
	} else {
		data = logBuf.GetBuffer()
	}
	if data == nil {
		data = []byte{}
	}

	reply := EncodeSvcLog(data)
	return c.writePacket(RplySvcLog, reply)
}

func (c *Connection) handleReloadService(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	// Refuse if service is in a transitional state
	state := svc.State()
	if state != service.StateStopped && state != service.StateStarted {
		return c.writePacket(RplyNAK, nil)
	}

	loader := c.server.services.GetLoader()
	if loader == nil {
		return c.writePacket(RplyNAK, nil)
	}

	newSvc, err := loader.ReloadService(svc)
	if err != nil {
		return c.writePacket(RplyNAK, nil)
	}

	// If service was replaced (type change), update handle mapping
	if newSvc != svc {
		c.handles[handle] = newSvc
	}

	c.server.services.ProcessQueues()
	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handleUnloadService(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	// Service must be stopped
	if svc.State() != service.StateStopped {
		return c.writePacket(RplyNotStopped, nil)
	}

	// Count how many handles in this connection point to the service
	handleCount := 0
	for _, s := range c.handles {
		if s == svc {
			handleCount++
		}
	}

	// Check if service has only ordering dependents (no active non-ordering refs)
	if !svc.Record().HasLoneRef(handleCount) {
		return c.writePacket(RplyNAK, nil)
	}

	// Unload: clean up deps and remove from set
	c.server.services.UnloadService(svc)

	// Remove all handles pointing to this service
	for h, s := range c.handles {
		if s == svc {
			delete(c.handles, h)
		}
	}

	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handleSetEnv(payload []byte) error {
	handle, key, value, isUnset, err := DecodeSetEnv(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	if isUnset {
		svc.Record().UnsetEnvVar(key)
	} else {
		svc.Record().SetEnvVar(key, value)
	}
	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handleGetAllEnv(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	env := svc.Record().GetAllEnv()
	reply := EncodeEnvList(env)
	return c.writePacket(RplyEnvList, reply)
}

func (c *Connection) handleAddDep(payload []byte) error {
	handleFrom, handleTo, depType, err := DecodeDepRequest(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	from := c.getService(handleFrom)
	to := c.getService(handleTo)
	if from == nil || to == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	if depType > 5 {
		return c.writePacket(RplyBadReq, nil)
	}

	from.Record().AddDep(to, service.DependencyType(depType))
	c.server.services.ProcessQueues()
	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handleRmDep(payload []byte) error {
	handleFrom, handleTo, depType, err := DecodeDepRequest(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	from := c.getService(handleFrom)
	to := c.getService(handleTo)
	if from == nil || to == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	if depType > 5 {
		return c.writePacket(RplyBadReq, nil)
	}

	if !from.Record().RmDep(to, service.DependencyType(depType)) {
		return c.writePacket(RplyNAK, nil)
	}
	c.server.services.ProcessQueues()
	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handleEnableService(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	if c.server.services.IsShuttingDown() {
		return c.writePacket(RplyShuttingDown, nil)
	}

	// Determine "from" service: explicit handle or boot service
	var fromSvc service.Service
	if len(payload) >= 8 {
		fromHandle := binary.LittleEndian.Uint32(payload[4:])
		fromSvc = c.getService(fromHandle)
	}
	if fromSvc == nil {
		bootName := c.server.services.BootServiceName()
		if bootName == "" {
			return c.writePacket(RplyNAK, nil)
		}
		var loadErr error
		fromSvc, loadErr = c.server.services.LoadService(bootName)
		if loadErr != nil || fromSvc == nil {
			return c.writePacket(RplyNAK, nil)
		}
	}

	// Add waits-for dependency from source to target
	fromSvc.Record().AddDep(svc, service.DepWaitsFor)

	// Start the target service
	c.server.services.StartService(svc)
	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handleDisableService(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	// Determine "from" service: explicit handle or boot service
	var fromSvc service.Service
	if len(payload) >= 8 {
		fromHandle := binary.LittleEndian.Uint32(payload[4:])
		fromSvc = c.getService(fromHandle)
	}
	if fromSvc == nil {
		bootName := c.server.services.BootServiceName()
		if bootName == "" {
			return c.writePacket(RplyNAK, nil)
		}
		fromSvc = c.server.services.FindService(bootName, false)
		if fromSvc == nil {
			return c.writePacket(RplyNAK, nil)
		}
	}

	// Remove waits-for dependency from source to target
	fromSvc.Record().RmDep(svc, service.DepWaitsFor)

	// Stop the target service
	c.server.services.StopService(svc)
	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handleQueryServiceName(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	return c.writePacket(RplyServiceName, EncodeServiceName(svc.Name()))
}

func (c *Connection) handleQueryServiceDscDir() error {
	loader := c.server.services.GetLoader()
	if loader == nil {
		// No loader configured, return empty list
		reply := make([]byte, 2)
		return c.writePacket(RplyServiceDscDir, reply)
	}

	dirs := loader.ServiceDirs()
	// Wire format: count(2) + [dirLen(2) + dir(N)]*
	size := 2
	for _, d := range dirs {
		size += 2 + len(d)
	}
	buf := make([]byte, size)
	binary.LittleEndian.PutUint16(buf, uint16(len(dirs)))
	off := 2
	for _, d := range dirs {
		binary.LittleEndian.PutUint16(buf[off:], uint16(len(d)))
		copy(buf[off+2:], d)
		off += 2 + len(d)
	}
	return c.writePacket(RplyServiceDscDir, buf)
}

func (c *Connection) handleListenEnv() error {
	if !c.listenEnv {
		c.listenEnv = true
		c.server.services.AddEnvListener(c)
	}
	return c.writePacket(RplyACK, nil)
}

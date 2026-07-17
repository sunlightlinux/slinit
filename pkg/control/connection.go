package control

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/config"
	"github.com/sunlightlinux/slinit/pkg/persist"
	"github.com/sunlightlinux/slinit/pkg/service"
)

var errConnClosed = errors.New("connection closed")

// replyPool provides reusable byte buffers for small reply packets.
// Most control replies are 4-16 bytes; cap=64 covers all common cases.
var replyPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 0, 64)
		return &b
	},
}

// getReplyBuf returns a pooled buffer reset to the requested length.
func getReplyBuf(n int) []byte {
	bp := replyPool.Get().(*[]byte)
	b := *bp
	if cap(b) >= n {
		return b[:n]
	}
	// Rare: requested size exceeds pool cap, allocate fresh
	return make([]byte, n)
}

// putReplyBuf returns a buffer to the pool if it fits.
func putReplyBuf(b []byte) {
	if cap(b) <= 64 {
		replyPool.Put(&b)
	}
}

// Connection represents a single control client connection.
// It implements service.ServiceListener and service.EnvListener to receive
// push notifications about service state changes and environment changes.
type Connection struct {
	server     *Server
	conn       net.Conn
	handles    map[uint32]service.Service
	revHandles map[service.Service]uint32 // reverse map for O(1) service→handle lookup
	nextHandle uint32
	listenEnv  bool       // true if client subscribed to env events
	writeMu    sync.Mutex // serializes all writes to conn
	closeOnce  sync.Once
	closed     bool

	// peerAuthorized is set at construction time from SO_PEERCRED.
	// True iff the connecting client has UID 0 (root) or matches the
	// daemon's own UID (the typical case for --user mode where the
	// socket lives under the user's runtime dir).
	// The 0600 socket mode already restricts access at the FS layer;
	// this is defense-in-depth against perm/race mistakes and against
	// fds passed in by less trustworthy parents.
	peerAuthorized bool
}

func newConnection(server *Server, conn net.Conn) *Connection {
	c := &Connection{
		server:     server,
		conn:       conn,
		handles:    make(map[uint32]service.Service, 8),
		revHandles: make(map[service.Service]uint32, 8),
		nextHandle: 1,
	}
	if uid, ok := peerUID(conn); ok {
		ownUID := uint32(os.Getuid())
		c.peerAuthorized = (uid == 0 || uid == ownUID)
	}
	// If peerUID failed (non-Unix conn / kernel didn't return creds),
	// peerAuthorized stays false → all commands rejected. This is the
	// safe default; the only legitimate non-Unix path is unit tests
	// (net.Pipe) which exercise dispatch directly without going through
	// this constructor.
	return c
}

// writePacket writes a packet to the connection, protected by writeMu.
func (c *Connection) writePacket(pktType uint8, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return errConnClosed
	}
	return WritePacket(c.conn, pktType, payload)
}

func (c *Connection) close() {
	c.closeOnce.Do(func() {
		c.writeMu.Lock()
		c.closed = true
		c.writeMu.Unlock()
		// Unregister as listener from all unique services using revHandles
		// (revHandles has one entry per unique service, no dedup needed)
		for svc := range c.revHandles {
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
	// O(1) check if this service already has a handle
	if h, ok := c.revHandles[svc]; ok {
		return h
	}
	h := c.nextHandle
	c.nextHandle++
	c.handles[h] = svc
	c.revHandles[svc] = h
	// Auto-subscribe as listener for service events
	svc.Record().AddListener(c)
	return h
}

func (c *Connection) getService(handle uint32) service.Service {
	return c.handles[handle]
}

// findHandle returns the handle for a given service, or 0 and false if not found.
func (c *Connection) findHandle(svc service.Service) (uint32, bool) {
	h, ok := c.revHandles[svc]
	return h, ok
}

// ServiceEvent implements service.ServiceListener.
// Called from service state machine goroutines when state changes occur.
func (c *Connection) ServiceEvent(svc service.Service, event service.ServiceEvent) {
	handle, ok := c.findHandle(svc)
	if !ok {
		return
	}
	// Send v5 event first, then v4 for backwards compatibility
	payload5 := EncodeServiceEvent5(handle, uint8(event), svc)
	c.writePacket(InfoServiceEvent5, payload5) //nolint: errcheck
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

		// Set a read deadline so we periodically re-check ctx.Done
		// instead of blocking indefinitely on a dead connection.
		if tc, ok := c.conn.(interface{ SetReadDeadline(time.Time) error }); ok {
			tc.SetReadDeadline(time.Now().Add(30 * time.Second))
		}

		cmd, payload, err := ReadPacket(c.conn)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue // deadline expired, loop back to check ctx
			}
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
	// Defense-in-depth: even though the control socket is mode 0600, an
	// unauthorized peer (different UID, kernel without SO_PEERCRED, or a
	// non-Unix fd passed via --use-passed-cfd from a less trusted parent)
	// must not be able to issue commands. The socket file mode is the
	// primary boundary; this check exists so a perm/race mistake doesn't
	// hand a non-root user the ability to shut down the system.
	if !c.peerAuthorized {
		return c.writePacket(RplyBadReq, nil)
	}
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
	case CmdReloadAll:
		return c.handleReloadAll()
	case CmdReloadSignal:
		return c.handleReloadSignal(payload)
	case CmdUnloadService:
		return c.handleUnloadService(payload)
	case CmdSetEnv:
		return c.handleSetEnv(payload)
	case CmdGetAllEnv:
		return c.handleGetAllEnv(payload)
	case CmdResetEnv:
		return c.handleResetEnv(payload)
	case CmdAddDep:
		return c.handleAddDep(payload)
	case CmdRmDep:
		return c.handleRmDep(payload)
	case CmdEnableService:
		return c.handleEnableService(payload, false)
	case CmdEnableServiceV7:
		return c.handleEnableService(payload, true)
	case CmdDisableService:
		return c.handleDisableService(payload)
	case CmdQueryServiceName:
		return c.handleQueryServiceName(payload)
	case CmdQueryServiceDscDir:
		return c.handleQueryServiceDscDir()
	case CmdListenEnv:
		return c.handleListenEnv()
	case CmdListServices5:
		return c.handleListServices5()
	case CmdServiceStatus5:
		return c.handleServiceStatus5(payload)
	case CmdQueryLoadMech:
		return c.handleQueryLoadMech()
	case CmdQueryDependents:
		return c.handleQueryDependents(payload)
	case CmdQueryDependencies:
		return c.handleQueryDependencies(payload)
	case CmdQueryDescription:
		return c.handleQueryDescription(payload)
	case CmdQueryMetadata:
		return c.handleQueryMetadata(payload)
	case CmdActivateProfile:
		return c.handleActivateProfile(payload)
	case CmdQueryProfile:
		return c.handleQueryProfile()
	case CmdListProfiles:
		return c.handleListProfiles()
	case CmdQueryBundleMembers:
		return c.handleQueryBundleMembers(payload)
	case CmdPauseService:
		return c.handlePauseService(payload)
	case CmdContinueService:
		return c.handleContinueService(payload)
	case CmdOnceService:
		return c.handleOnceService(payload)
	case CmdServiceStatus6:
		return c.handleServiceStatus6(payload)
	case CmdRunAction:
		return c.handleRunAction(payload)
	case CmdListActions:
		return c.handleListActions(payload)
	case CmdScheduleShutdown:
		return c.handleScheduleShutdown(payload)
	case CmdCancelShutdown:
		return c.handleCancelShutdown()
	case CmdQueryShutdown:
		return c.handleQueryShutdown()
	case CmdWallNotice:
		return c.handleWallNotice(payload)
	case CmdResetFailed:
		return c.handleResetFailed(payload)
	case CmdFreezeService:
		return c.handleFreezeService(payload, true)
	case CmdThawService:
		return c.handleFreezeService(payload, false)
	default:
		return c.writePacket(RplyBadReq, nil)
	}
}

// handleFreezeService writes to cgroup.freeze on the target service's
// cgroup v2 directory. `freeze == true` corresponds to CmdFreezeService
// ("1"), false to CmdThawService ("0"). Returns RplyNAK with the OS
// error surfaced via stderr when the cgroup path is missing or the
// write fails (typically means the daemon isn't running on cgroup v2
// or the service has no cgroup path configured).
func (c *Connection) handleFreezeService(payload []byte, freeze bool) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}
	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}
	if freeze {
		err = svc.Record().Freeze()
	} else {
		err = svc.Record().Thaw()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit: %v\n", err)
		return c.writePacket(RplyNAK, nil)
	}
	return c.writePacket(RplyACK, nil)
}

// handleResetFailed clears startFailed on a single service (payload is a
// 4-byte handle) or on every loaded service (payload is empty — the
// "--all" wire form). Idempotent; returns RplyACK either way.
func (c *Connection) handleResetFailed(payload []byte) error {
	if len(payload) == 0 {
		// --all: iterate every loaded service and clear the flag.
		for _, svc := range c.server.services.ListServices() {
			svc.Record().ResetFailed()
		}
		return c.writePacket(RplyACK, nil)
	}
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}
	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}
	svc.Record().ResetFailed()
	return c.writePacket(RplyACK, nil)
}

// --- Command handlers ---

func (c *Connection) handleQueryVersion() error {
	payload := getReplyBuf(4)
	binary.LittleEndian.PutUint16(payload[0:], MinCompatVersion)
	binary.LittleEndian.PutUint16(payload[2:], CPVersion)
	err := c.writePacket(RplyCPVersion, payload)
	putReplyBuf(payload)
	return err
}

func (c *Connection) handleFindService(payload []byte) error {
	name, _, err := DecodeServiceName(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	if err := config.ValidateServiceName(name); err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.server.services.FindService(name, false)
	if svc == nil {
		return c.writePacket(RplyNoService, nil)
	}

	handle := c.allocHandle(svc)
	reply := getReplyBuf(6)
	reply[0] = uint8(svc.State())
	binary.LittleEndian.PutUint32(reply[1:], handle)
	reply[5] = uint8(svc.TargetState())
	err = c.writePacket(RplyServiceRecord, reply)
	putReplyBuf(reply)
	return err
}

func (c *Connection) handleLoadService(payload []byte) error {
	name, _, err := DecodeServiceName(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	if err := config.ValidateServiceName(name); err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc, err := c.server.services.LoadService(name)
	if err != nil {
		// Use typed error checks instead of fragile string matching
		var notFound *service.ServiceNotFound
		var loadErr *config.ServiceLoadError
		var parseErr *config.ParseError
		switch {
		case errors.As(err, &notFound):
			return c.writePacket(RplyNoService, nil)
		case errors.As(err, &parseErr):
			return c.writePacket(RplyServiceDescErr, nil)
		case errors.As(err, &loadErr):
			return c.writePacket(RplyServiceLoadErr2, nil)
		default:
			return c.writePacket(RplyServiceLoadErr, nil)
		}
	}

	handle := c.allocHandle(svc)
	reply := getReplyBuf(6)
	reply[0] = uint8(svc.State())
	binary.LittleEndian.PutUint32(reply[1:], handle)
	reply[5] = uint8(svc.TargetState())
	err = c.writePacket(RplyServiceRecord, reply)
	putReplyBuf(reply)
	return err
}

// sendPreACK sends a PREACK packet if the pre-ack flag (bit 7) is set.
// PREACK acts as a synchronization point for clients tracking service events
// during restart operations — events before PREACK are from old state,
// events after PREACK are from the command being executed.
func (c *Connection) sendPreACK(flags uint8) error {
	if flags&0x80 != 0 {
		return c.writePacket(RplyPreACK, nil)
	}
	return nil
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

	if svc.Record().IsStopPinned() {
		return c.writePacket(RplyPinnedStopped, nil)
	}

	// refuse-manual-start blocks the direct control-socket path but
	// not the dependency graph. StartService is only invoked here
	// from the control connection, so gating at this call site is
	// exactly the systemd semantics.
	if svc.Record().RefusesManualStart() {
		return c.writePacket(RplyManualRefused, nil)
	}

	if err := c.sendPreACK(flags); err != nil {
		return err
	}

	c.server.services.StartService(svc)
	if pin {
		svc.PinStart()
		// Persist the pin so a reboot keeps the operator's intent.
		// Errors are logged; a full disk must not fail the start.
		if err := c.server.Pins.Set(svc.Name(), persist.IntentPinnedStarted); err != nil {
			fmt.Fprintf(os.Stderr, "slinit: %v\n", err)
		}
	}
	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handleWakeService(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	var flags uint8
	if len(payload) >= 5 {
		flags = payload[4]
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

	if err := c.sendPreACK(flags); err != nil {
		return err
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
	restart := flags&0x04 != 0

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	if svc.State() == service.StateStopped {
		return c.writePacket(RplyAlreadySS, nil)
	}

	if !force && svc.Record().IsStartPinned() {
		return c.writePacket(RplyPinnedStarted, nil)
	}

	// refuse-manual-stop mirrors refuse-manual-start on the shutdown
	// side. `force` overrides so an operator can still forcibly stop
	// a runaway service if that's really required.
	if !force && svc.Record().RefusesManualStop() {
		return c.writePacket(RplyManualRefused, nil)
	}

	if err := c.sendPreACK(flags); err != nil {
		return err
	}

	if force {
		c.server.services.ForceStopService(svc)
	} else {
		c.server.services.StopService(svc)
	}
	if pin {
		svc.PinStop()
		if err := c.server.Pins.Set(svc.Name(), persist.IntentPinnedStopped); err != nil {
			fmt.Fprintf(os.Stderr, "slinit: %v\n", err)
		}
	}
	if restart {
		// Re-start the service after stopping (restart operation)
		c.server.services.StartService(svc)
		if pin {
			svc.PinStart()
			if err := c.server.Pins.Set(svc.Name(), persist.IntentPinnedStarted); err != nil {
				fmt.Fprintf(os.Stderr, "slinit: %v\n", err)
			}
		}
	}
	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handleReleaseService(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	var flags uint8
	if len(payload) >= 5 {
		flags = payload[4]
	}

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	if svc.State() == service.StateStopped {
		return c.writePacket(RplyAlreadySS, nil)
	}

	if err := c.sendPreACK(flags); err != nil {
		return err
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

func (c *Connection) handleListServices5() error {
	services := c.server.services.ListServices()
	for _, svc := range services {
		info := EncodeSvcInfo5(svc)
		if err := c.writePacket(RplySvcInfo, info); err != nil {
			return err
		}
	}
	return c.writePacket(RplyListDone, nil)
}

func (c *Connection) handleServiceStatus5(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	status := EncodeServiceStatus5(svc)
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

// handleScheduleShutdown schedules a delayed shutdown.
// Payload: [type(1)] [delay_secs(4, big-endian)] [msg_len(2, LE)?] [msg_bytes...?]
// delay_secs == 0 means immediate (same as CmdShutdown).
// The message tail is optional — a 5-byte payload from an older client
// still parses, keeping wire compatibility with pre-message callers.
func (c *Connection) handleScheduleShutdown(payload []byte) error {
	if len(payload) < 5 {
		return c.writePacket(RplyBadReq, nil)
	}

	shutType := service.ShutdownType(payload[0])
	delaySecs := uint32(payload[1])<<24 | uint32(payload[2])<<16 |
		uint32(payload[3])<<8 | uint32(payload[4])
	delay := time.Duration(delaySecs) * time.Second

	// Optional trailing message. Guard against a truncated length
	// header (extra byte after the fixed part with no msg_len) — treat
	// as no message.
	message := ""
	if len(payload) >= 7 {
		msgLen := int(payload[5]) | int(payload[6])<<8
		start := 7
		if msgLen > 0 && start+msgLen <= len(payload) {
			message = string(payload[start : start+msgLen])
		}
	}

	c.server.ScheduleShutdown(shutType, delay, message)
	return c.writePacket(RplyACK, nil)
}

// handleWallNotice broadcasts an operator-supplied message to every
// logged-in user without scheduling a shutdown. Powers LSB-shutdown's
// `-k` warning-only mode. Payload: [msg_len(2, LE)] [msg_bytes...].
func (c *Connection) handleWallNotice(payload []byte) error {
	if len(payload) < 2 {
		return c.writePacket(RplyBadReq, nil)
	}
	msgLen := int(payload[0]) | int(payload[1])<<8
	if msgLen == 0 || len(payload) < 2+msgLen {
		return c.writePacket(RplyBadReq, nil)
	}
	message := string(payload[2 : 2+msgLen])
	if c.server.WallNoticeFunc != nil {
		c.server.WallNoticeFunc(message)
	}
	return c.writePacket(RplyACK, nil)
}

// handleCancelShutdown cancels a pending scheduled shutdown.
func (c *Connection) handleCancelShutdown() error {
	if c.server.CancelShutdown() {
		return c.writePacket(RplyACK, nil)
	}
	// No shutdown was pending.
	return c.writePacket(RplyNAK, nil)
}

// handleQueryShutdown returns info about a pending scheduled shutdown.
// Reply payload: [type(1)] [remaining_secs(4, big-endian)]
// If no shutdown is pending, replies NAK.
func (c *Connection) handleQueryShutdown() error {
	st, remaining, ok := c.server.ScheduledShutdownInfo()
	if !ok {
		return c.writePacket(RplyNAK, nil)
	}

	secs := uint32(remaining.Seconds())
	payload := []byte{
		uint8(st),
		byte(secs >> 24), byte(secs >> 16), byte(secs >> 8), byte(secs),
	}
	return c.writePacket(RplyShutdownStatus, payload)
}

func (c *Connection) handleCloseHandle(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.handles[handle]
	delete(c.handles, handle)

	// Only remove listener if no other handle references this service
	if svc != nil {
		if rh, ok := c.revHandles[svc]; ok && rh == handle {
			// The reverse map pointed to this handle; find another or remove
			var found bool
			for h, s := range c.handles {
				if s == svc {
					c.revHandles[svc] = h
					found = true
					break
				}
			}
			if !found {
				delete(c.revHandles, svc)
				svc.Record().RemoveListener(c)
			}
		}
		// else: revHandles points to a different handle for this service, still referenced
	}
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

// handleReloadSignal sends the service's configured `reload-signal`
// to its main running process. Different from handleReloadService —
// that one re-reads the service description from disk; this one
// tells the running daemon to re-read its own config (the
// nginx-reload / SIGHUP-style operation).
//
// Replies:
//   - RplyBadReq: bad payload or unknown handle
//   - RplyNAK: service has no reload-signal configured
//   - RplySignalNoPID: service is not running
//   - RplySignalErr: kill(2) returned an error
//   - RplyACK: signal sent
func (c *Connection) handleReloadSignal(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	sig := svc.Record().ReloadSignal()
	if sig == 0 {
		return c.writePacket(RplyNAK, nil)
	}

	pid := svc.PID()
	if pid <= 0 {
		return c.writePacket(RplySignalNoPID, nil)
	}

	if ps, ok := svc.(*service.ProcessService); ok {
		if ps.SendSignalWithControl(sig) {
			return c.writePacket(RplyACK, nil)
		}
		return c.writePacket(RplySignalErr, []byte("signal failed"))
	}

	if err := syscall.Kill(pid, sig); err != nil {
		return c.writePacket(RplySignalErr, []byte(fmt.Sprintf("%v", err)))
	}
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

	// Use SendSignalWithControl if available (ProcessService supports control-command-*)
	if ps, ok := svc.(*service.ProcessService); ok {
		if ps.SendSignalWithControl(sig) {
			return c.writePacket(RplyACK, nil)
		}
		return c.writePacket(RplySignalErr, []byte("signal failed"))
	}

	if err := syscall.Kill(pid, sig); err != nil {
		return c.writePacket(RplySignalErr, []byte(fmt.Sprintf("%v", err)))
	}
	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handlePauseService(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}
	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}
	ps, ok := svc.(*service.ProcessService)
	if !ok {
		return c.writePacket(RplyNAK, nil)
	}
	if ps.Pause() {
		return c.writePacket(RplyACK, nil)
	}
	return c.writePacket(RplyNAK, nil)
}

func (c *Connection) handleContinueService(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}
	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}
	ps, ok := svc.(*service.ProcessService)
	if !ok {
		return c.writePacket(RplyNAK, nil)
	}
	if ps.Continue() {
		return c.writePacket(RplyACK, nil)
	}
	return c.writePacket(RplyNAK, nil)
}

func (c *Connection) handleOnceService(payload []byte) error {
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
	// Start once: set auto-restart to never, then start
	svc.Record().SetAutoRestart(service.RestartNever)
	c.server.services.StartService(svc)
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
	// Drop any persisted intent — unpin means the operator no longer
	// wants slinit to re-apply pins on the next boot.
	if err := c.server.Pins.Clear(svc.Name()); err != nil {
		fmt.Fprintf(os.Stderr, "slinit: %v\n", err)
	}
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

	switch svc.GetLogType() {
	case service.LogToBuffer:
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
		return c.writePacket(RplySvcLog, EncodeSvcLog(data))

	case service.LogToFile:
		// --clear has no sensible semantic for a tail read; refuse.
		if flags&CatLogFlagClear != 0 {
			return c.writePacket(RplyNAK, nil)
		}
		path := svc.GetLogFile()
		if path == "" {
			return c.writePacket(RplyNAK, nil)
		}
		data, err := readLogFileTail(path, 64*1024)
		if err != nil {
			return c.writePacket(RplyNAK, nil)
		}
		return c.writePacket(RplySvcLog, EncodeSvcLog(data))

	default:
		return c.writePacket(RplyNAK, nil)
	}
}

// readLogFileTail returns the last `max` bytes of a file (or whole file if smaller).
// Aligns to the next newline after the seek point so partial first line is dropped.
func readLogFileTail(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()

	offset := int64(0)
	if size > max {
		offset = size - max
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if nl := strings.IndexByte(string(data), '\n'); nl >= 0 && nl+1 < len(data) {
			data = data[nl+1:]
		}
	}
	return data, nil
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

// handleReloadAll rescans every currently-loaded service description
// from disk. Mirrors the per-service handleReloadService but in bulk:
// services in transitional states (Starting / Stopping / Started-but-
// going-down) are skipped silently (operator can retry); only services
// in Stopped or Started can have their config swapped safely. Returns
// a summary (uint16 succeeded + uint16 failed) so the operator sees
// scope without scrolling the daemon log.
//
// Per-connection handle remapping: if a service was replaced (a type
// change between reads), update this connection's handle map so the
// caller's outstanding handle keeps resolving. Other connections that
// hold a stale handle to the old service object are left untouched —
// same trade-off as the single-service handleReloadService, fixing
// it system-wide is a separate concern.
func (c *Connection) handleReloadAll() error {
	loader := c.server.services.GetLoader()
	if loader == nil {
		return c.writePacket(RplyNAK, nil)
	}

	var ok, failed uint16

	for _, svc := range c.server.services.ListServices() {
		state := svc.State()
		if state != service.StateStopped && state != service.StateStarted {
			// Skipped (transitional). Don't count as failed —
			// the config on disk may be fine, just bad timing.
			continue
		}

		newSvc, err := loader.ReloadService(svc)
		if err != nil {
			failed++
			continue
		}
		ok++

		if newSvc != svc {
			// Type change: swap any of THIS connection's handles
			// pointing at the old object.
			if h, found := c.revHandles[svc]; found {
				delete(c.revHandles, svc)
				c.handles[h] = newSvc
				c.revHandles[newSvc] = h
			}
		}
	}

	c.server.services.ProcessQueues()

	payload := make([]byte, 4)
	binary.LittleEndian.PutUint16(payload[0:2], ok)
	binary.LittleEndian.PutUint16(payload[2:4], failed)
	return c.writePacket(RplyReloadAllResult, payload)
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

	// Unregister as listener before removing handles
	svc.Record().RemoveListener(c)

	// Unload: clean up deps and remove from set
	c.server.services.UnloadService(svc)

	// Remove all handles pointing to this service
	for h, s := range c.handles {
		if s == svc {
			delete(c.handles, h)
		}
	}
	delete(c.revHandles, svc)

	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handleSetEnv(payload []byte) error {
	handle, key, value, isUnset, err := DecodeSetEnv(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	if handle == 0 {
		// Global environment
		if isUnset {
			c.server.services.GlobalUnsetEnv(key)
		} else {
			c.server.services.GlobalSetEnv(key, value)
		}
	} else {
		// Per-service environment
		svc := c.getService(handle)
		if svc == nil {
			return c.writePacket(RplyBadReq, nil)
		}
		if isUnset {
			svc.Record().UnsetEnvVar(key)
		} else {
			svc.Record().SetEnvVar(key, value)
		}
	}
	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handleResetEnv(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}
	if handle == 0 {
		// Global reset is not yet supported (would require snapshotting
		// the daemon's startup env to know which keys are runtime
		// mutations vs. defaults).
		return c.writePacket(RplyNAK, nil)
	}
	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}
	svc.Record().ResetEnv()
	return c.writePacket(RplyACK, nil)
}

func (c *Connection) handleGetAllEnv(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	if handle == 0 {
		// Global environment
		globalEnv := c.server.services.GlobalEnv()
		env := make(map[string]string, len(globalEnv))
		for _, entry := range globalEnv {
			if k, v, ok := strings.Cut(entry, "="); ok {
				env[k] = v
			}
		}
		reply := EncodeEnvList(env)
		return c.writePacket(RplyEnvList, reply)
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

	// Reject self-dependencies
	if from == to {
		return c.writePacket(RplyNAK, nil)
	}

	if depType > 5 {
		return c.writePacket(RplyBadReq, nil)
	}

	// Check for circular dependency before adding
	if service.CheckCircularDep(from, to) {
		return c.writePacket(RplyNAK, nil)
	}

	// Add the dependency
	dep := from.Record().AddDep(to, service.DependencyType(depType))

	// Update dependency depths with rollback on failure
	var updater service.DepDepthUpdater
	updater.AddPotentialUpdate(from)
	if err := updater.ProcessUpdates(); err != nil {
		// Depth limit exceeded — remove the dep we just added and rollback depths
		from.Record().RmDep(to, service.DependencyType(depType))
		updater.Rollback()
		_ = dep
		return c.writePacket(RplyNAK, nil)
	}
	updater.Commit()

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

	// Recalculate depths after removal
	var updater service.DepDepthUpdater
	updater.AddPotentialUpdate(from)
	// Also queue dependents of from since its depth may decrease
	for _, dept := range from.Record().Dependents() {
		updater.AddPotentialUpdate(dept.From)
	}
	if err := updater.ProcessUpdates(); err != nil {
		// Depth recalc on remove should never fail (depths only decrease),
		// but commit anyway to be safe.
		updater.Rollback()
	} else {
		updater.Commit()
	}

	c.server.services.ProcessQueues()
	return c.writePacket(RplyACK, nil)
}

// handleEnableService adds a waits-for dep from a "from" service to the
// target and starts the target. When v7 is true the reply carries the
// target's current status (via SERVICESTATUS + dep_exists prefix + v6
// buffer) so the client learns terminal state from the same round-trip
// that added the dep — matches dinit d7d843b's ENABLE_SERVICE_V7.
func (c *Connection) handleEnableService(payload []byte, v7 bool) error {
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

	// Determine "from" service: explicit handle → enable-via → boot service
	var fromSvc service.Service
	if len(payload) >= 8 {
		fromHandle := binary.LittleEndian.Uint32(payload[4:])
		fromSvc = c.getService(fromHandle)
	}
	if fromSvc == nil {
		// Check @meta enable-via on the target service
		fromName := svc.Record().EnableVia()
		if fromName == "" {
			fromName = c.server.services.BootServiceName()
		}
		if fromName == "" {
			return c.writePacket(RplyNAK, nil)
		}
		var loadErr error
		fromSvc, loadErr = c.server.services.LoadService(fromName)
		if loadErr != nil || fromSvc == nil {
			return c.writePacket(RplyNAK, nil)
		}
	}

	// Add waits-for dependency from source to target. Detect whether the
	// dep already existed so v7 clients can report it (dinit exposes
	// this via the dep_exists byte). We treat "already exists" as any
	// non-BEFORE/AFTER dep of any type on the same target — matching
	// dinit's `add_service_dep` behaviour where a WAITS_FOR request on a
	// service that already has a REGULAR dep on the same target is a
	// no-op.
	depExists := false
	for _, dep := range fromSvc.Record().Dependencies() {
		if dep.To == svc && dep.DepType != service.DepBefore &&
			dep.DepType != service.DepAfter {
			depExists = true
			break
		}
	}

	if !depExists {
		if service.CheckCircularDep(fromSvc, svc) {
			return c.writePacket(RplyNAK, nil)
		}
		fromSvc.Record().AddDep(svc, service.DepWaitsFor)

		// Persist by creating a waits-for.d symlink in the source
		// service's load directory, so the dependency survives a
		// daemon restart. A persistence failure is logged but does
		// not undo the runtime change — operators who re-enable after
		// the disk is full or read-only should see the error in logs
		// and re-run once the underlying issue is fixed.
		if err := persistEnable(fromSvc, svc); err != nil {
			fmt.Fprintf(os.Stderr, "slinit: enable: persist waits-for.d link: %v\n", err)
		}
	}

	// Start the target service
	c.server.services.StartService(svc)

	if v7 {
		// Wire: [RplyServiceStatus][dep_exists(1B)][status_v6(22B)]
		status := EncodeServiceStatus6(svc)
		reply := make([]byte, 1+len(status))
		if depExists {
			reply[0] = 1
		}
		copy(reply[1:], status)
		return c.writePacket(RplyServiceStatus, reply)
	}
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

	// Determine "from" service: explicit handle → enable-via → boot service
	var fromSvc service.Service
	if len(payload) >= 8 {
		fromHandle := binary.LittleEndian.Uint32(payload[4:])
		fromSvc = c.getService(fromHandle)
	}
	if fromSvc == nil {
		// Check @meta enable-via on the target service
		fromName := svc.Record().EnableVia()
		if fromName == "" {
			fromName = c.server.services.BootServiceName()
		}
		if fromName == "" {
			return c.writePacket(RplyNAK, nil)
		}
		fromSvc = c.server.services.FindService(fromName, false)
		if fromSvc == nil {
			return c.writePacket(RplyNAK, nil)
		}
	}

	// Remove waits-for dependency from source to target
	fromSvc.Record().RmDep(svc, service.DepWaitsFor)

	// Remove the persisted waits-for.d symlink (if any). Errors other
	// than ENOENT are logged but not propagated — the in-memory dep is
	// gone and the daemon's view is authoritative; the operator can
	// inspect the log and clean up the on-disk artifact if needed.
	if err := persistDisable(fromSvc, svc); err != nil {
		fmt.Fprintf(os.Stderr, "slinit: disable: remove waits-for.d link: %v\n", err)
	}

	// Stop the target service
	c.server.services.StopService(svc)
	return c.writePacket(RplyACK, nil)
}

// persistEnable creates a symlink from <fromSvc-dir>/waits-for.d/<to-name>
// to ../<to-name>, so the enable survives a daemon restart. If fromSvc has
// no recorded load directory (e.g. it was added programmatically without
// loading from disk), the call is a no-op — there is no on-disk service
// description to anchor the link.
//
// The fromSvc service-dir is the directory the description was loaded
// from. We don't follow user-overridden waits-for.d= paths here; using
// the canonical subdirectory matches the offline path slinitctl uses and
// the directory the loader scans by default.
func persistEnable(fromSvc, toSvc service.Service) error {
	dir := fromSvc.Record().ServiceDir()
	if dir == "" {
		return nil
	}
	waitsDir := filepath.Join(dir, "waits-for.d")
	if err := os.MkdirAll(waitsDir, 0755); err != nil {
		return err
	}
	link := filepath.Join(waitsDir, toSvc.Name())
	if _, err := os.Lstat(link); err == nil {
		return nil // already present
	}
	target := filepath.Join("..", toSvc.Name())
	return os.Symlink(target, link)
}

// persistDisable removes the symlink created by persistEnable. ENOENT is
// not an error: the disable still succeeded in memory and an absent link
// is the desired end state.
func persistDisable(fromSvc, toSvc service.Service) error {
	dir := fromSvc.Record().ServiceDir()
	if dir == "" {
		return nil
	}
	link := filepath.Join(dir, "waits-for.d", toSvc.Name())
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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

func (c *Connection) handleQueryDescription(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	// Reuse the length-prefixed string encoding from EncodeServiceName.
	return c.writePacket(RplyDescription, EncodeServiceName(svc.Record().Description()))
}

func (c *Connection) handleQueryMetadata(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}
	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}
	rec := svc.Record()
	return c.writePacket(RplyMetadata, EncodeMetadata(rec.Author(), rec.Version(), rec.Usage()))
}

// handleActivateProfile decodes a length-prefixed profile name and
// asks the ServiceSet to swap. Failure to validate ("no service
// declares this profile") comes back as RplyNAK so the client can
// distinguish it from a protocol error.
func (c *Connection) handleActivateProfile(payload []byte) error {
	name, _, err := DecodeServiceName(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}
	res, aerr := c.server.services.ActivateProfile(name)
	if aerr != nil {
		return c.writePacket(RplyNAK, []byte(aerr.Error()))
	}
	return c.writePacket(RplyActivateResult,
		EncodeActivateResult(res.Active, res.Stopped, res.Started, res.Kept))
}

// handleQueryProfile reports which profile is currently active.
// Empty string means "no filter" — a valid state, not an error.
func (c *Connection) handleQueryProfile() error {
	return c.writePacket(RplyProfile, EncodeServiceName(c.server.services.ActiveProfile()))
}

// handleListProfiles enumerates every profile tag declared by any
// loaded service. Reply is a sorted list; empty list is valid when
// no service uses profiles.
func (c *Connection) handleListProfiles() error {
	return c.writePacket(RplyProfileList, EncodeStringList(c.server.services.ListProfiles()))
}

// handleQueryBundleMembers returns the s6-rc-style bundle member list
// stored on the service record. Empty list is a valid reply when the
// service is not a bundle — the client uses that to decide whether to
// render a "Bundle members:" section in `slinitctl status`.
func (c *Connection) handleQueryBundleMembers(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}
	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}
	return c.writePacket(RplyBundleMembers, EncodeStringList(svc.Record().BundleMembers()))
}

func (c *Connection) handleQueryServiceDscDir() error {
	loader := c.server.services.GetLoader()
	if loader == nil {
		// No loader configured, return empty list
		reply := make([]byte, 2)
		return c.writePacket(RplyServiceDscDir, reply)
	}

	// Dinit-parity (upstream 044b950 + 1300c63): resolve every directory
	// path against the daemon's working directory before sending it to
	// the client. dinitctl traditionally treated whatever the daemon
	// returned as authoritative and joined relative paths against its
	// OWN cwd, which silently lied when the two processes had different
	// working directories. Doing the Abs() server-side closes the gap
	// for every existing client without a protocol bump.
	rawDirs := loader.ServiceDirs()
	dirs := make([]string, len(rawDirs))
	for i, d := range rawDirs {
		if abs, err := filepath.Abs(d); err == nil {
			dirs[i] = abs
		} else {
			// Abs only fails if Getwd does — keep the raw value so a
			// best-effort answer reaches the client.
			dirs[i] = d
		}
	}
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

func (c *Connection) handleQueryDependents(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	dependents := svc.Dependents()
	// Allocate handles for each dependent and return them
	// Wire format: count(4) + [handle(4)]*
	buf := make([]byte, 4+4*len(dependents))
	binary.LittleEndian.PutUint32(buf, uint32(len(dependents)))
	off := 4
	for _, dep := range dependents {
		depHandle := c.allocHandle(dep.From)
		binary.LittleEndian.PutUint32(buf[off:], depHandle)
		off += 4
	}
	return c.writePacket(RplyDependents, buf)
}

func (c *Connection) handleQueryDependencies(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	deps := svc.Record().Dependencies()
	// Wire format: count(4) + [handle(4) + depType(1)]*
	buf := make([]byte, 4+5*len(deps))
	binary.LittleEndian.PutUint32(buf, uint32(len(deps)))
	off := 4
	for _, dep := range deps {
		depHandle := c.allocHandle(dep.To)
		binary.LittleEndian.PutUint32(buf[off:], depHandle)
		buf[off+4] = uint8(dep.DepType)
		off += 5
	}
	return c.writePacket(RplyDependencies, buf)
}

func (c *Connection) handleQueryLoadMech() error {
	loader := c.server.services.GetLoader()
	cwd, _ := os.Getwd()

	var dirs []string
	if loader != nil {
		dirs = loader.ServiceDirs()
	}

	// Wire format: loaderType(1) + cwdLen(4) + cwd(N) + numDirs(4) + [dirLen(4) + dir(N)]*
	size := 1 + 4 + len(cwd) + 4
	for _, d := range dirs {
		size += 4 + len(d)
	}
	buf := make([]byte, size)
	buf[0] = 1 // SSET_TYPE_DIRLOAD
	off := 1
	binary.LittleEndian.PutUint32(buf[off:], uint32(len(cwd)))
	off += 4
	copy(buf[off:], cwd)
	off += len(cwd)
	binary.LittleEndian.PutUint32(buf[off:], uint32(len(dirs)))
	off += 4
	for _, d := range dirs {
		binary.LittleEndian.PutUint32(buf[off:], uint32(len(d)))
		off += 4
		copy(buf[off:], d)
		off += len(d)
	}
	return c.writePacket(RplyLoaderMech, buf)
}

func (c *Connection) handleServiceStatus6(payload []byte) error {
	handle, err := DecodeHandle(payload)
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	status := EncodeServiceStatus6(svc)
	return c.writePacket(RplyServiceStatus, status)
}

func (c *Connection) handleListenEnv() error {
	if !c.listenEnv {
		c.listenEnv = true
		c.server.services.AddEnvListener(c)
	}
	return c.writePacket(RplyACK, nil)
}

// handleRunAction runs an extra-command action on a service.
// Payload: handle(4) + actionNameLen(2) + actionName(N)
func (c *Connection) handleRunAction(payload []byte) error {
	if len(payload) < 6 {
		return c.writePacket(RplyBadReq, nil)
	}
	handle := binary.LittleEndian.Uint32(payload)
	actionName, _, err := DecodeServiceName(payload[4:])
	if err != nil {
		return c.writePacket(RplyBadReq, nil)
	}

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	rec := svc.Record()
	cmd, ok := rec.LookupExtraCommand(actionName)
	if !ok {
		return c.writePacket(RplyNAK, []byte("unknown action: "+actionName))
	}

	// Execute the action command synchronously and capture output.
	execCmd := exec.Command(cmd[0], cmd[1:]...)
	output, execErr := execCmd.CombinedOutput()
	if execErr != nil {
		// Return NAK with the error message + any partial output
		msg := fmt.Sprintf("action '%s' failed: %v\n%s", actionName, execErr, string(output))
		return c.writePacket(RplyNAK, []byte(msg))
	}

	// Return the output (may be empty for actions that produce none)
	return c.writePacket(RplyActionOutput, output)
}

// handleListActions returns the list of available extra-command actions.
// Payload: handle(4)
func (c *Connection) handleListActions(payload []byte) error {
	if len(payload) < 4 {
		return c.writePacket(RplyBadReq, nil)
	}
	handle := binary.LittleEndian.Uint32(payload)

	svc := c.getService(handle)
	if svc == nil {
		return c.writePacket(RplyBadReq, nil)
	}

	actions := svc.Record().ListExtraActions()
	result := strings.Join(actions, "\n")
	return c.writePacket(RplyActionList, []byte(result))
}

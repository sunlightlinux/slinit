// Package dinitwire speaks dinit's own control protocol, as a client.
//
// slinit's control protocol uses dinit's command and reply numbers but not
// its wire format: slinit frames every packet as type, 2-byte length,
// payload, while dinit has no length prefix and gives each command its own
// fixed layout, often with the fields in a different order. The two cannot
// talk to each other. This package is the dinit side, so that slinitctl can
// drive a dinit daemon.
//
// The layouts are taken from dinit v0.23.0 (src/control.cc,
// src/includes/control-cmds.h, control-datatypes.h), protocol version 7.
// dinit writes multi-byte fields in host byte order and sizes two of them
// with size_t and struct timespec; this package assumes the client runs on
// the daemon's machine, as a control-socket client does, and that the
// machine is little-endian with a 64-bit size_t and a 16-byte timespec.
package dinitwire

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// Commands (dinit's cp_cmd). slinit's control.Cmd* constants carry the same
// numbers for these; they are repeated here so the package stands alone.
const (
	CmdQueryVersion       uint8 = 0
	CmdFindService        uint8 = 1
	CmdLoadService        uint8 = 2
	CmdStartService       uint8 = 3
	CmdStopService        uint8 = 4
	CmdWakeService        uint8 = 5
	CmdReleaseService     uint8 = 6
	CmdUnpinService       uint8 = 7
	CmdListServices       uint8 = 8
	CmdUnloadService      uint8 = 9
	CmdShutdown           uint8 = 10
	CmdAddDep             uint8 = 11
	CmdRmDep              uint8 = 12
	CmdQueryLoadMech      uint8 = 13
	CmdEnableService      uint8 = 14
	CmdQueryServiceName   uint8 = 15
	CmdReloadService      uint8 = 16
	CmdSetEnv             uint8 = 17
	CmdServiceStatus      uint8 = 18
	CmdSetTrigger         uint8 = 19
	CmdCatLog             uint8 = 20
	CmdSignal             uint8 = 21
	CmdQueryServiceDscDir uint8 = 22
	CmdCloseHandle        uint8 = 23
	CmdGetAllEnv          uint8 = 24
	CmdListServices5      uint8 = 25
	CmdServiceStatus5     uint8 = 26
	CmdListenEnv          uint8 = 27
	CmdServiceStatus6     uint8 = 28
	CmdEnableServiceV7    uint8 = 29
	CmdRmDepV7            uint8 = 30

	// MaxCmd is the highest command number dinit v0.23.0 understands.
	MaxCmd = CmdRmDepV7
)

// Replies (dinit's cp_rply) that carry a payload or that this package has
// to recognise. A reply not listed here is a single byte.
const (
	RplyCPVersion     uint8 = 58
	RplyServiceRecord uint8 = 59
	RplySvcInfo       uint8 = 62
	RplyListDone      uint8 = 63
	RplyLoaderMech    uint8 = 64
	RplyDependents    uint8 = 65
	RplyServiceName   uint8 = 66
	RplyServiceStatus uint8 = 70
	RplyServiceLog    uint8 = 73
	RplySvcDscDir     uint8 = 77
	RplyAllEnv        uint8 = 78
	RplyPreACK        uint8 = 79
)

// Information packets (dinit's cp_info): sent unasked, between replies.
const (
	InfoServiceEvent  uint8 = 100
	InfoServiceEvent5 uint8 = 101
	InfoEnvEvent      uint8 = 102
)

// dinit's status flag bits (fill_status_buffer in control.cc).
const (
	FlagWaitingForConsole uint8 = 1 << 0
	FlagHasConsole        uint8 = 1 << 1
	FlagStartSkipped      uint8 = 1 << 2
	FlagMarkedActive      uint8 = 1 << 3
	FlagHasPID            uint8 = 1 << 4
)

// stopReasonExecFailed is stopped_reason_t::EXECFAILED: with it the status
// buffer carries the exec stage and errno where the exit status would be.
const stopReasonExecFailed = 4

// Status buffer sizes.
const (
	statusSize  = 10 // 6 + sizeof(int)
	status5Size = 14 // 6 + 2*sizeof(int)
	status6Size = 30 // status5 + sizeof(struct timespec)
)

// Status is a dinit status buffer, decoded. Which of the tail fields mean
// anything depends on the version asked for and on HasPID.
type Status struct {
	State      uint8
	Target     uint8
	Flags      uint8 // dinit's bits, Flag*
	StopReason uint8
	ExecStage  uint16 // set when StopReason is exec-failed

	HasPID bool
	PID    int32

	// Without a pid, version 1 has ExitStatus: 0 for a clean exit, -1 for
	// anything else, or the errno when the exec failed.
	ExitStatus int32
	// Without a pid, versions 5 and 6 have the wait status as si_code and
	// si_status; when the exec failed SiCode holds the errno.
	SiCode   int32
	SiStatus int32

	// Version 6: modification time of the service description.
	ModSec  int64
	ModNsec int64
}

func decodeStatusHead(b []byte) Status {
	st := Status{
		State:      b[0],
		Target:     b[1],
		Flags:      b[2],
		StopReason: b[3],
	}
	st.HasPID = st.Flags&FlagHasPID != 0
	if !st.HasPID && st.StopReason == stopReasonExecFailed {
		st.ExecStage = binary.LittleEndian.Uint16(b[4:])
	}
	return st
}

func decodeStatus(b []byte) Status {
	st := decodeStatusHead(b)
	v := int32(binary.LittleEndian.Uint32(b[6:]))
	if st.HasPID {
		st.PID = v
	} else {
		st.ExitStatus = v
	}
	return st
}

func decodeStatus5(b []byte) Status {
	st := decodeStatusHead(b)
	v := int32(binary.LittleEndian.Uint32(b[6:]))
	if st.HasPID {
		st.PID = v
	} else {
		st.SiCode = v
		st.SiStatus = int32(binary.LittleEndian.Uint32(b[10:]))
	}
	return st
}

func decodeStatus6(b []byte) Status {
	st := decodeStatus5(b)
	st.ModSec = int64(binary.LittleEndian.Uint64(b[14:]))
	st.ModNsec = int64(binary.LittleEndian.Uint64(b[22:]))
	return st
}

// ListEntry is one service from a list reply.
type ListEntry struct {
	Name   string
	Status Status
}

// LoadMech is the reply to QueryLoadMech.
type LoadMech struct {
	Type uint8
	Cwd  string
	Dirs []string
}

// Event is an information packet.
type Event struct {
	Type uint8 // InfoServiceEvent, InfoServiceEvent5 or InfoEnvEvent

	// Service events.
	Handle uint32
	Event  uint8
	Status Status

	// Environment events.
	Overridden bool
	Var        string
}

// Conn is a client connection to a dinit daemon. It is not safe for
// concurrent use: the protocol is request and reply on one stream.
type Conn struct {
	w      io.Writer
	r      *bufio.Reader
	events []Event
}

// New wraps an open control-socket connection.
func New(rw io.ReadWriter) *Conn {
	return &Conn{w: rw, r: bufio.NewReader(rw)}
}

func (c *Conn) readN(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(c.r, b); err != nil {
		return nil, err
	}
	return b, nil
}

// readInfo reads the rest of an information packet whose type byte has been
// consumed. Every information packet has its length in the second byte; the
// environment event carries its variable after that length.
func (c *Conn) readInfo(typ uint8) (Event, error) {
	ln, err := c.r.ReadByte()
	if err != nil {
		return Event{}, err
	}
	if ln < 2 {
		return Event{}, fmt.Errorf("dinit: information packet %d with length %d", typ, ln)
	}
	body, err := c.readN(int(ln) - 2)
	if err != nil {
		return Event{}, err
	}
	ev := Event{Type: typ}
	switch typ {
	case InfoServiceEvent, InfoServiceEvent5:
		want := 5 + statusSize
		if typ == InfoServiceEvent5 {
			want = 5 + status5Size
		}
		if len(body) < want {
			return Event{}, fmt.Errorf("dinit: service event %d too short: %d", typ, len(body))
		}
		ev.Handle = binary.LittleEndian.Uint32(body)
		ev.Event = body[4]
		if typ == InfoServiceEvent5 {
			ev.Status = decodeStatus5(body[5:])
		} else {
			ev.Status = decodeStatus(body[5:])
		}
	case InfoEnvEvent:
		if len(body) < 3 {
			return Event{}, fmt.Errorf("dinit: environment event too short: %d", len(body))
		}
		ev.Overridden = body[0] != 0
		data, err := c.readN(int(binary.LittleEndian.Uint16(body[1:])))
		if err != nil {
			return Event{}, err
		}
		// The length dinit sends counts the string's NUL terminator.
		ev.Var = strings.TrimSuffix(string(data), "\x00")
	}
	return ev, nil
}

// replyCode reads the next reply's code, setting aside any information
// packets that arrive before it.
func (c *Conn) replyCode() (uint8, error) {
	for {
		b, err := c.r.ReadByte()
		if err != nil {
			return 0, err
		}
		if b < InfoServiceEvent {
			return b, nil
		}
		ev, err := c.readInfo(b)
		if err != nil {
			return 0, err
		}
		c.events = append(c.events, ev)
	}
}

// NextEvent returns the next information packet, waiting for one if none
// has been set aside. A reply arriving instead is a protocol error: nothing
// was asked.
func (c *Conn) NextEvent() (Event, error) {
	if len(c.events) > 0 {
		ev := c.events[0]
		c.events = c.events[1:]
		return ev, nil
	}
	b, err := c.r.ReadByte()
	if err != nil {
		return Event{}, err
	}
	if b < InfoServiceEvent {
		return Event{}, fmt.Errorf("dinit: unexpected reply %d while waiting for an event", b)
	}
	return c.readInfo(b)
}

// PendingEvents reports how many information packets are set aside.
func (c *Conn) PendingEvents() int { return len(c.events) }

func handleBytes(h uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, h)
	return b
}

func (c *Conn) send(parts ...[]byte) error {
	var pkt []byte
	for _, p := range parts {
		pkt = append(pkt, p...)
	}
	_, err := c.w.Write(pkt)
	return err
}

// QueryVersion returns the daemon's minimum compatible and actual protocol
// versions.
func (c *Conn) QueryVersion() (minCompat, actual uint16, err error) {
	if err = c.send([]byte{CmdQueryVersion}); err != nil {
		return
	}
	code, err := c.replyCode()
	if err != nil {
		return
	}
	if code != RplyCPVersion {
		return 0, 0, fmt.Errorf("dinit: unexpected version reply %d", code)
	}
	b, err := c.readN(4)
	if err != nil {
		return
	}
	return binary.LittleEndian.Uint16(b), binary.LittleEndian.Uint16(b[2:]), nil
}

// ServiceRecord is the reply to a successful find or load.
type ServiceRecord struct {
	State  uint8
	Handle uint32
	Target uint8
}

// FindOrLoad sends CmdFindService or CmdLoadService. On success code is
// RplyServiceRecord and rec is set; otherwise code is the daemon's one-byte
// refusal.
func (c *Conn) FindOrLoad(cmd uint8, name string) (code uint8, rec ServiceRecord, err error) {
	if len(name) == 0 || len(name) > 1021 {
		return 0, rec, fmt.Errorf("dinit: service name length %d out of range", len(name))
	}
	ln := make([]byte, 2)
	binary.LittleEndian.PutUint16(ln, uint16(len(name)))
	if err = c.send([]byte{cmd}, ln, []byte(name)); err != nil {
		return
	}
	if code, err = c.replyCode(); err != nil || code != RplyServiceRecord {
		return
	}
	b, err := c.readN(6)
	if err != nil {
		return
	}
	rec = ServiceRecord{State: b[0], Handle: binary.LittleEndian.Uint32(b[1:]), Target: b[5]}
	return
}

// StartStop flag bits (process_start_stop in control.cc).
const (
	StartStopPin     uint8 = 1 << 0
	StartStopGentle  uint8 = 1 << 1 // stop: do not force past dependents
	StartStopRestart uint8 = 1 << 2 // stop: start again afterwards
	StartStopPreACK  uint8 = 1 << 7
)

// StartStopReply is the reply to start, stop, wake or release.
type StartStopReply struct {
	PreACK bool  // the daemon sent RplyPreACK before the reply proper
	Code   uint8 // the reply proper
	// Dependents holds the handles of the dependents a gentle stop would
	// take down, when Code is RplyDependents.
	Dependents []uint32
}

// StartStop sends CmdStartService, CmdStopService, CmdWakeService or
// CmdReleaseService.
func (c *Conn) StartStop(cmd, flags uint8, handle uint32) (rep StartStopReply, err error) {
	if err = c.send([]byte{cmd, flags}, handleBytes(handle)); err != nil {
		return
	}
	code, err := c.replyCode()
	if err != nil {
		return
	}
	if code == RplyPreACK {
		rep.PreACK = true
		if code, err = c.replyCode(); err != nil {
			return
		}
	}
	rep.Code = code
	if code != RplyDependents {
		return
	}
	nb, err := c.readN(8) // size_t count
	if err != nil {
		return
	}
	n := binary.LittleEndian.Uint64(nb)
	if n > 1<<20 {
		return rep, fmt.Errorf("dinit: implausible dependent count %d", n)
	}
	hb, err := c.readN(int(n) * 4)
	if err != nil {
		return
	}
	for i := 0; i < int(n); i++ {
		rep.Dependents = append(rep.Dependents, binary.LittleEndian.Uint32(hb[i*4:]))
	}
	return
}

// HandleCmd sends a command that is just a handle and is answered with one
// byte: CmdUnpinService, CmdUnloadService, CmdReloadService, CmdCloseHandle.
func (c *Conn) HandleCmd(cmd uint8, handle uint32) (uint8, error) {
	if err := c.send([]byte{cmd}, handleBytes(handle)); err != nil {
		return 0, err
	}
	return c.replyCode()
}

// Shutdown asks the daemon to stop all services and shut down as typ
// (dinit's shutdown_type_t).
func (c *Conn) Shutdown(typ uint8) (uint8, error) {
	if err := c.send([]byte{CmdShutdown, typ}); err != nil {
		return 0, err
	}
	return c.replyCode()
}

// List sends CmdListServices5 and returns every loaded service.
func (c *Conn) List() ([]ListEntry, error) {
	if err := c.send([]byte{CmdListServices5}); err != nil {
		return nil, err
	}
	var out []ListEntry
	for {
		code, err := c.replyCode()
		if err != nil {
			return nil, err
		}
		if code == RplyListDone {
			return out, nil
		}
		if code != RplySvcInfo {
			return nil, fmt.Errorf("dinit: unexpected reply %d in service list", code)
		}
		ln, err := c.r.ReadByte()
		if err != nil {
			return nil, err
		}
		b, err := c.readN(status5Size + int(ln))
		if err != nil {
			return nil, err
		}
		out = append(out, ListEntry{Name: string(b[status5Size:]), Status: decodeStatus5(b)})
	}
}

// Status sends CmdServiceStatus, CmdServiceStatus5 or CmdServiceStatus6.
// code is RplyServiceStatus on success.
func (c *Conn) Status(cmd uint8, handle uint32) (code uint8, st Status, err error) {
	size, dec := statusSize, decodeStatus
	switch cmd {
	case CmdServiceStatus:
	case CmdServiceStatus5:
		size, dec = status5Size, decodeStatus5
	case CmdServiceStatus6:
		size, dec = status6Size, decodeStatus6
	default:
		return 0, st, fmt.Errorf("dinit: %d is not a status command", cmd)
	}
	if err = c.send([]byte{cmd}, handleBytes(handle)); err != nil {
		return
	}
	if code, err = c.replyCode(); err != nil || code != RplyServiceStatus {
		return
	}
	b, err := c.readN(1 + size) // reserved byte, then the buffer
	if err != nil {
		return
	}
	return code, dec(b[1:]), nil
}

// DepReply is the reply to a dependency command.
type DepReply struct {
	Code uint8
	// The V7 commands answer RplyServiceStatus with Flag — whether the
	// dependency already existed (enable) or was removed (remove) — and the
	// status of the "to" service.
	Flag   bool
	Status Status
}

// Dep sends CmdAddDep, CmdRmDep, CmdEnableService, CmdEnableServiceV7 or
// CmdRmDepV7 for the dependency of from on to.
func (c *Conn) Dep(cmd, depType uint8, from, to uint32) (rep DepReply, err error) {
	if err = c.send([]byte{cmd, depType}, handleBytes(from), handleBytes(to)); err != nil {
		return
	}
	if rep.Code, err = c.replyCode(); err != nil || rep.Code != RplyServiceStatus {
		return
	}
	b, err := c.readN(1 + status6Size)
	if err != nil {
		return
	}
	rep.Flag = b[0] != 0
	rep.Status = decodeStatus6(b[1:])
	return
}

// QueryLoadMech asks how the daemon finds service descriptions.
func (c *Conn) QueryLoadMech() (code uint8, lm LoadMech, err error) {
	if err = c.send([]byte{CmdQueryLoadMech}); err != nil {
		return
	}
	if code, err = c.replyCode(); err != nil || code != RplyLoaderMech {
		return
	}
	// type(1), total size(4), dir count(4), then cwd and each dir as
	// length(4) + bytes.
	hd, err := c.readN(9)
	if err != nil {
		return
	}
	lm.Type = hd[0]
	ndirs := binary.LittleEndian.Uint32(hd[5:])
	if ndirs > 1<<16 {
		return code, lm, fmt.Errorf("dinit: implausible service directory count %d", ndirs)
	}
	str := func() (string, error) {
		lb, err := c.readN(4)
		if err != nil {
			return "", err
		}
		n := binary.LittleEndian.Uint32(lb)
		if n > 1<<20 {
			return "", fmt.Errorf("dinit: implausible path length %d", n)
		}
		b, err := c.readN(int(n))
		return string(b), err
	}
	if lm.Cwd, err = str(); err != nil {
		return
	}
	for i := uint32(0); i < ndirs; i++ {
		var d string
		if d, err = str(); err != nil {
			return
		}
		lm.Dirs = append(lm.Dirs, d)
	}
	return
}

// QueryServiceName returns the name behind a handle.
func (c *Conn) QueryServiceName(handle uint32) (code uint8, name string, err error) {
	if err = c.send([]byte{CmdQueryServiceName, 0}, handleBytes(handle)); err != nil {
		return
	}
	if code, err = c.replyCode(); err != nil || code != RplyServiceName {
		return
	}
	hd, err := c.readN(3) // reserved, length(2)
	if err != nil {
		return
	}
	b, err := c.readN(int(binary.LittleEndian.Uint16(hd[1:])))
	return code, string(b), err
}

// SetEnv sets VAR=VALUE in the daemon's environment, or unsets VAR when v
// has no '='. dinit's environment is global: there is no per-service one.
func (c *Conn) SetEnv(v string) (uint8, error) {
	if len(v) == 0 || len(v) > 1021 {
		return 0, fmt.Errorf("dinit: environment variable length %d out of range", len(v))
	}
	ln := make([]byte, 2)
	binary.LittleEndian.PutUint16(ln, uint16(len(v)))
	if err := c.send([]byte{CmdSetEnv}, ln, []byte(v)); err != nil {
		return 0, err
	}
	return c.replyCode()
}

// GetAllEnv returns the daemon's environment as NAME=VALUE strings.
func (c *Conn) GetAllEnv() (code uint8, env []string, err error) {
	if err = c.send([]byte{CmdGetAllEnv, 0}); err != nil {
		return
	}
	if code, err = c.replyCode(); err != nil || code != RplyAllEnv {
		return
	}
	nb, err := c.readN(8) // size_t
	if err != nil {
		return
	}
	n := binary.LittleEndian.Uint64(nb)
	if n > 1<<26 {
		return code, nil, fmt.Errorf("dinit: implausible environment size %d", n)
	}
	b, err := c.readN(int(n))
	if err != nil {
		return
	}
	for len(b) > 0 {
		i := 0
		for i < len(b) && b[i] != 0 {
			i++
		}
		env = append(env, string(b[:i]))
		if i == len(b) {
			break
		}
		b = b[i+1:]
	}
	return
}

// ListenEnv asks for an environment event whenever a variable changes.
func (c *Conn) ListenEnv() (uint8, error) {
	if err := c.send([]byte{CmdListenEnv}); err != nil {
		return 0, err
	}
	return c.replyCode()
}

// SetTrigger sets or clears the trigger of a triggered service.
func (c *Conn) SetTrigger(handle uint32, on bool) (uint8, error) {
	v := byte(0)
	if on {
		v = 1
	}
	if err := c.send([]byte{CmdSetTrigger}, handleBytes(handle), []byte{v}); err != nil {
		return 0, err
	}
	return c.replyCode()
}

// CatLog returns the buffered output of a service; flags bit 0 clears the
// buffer afterwards.
func (c *Conn) CatLog(flags uint8, handle uint32) (code uint8, data []byte, err error) {
	if err = c.send([]byte{CmdCatLog, flags}, handleBytes(handle)); err != nil {
		return
	}
	if code, err = c.replyCode(); err != nil || code != RplyServiceLog {
		return
	}
	hd, err := c.readN(5) // flags, length (unsigned)
	if err != nil {
		return
	}
	n := binary.LittleEndian.Uint32(hd[1:])
	if n > 1<<28 {
		return code, nil, fmt.Errorf("dinit: implausible log size %d", n)
	}
	data, err = c.readN(int(n))
	return
}

// Signal sends a signal to a service's process.
func (c *Conn) Signal(sig int32, handle uint32) (uint8, error) {
	sb := make([]byte, 4)
	binary.LittleEndian.PutUint32(sb, uint32(sig))
	if err := c.send([]byte{CmdSignal}, sb, handleBytes(handle)); err != nil {
		return 0, err
	}
	return c.replyCode()
}

// QueryServiceDscDir returns the directory a service's description was
// loaded from.
func (c *Conn) QueryServiceDscDir(handle uint32) (code uint8, dir string, err error) {
	if err = c.send([]byte{CmdQueryServiceDscDir, 0}, handleBytes(handle)); err != nil {
		return
	}
	if code, err = c.replyCode(); err != nil || code != RplySvcDscDir {
		return
	}
	lb, err := c.readN(4)
	if err != nil {
		return
	}
	n := binary.LittleEndian.Uint32(lb)
	if n > 1<<20 {
		return code, "", fmt.Errorf("dinit: implausible path length %d", n)
	}
	b, err := c.readN(int(n))
	return code, string(b), err
}

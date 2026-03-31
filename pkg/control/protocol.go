// Package control implements the control socket protocol for slinit,
// enabling runtime management of services via Unix domain sockets.
// The binary protocol is inspired by dinit's control protocol.
package control

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// encodeStatusFlags returns common status flags for a service.
func encodeStatusFlags(svc service.Service) uint8 {
	var flags uint8
	if svc.PID() > 0 {
		flags |= StatusFlagHasPID
	}
	if svc.Record().IsMarkedActive() {
		flags |= StatusFlagMarkedActive
	}
	if svc.Record().HasConsole() {
		flags |= StatusFlagHasConsole
	}
	if svc.Record().DidStartFail() {
		flags |= StatusFlagStartFailed
	}
	return flags
}

// Protocol versioning for slinit control protocol.
// CPVersion is the current protocol version implemented by this build.
// MinCompatVersion is the minimum version a peer must support.
// Version reply format: min_compat(2) + actual_version(2) = 4 bytes.
const (
	CPVersion        uint16 = 6
	MinCompatVersion uint16 = 1
)

// Command codes (client → server).
// Numbers 0–28 match dinit's cp_cmd enum for wire compatibility.
const (
	CmdQueryVersion       uint8 = 0
	CmdFindService        uint8 = 1
	CmdLoadService        uint8 = 2
	CmdStartService       uint8 = 3
	CmdStopService        uint8 = 4
	CmdWakeService        uint8 = 5
	CmdReleaseService     uint8 = 6
	CmdUnpinService       uint8 = 7
	CmdListServices       uint8 = 8  // deprecated, use CmdListServices5
	CmdUnloadService      uint8 = 9
	CmdShutdown           uint8 = 10
	CmdAddDep             uint8 = 11
	CmdRmDep              uint8 = 12
	CmdQueryLoadMech      uint8 = 13
	CmdEnableService      uint8 = 14
	CmdQueryServiceName   uint8 = 15
	CmdReloadService      uint8 = 16
	CmdSetEnv             uint8 = 17
	CmdServiceStatus      uint8 = 18 // deprecated, use CmdServiceStatus5
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

	// slinit extensions (beyond dinit's range)
	CmdBootTime        uint8 = 40
	CmdDisableService  uint8 = 41
	CmdQueryDependents uint8 = 42
)

// Reply codes (server → client).
// Numbers 50–79 match dinit's cp_rply enum for wire compatibility.
const (
	RplyACK             uint8 = 50
	RplyNAK             uint8 = 51
	RplyBadReq          uint8 = 52
	RplyOOM             uint8 = 53
	RplyServiceLoadErr  uint8 = 54
	RplyServiceOOM      uint8 = 55
	RplyCPVersion       uint8 = 58
	RplyServiceRecord   uint8 = 59
	RplyNoService       uint8 = 60
	RplyAlreadySS       uint8 = 61
	RplySvcInfo         uint8 = 62
	RplyListDone        uint8 = 63
	RplyLoaderMech      uint8 = 64 // dinit: LOADER_MECH
	RplyDependents      uint8 = 65 // dinit: DEPENDENTS
	RplyServiceName2    uint8 = 66 // dinit: SERVICENAME (from handle)
	RplyPinnedStopped   uint8 = 67
	RplyPinnedStarted   uint8 = 68
	RplyShuttingDown    uint8 = 69
	RplyServiceStatus   uint8 = 70
	RplyServiceDescErr  uint8 = 71
	RplyServiceLoadErr2 uint8 = 72
	RplySvcLog          uint8 = 73
	RplySignalNoPID     uint8 = 74
	RplySignalBadSig    uint8 = 75
	RplySignalErr       uint8 = 76
	RplyServiceDscDir   uint8 = 77 // dinit: SVCDSCDIR
	RplyEnvList         uint8 = 78 // dinit: ALLENV
	RplyPreACK          uint8 = 79 // dinit: PREACK

	// slinit extensions (beyond dinit's range)
	RplyBootTime      uint8 = 90
	RplyNotStopped    uint8 = 91
	RplyServiceName   uint8 = 92 // slinit query-name reply
)

// Info codes (server → client, unsolicited).
const (
	InfoServiceEvent  uint8 = 100
	InfoServiceEvent5 uint8 = 101
	InfoEnvEvent      uint8 = 102
)

// ServiceEvent codes (matches service.ServiceEvent).
const (
	SvcEventStarted       uint8 = 0
	SvcEventStopped       uint8 = 1
	SvcEventFailedStart   uint8 = 2
	SvcEventStartCancelled uint8 = 3
	SvcEventStopCancelled uint8 = 4
)

// Status flags byte bits.
const (
	StatusFlagHasPID        uint8 = 1 << 0
	StatusFlagMarkedActive  uint8 = 1 << 1
	StatusFlagWaitingDeps   uint8 = 1 << 2
	StatusFlagHasConsole    uint8 = 1 << 3
	StatusFlagStartFailed   uint8 = 1 << 4
)

// Packet header: 1-byte command/reply + 2-byte payload length (little-endian).
// Maximum payload size.
const MaxPayloadSize = 65535

// CatLog request flags.
const CatLogFlagClear uint8 = 1 << 0

// WritePacket writes a packet: [type(1)][payloadLen(2)][payload(N)].
// Uses a single write call for small packets to reduce syscall overhead.
func WritePacket(w io.Writer, pktType uint8, payload []byte) error {
	pLen := len(payload)
	if pLen > MaxPayloadSize {
		return fmt.Errorf("payload too large: %d > %d", pLen, MaxPayloadSize)
	}
	// For small packets (<=256 bytes), combine header+payload into one write
	// to reduce syscall count. Most control packets are well under this limit.
	if pLen <= 256 {
		buf := make([]byte, 3+pLen)
		buf[0] = pktType
		binary.LittleEndian.PutUint16(buf[1:], uint16(pLen))
		copy(buf[3:], payload)
		_, err := w.Write(buf)
		return err
	}
	hdr := [3]byte{pktType}
	binary.LittleEndian.PutUint16(hdr[1:], uint16(pLen))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if pLen > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// ReadPacket reads a packet: [type(1)][payloadLen(2)][payload(N)].
func ReadPacket(r io.Reader) (pktType uint8, payload []byte, err error) {
	var hdr [3]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	pktType = hdr[0]
	pLen := binary.LittleEndian.Uint16(hdr[1:])
	if pLen > MaxPayloadSize {
		return 0, nil, fmt.Errorf("payload too large: %d", pLen)
	}
	if pLen > 0 {
		payload = make([]byte, pLen)
		if _, err = io.ReadFull(r, payload); err != nil {
			return 0, nil, err
		}
	}
	return pktType, payload, nil
}

// EncodeServiceName encodes a service name as [len(2)][name(N)].
func EncodeServiceName(name string) []byte {
	b := make([]byte, 2+len(name))
	binary.LittleEndian.PutUint16(b, uint16(len(name)))
	copy(b[2:], name)
	return b
}

// DecodeServiceName decodes a service name from [len(2)][name(N)].
// Returns the name and number of bytes consumed.
func DecodeServiceName(data []byte) (string, int, error) {
	if len(data) < 2 {
		return "", 0, fmt.Errorf("data too short for service name length")
	}
	nameLen := int(binary.LittleEndian.Uint16(data))
	if len(data) < 2+nameLen {
		return "", 0, fmt.Errorf("data too short for service name: need %d, have %d", 2+nameLen, len(data))
	}
	return string(data[2 : 2+nameLen]), 2 + nameLen, nil
}

// EncodeHandle encodes a uint32 handle.
func EncodeHandle(h uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, h)
	return b
}

// DecodeHandle decodes a uint32 handle from data.
func DecodeHandle(data []byte) (uint32, error) {
	if len(data) < 4 {
		return 0, fmt.Errorf("data too short for handle: need 4, have %d", len(data))
	}
	return binary.LittleEndian.Uint32(data), nil
}

// ServiceStatusInfo holds the status information for a service.
type ServiceStatusInfo struct {
	State       service.ServiceState
	TargetState service.ServiceState
	SvcType     service.ServiceType
	Flags       uint8
	PID         int32
	ExitStatus  int32
}

// EncodeServiceStatus encodes service status into bytes.
// Format: state(1) + target(1) + type(1) + flags(1) + pid(4) + exitStatus(4) = 12 bytes.
func EncodeServiceStatus(svc service.Service) []byte {
	buf := make([]byte, 12)
	encodeStatusInto(buf, svc)
	return buf
}

// encodeStatusInto writes 12-byte status encoding into buf (must be >= 12 bytes).
func encodeStatusInto(buf []byte, svc service.Service) {
	buf[0] = uint8(svc.State())
	buf[1] = uint8(svc.TargetState())
	buf[2] = uint8(svc.Type())
	buf[3] = encodeStatusFlags(svc)
	binary.LittleEndian.PutUint32(buf[4:], uint32(int32(svc.PID())))
	es := svc.GetExitStatus()
	binary.LittleEndian.PutUint32(buf[8:], uint32(es.ExitCode()))
}

// DecodeServiceStatus decodes service status from bytes.
func DecodeServiceStatus(data []byte) (ServiceStatusInfo, error) {
	if len(data) < 12 {
		return ServiceStatusInfo{}, fmt.Errorf("data too short for status: need 12, have %d", len(data))
	}
	return ServiceStatusInfo{
		State:       service.ServiceState(data[0]),
		TargetState: service.ServiceState(data[1]),
		SvcType:     service.ServiceType(data[2]),
		Flags:       data[3],
		PID:         int32(binary.LittleEndian.Uint32(data[4:])),
		ExitStatus:  int32(binary.LittleEndian.Uint32(data[8:])),
	}, nil
}

// --- Protocol v5 extended formats ---

// ServiceStatusInfo5 holds extended status information (v5 protocol).
type ServiceStatusInfo5 struct {
	State       service.ServiceState
	TargetState service.ServiceState
	Flags       uint8
	StopReason  uint8
	ExecStage   uint16
	SiCode      int32
	SiStatus    int32
}

// EncodeServiceStatus5 encodes extended service status into 14 bytes.
// Format: state(1) + target(1) + flags(1) + stopReason(1) + execStage(2) + siCode(4) + siStatus(4) = 14 bytes.
func EncodeServiceStatus5(svc service.Service) []byte {
	buf := make([]byte, 14)
	encodeStatus5Into(buf, svc)
	return buf
}

// encodeStatus5Into writes 14-byte v5 status encoding into buf (must be >= 14 bytes).
func encodeStatus5Into(buf []byte, svc service.Service) {
	buf[0] = uint8(svc.State())
	buf[1] = uint8(svc.TargetState())
	buf[2] = encodeStatusFlags(svc)
	buf[3] = uint8(svc.StopReason())

	es := svc.GetExitStatus()
	if es.ExecFailed {
		binary.LittleEndian.PutUint16(buf[4:], uint16(es.ExecStage))
		binary.LittleEndian.PutUint32(buf[6:], uint32(es.ExecErrno))
	} else {
		binary.LittleEndian.PutUint16(buf[4:], 0)
		binary.LittleEndian.PutUint32(buf[6:], uint32(es.SiCode()))
		binary.LittleEndian.PutUint32(buf[10:], uint32(es.SiStatus()))
	}
}

// DecodeServiceStatus5 decodes extended service status from 14 bytes.
func DecodeServiceStatus5(data []byte) (ServiceStatusInfo5, error) {
	if len(data) < 14 {
		return ServiceStatusInfo5{}, fmt.Errorf("data too short for status5: need 14, have %d", len(data))
	}
	return ServiceStatusInfo5{
		State:       service.ServiceState(data[0]),
		TargetState: service.ServiceState(data[1]),
		Flags:       data[2],
		StopReason:  data[3],
		ExecStage:   binary.LittleEndian.Uint16(data[4:]),
		SiCode:      int32(binary.LittleEndian.Uint32(data[6:])),
		SiStatus:    int32(binary.LittleEndian.Uint32(data[10:])),
	}, nil
}

// EncodeSvcInfo5 encodes a v5 service info entry for list command.
// Format: nameLen(2) + name(N) + statusV5(14).
func EncodeSvcInfo5(svc service.Service) []byte {
	name := svc.Name()
	buf := make([]byte, 2+len(name)+14)
	binary.LittleEndian.PutUint16(buf, uint16(len(name)))
	copy(buf[2:], name)
	encodeStatus5Into(buf[2+len(name):], svc)
	return buf
}

// DecodeSvcInfo5 decodes a v5 service info entry.
func DecodeSvcInfo5(data []byte) (SvcInfoEntry5, int, error) {
	name, n, err := DecodeServiceName(data)
	if err != nil {
		return SvcInfoEntry5{}, 0, err
	}
	if len(data) < n+14 {
		return SvcInfoEntry5{}, 0, fmt.Errorf("data too short for svc info5")
	}
	status, err := DecodeServiceStatus5(data[n:])
	if err != nil {
		return SvcInfoEntry5{}, 0, err
	}
	return SvcInfoEntry5{
		Name:   name,
		Status: status,
	}, n + 14, nil
}

// SvcInfoEntry5 holds v5 list info for one service.
type SvcInfoEntry5 struct {
	Name   string
	Status ServiceStatusInfo5
}

// EncodeServiceEvent5 encodes a v5 service event push notification.
// Format: handle(4) + event(1) + statusV5(14) = 19 bytes.
func EncodeServiceEvent5(handle uint32, event uint8, svc service.Service) []byte {
	buf := make([]byte, 19)
	binary.LittleEndian.PutUint32(buf, handle)
	buf[4] = event
	encodeStatus5Into(buf[5:], svc)
	return buf
}

// DecodeServiceEvent5 decodes a v5 service event.
func DecodeServiceEvent5(data []byte) (handle uint32, event uint8, status ServiceStatusInfo5, err error) {
	if len(data) < 19 {
		err = fmt.Errorf("data too short for service event5: need 19, have %d", len(data))
		return
	}
	handle = binary.LittleEndian.Uint32(data)
	event = data[4]
	status, err = DecodeServiceStatus5(data[5:])
	return
}

// SvcInfoEntry holds list info for one service.
type SvcInfoEntry struct {
	Name        string
	State       service.ServiceState
	TargetState service.ServiceState
	SvcType     service.ServiceType
	Flags       uint8
	PID         int32
}

// EncodeSvcInfo encodes a service info entry for list command.
// Format: nameLen(2) + name(N) + state(1) + target(1) + type(1) + flags(1) + pid(4).
func EncodeSvcInfo(svc service.Service) []byte {
	name := svc.Name()
	buf := make([]byte, 2+len(name)+8)
	binary.LittleEndian.PutUint16(buf, uint16(len(name)))
	copy(buf[2:], name)
	off := 2 + len(name)
	buf[off] = uint8(svc.State())
	buf[off+1] = uint8(svc.TargetState())
	buf[off+2] = uint8(svc.Type())
	buf[off+3] = encodeStatusFlags(svc)
	binary.LittleEndian.PutUint32(buf[off+4:], uint32(int32(svc.PID())))
	return buf
}

// DecodeSvcInfo decodes a service info entry.
func DecodeSvcInfo(data []byte) (SvcInfoEntry, int, error) {
	name, n, err := DecodeServiceName(data)
	if err != nil {
		return SvcInfoEntry{}, 0, err
	}
	if len(data) < n+8 {
		return SvcInfoEntry{}, 0, fmt.Errorf("data too short for svc info")
	}
	entry := SvcInfoEntry{
		Name:        name,
		State:       service.ServiceState(data[n]),
		TargetState: service.ServiceState(data[n+1]),
		SvcType:     service.ServiceType(data[n+2]),
		Flags:       data[n+3],
		PID:         int32(binary.LittleEndian.Uint32(data[n+4:])),
	}
	return entry, n + 8, nil
}

// --- Boot timing protocol ---

// BootTimeEntry holds timing data for one service.
type BootTimeEntry struct {
	Name      string
	StartupNs int64 // startup duration in nanoseconds
	State     service.ServiceState
	SvcType   service.ServiceType
	PID       int32
}

// BootTimeInfo holds the complete boot timing data.
type BootTimeInfo struct {
	KernelUptimeNs int64
	BootStartNs    int64
	BootReadyNs    int64 // 0 if boot service hasn't reached STARTED yet
	BootSvcName    string
	Services       []BootTimeEntry
}

// EncodeBootTime encodes boot timing info into bytes.
// Wire format: kernelUptime(8) + bootStart(8) + bootReady(8) +
// nameLen(2) + name(N) + numSvcs(2) +
// [per svc: nameLen(2) + name(N) + startupNs(8) + state(1) + type(1) + pid(4)]
func EncodeBootTime(info BootTimeInfo) []byte {
	// Calculate total size
	size := 8 + 8 + 8 + 2 + len(info.BootSvcName) + 2
	for _, s := range info.Services {
		size += 2 + len(s.Name) + 8 + 1 + 1 + 4
	}

	buf := make([]byte, size)
	off := 0

	binary.LittleEndian.PutUint64(buf[off:], uint64(info.KernelUptimeNs))
	off += 8
	binary.LittleEndian.PutUint64(buf[off:], uint64(info.BootStartNs))
	off += 8
	binary.LittleEndian.PutUint64(buf[off:], uint64(info.BootReadyNs))
	off += 8

	binary.LittleEndian.PutUint16(buf[off:], uint16(len(info.BootSvcName)))
	off += 2
	copy(buf[off:], info.BootSvcName)
	off += len(info.BootSvcName)

	binary.LittleEndian.PutUint16(buf[off:], uint16(len(info.Services)))
	off += 2

	for _, s := range info.Services {
		binary.LittleEndian.PutUint16(buf[off:], uint16(len(s.Name)))
		off += 2
		copy(buf[off:], s.Name)
		off += len(s.Name)
		binary.LittleEndian.PutUint64(buf[off:], uint64(s.StartupNs))
		off += 8
		buf[off] = uint8(s.State)
		off++
		buf[off] = uint8(s.SvcType)
		off++
		binary.LittleEndian.PutUint32(buf[off:], uint32(s.PID))
		off += 4
	}

	return buf
}

// DecodeBootTime decodes boot timing info from bytes.
func DecodeBootTime(data []byte) (BootTimeInfo, error) {
	if len(data) < 28 {
		return BootTimeInfo{}, fmt.Errorf("data too short for boot time header")
	}

	var info BootTimeInfo
	off := 0

	info.KernelUptimeNs = int64(binary.LittleEndian.Uint64(data[off:]))
	off += 8
	info.BootStartNs = int64(binary.LittleEndian.Uint64(data[off:]))
	off += 8
	info.BootReadyNs = int64(binary.LittleEndian.Uint64(data[off:]))
	off += 8

	if len(data) < off+2 {
		return BootTimeInfo{}, fmt.Errorf("data too short for boot service name length")
	}
	nameLen := int(binary.LittleEndian.Uint16(data[off:]))
	off += 2
	if len(data) < off+nameLen {
		return BootTimeInfo{}, fmt.Errorf("data too short for boot service name")
	}
	info.BootSvcName = string(data[off : off+nameLen])
	off += nameLen

	if len(data) < off+2 {
		return BootTimeInfo{}, fmt.Errorf("data too short for service count")
	}
	numSvcs := int(binary.LittleEndian.Uint16(data[off:]))
	off += 2

	info.Services = make([]BootTimeEntry, 0, numSvcs)
	for i := 0; i < numSvcs; i++ {
		if len(data) < off+2 {
			return BootTimeInfo{}, fmt.Errorf("data too short for service %d name length", i)
		}
		sNameLen := int(binary.LittleEndian.Uint16(data[off:]))
		off += 2
		if len(data) < off+sNameLen+14 {
			return BootTimeInfo{}, fmt.Errorf("data too short for service %d", i)
		}
		entry := BootTimeEntry{
			Name:      string(data[off : off+sNameLen]),
			StartupNs: int64(binary.LittleEndian.Uint64(data[off+sNameLen:])),
			State:     service.ServiceState(data[off+sNameLen+8]),
			SvcType:   service.ServiceType(data[off+sNameLen+9]),
			PID:       int32(binary.LittleEndian.Uint32(data[off+sNameLen+10:])),
		}
		off += sNameLen + 14
		info.Services = append(info.Services, entry)
	}

	return info, nil
}

// --- CatLog protocol ---

// EncodeCatLogRequest encodes a catlog request.
// Wire format: flags(1) + handle(4) = 5 bytes.
func EncodeCatLogRequest(handle uint32, clear bool) []byte {
	buf := make([]byte, 5)
	if clear {
		buf[0] = CatLogFlagClear
	}
	binary.LittleEndian.PutUint32(buf[1:], handle)
	return buf
}

// DecodeCatLogRequest decodes a catlog request.
func DecodeCatLogRequest(data []byte) (flags uint8, handle uint32, err error) {
	if len(data) < 5 {
		return 0, 0, fmt.Errorf("data too short for catlog request")
	}
	return data[0], binary.LittleEndian.Uint32(data[1:]), nil
}

// EncodeSvcLog encodes a service log response.
// Wire format: flags(1) + bufLen(4) + buffer(N).
func EncodeSvcLog(logData []byte) []byte {
	buf := make([]byte, 1+4+len(logData))
	buf[0] = 0 // flags reserved
	binary.LittleEndian.PutUint32(buf[1:], uint32(len(logData)))
	copy(buf[5:], logData)
	return buf
}

// DecodeSvcLog decodes a service log response.
func DecodeSvcLog(data []byte) (flags uint8, logData []byte, err error) {
	if len(data) < 5 {
		return 0, nil, fmt.Errorf("data too short for svc log")
	}
	flags = data[0]
	bufLen := binary.LittleEndian.Uint32(data[1:])
	if uint32(len(data)) < 5+bufLen {
		return 0, nil, fmt.Errorf("data too short for log buffer")
	}
	return flags, data[5 : 5+bufLen], nil
}

// --- SetEnv / GetAllEnv protocol ---

// EncodeSetEnv encodes a set-env request.
// Wire format: handle(4) + nameLen(2) + name + valueLen(2) + value.
// valueLen==0 means unset the variable.
func EncodeSetEnv(handle uint32, key, value string, unset bool) []byte {
	valBytes := []byte(value)
	if unset {
		valBytes = nil
	}
	buf := make([]byte, 4+2+len(key)+2+len(valBytes))
	binary.LittleEndian.PutUint32(buf, handle)
	binary.LittleEndian.PutUint16(buf[4:], uint16(len(key)))
	copy(buf[6:], key)
	off := 6 + len(key)
	binary.LittleEndian.PutUint16(buf[off:], uint16(len(valBytes)))
	copy(buf[off+2:], valBytes)
	return buf
}

// DecodeSetEnv decodes a set-env request.
// Returns handle, key, value, isUnset.
func DecodeSetEnv(data []byte) (handle uint32, key, value string, isUnset bool, err error) {
	if len(data) < 8 {
		return 0, "", "", false, fmt.Errorf("data too short for set-env")
	}
	handle = binary.LittleEndian.Uint32(data)
	nameLen := int(binary.LittleEndian.Uint16(data[4:]))
	if len(data) < 6+nameLen+2 {
		return 0, "", "", false, fmt.Errorf("data too short for set-env key")
	}
	key = string(data[6 : 6+nameLen])
	off := 6 + nameLen
	valueLen := int(binary.LittleEndian.Uint16(data[off:]))
	if valueLen == 0 {
		return handle, key, "", true, nil
	}
	if len(data) < off+2+valueLen {
		return 0, "", "", false, fmt.Errorf("data too short for set-env value")
	}
	value = string(data[off+2 : off+2+valueLen])
	return handle, key, value, false, nil
}

// EncodeEnvList encodes a getallenv reply.
// Wire format: count(2) + [nameLen(2) + name + valueLen(2) + value]*N.
func EncodeEnvList(env map[string]string) []byte {
	// Single-pass: grow buffer as we encode, avoiding double iteration.
	// Estimate 32 bytes per entry (typical KEY=value).
	buf := make([]byte, 2, 2+len(env)*32)
	binary.LittleEndian.PutUint16(buf, uint16(len(env)))
	var tmp [2]byte
	for k, v := range env {
		binary.LittleEndian.PutUint16(tmp[:], uint16(len(k)))
		buf = append(buf, tmp[:]...)
		buf = append(buf, k...)
		binary.LittleEndian.PutUint16(tmp[:], uint16(len(v)))
		buf = append(buf, tmp[:]...)
		buf = append(buf, v...)
	}
	return buf
}

// DecodeEnvList decodes a getallenv reply.
func DecodeEnvList(data []byte) (map[string]string, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("data too short for env list")
	}
	count := int(binary.LittleEndian.Uint16(data))
	off := 2
	env := make(map[string]string, count)
	for i := 0; i < count; i++ {
		if len(data) < off+2 {
			return nil, fmt.Errorf("data too short for env key %d", i)
		}
		kLen := int(binary.LittleEndian.Uint16(data[off:]))
		off += 2
		if len(data) < off+kLen+2 {
			return nil, fmt.Errorf("data too short for env key %d value", i)
		}
		key := string(data[off : off+kLen])
		off += kLen
		vLen := int(binary.LittleEndian.Uint16(data[off:]))
		off += 2
		if len(data) < off+vLen {
			return nil, fmt.Errorf("data too short for env value %d", i)
		}
		env[key] = string(data[off : off+vLen])
		off += vLen
	}
	return env, nil
}

// --- AddDep / RmDep protocol ---

// EncodeDepRequest encodes an add-dep or rm-dep request.
// Wire format: handleFrom(4) + handleTo(4) + depType(1).
func EncodeDepRequest(handleFrom, handleTo uint32, depType uint8) []byte {
	buf := make([]byte, 9)
	binary.LittleEndian.PutUint32(buf, handleFrom)
	binary.LittleEndian.PutUint32(buf[4:], handleTo)
	buf[8] = depType
	return buf
}

// DecodeDepRequest decodes an add-dep or rm-dep request.
func DecodeDepRequest(data []byte) (handleFrom, handleTo uint32, depType uint8, err error) {
	if len(data) < 9 {
		return 0, 0, 0, fmt.Errorf("data too short for dep request")
	}
	handleFrom = binary.LittleEndian.Uint32(data)
	handleTo = binary.LittleEndian.Uint32(data[4:])
	depType = data[8]
	return handleFrom, handleTo, depType, nil
}

// --- ServiceEvent protocol ---

// EncodeServiceEvent encodes a service event notification.
// Wire format: handle(4) + event(1) + status(12) = 17 bytes.
func EncodeServiceEvent(handle uint32, event uint8, svc service.Service) []byte {
	buf := make([]byte, 17)
	binary.LittleEndian.PutUint32(buf, handle)
	buf[4] = event
	encodeStatusInto(buf[5:], svc)
	return buf
}

// DecodeServiceEvent decodes a service event notification.
func DecodeServiceEvent(data []byte) (handle uint32, event uint8, status ServiceStatusInfo, err error) {
	if len(data) < 17 {
		return 0, 0, ServiceStatusInfo{}, fmt.Errorf("data too short for service event: need 17, have %d", len(data))
	}
	handle = binary.LittleEndian.Uint32(data)
	event = data[4]
	status, err = DecodeServiceStatus(data[5:])
	return handle, event, status, err
}

// --- EnvEvent protocol ---

// EnvEvent flags.
const (
	EnvEventFlagOverride uint8 = 1 // variable overrides a previous value
)

// EncodeEnvEvent encodes an env change notification.
// Wire format: flags(1) + varLen(2) + varString(N).
// varString is "KEY=VALUE" for set, "KEY" for unset.
func EncodeEnvEvent(varString string, override bool) []byte {
	buf := make([]byte, 1+2+len(varString))
	if override {
		buf[0] = EnvEventFlagOverride
	}
	binary.LittleEndian.PutUint16(buf[1:], uint16(len(varString)))
	copy(buf[3:], varString)
	return buf
}

// DecodeEnvEvent decodes an env change notification.
func DecodeEnvEvent(data []byte) (flags uint8, varString string, err error) {
	if len(data) < 3 {
		return 0, "", fmt.Errorf("data too short for env event")
	}
	flags = data[0]
	varLen := int(binary.LittleEndian.Uint16(data[1:]))
	if len(data) < 3+varLen {
		return 0, "", fmt.Errorf("data too short for env event value")
	}
	return flags, string(data[3 : 3+varLen]), nil
}

// --- Protocol v6 extended formats ---

// ServiceStatusInfo6 holds extended status with file modification timestamp (v6).
type ServiceStatusInfo6 struct {
	ServiceStatusInfo5
	LoadModTime int64 // Unix timestamp (seconds) of description file at load time
}

// EncodeServiceStatus6 encodes v6 service status into 22 bytes.
// Format: statusV5(14) + loadModTime(8) = 22 bytes.
func EncodeServiceStatus6(svc service.Service) []byte {
	buf := make([]byte, 22)
	copy(buf, EncodeServiceStatus5(svc))
	modTime := svc.Record().LoadModTime()
	if !modTime.IsZero() {
		binary.LittleEndian.PutUint64(buf[14:], uint64(modTime.Unix()))
	}
	return buf
}

// DecodeServiceStatus6 decodes v6 service status from 22 bytes.
func DecodeServiceStatus6(data []byte) (ServiceStatusInfo6, error) {
	if len(data) < 22 {
		return ServiceStatusInfo6{}, fmt.Errorf("data too short for status6: need 22, have %d", len(data))
	}
	s5, err := DecodeServiceStatus5(data)
	if err != nil {
		return ServiceStatusInfo6{}, err
	}
	return ServiceStatusInfo6{
		ServiceStatusInfo5: s5,
		LoadModTime:        int64(binary.LittleEndian.Uint64(data[14:])),
	}, nil
}

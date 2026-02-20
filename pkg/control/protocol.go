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

// Protocol version for slinit control protocol.
const ProtocolVersion uint16 = 1

// Command codes (client → server).
const (
	CmdQueryVersion  uint8 = 0
	CmdFindService   uint8 = 1
	CmdLoadService   uint8 = 2
	CmdStartService  uint8 = 3
	CmdStopService   uint8 = 4
	CmdWakeService   uint8 = 5
	CmdReleaseService uint8 = 6
	CmdUnpinService  uint8 = 7
	CmdListServices  uint8 = 8
	CmdShutdown      uint8 = 10
	CmdServiceStatus uint8 = 18
	CmdSetTrigger    uint8 = 19
	CmdSignal        uint8 = 21
	CmdCloseHandle   uint8 = 23
)

// Reply codes (server → client).
const (
	RplyACK           uint8 = 50
	RplyNAK           uint8 = 51
	RplyBadReq        uint8 = 52
	RplyCPVersion     uint8 = 58
	RplyServiceRecord uint8 = 59
	RplyNoService     uint8 = 60
	RplyAlreadySS     uint8 = 61
	RplySvcInfo       uint8 = 62
	RplyListDone      uint8 = 63
	RplyShuttingDown  uint8 = 69
	RplyServiceStatus uint8 = 70
	RplySignalNoPID   uint8 = 74
	RplySignalBadSig  uint8 = 75
	RplySignalErr     uint8 = 76
)

// Info codes (server → client, unsolicited).
const (
	InfoServiceEvent uint8 = 100
)

// Status flags byte bits.
const (
	StatusFlagHasPID        uint8 = 1 << 0
	StatusFlagMarkedActive  uint8 = 1 << 1
	StatusFlagWaitingDeps   uint8 = 1 << 2
)

// Packet header: 1-byte command/reply + 2-byte payload length (little-endian).
// Maximum payload size.
const MaxPayloadSize = 4096

// WritePacket writes a packet: [type(1)][payloadLen(2)][payload(N)].
func WritePacket(w io.Writer, pktType uint8, payload []byte) error {
	if len(payload) > MaxPayloadSize {
		return fmt.Errorf("payload too large: %d > %d", len(payload), MaxPayloadSize)
	}
	hdr := [3]byte{pktType}
	binary.LittleEndian.PutUint16(hdr[1:], uint16(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
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
	buf[0] = uint8(svc.State())
	buf[1] = uint8(svc.TargetState())
	buf[2] = uint8(svc.Type())

	var flags uint8
	pid := svc.PID()
	if pid > 0 {
		flags |= StatusFlagHasPID
	}
	buf[3] = flags
	binary.LittleEndian.PutUint32(buf[4:], uint32(int32(pid)))

	es := svc.GetExitStatus()
	binary.LittleEndian.PutUint32(buf[8:], uint32(es.ExitCode()))

	return buf
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

// SvcInfoEntry holds list info for one service.
type SvcInfoEntry struct {
	Name        string
	State       service.ServiceState
	TargetState service.ServiceState
	SvcType     service.ServiceType
	PID         int32
}

// EncodeSvcInfo encodes a service info entry for list command.
// Format: nameLen(2) + name(N) + state(1) + target(1) + type(1) + pid(4).
func EncodeSvcInfo(svc service.Service) []byte {
	name := svc.Name()
	buf := make([]byte, 2+len(name)+7)
	binary.LittleEndian.PutUint16(buf, uint16(len(name)))
	copy(buf[2:], name)
	off := 2 + len(name)
	buf[off] = uint8(svc.State())
	buf[off+1] = uint8(svc.TargetState())
	buf[off+2] = uint8(svc.Type())
	binary.LittleEndian.PutUint32(buf[off+3:], uint32(int32(svc.PID())))
	return buf
}

// DecodeSvcInfo decodes a service info entry.
func DecodeSvcInfo(data []byte) (SvcInfoEntry, int, error) {
	name, n, err := DecodeServiceName(data)
	if err != nil {
		return SvcInfoEntry{}, 0, err
	}
	if len(data) < n+7 {
		return SvcInfoEntry{}, 0, fmt.Errorf("data too short for svc info")
	}
	entry := SvcInfoEntry{
		Name:        name,
		State:       service.ServiceState(data[n]),
		TargetState: service.ServiceState(data[n+1]),
		SvcType:     service.ServiceType(data[n+2]),
		PID:         int32(binary.LittleEndian.Uint32(data[n+3:])),
	}
	return entry, n + 7, nil
}

// Package autofs provides a Go interface to the Linux kernel autofs4
// protocol for implementing lazy on-demand filesystem mounts.
package autofs

import (
	"encoding/binary"
	"fmt"
	"unsafe"
)

// Autofs protocol version constants.
const (
	ProtoVersion    = 5
	MinProtoVersion = 5
	MaxProtoVersion = 5
	ProtoSubVersion = 6
)

// Autofs ioctl magic number and command base.
const (
	iocMagic = 0x93
)

// Autofs ioctl commands.
// Computed using standard Linux ioctl encoding:
//
//	_IO(type, nr)           = (type<<8 | nr)
//	_IOR(type, nr, size)    = (2<<30 | size<<16 | type<<8 | nr)
//	_IOW(type, nr, size)    = (1<<30 | size<<16 | type<<8 | nr)
var (
	// Size of an int/pointer for ioctl encoding (platform-dependent).
	ioctlIntSize = uint32(unsafe.Sizeof(int(0)))

	AUTOFS_IOC_READY        = uintptr(iocMagic<<8 | 0x60)                                        // _IO(0x93, 0x60)
	AUTOFS_IOC_FAIL         = uintptr(iocMagic<<8 | 0x61)                                        // _IO(0x93, 0x61)
	AUTOFS_IOC_CATATONIC    = uintptr(iocMagic<<8 | 0x62)                                        // _IO(0x93, 0x62)
	AUTOFS_IOC_PROTOVER     = uintptr(2<<30 | uint32(ioctlIntSize)<<16 | iocMagic<<8 | 0x63)     // _IOR(0x93, 0x63, int)
	AUTOFS_IOC_SETTIMEOUT   = uintptr(3<<30 | uint32(unsafe.Sizeof(uint64(0)))<<16 | iocMagic<<8 | 0x64) // _IOWR(0x93, 0x64, ulong)
	AUTOFS_IOC_EXPIRE       = uintptr(2<<30 | uint32(unsafe.Sizeof(AutofsExpireArgs{}))<<16 | iocMagic<<8 | 0x65) // _IOR(0x93, 0x65, autofs_expire)
	AUTOFS_IOC_EXPIRE_MULTI = uintptr(iocMagic<<8 | 0x66)                                        // _IOW(0x93, 0x66, int) -- but arg is 0/flags, use _IO
)

// Packet types in autofs v5.
const (
	PktTypeMissing        = 0
	PktTypeExpire         = 1
	PktTypeExpireMulti    = 2
	PktTypeMissingIndirect  = 3
	PktTypeExpireIndirect   = 4
	PktTypeMissingDirect    = 5
	PktTypeExpireDirect     = 6
)

// Mount types for autofs.
const (
	TypeIndirect = "indirect"
	TypeDirect   = "direct"
)

// Expire flags.
const (
	ExpNormal    = 0x00
	ExpImmediate = 0x01
	ExpLeaves    = 0x02
	ExpForced    = 0x04
)

// AutofsExpireArgs is used with AUTOFS_IOC_EXPIRE.
type AutofsExpireArgs struct {
	Len  uint32
	Name [256]byte
}

// V5Packet represents an autofs v5 kernel notification packet.
// The kernel writes this to the pipe when a lookup occurs under the mount point.
type V5Packet struct {
	ProtoVersion   int32
	Type           int32
	WaitQueueToken uint32
	Dev            uint32
	Ino            uint64
	UID            uint32
	GID            uint32
	PID            uint32
	TGID           uint32
	Len            uint32
	Name           [256]byte
}

// V5PacketSize is the wire size of an autofs v5 packet.
const V5PacketSize = 300

// ParseV5Packet decodes a raw autofs v5 packet from the pipe.
func ParseV5Packet(buf []byte) (*V5Packet, error) {
	if len(buf) < V5PacketSize {
		return nil, fmt.Errorf("autofs packet too short: %d < %d", len(buf), V5PacketSize)
	}
	pkt := &V5Packet{}
	pkt.ProtoVersion = int32(binary.LittleEndian.Uint32(buf[0:4]))
	pkt.Type = int32(binary.LittleEndian.Uint32(buf[4:8]))
	pkt.WaitQueueToken = binary.LittleEndian.Uint32(buf[8:12])
	pkt.Dev = binary.LittleEndian.Uint32(buf[12:16])
	pkt.Ino = binary.LittleEndian.Uint64(buf[16:24])
	pkt.UID = binary.LittleEndian.Uint32(buf[24:28])
	pkt.GID = binary.LittleEndian.Uint32(buf[28:32])
	pkt.PID = binary.LittleEndian.Uint32(buf[32:36])
	pkt.TGID = binary.LittleEndian.Uint32(buf[36:40])
	pkt.Len = binary.LittleEndian.Uint32(buf[40:44])
	copy(pkt.Name[:], buf[44:V5PacketSize])
	return pkt, nil
}

// NameString returns the null-terminated name from the packet.
func (p *V5Packet) NameString() string {
	for i, b := range p.Name {
		if b == 0 {
			return string(p.Name[:i])
		}
	}
	return string(p.Name[:])
}

// IsMissing returns true if this is a missing (lookup) packet type.
func (p *V5Packet) IsMissing() bool {
	switch p.Type {
	case PktTypeMissing, PktTypeMissingIndirect, PktTypeMissingDirect:
		return true
	}
	return false
}

// IsExpire returns true if this is an expire packet type.
func (p *V5Packet) IsExpire() bool {
	switch p.Type {
	case PktTypeExpire, PktTypeExpireMulti, PktTypeExpireIndirect, PktTypeExpireDirect:
		return true
	}
	return false
}

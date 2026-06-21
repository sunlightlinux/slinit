package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

// Linux capability syscall data for version 3 (_LINUX_CAPABILITY_VERSION_3,
// dating from kernel 2.6.26). The header tags which version to use; the
// data is two 32-bit words per set (effective/permitted/inheritable)
// covering caps 0-63.
const linuxCapabilityVersion3 uint32 = 0x20080522

type capUserHeader struct {
	version uint32
	pid     int32
}

type capUserData struct {
	effective   uint32
	permitted   uint32
	inheritable uint32
}

// capRaiseInheritable adds capNum to the calling thread's Inheritable
// set, leaving Effective and Permitted unchanged. This is the
// precondition for PR_CAP_AMBIENT_RAISE: the kernel refuses to raise a
// cap into the ambient set if it isn't already in (Permitted ∩
// Inheritable).
func capRaiseInheritable(capNum uintptr) error {
	hdr := capUserHeader{version: linuxCapabilityVersion3, pid: 0}
	var data [2]capUserData

	// capget — read current sets.
	_, _, errno := syscall.Syscall(syscall.SYS_CAPGET,
		uintptr(unsafe.Pointer(&hdr)),
		uintptr(unsafe.Pointer(&data[0])),
		0)
	if errno != 0 {
		return fmt.Errorf("capget: %w", errno)
	}

	idx := capNum / 32
	bit := uint32(1) << (capNum % 32)
	if idx > 1 {
		return fmt.Errorf("cap number %d out of range", capNum)
	}
	data[idx].inheritable |= bit

	_, _, errno = syscall.Syscall(syscall.SYS_CAPSET,
		uintptr(unsafe.Pointer(&hdr)),
		uintptr(unsafe.Pointer(&data[0])),
		0)
	if errno != 0 {
		return fmt.Errorf("capset: %w", errno)
	}
	return nil
}

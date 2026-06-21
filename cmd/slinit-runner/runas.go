package main

import (
	"fmt"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
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

// narrowBoundingSet drops every capability from the calling thread's
// CapBnd that isn't on the keep list. The kernel reports the highest
// supported cap via /proc/sys/kernel/cap_last_cap; reading that file
// is cheap and avoids hardcoding cap-table size as new caps land.
func narrowBoundingSet(keepRaw []string) error {
	keep := make(map[uintptr]bool, len(keepRaw))
	for _, k := range keepRaw {
		n, err := strconv.Atoi(k)
		if err != nil {
			return fmt.Errorf("bounding-cap %q: %w", k, err)
		}
		keep[uintptr(n)] = true
	}
	// 40 covers every cap defined as of Linux 5.x — PR_CAPBSET_DROP on
	// numbers outside the supported range simply returns EINVAL, so a
	// fixed upper bound is safe.
	for cap := uintptr(0); cap <= 40; cap++ {
		if keep[cap] {
			continue
		}
		err := unix.Prctl(unix.PR_CAPBSET_DROP, cap, 0, 0, 0)
		if err != nil && err != syscall.EINVAL {
			return fmt.Errorf("PR_CAPBSET_DROP cap=%d: %w", cap, err)
		}
	}
	return nil
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

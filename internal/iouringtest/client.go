//go:build linux

// Package iouringtest is a minimal io_uring client for tests: one request
// on a fresh ring, no liburing. Only tests import it, so it is not linked
// into any shipped binary.
package iouringtest

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Opcodes the tests use (enum io_uring_op).
const (
	OpNop    = 0
	OpOpenat = 18
	OpSocket = 45
)

// SQE is struct io_uring_sqe, with the fields the tests set.
type SQE struct {
	Opcode    uint8
	Flags     uint8
	Ioprio    uint16
	FD        int32
	Off       uint64
	Addr      uint64
	Len       uint32
	OpenFlags uint32
	UserData  uint64
	_         [24]byte
}

// One submits sqe on a fresh ring and returns its result: a descriptor
// (or 0) on success, -errno on failure. err reports a ring that could not
// be set up or entered.
func One(sqe SQE) (int32, error) {
	var p [120]byte // struct io_uring_params
	fd, _, e := unix.Syscall(unix.SYS_IO_URING_SETUP, 4, uintptr(unsafe.Pointer(&p[0])), 0)
	if e != 0 {
		return 0, fmt.Errorf("io_uring_setup: %w", e)
	}
	defer unix.Close(int(fd))
	u32 := func(off int) uint32 { return *(*uint32)(unsafe.Pointer(&p[off])) }
	sqEntries, cqEntries, features := u32(0), u32(4), u32(20)
	sqTail, sqMask, sqArray := u32(40+4), u32(40+8), u32(40+24)
	cqHead, cqMask, cqCQEs := u32(80+0), u32(80+8), u32(80+20)

	sqSize := int(sqArray) + int(sqEntries)*4
	cqSize := int(cqCQEs) + int(cqEntries)*16
	single := features&1 != 0 // IORING_FEAT_SINGLE_MMAP
	if single && cqSize > sqSize {
		sqSize = cqSize
	}
	prot, flags := unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED|unix.MAP_POPULATE
	sq, err := unix.Mmap(int(fd), 0, sqSize, prot, flags)
	if err != nil {
		return 0, err
	}
	defer unix.Munmap(sq)
	cq := sq
	if !single {
		if cq, err = unix.Mmap(int(fd), 0x8000000, cqSize, prot, flags); err != nil {
			return 0, err
		}
		defer unix.Munmap(cq)
	}
	sqes, err := unix.Mmap(int(fd), 0x10000000, int(sqEntries)*64, prot, flags)
	if err != nil {
		return 0, err
	}
	defer unix.Munmap(sqes)

	tail := (*uint32)(unsafe.Pointer(&sq[sqTail]))
	idx := *tail & *(*uint32)(unsafe.Pointer(&sq[sqMask]))
	*(*SQE)(unsafe.Pointer(&sqes[idx*64])) = sqe
	*(*uint32)(unsafe.Pointer(&sq[int(sqArray)+int(idx)*4])) = idx
	*tail = *tail + 1
	if _, _, e := unix.Syscall6(unix.SYS_IO_URING_ENTER, fd, 1, 1, 1 /* GETEVENTS */, 0, 0); e != 0 {
		return 0, fmt.Errorf("io_uring_enter: %w", e)
	}
	head := *(*uint32)(unsafe.Pointer(&cq[cqHead])) & *(*uint32)(unsafe.Pointer(&cq[cqMask]))
	return *(*int32)(unsafe.Pointer(&cq[int(cqCQEs)+int(head)*16+8])), nil
}

// Socket creates a SOCK_STREAM socket of family af through the ring and
// closes it again.
func Socket(af int) (int32, error) {
	r, err := One(SQE{Opcode: OpSocket, FD: int32(af), Off: unix.SOCK_STREAM})
	if r >= 0 && err == nil {
		unix.Close(int(r))
	}
	return r, err
}

// Create creates path with mode through the ring (IORING_OP_OPENAT with
// O_CREAT) and closes it again.
func Create(path string, mode uint32) (int32, error) {
	b, err := unix.BytePtrFromString(path)
	if err != nil {
		return 0, err
	}
	r, err := One(SQE{Opcode: OpOpenat, FD: unix.AT_FDCWD,
		Addr: uint64(uintptr(unsafe.Pointer(b))), Len: mode, OpenFlags: unix.O_CREAT | unix.O_WRONLY})
	runtime.KeepAlive(b)
	if r >= 0 && err == nil {
		unix.Close(int(r))
	}
	return r, err
}

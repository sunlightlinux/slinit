package bpfdev

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// progName tells slinit's device programs apart from any other on the
// cgroup, so a restart replaces its own and nothing else.
const progName = "slinit_dev"

// attrSize is the bpf_attr passed: larger than any command here reads;
// the kernel accepts a larger union when the tail is zero.
const attrSize = 128

func bpf(cmd int, attr []byte) (int, error) {
	r, _, errno := unix.Syscall(unix.SYS_BPF, uintptr(cmd), uintptr(unsafe.Pointer(&attr[0])), uintptr(len(attr)))
	runtime.KeepAlive(attr)
	if errno != 0 {
		return -1, errno
	}
	return int(r), nil
}

var ne = binary.NativeEndian

func encode(p []Insn) []byte {
	b := make([]byte, 8*len(p))
	for i, in := range p {
		b[8*i] = in.Code
		b[8*i+1] = in.Regs
		ne.PutUint16(b[8*i+2:], uint16(in.Off))
		ne.PutUint32(b[8*i+4:], uint32(in.Imm))
	}
	return b
}

// load loads the program and returns its fd. A program the verifier
// refuses is loaded again with its log, for the error.
func load(p []Insn) (int, error) {
	code := encode(p)
	license := []byte("Apache-2.0\x00")
	try := func(logBuf []byte) (int, error) {
		attr := make([]byte, attrSize)
		ne.PutUint32(attr[0:], unix.BPF_PROG_TYPE_CGROUP_DEVICE)
		ne.PutUint32(attr[4:], uint32(len(p)))
		ne.PutUint64(attr[8:], uint64(uintptr(unsafe.Pointer(&code[0]))))
		ne.PutUint64(attr[16:], uint64(uintptr(unsafe.Pointer(&license[0]))))
		if logBuf != nil {
			ne.PutUint32(attr[24:], 1)
			ne.PutUint32(attr[28:], uint32(len(logBuf)))
			ne.PutUint64(attr[32:], uint64(uintptr(unsafe.Pointer(&logBuf[0]))))
		}
		copy(attr[48:64], progName)
		ne.PutUint32(attr[68:], unix.BPF_CGROUP_DEVICE) // expected_attach_type
		fd, err := bpf(unix.BPF_PROG_LOAD, attr)
		runtime.KeepAlive(code)
		runtime.KeepAlive(license)
		runtime.KeepAlive(logBuf)
		return fd, err
	}
	fd, err := try(nil)
	if err == unix.EACCES || err == unix.EINVAL {
		logBuf := make([]byte, 64<<10)
		if _, err2 := try(logBuf); err2 != nil {
			if i := bytes.IndexByte(logBuf, 0); i > 0 {
				return -1, fmt.Errorf("load device program: %w: %s", err, bytes.TrimSpace(logBuf[:i]))
			}
		}
	}
	if err != nil {
		return -1, fmt.Errorf("load device program: %w", err)
	}
	return fd, nil
}

// ours returns the fds of slinit's device programs on the cgroup.
func ours(cgroupFD int) ([]int, error) {
	ids := make([]uint32, 64)
	attr := make([]byte, attrSize)
	ne.PutUint32(attr[0:], uint32(cgroupFD))
	ne.PutUint32(attr[4:], unix.BPF_CGROUP_DEVICE)
	ne.PutUint64(attr[16:], uint64(uintptr(unsafe.Pointer(&ids[0]))))
	ne.PutUint32(attr[24:], uint32(len(ids)))
	if _, err := bpf(unix.BPF_PROG_QUERY, attr); err != nil {
		return nil, fmt.Errorf("query device programs: %w", err)
	}
	runtime.KeepAlive(ids)
	var fds []int
	for _, id := range ids[:ne.Uint32(attr[24:])] {
		a := make([]byte, attrSize)
		ne.PutUint32(a[0:], id)
		fd, err := bpf(unix.BPF_PROG_GET_FD_BY_ID, a)
		if err != nil {
			continue // detached meanwhile
		}
		info := make([]byte, 256)
		b := make([]byte, attrSize)
		ne.PutUint32(b[0:], uint32(fd))
		ne.PutUint32(b[4:], uint32(len(info)))
		ne.PutUint64(b[8:], uint64(uintptr(unsafe.Pointer(&info[0]))))
		_, err = bpf(unix.BPF_OBJ_GET_INFO_BY_FD, b)
		runtime.KeepAlive(info)
		if err == nil && string(bytes.TrimRight(info[64:80], "\x00")) == progName {
			fds = append(fds, fd)
			continue
		}
		unix.Close(fd)
	}
	return fds, nil
}

func attachCmd(cmd int, cgroupFD, progFD int, flags uint32) error {
	attr := make([]byte, attrSize)
	ne.PutUint32(attr[0:], uint32(cgroupFD))
	ne.PutUint32(attr[4:], uint32(progFD))
	ne.PutUint32(attr[8:], unix.BPF_CGROUP_DEVICE)
	ne.PutUint32(attr[12:], flags)
	_, err := bpf(cmd, attr)
	return err
}

// Attach installs the filter on the cgroup open at cgroupFD, replacing
// a filter slinit attached there before (a restart, a reload that
// changed it): the new one is attached first, so the cgroup is never
// without one. With BPF_F_ALLOW_MULTI, as systemd attaches, so a
// delegated payload may still attach its own below.
func Attach(cgroupFD int, p []Insn) error {
	old, err := ours(cgroupFD)
	if err != nil {
		return err
	}
	defer func() {
		for _, fd := range old {
			unix.Close(fd)
		}
	}()
	fd, err := load(p)
	if err != nil {
		return err
	}
	defer unix.Close(fd) // the attachment keeps the program
	if err := attachCmd(unix.BPF_PROG_ATTACH, cgroupFD, fd, unix.BPF_F_ALLOW_MULTI); err != nil {
		return fmt.Errorf("attach device program: %w", err)
	}
	for _, o := range old {
		if err := attachCmd(unix.BPF_PROG_DETACH, cgroupFD, o, 0); err != nil {
			return fmt.Errorf("detach the previous device program: %w", err)
		}
	}
	return nil
}

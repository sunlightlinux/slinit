// Package ebpf is the little of eBPF slinit uses, over plain bpf(2)
// calls with no library: an instruction type, loading a program,
// attaching it to a cgroup in place of the one slinit attached there
// before, and LPM-trie maps. pkg/bpfdev (the device filter) and
// pkg/bpfip (the IP address filter) build their programs on it.
package ebpf

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Insn is one instruction (struct bpf_insn).
type Insn struct {
	Code uint8
	Regs uint8 // dst in the low nibble, src in the high
	Off  int16
	Imm  int32
}

// Regs packs a destination and a source register.
func Regs(dst, src uint8) uint8 { return dst | src<<4 }

// attrSize is the bpf_attr passed: larger than any command here reads;
// the kernel takes a larger union when the tail is zero.
const attrSize = 128

var ne = binary.NativeEndian

// heap returns a zeroed buffer on the heap. Every buffer whose address
// goes into a bpf_attr comes from here: the address is written there as
// a number, and the Go runtime moves a goroutine's stack when it grows,
// adjusting the pointers it knows of and not that number, so a buffer
// on the stack could be read or written by the kernel at its old,
// freed place. A slice a noinline function returns escapes to the
// heap, which the collector does not move.
//
//go:noinline
func heap(n int) []byte { return make([]byte, n) }

func bpf(cmd int, attr []byte) (int, error) {
	r, _, errno := unix.Syscall(unix.SYS_BPF, uintptr(cmd), uintptr(unsafe.Pointer(&attr[0])), uintptr(len(attr)))
	runtime.KeepAlive(attr)
	if errno != 0 {
		return -1, errno
	}
	return int(r), nil
}

func encode(p []Insn) []byte {
	b := heap(8 * len(p))
	for i, in := range p {
		b[8*i] = in.Code
		b[8*i+1] = in.Regs
		ne.PutUint16(b[8*i+2:], uint16(in.Off))
		ne.PutUint32(b[8*i+4:], uint32(in.Imm))
	}
	return b
}

func ptr(b []byte) uint64 { return uint64(uintptr(unsafe.Pointer(&b[0]))) }

// Load loads a program and returns its fd. name (at most 15 bytes)
// tells slinit's programs apart on a cgroup (Replace). A program the
// verifier refuses is loaded again with its log, for the error.
func Load(progType, attachType uint32, name string, p []Insn) (int, error) {
	if len(name) > 15 {
		return -1, fmt.Errorf("program name %q too long", name)
	}
	code := encode(p)
	license := heap(16)
	copy(license, "Apache-2.0")
	try := func(logBuf []byte) (int, error) {
		attr := heap(attrSize)
		ne.PutUint32(attr[0:], progType)
		ne.PutUint32(attr[4:], uint32(len(p)))
		ne.PutUint64(attr[8:], ptr(code))
		ne.PutUint64(attr[16:], ptr(license))
		if logBuf != nil {
			ne.PutUint32(attr[24:], 1)
			ne.PutUint32(attr[28:], uint32(len(logBuf)))
			ne.PutUint64(attr[32:], ptr(logBuf))
		}
		copy(attr[48:64], name)
		ne.PutUint32(attr[68:], attachType) // expected_attach_type
		fd, err := bpf(unix.BPF_PROG_LOAD, attr)
		runtime.KeepAlive(code)
		runtime.KeepAlive(license)
		runtime.KeepAlive(logBuf)
		return fd, err
	}
	fd, err := try(nil)
	if err == unix.EACCES || err == unix.EINVAL {
		logBuf := heap(64 << 10)
		fd2, err2 := try(logBuf)
		if err2 == nil {
			unix.Close(fd2) // loaded the second time: report the first error
		} else if i := bytes.IndexByte(logBuf, 0); i > 0 {
			return -1, fmt.Errorf("load %s: %w: %s", name, err, bytes.TrimSpace(logBuf[:i]))
		}
	}
	if err != nil {
		return -1, fmt.Errorf("load %s: %w", name, err)
	}
	return fd, nil
}

// named returns the fds of the programs called name attached to the
// cgroup at attachType.
func named(cgroupFD int, attachType uint32, name string) ([]int, error) {
	const maxIDs = 256
	ids := heap(4 * maxIDs)
	attr := heap(attrSize)
	ne.PutUint32(attr[0:], uint32(cgroupFD))
	ne.PutUint32(attr[4:], attachType)
	ne.PutUint64(attr[16:], ptr(ids))
	ne.PutUint32(attr[24:], maxIDs)
	_, err := bpf(unix.BPF_PROG_QUERY, attr)
	runtime.KeepAlive(ids)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", name, err)
	}
	var fds []int
	fail := func(err error) ([]int, error) {
		for _, fd := range fds {
			unix.Close(fd)
		}
		return nil, err
	}
	for i := 0; i < min(int(ne.Uint32(attr[24:])), maxIDs); i++ {
		a := heap(attrSize)
		ne.PutUint32(a[0:], ne.Uint32(ids[4*i:]))
		fd, err := bpf(unix.BPF_PROG_GET_FD_BY_ID, a)
		if err == unix.ENOENT {
			continue // detached meanwhile
		}
		if err != nil {
			// Not knowing whether it is ours would leave an old filter
			// on, stricter or wider than the new one.
			return fail(fmt.Errorf("query %s: program fd: %w", name, err))
		}
		info := heap(256)
		b := heap(attrSize)
		ne.PutUint32(b[0:], uint32(fd))
		ne.PutUint32(b[4:], uint32(len(info)))
		ne.PutUint64(b[8:], ptr(info))
		_, err = bpf(unix.BPF_OBJ_GET_INFO_BY_FD, b)
		runtime.KeepAlive(info)
		if err != nil {
			unix.Close(fd)
			return fail(fmt.Errorf("query %s: program info: %w", name, err))
		}
		if string(bytes.TrimRight(info[64:80], "\x00")) == name {
			fds = append(fds, fd)
			continue
		}
		unix.Close(fd)
	}
	return fds, nil
}

func attachCmd(cmd int, cgroupFD, progFD int, attachType, flags uint32) error {
	attr := heap(attrSize)
	ne.PutUint32(attr[0:], uint32(cgroupFD))
	ne.PutUint32(attr[4:], uint32(progFD))
	ne.PutUint32(attr[8:], attachType)
	ne.PutUint32(attr[12:], flags)
	_, err := bpf(cmd, attr)
	return err
}

// Replace attaches progFD to the cgroup at attachType, with
// BPF_F_ALLOW_MULTI as systemd attaches (a delegated payload may attach
// its own below), then detaches the programs called name attached
// there before: the cgroup is never without one, and a changed program
// takes effect rather than stack on the old.
func Replace(cgroupFD int, attachType uint32, name string, progFD int) error {
	old, err := named(cgroupFD, attachType, name)
	if err != nil {
		return err
	}
	defer func() {
		for _, fd := range old {
			unix.Close(fd)
		}
	}()
	if err := attachCmd(unix.BPF_PROG_ATTACH, cgroupFD, progFD, attachType, unix.BPF_F_ALLOW_MULTI); err != nil {
		return fmt.Errorf("attach %s: %w", name, err)
	}
	for _, o := range old {
		if err := attachCmd(unix.BPF_PROG_DETACH, cgroupFD, o, attachType, 0); err != nil {
			return fmt.Errorf("detach the previous %s: %w", name, err)
		}
	}
	return nil
}

// Detach removes the programs called name from the cgroup at
// attachType: a filter a reload took away.
func Detach(cgroupFD int, attachType uint32, name string) error {
	old, err := named(cgroupFD, attachType, name)
	if err != nil {
		return err
	}
	for _, o := range old {
		err2 := attachCmd(unix.BPF_PROG_DETACH, cgroupFD, o, attachType, 0)
		unix.Close(o)
		if err2 != nil && err == nil {
			err = fmt.Errorf("detach %s: %w", name, err2)
		}
	}
	return err
}

// NewLPMTrie creates a longest-prefix-match map whose keys are a
// 32-bit prefix length and dataSize bytes of address, with 8-byte
// values.
func NewLPMTrie(name string, dataSize, maxEntries int) (int, error) {
	attr := heap(attrSize)
	ne.PutUint32(attr[0:], unix.BPF_MAP_TYPE_LPM_TRIE)
	ne.PutUint32(attr[4:], uint32(4+dataSize))
	ne.PutUint32(attr[8:], 8)
	ne.PutUint32(attr[12:], uint32(maxEntries))
	ne.PutUint32(attr[16:], unix.BPF_F_NO_PREALLOC)
	copy(attr[28:44], name[:min(len(name), 15)])
	fd, err := bpf(unix.BPF_MAP_CREATE, attr)
	if err != nil {
		return -1, fmt.Errorf("create map %s: %w", name, err)
	}
	return fd, nil
}

// LPMKey is a key of an LPM-trie map: the prefix length, then the
// address in network order.
func LPMKey(bits int, addr []byte) []byte {
	k := make([]byte, 4+len(addr))
	ne.PutUint32(k, uint32(bits))
	copy(k[4:], addr)
	return k
}

// MapUpdate sets key to value in the map.
func MapUpdate(fd int, key, value []byte) error {
	k, v := heap(len(key)), heap(len(value))
	copy(k, key)
	copy(v, value)
	key, value = k, v
	attr := heap(attrSize)
	ne.PutUint32(attr[0:], uint32(fd))
	ne.PutUint64(attr[8:], ptr(key))
	ne.PutUint64(attr[16:], ptr(value))
	_, err := bpf(unix.BPF_MAP_UPDATE_ELEM, attr)
	runtime.KeepAlive(key)
	runtime.KeepAlive(value)
	return err
}

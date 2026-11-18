//go:build linux

package seccomp

// io_uring filters.
//
// An io_uring operation does the work of a system call without making it:
// IORING_OP_OPENAT opens a file like openat(2), IORING_OP_SOCKET creates a
// socket like socket(2). A seccomp filter never sees those calls, so a
// service allowed io_uring could do through a ring what system-call-filter,
// restrict-address-families or restrict-suidsgid deny it.
//
// Linux 7.0 lets a task attach classic BPF programs to io_uring operations
// (IORING_REGISTER_BPF_FILTER with the ring fd -1). They see a struct
// io_uring_bpf_ctx instead of seccomp_data, return 0 to refuse a request
// (it fails with EACCES) and 1 to let it through, and are copied into every
// ring the task or its children create, across execve. This is systemd's
// io-uring-filter.c (f80d935027, b29c8d6a1e, d2fe659ff6, 9dcd9ffd89).

import (
	"fmt"
	"runtime"
	"strings"
	"unicode"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	ioringRegisterBPFFilter = 37 // IORING_REGISTER_BPF_FILTER
	ioUringBPFCmdFilter     = 1  // IO_URING_BPF_CMD_FILTER
)

// ioUringBPF is struct io_uring_bpf with its one union member, struct
// io_uring_bpf_filter (linux/io_uring/bpf_filter.h).
type ioUringBPF struct {
	cmdType   uint16
	cmdFlags  uint16
	resv      uint32
	opcode    uint32
	flags     uint32
	filterLen uint32
	pduSize   uint8
	_         [3]uint8
	filterPtr uint64
	_         [5]uint64
}

// Offsets into struct io_uring_bpf_ctx: user_data (8), opcode, sqe_flags,
// pdu_size, pad[5], then the per-operation union.
const (
	ctxSocketFamily = 16 // socket.family (u32)
	ctxOpenFlags    = 16 // open.flags (u64)
	ctxOpenMode     = 24 // open.mode (u64)

	pduSizeSocket = 12 // sizeof socket {family, type, protocol}
	pduSizeOpen   = 24 // sizeof open {flags, mode, resolve}
)

// lowWord is the offset of the low 32 bits of the u64 at off: filters can
// only load 32-bit words, and every bit checked here is in the low half.
func lowWord(off uint32) uint32 {
	var x uint16 = 1
	if *(*byte)(unsafe.Pointer(&x)) == 1 { // little-endian
		return off
	}
	return off + 4
}

// ioUringOps names the operations by opcode (enum io_uring_op), in
// systemd's spelling, which IOUringFilter= accepts.
var ioUringOps = []string{
	"nop", "readv", "writev", "fsync", "read_fixed", "write_fixed",
	"poll_add", "poll_remove", "sync_file_range", "sendmsg", "recvmsg",
	"timeout", "timeout_remove", "accept", "async_cancel", "link_timeout",
	"connect", "fallocate", "openat", "close", "files_update", "statx",
	"read", "write", "fadvise", "madvise", "send", "recv", "openat2",
	"epoll_ctl", "splice", "provide_buffers", "remove_buffers", "tee",
	"shutdown", "renameat", "unlinkat", "mkdirat", "symlinkat", "linkat",
	"msg_ring", "fsetxattr", "setxattr", "fgetxattr", "getxattr", "socket",
	"uring_cmd", "send_zc", "sendmsg_zc", "read_multishot", "waitid",
	"futex_wait", "futex_wake", "futex_waitv", "fixed_fd_install",
	"ftruncate", "bind", "listen", "recv_zc", "epoll_wait", "readv_fixed",
	"writev_fixed", "pipe", "nop128", "uring_cmd128",
}

const (
	ioringOpOpenat  = 18
	ioringOpOpenat2 = 28
	ioringOpSocket  = 45
)

// IOUringOpNumber returns the opcode of an io_uring operation name.
func IOUringOpNumber(name string) (int, bool) {
	for i, n := range ioUringOps {
		if n == name {
			return i, true
		}
	}
	return 0, false
}

// ioUringOpSyscalls maps each operation to the system calls it can do the
// work of. An operation missing here only manages the ring itself.
// IORING_OP_URING_CMD passes commands to drivers like ioctl(2) and runs
// the socket option calls on sockets.
var ioUringOpSyscalls = map[string][]string{
	"readv":           {"readv", "preadv", "preadv2"},
	"writev":          {"writev", "pwritev", "pwritev2"},
	"fsync":           {"fsync", "fdatasync"},
	"read_fixed":      {"read", "pread64"},
	"write_fixed":     {"write", "pwrite64"},
	"poll_add":        {"poll", "ppoll", "ppoll_time64"},
	"sync_file_range": {"sync_file_range", "sync_file_range2"},
	"sendmsg":         {"sendmsg"},
	"recvmsg":         {"recvmsg"},
	"accept":          {"accept", "accept4"},
	"connect":         {"connect"},
	"fallocate":       {"fallocate"},
	"openat":          {"open", "openat"},
	"close":           {"close"},
	"statx":           {"statx"},
	"read":            {"read", "pread64"},
	"write":           {"write", "pwrite64"},
	"fadvise":         {"fadvise64", "fadvise64_64", "arm_fadvise64_64"},
	"madvise":         {"madvise"},
	"send":            {"sendto"},
	"recv":            {"recvfrom"},
	"openat2":         {"openat2"},
	"epoll_ctl":       {"epoll_ctl"},
	"splice":          {"splice"},
	"tee":             {"tee"},
	"shutdown":        {"shutdown"},
	"renameat":        {"rename", "renameat", "renameat2"},
	"unlinkat":        {"unlink", "unlinkat", "rmdir"},
	"mkdirat":         {"mkdir", "mkdirat"},
	"symlinkat":       {"symlink", "symlinkat"},
	"linkat":          {"link", "linkat"},
	"fsetxattr":       {"fsetxattr"},
	"setxattr":        {"setxattr", "setxattrat"},
	"fgetxattr":       {"fgetxattr"},
	"getxattr":        {"getxattr", "getxattrat"},
	"socket":          {"socket"},
	"uring_cmd":       {"ioctl", "setsockopt", "getsockopt", "getsockname"},
	"send_zc":         {"sendto"},
	"sendmsg_zc":      {"sendmsg"},
	"read_multishot":  {"read"},
	"waitid":          {"waitid"},
	"futex_wait":      {"futex", "futex_time64"},
	"futex_wake":      {"futex", "futex_time64"},
	"futex_waitv":     {"futex_waitv"},
	"ftruncate":       {"ftruncate", "ftruncate64"},
	"bind":            {"bind"},
	"listen":          {"listen"},
	"recv_zc":         {"recvfrom"},
	"epoll_wait":      {"epoll_wait", "epoll_pwait", "epoll_pwait2"},
	"readv_fixed":     {"readv", "preadv", "preadv2"},
	"writev_fixed":    {"writev", "pwritev", "pwritev2"},
	"pipe":            {"pipe", "pipe2"},
	"uring_cmd128":    {"ioctl", "setsockopt", "getsockopt", "getsockname"},
}

func registerIOUringFilter(op int, insns []unix.SockFilter, pduSize uint8) error {
	reg := ioUringBPF{
		cmdType:   ioUringBPFCmdFilter,
		opcode:    uint32(op),
		filterLen: uint32(len(insns)),
		pduSize:   pduSize,
		filterPtr: uint64(uintptr(unsafe.Pointer(&insns[0]))),
	}
	ringFD := -1 // -1: attach to the calling task
	_, _, e := unix.Syscall6(unix.SYS_IO_URING_REGISTER, uintptr(ringFD),
		ioringRegisterBPFFilter, uintptr(unsafe.Pointer(&reg)), 1, 0, 0)
	// The kernel reads insns through a uintptr inside reg: keep both
	// alive until the call has returned.
	runtime.KeepAlive(insns)
	runtime.KeepAlive(&reg)
	if e != 0 {
		return e
	}
	return nil
}

// IOUringFilterSupported reports whether the kernel lets this task attach
// io_uring filters. It needs no_new_privs (or CAP_SYS_ADMIN) to be
// meaningful: without it the probe answers EACCES, which reads as "not
// supported". The kernel checks support and permission before it reads
// the filter, so registering from a NULL pointer fails with EFAULT exactly
// when filters are available. Kernels without task filters say EINVAL,
// kernels before 6.2 EBADF, and a seccomp filter around us any error;
// all of those mean no.
//
// systemd probes in a forked child, in case an outer filter kills on
// io_uring_register(). slinit-runner cannot fork a Go child cheaply; if
// an outer filter kills it here, the service fails to start, which is the
// safe way to fail.
func IOUringFilterSupported() bool {
	ringFD := -1
	_, _, e := unix.Syscall6(unix.SYS_IO_URING_REGISTER, uintptr(ringFD),
		ioringRegisterBPFFilter, 0, 1, 0, 0)
	return e == unix.EFAULT
}

var ioUringDenyInsns = []unix.SockFilter{retK(0)}

func retK(k uint32) unix.SockFilter { return unix.SockFilter{Code: bpfRET | bpfK, K: k} }

// RestrictIOUringOps refuses the io_uring operations in ops (opcodes), or
// with allowList every operation not in ops, including the ones the kernel
// implements and this table does not name: opcodes are numbered without
// gaps, so the first one the kernel rejects ends them. An opcode the
// kernel does not implement is skipped; a request using it fails anyway.
func RestrictIOUringOps(ops map[int]bool, allowList bool) error {
	for op := range ioUringOps {
		if ops[op] == allowList {
			continue
		}
		if err := registerIOUringFilter(op, ioUringDenyInsns, 0); err != nil {
			if err == unix.EINVAL {
				continue
			}
			return fmt.Errorf("io_uring: deny opcode %d (%s): %w", op, ioUringOps[op], err)
		}
	}
	if !allowList {
		return nil
	}
	for op := len(ioUringOps); op <= 255; op++ {
		err := registerIOUringFilter(op, ioUringDenyInsns, 0)
		if err == unix.EINVAL {
			break
		}
		if err != nil {
			return fmt.Errorf("io_uring: deny opcode %d: %w", op, err)
		}
	}
	return nil
}

// RestrictIOUringAddressFamilies refuses IORING_OP_SOCKET requests whose
// address family is not in allowed, as restrict-address-families does for
// socket(2). An empty list refuses every family.
func RestrictIOUringAddressFamilies(allowed []int) error {
	if len(allowed) > 200 {
		return fmt.Errorf("io_uring: %d address families, more than a filter can list", len(allowed))
	}
	n := len(allowed)
	insns := []unix.SockFilter{{Code: bpfLD | bpfW | bpfABS, K: ctxSocketFamily}}
	for i, af := range allowed {
		// A match skips the remaining comparisons and the refusal.
		insns = append(insns, unix.SockFilter{Code: bpfJMP | bpfJEQ | bpfK, K: uint32(af), Jt: uint8(n - i), Jf: 0})
	}
	insns = append(insns, retK(0), retK(1))
	if err := registerIOUringFilter(ioringOpSocket, insns, pduSizeSocket); err != nil {
		return fmt.Errorf("io_uring: restrict address families: %w", err)
	}
	return nil
}

// RestrictIOUringSUIDSGID refuses IORING_OP_OPENAT and IORING_OP_OPENAT2
// requests that create a file with the set-user-ID or set-group-ID bit, as
// restrict-suidsgid does for open(2). Unlike openat2(2), whose flags a
// seccomp filter cannot read, the io_uring context carries them, so
// OPENAT2 is checked rather than refused.
func RestrictIOUringSUIDSGID() error {
	insns := []unix.SockFilter{
		{Code: bpfLD | bpfW | bpfABS, K: lowWord(ctxOpenFlags)},
		{Code: bpfJMP | bpfJSET | bpfK, K: oCreatOrTmpfile, Jt: 1, Jf: 0},
		retK(1), // creates nothing
		{Code: bpfLD | bpfW | bpfABS, K: lowWord(ctxOpenMode)},
		{Code: bpfJMP | bpfJSET | bpfK, K: suidSGIDMask, Jt: 0, Jf: 1},
		retK(0),
		retK(1),
	}
	for _, op := range []int{ioringOpOpenat, ioringOpOpenat2} {
		if err := registerIOUringFilter(op, insns, pduSizeOpen); err != nil {
			return fmt.Errorf("io_uring: restrict SUID/SGID (%s): %w", ioUringOps[op], err)
		}
	}
	return nil
}

// Denies reports whether f refuses the named syscall. A log-only filter
// refuses nothing.
func (f *Filter) Denies(name string) bool {
	if f.DefaultAction == ActionLog {
		return false
	}
	listed := false
	for _, s := range f.Syscalls {
		if s == name {
			listed = true
			break
		}
	}
	if f.Mode == ModeAllow {
		return !listed
	}
	return listed
}

// IOUringOpsDeniedBy returns the operations f refuses through the system
// calls they stand for: an operation is refused when f refuses any of its
// syscalls. Only operations this table names are returned — a refused
// request fails with EACCES while the kernel still reports the operation
// as supported, so a program could not fall back from one it does not
// know about. A syscall missing from the native table is not judged.
func IOUringOpsDeniedBy(f *Filter) map[int]bool {
	out := map[int]bool{}
	for op, name := range ioUringOps {
		for _, sc := range ioUringOpSyscalls[name] {
			if _, ok := SyscallNumber(sc); !ok {
				continue
			}
			if f.Denies(sc) {
				out[op] = true
				break
			}
		}
	}
	return out
}

// ParseIOUringFilter turns io-uring-filter assignments into the set of
// operations and whether it is an allow list, with systemd's
// IOUringFilter= semantics (config_parse_io_uring_filter): each
// assignment is a space- or comma-separated list of operation names,
// prefixed with ~ to deny the listed operations rather than allow them.
// The first assignment decides the kind of list; a later assignment of
// the other kind removes its operations from it. An empty assignment
// resets the list.
//
// Unlike systemd, which warns and ignores a name it does not know, an
// unknown name is an error here, as an unknown syscall is for
// system-call-filter: in a deny list a misspelt name would leave that
// operation allowed without a word.
func ParseIOUringFilter(assignments []string) (ops map[int]bool, allowList bool, err error) {
	for _, a := range assignments {
		a = strings.TrimSpace(a)
		if a == "" {
			ops, allowList = nil, false
			continue
		}
		invert := strings.HasPrefix(a, "~")
		a = strings.TrimPrefix(a, "~")
		if ops == nil {
			ops, allowList = map[int]bool{}, !invert
		}
		for _, name := range strings.FieldsFunc(a, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
			op, ok := IOUringOpNumber(name)
			if !ok {
				return nil, false, fmt.Errorf("io-uring-filter: unknown io_uring operation %q", name)
			}
			if invert != allowList {
				ops[op] = true
			} else {
				delete(ops, op)
			}
		}
	}
	return ops, allowList, nil
}

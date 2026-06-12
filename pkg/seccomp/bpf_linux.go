//go:build linux

package seccomp

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// cBPF opcodes we need. These are stable across kernel versions and
// match the values in <linux/bpf_common.h>; defining them here keeps
// the call sites readable.
const (
	bpfLD  = 0x00
	bpfJMP = 0x05
	bpfRET = 0x06

	bpfW   = 0x00 // word (32-bit)
	bpfABS = 0x20 // absolute
	bpfK   = 0x00 // immediate operand

	bpfJEQ = 0x10
	bpfJA  = 0x00 // unconditional (used with bpfJMP+bpfK)
)

// seccomp_data layout offsets (from <linux/seccomp.h>):
//
//	struct seccomp_data {
//	    int  nr;                // offset 0
//	    u32  arch;              // offset 4
//	    u64  instruction_pointer; // 8
//	    u64  args[6];           // 16..63
//	};
const (
	offsetNR   = 0
	offsetArch = 4
)

// Compile turns a Filter into a cBPF program suitable for handing to
// SECCOMP_SET_MODE_FILTER. The structure is:
//
//  1. Load arch (offset 4)
//  2. Compare against each allowed arch; if any match, jump past the
//     "arch reject" return.
//  3. RET KILL_PROCESS (the unconditional arch-reject).
//  4. Load nr (offset 0).
//  5. For each log syscall: if nr==N → RET LOG.
//  6. For each filtered syscall: if nr==N → RET ALLOW (allow mode) or
//     RET <default> (deny mode).
//  7. RET <default-action> (everything else): KILL in allow mode,
//     ALLOW in deny mode.
//
// The function is intentionally straightforward (no jump optimisation)
// because the BPF instruction limit (4096) and JT/JF range (255 each)
// are not a constraint at the syscall counts slinit handles.
func Compile(f *Filter) ([]unix.SockFilter, error) {
	if f == nil {
		return nil, fmt.Errorf("nil filter")
	}
	syscalls := f.SortedSyscallNumbers()
	logSyscalls := f.SortedLogNumbers()

	// Compute the default action for the "non-allowed" side and the
	// per-entry hit action.
	var defaultAction Action
	var perEntryAction Action
	switch f.Mode {
	case ModeAllow:
		perEntryAction = ActionAllow
		defaultAction = f.DefaultAction
		if defaultAction == 0 {
			defaultAction = ActionKill
		}
	case ModeDeny:
		perEntryAction = f.DefaultAction
		if perEntryAction == 0 {
			perEntryAction = ActionKill
		}
		defaultAction = ActionAllow
	default:
		return nil, fmt.Errorf("unknown filter mode %d", f.Mode)
	}

	prog := make([]unix.SockFilter, 0, 16+len(syscalls)+len(logSyscalls))

	// 1. Load arch into A.
	prog = append(prog, unix.SockFilter{
		Code: bpfLD | bpfW | bpfABS, K: offsetArch,
	})

	// 2. For each accepted arch, compare and skip the arch-reject if
	// it matches. The JF field jumps over the *next* arch compare; the
	// JT field jumps far enough forward to skip the arch-reject RET.
	archs := f.Archs
	if len(archs) == 0 {
		archs = []string{NativeArch()}
	}
	// Each arch compare is 1 insn, then 1 arch-reject RET. Total
	// "header" length = len(archs) + 1. The Jt to "past the reject"
	// from compare i is (len(archs)-i).
	for i, arch := range archs {
		n := archNumber(arch)
		jt := uint8(len(archs) - i) // jump past archReject
		prog = append(prog, unix.SockFilter{
			Code: bpfJMP | bpfJEQ | bpfK, K: n, Jt: jt, Jf: 0,
		})
	}
	prog = append(prog, retInsn(ActionKill)) // arch-reject

	// 3. Load nr into A.
	prog = append(prog, unix.SockFilter{
		Code: bpfLD | bpfW | bpfABS, K: offsetNR,
	})

	// 4. Log-only branches come first: a syscall in LogSyscalls always
	// hits ActionLog regardless of the allow/deny mode below. The
	// reasoning is "I want to see this called, then handle filtering
	// separately"; reversing this order would let an allow-branch
	// shadow the log entry silently.
	for _, n := range logSyscalls {
		prog = append(prog, unix.SockFilter{
			Code: bpfJMP | bpfJEQ | bpfK, K: uint32(n), Jt: 0, Jf: 1,
		})
		prog = append(prog, retInsn(ActionLog))
	}

	// 5. Per-syscall branches for the main filter.
	for _, n := range syscalls {
		prog = append(prog, unix.SockFilter{
			Code: bpfJMP | bpfJEQ | bpfK, K: uint32(n), Jt: 0, Jf: 1,
		})
		prog = append(prog, retInsn(perEntryAction))
	}

	// 6. Trailing default action.
	prog = append(prog, retInsn(defaultAction))

	if len(prog) > 4096 {
		return nil, fmt.Errorf("seccomp: program exceeds 4096 instructions (got %d)", len(prog))
	}
	return prog, nil
}

// retInsn encodes a "ret K" instruction with the given action value.
func retInsn(a Action) unix.SockFilter {
	return unix.SockFilter{Code: bpfRET | bpfK, K: uint32(a)}
}

// Install installs prog as the calling task's seccomp filter. The
// caller must already have PR_SET_NO_NEW_PRIVS set (or be root),
// otherwise the kernel rejects the install.
//
// We use the seccomp(2) syscall directly via the unix package wrapper
// so the program is parsed by the new-style API (which supports
// SECCOMP_RET_KILL_PROCESS and the user-notify action). The legacy
// prctl(PR_SET_SECCOMP, SECCOMP_MODE_FILTER) path would also work but
// limits the available return actions on older kernels.
func Install(prog []unix.SockFilter) error {
	if len(prog) == 0 {
		return fmt.Errorf("seccomp: empty program")
	}
	fprog := unix.SockFprog{
		Len:    uint16(len(prog)),
		Filter: &prog[0],
	}
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP,
		uintptr(unix.SECCOMP_SET_MODE_FILTER), 0,
		uintptr(unsafe.Pointer(&fprog)))
	if errno != 0 {
		return fmt.Errorf("seccomp(SET_MODE_FILTER): %w", errno)
	}
	return nil
}

// EnsureNoNewPrivs sets PR_SET_NO_NEW_PRIVS on the calling task. The
// seccomp install requires this for non-root callers and is a no-op if
// already set. We surface errors so the runner can fail closed if the
// kernel refuses the prctl.
func EnsureNoNewPrivs() error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl(PR_SET_NO_NEW_PRIVS): %w", err)
	}
	return nil
}

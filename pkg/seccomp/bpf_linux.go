//go:build linux

package seccomp

import (
	"fmt"
	"slices"
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
	bpfJGE = 0x30
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

// x32SyscallBit marks a syscall made through the x32 ABI. Such a call
// reports AUDIT_ARCH_X86_64, so the arch check lets it through, and its
// number is the x86_64 one with this bit set, so it matches none of the
// x86_64 branches. libseccomp (and so systemd) refuses it unless x32 is
// allowed; slinit does not allow x32.
const x32SyscallBit = 0x40000000

// x32Reject loads nothing: with nr in A, it kills a call whose number has
// the x32 bit (or a higher one) set, and falls through otherwise.
//
// nr -1 is let through first. It is not a call but what a tracer writes
// to cancel one (strace fault injection, a debugger), and what probes
// use; the kernel answers it with ENOSYS. Unsigned, it is above the x32
// bit, so without this every filtered service died of SIGSYS under
// strace -e inject.
func x32Reject() []unix.SockFilter {
	return []unix.SockFilter{
		{Code: bpfJMP | bpfJEQ | bpfK, K: 0xffffffff, Jt: 2, Jf: 0},
		{Code: bpfJMP | bpfJGE | bpfK, K: x32SyscallBit, Jt: 0, Jf: 1},
		retInsn(ActionKill),
	}
}

// Compile turns a Filter into a cBPF program suitable for handing to
// SECCOMP_SET_MODE_FILTER. The structure is:
//
//  1. Load arch (offset 4)
//  2. Compare against the native arch; if it matches and is allowed,
//     jump to the nr load. Another allowed arch jumps to a RET of the
//     filter's refusal action.
//  3. RET KILL_PROCESS (the unconditional arch-reject).
//  4. Load nr (offset 0).
//  5. For each filtered syscall: if nr==N → RET ALLOW (allow mode) or
//     RET <default> (deny mode).
//  6. RET <default-action> (everything else): KILL in allow mode,
//     ALLOW in deny mode.
//
// f.LogSyscalls are not part of this program: CompileLog builds them
// as a filter of their own.
//
// The function is intentionally straightforward (no jump optimisation)
// because the BPF instruction limit (4096) and JT/JF range (255 each)
// are not a constraint at the syscall counts slinit handles.
func Compile(f *Filter) ([]unix.SockFilter, error) {
	if f == nil {
		return nil, fmt.Errorf("nil filter")
	}
	// Every entry must resolve: one skipped would leave a denied call
	// allowed. Build checks names already, but the hardening and
	// io_uring filters are built directly.
	for _, name := range f.Syscalls {
		if _, ok := SyscallNumber(name); !ok && !absentSyscalls[name] {
			return nil, fmt.Errorf("seccomp: syscall %q unknown on %s, refusing to filter without it", name, NativeArch())
		}
	}
	syscalls := f.SortedSyscallNumbers()

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
		// No list is no filter: system-call-architectures,
		// system-call-log or system-call-error-number set alone leave
		// every native syscall allowed, as under systemd, which
		// installs the arch check apart from SystemCallFilter=. An
		// empty allowlist used to refuse every syscall, so
		// system-call-architectures=native killed the service at its
		// first call.
		if len(syscalls) == 0 {
			defaultAction = ActionAllow
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

	prog := make([]unix.SockFilter, 0, 16+len(syscalls))

	// 1. Load arch into A.
	prog = append(prog, unix.SockFilter{
		Code: bpfLD | bpfW | bpfABS, K: offsetArch,
	})

	// 2. Arch dispatch. The syscall branches below use native numbers,
	// so only the native arch may reach them. Another accepted arch
	// (system-call-architectures=native x86) has no table here: every
	// call through it takes the filter's refusal action, failing
	// closed. It used to be dispatched through the native branches,
	// where its numbers mean other calls (i386 mount is x86_64
	// access). Unlisted arches are killed.
	archs := f.Archs
	if len(archs) == 0 {
		archs = []string{NativeArch()}
	}
	nativeListed := false
	var foreign []uint32
	for _, arch := range archs {
		n := archNumber(arch)
		if arch == NativeArch() {
			nativeListed = true
		} else if !slices.Contains(foreign, n) {
			foreign = append(foreign, n)
		}
	}
	refuse := 0
	if len(foreign) > 0 {
		refuse = 1
	}
	if nativeListed {
		// Jump past the foreign compares, the arch reject and the
		// refusal, onto the nr load.
		prog = append(prog, unix.SockFilter{
			Code: bpfJMP | bpfJEQ | bpfK, K: archNumber(NativeArch()),
			Jt: uint8(len(foreign) + 1 + refuse), Jf: 0,
		})
	}
	for i, n := range foreign {
		// Jump past the remaining compares and the arch reject, onto
		// the refusal.
		prog = append(prog, unix.SockFilter{
			Code: bpfJMP | bpfJEQ | bpfK, K: n, Jt: uint8(len(foreign) - i), Jf: 0,
		})
	}
	prog = append(prog, retInsn(ActionKill)) // arch-reject
	if refuse == 1 {
		refuseAction := f.DefaultAction
		if refuseAction == 0 {
			refuseAction = ActionKill
		}
		// With no filter list nothing is being refused, natively or
		// otherwise: system-call-architectures alone only limits which
		// arches may call at all, as systemd installs it.
		if len(syscalls) == 0 {
			refuseAction = ActionAllow
		}
		prog = append(prog, retInsn(refuseAction))
	}

	// 3. Load nr into A; on x86-64, kill an x32 call, which would
	// otherwise miss every branch below and take the deny-mode ALLOW.
	prog = append(prog, unix.SockFilter{
		Code: bpfLD | bpfW | bpfABS, K: offsetNR,
	})
	if NativeArch() == "x86-64" {
		prog = append(prog, x32Reject()...)
	}

	// 4. Per-syscall branches for the main filter.
	for _, n := range syscalls {
		prog = append(prog, unix.SockFilter{
			Code: bpfJMP | bpfJEQ | bpfK, K: uint32(n), Jt: 0, Jf: 1,
		})
		prog = append(prog, retInsn(perEntryAction))
	}

	// 5. Trailing default action.
	prog = append(prog, retInsn(defaultAction))

	if len(prog) > 4096 {
		return nil, fmt.Errorf("seccomp: program exceeds 4096 instructions (got %d)", len(prog))
	}
	return prog, nil
}

// CompileLog builds the system-call-log filter: a native syscall in
// f.LogSyscalls returns SECCOMP_RET_LOG, everything else ALLOW. It is
// installed as a filter of its own, as systemd installs SystemCallLog=
// (exec-invoke.c, apply_syscall_log), so the kernel takes the most
// restrictive answer of it and the main filter: LOG never overrides a
// refusal. The log branches used to sit in front of the main filter's,
// in one program, and a call that was both denied and logged ran. A
// foreign-arch call is left to the main filter.
func CompileLog(f *Filter) ([]unix.SockFilter, error) {
	logSyscalls := f.SortedLogNumbers()
	prog := []unix.SockFilter{
		{Code: bpfLD | bpfW | bpfABS, K: offsetArch},
		{Code: bpfJMP | bpfJEQ | bpfK, K: archNumber(NativeArch()), Jt: 1, Jf: 0},
		retInsn(ActionAllow),
		{Code: bpfLD | bpfW | bpfABS, K: offsetNR},
	}
	for _, n := range logSyscalls {
		prog = append(prog, unix.SockFilter{
			Code: bpfJMP | bpfJEQ | bpfK, K: uint32(n), Jt: 0, Jf: 1,
		})
		prog = append(prog, retInsn(ActionLog))
	}
	prog = append(prog, retInsn(ActionAllow))
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

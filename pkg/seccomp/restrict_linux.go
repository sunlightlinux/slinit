//go:build linux

package seccomp

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Argument-checking BPF fragments for the systemd Restrict*/Memory*
// hardening cluster. Each returns a self-contained seccomp program that
// the runner installs alongside the operator's system-call-filter. The
// kernel runs every loaded filter on each syscall and picks the most
// restrictive result, so these compose cleanly with any other filter.
//
// Only the classic seccomp-BPF path is used (no BPF-LSM). Where systemd
// depends on BPF-LSM for a string-argument check (RestrictFileSystems=
// inspects the fsname passed to mount(2)), slinit uses the most-
// restrictive interpretation available in classic BPF: deny every mount
// syscall outright. Documented in each helper's comment.

// Extra cBPF opcodes used only by the arg-checking helpers.
const (
	bpfALU  = 0x04
	bpfAND  = 0x50
	bpfJSET = 0x40
)

// loadArgLow32 emits an LD.W.ABS that pulls the low 32 bits of
// seccomp_data.args[i] into the accumulator. seccomp_data.args is at
// offset 16, each entry 8 bytes; little-endian so the low word is the
// first 4 bytes.
func loadArgLow32(i int) unix.SockFilter {
	return unix.SockFilter{
		Code: bpfLD | bpfW | bpfABS,
		K:    uint32(16 + i*8),
	}
}

func loadNR() unix.SockFilter {
	return unix.SockFilter{Code: bpfLD | bpfW | bpfABS, K: offsetNR}
}

func loadArch() unix.SockFilter {
	return unix.SockFilter{Code: bpfLD | bpfW | bpfABS, K: offsetArch}
}

// jne emits "jump forward by `skip` if A != K" using JEQ with reversed
// arms — the natural way to say "skip this block when the compare
// misses" in cBPF.
func jne(k uint32, skip uint8) unix.SockFilter {
	return unix.SockFilter{Code: bpfJMP | bpfJEQ | bpfK, K: k, Jt: 0, Jf: skip}
}

// jsetFall guards a KILL insn that follows immediately: fall-through
// on bits-set (Jt=0 lands on the KILL), and skip forward `skipOnClear`
// insns on bits-clear (Jf jumps past the KILL and any per-syscall
// trailer). Chosen this shape because every restrict-* fragment kills
// on match; centralising the polarity avoids off-by-one bugs at every
// call site.
func jsetFall(k uint32, skipOnClear uint8) unix.SockFilter {
	return unix.SockFilter{Code: bpfJMP | bpfJSET | bpfK, K: k, Jt: 0, Jf: skipOnClear}
}

// alu_and emits A = A & K.
func aluAnd(k uint32) unix.SockFilter {
	return unix.SockFilter{Code: bpfALU | bpfAND | bpfK, K: k}
}

// archHeader emits the standard arch-check prelude used by every
// helper here: reject on wrong arch, then leave A pointing at nr for
// the caller's per-syscall dispatch.
func archHeader() []unix.SockFilter {
	return []unix.SockFilter{
		loadArch(),
		// If arch matches native, jump past the reject.
		{Code: bpfJMP | bpfJEQ | bpfK, K: archNumber(NativeArch()), Jt: 1, Jf: 0},
		retInsn(ActionKill),
		loadNR(),
	}
}

// CompileRestrictRealtime builds a filter that denies
// sched_setscheduler(_, policy, _) when policy is SCHED_FIFO, SCHED_RR,
// or SCHED_DEADLINE, with or without SCHED_RESET_ON_FORK.
// sched_setattr uses a struct pointer — the kernel can't safely read
// arbitrary user pointers in seccomp, so we blanket-deny that syscall.
// Same reasoning as systemd's implementation.
func CompileRestrictRealtime() ([]unix.SockFilter, error) {
	nrSchedSet, ok := SyscallNumber("sched_setscheduler")
	if !ok {
		return nil, fmt.Errorf("restrict-realtime: sched_setscheduler unknown")
	}
	nrSchedAttr, hasAttr := SyscallNumber("sched_setattr")

	prog := archHeader()

	// If nr != sched_setscheduler, jump ahead to the sched_setattr
	// dispatch. Layout below the dispatch is fixed to 6 insns for
	// sched_setscheduler (load arg[1] → AND → 3× JEQ→KILL → RET ALLOW),
	// so "skip past setscheduler" is 6 insns.
	prog = append(prog, jne(uint32(nrSchedSet), 6))

	// arg[1] is the policy int. Mask out SCHED_RESET_ON_FORK (0x40000000).
	prog = append(prog,
		loadArgLow32(1),
		aluAnd(^uint32(0x40000000)),
		// SCHED_FIFO=1, SCHED_RR=2, SCHED_DEADLINE=6. Each JEQ→KILL is
		// jt=0 skip-target of the next non-KILL insn; use jt to jump
		// forward to the shared KILL below.
		unix.SockFilter{Code: bpfJMP | bpfJEQ | bpfK, K: 1, Jt: 2, Jf: 0}, // FIFO → KILL
		unix.SockFilter{Code: bpfJMP | bpfJEQ | bpfK, K: 2, Jt: 1, Jf: 0}, // RR → KILL
		unix.SockFilter{Code: bpfJMP | bpfJEQ | bpfK, K: 6, Jt: 0, Jf: 1}, // DEADLINE → KILL (fallthrough for non-match)
		retInsn(ActionKill),
	)

	// sched_setattr blanket deny — struct pointer, can't inspect.
	if hasAttr {
		prog = append(prog,
			loadNR(),
			jne(uint32(nrSchedAttr), 1),
			retInsn(ActionKill),
		)
	}

	prog = append(prog, retInsn(ActionAllow))
	return prog, nil
}

// CompileRestrictNamespaces builds a filter that denies unshare/setns/
// clone when the flags include any CLONE_NEW* bit, and blanket-denies
// clone3 (its args live behind a user pointer). The CLONE_NEW* mask is
// the union of every namespace flag defined at time of writing.
func CompileRestrictNamespaces() ([]unix.SockFilter, error) {
	const cloneNewMask = uint32(0 |
		0x00000080 | // CLONE_NEWTIME
		0x00020000 | // CLONE_NEWNS
		0x02000000 | // CLONE_NEWCGROUP
		0x04000000 | // CLONE_NEWUTS
		0x08000000 | // CLONE_NEWIPC
		0x10000000 | // CLONE_NEWUSER
		0x20000000 | // CLONE_NEWPID
		0x40000000) // CLONE_NEWNET

	syscalls := map[string]int{}
	for _, name := range []string{"unshare", "setns", "clone", "clone3"} {
		if n, ok := SyscallNumber(name); ok {
			syscalls[name] = n
		}
	}

	prog := archHeader()

	// unshare(flags) — flags in arg[0].
	if n, ok := syscalls["unshare"]; ok {
		prog = append(prog,
			jne(uint32(n), 3),
			loadArgLow32(0),
			jsetFall(cloneNewMask, 1),
			retInsn(ActionKill),
		)
		prog = append(prog, loadNR())
	}
	// setns(fd, nstype) — nstype in arg[1].
	if n, ok := syscalls["setns"]; ok {
		prog = append(prog,
			jne(uint32(n), 3),
			loadArgLow32(1),
			jsetFall(cloneNewMask, 1),
			retInsn(ActionKill),
		)
		prog = append(prog, loadNR())
	}
	// clone(flags, ...) — flags in arg[0] on x86-64 glibc (kernel ABI:
	// clone(flags, stack, ptid, ctid, tls) with flags first). Same shape
	// as unshare.
	if n, ok := syscalls["clone"]; ok {
		prog = append(prog,
			jne(uint32(n), 3),
			loadArgLow32(0),
			jsetFall(cloneNewMask, 1),
			retInsn(ActionKill),
		)
		prog = append(prog, loadNR())
	}
	// clone3 — struct pointer, can't inspect. Blanket deny.
	if n, ok := syscalls["clone3"]; ok {
		prog = append(prog,
			jne(uint32(n), 1),
			retInsn(ActionKill),
		)
	}

	prog = append(prog, retInsn(ActionAllow))
	return prog, nil
}

// CompileRestrictSUIDSGID builds a filter that denies chmod/fchmod/
// fchmodat calls whose mode argument sets S_ISUID (04000) or S_ISGID
// (02000). Also denies chown-type calls that would grant setuid on a
// changed owner? — no, systemd only covers the chmod family (the
// documented behaviour), so we stop there.
func CompileRestrictSUIDSGID() ([]unix.SockFilter, error) {
	const suidMask = uint32(0o4000 | 0o2000) // S_ISUID | S_ISGID

	prog := archHeader()

	// chmod(path, mode) — mode is arg[1].
	if n, ok := SyscallNumber("chmod"); ok {
		prog = append(prog,
			jne(uint32(n), 3),
			loadArgLow32(1),
			jsetFall(suidMask, 1),
			retInsn(ActionKill),
		)
		prog = append(prog, loadNR())
	}
	// fchmod(fd, mode) — mode is arg[1] too.
	if n, ok := SyscallNumber("fchmod"); ok {
		prog = append(prog,
			jne(uint32(n), 3),
			loadArgLow32(1),
			jsetFall(suidMask, 1),
			retInsn(ActionKill),
		)
		prog = append(prog, loadNR())
	}
	// fchmodat(dirfd, path, mode, flags) — mode is arg[2].
	if n, ok := SyscallNumber("fchmodat"); ok {
		prog = append(prog,
			jne(uint32(n), 3),
			loadArgLow32(2),
			jsetFall(suidMask, 1),
			retInsn(ActionKill),
		)
	}

	prog = append(prog, retInsn(ActionAllow))
	return prog, nil
}

// CompileRestrictAddressFamilies builds a filter that denies socket(2)
// and socketpair(2) unless the address family is in `allowed`. Empty
// list means "no families allowed" — socket()/socketpair() are killed
// entirely. Values outside [0, 65535] are rejected.
func CompileRestrictAddressFamilies(allowed []int) ([]unix.SockFilter, error) {
	for _, af := range allowed {
		if af < 0 || af > 0xffff {
			return nil, fmt.Errorf("restrict-address-families: family %d out of range [0,65535]", af)
		}
	}

	prog := archHeader()

	emitCheck := func(syscallName string, argIdx int, isLast bool) {
		n, ok := SyscallNumber(syscallName)
		if !ok {
			return
		}
		// Layout after this jne:
		//   loadArg        (1)
		//   JEQ × len(allowed)
		//   retKill        (1)
		//   retAllow       (1)
		//   loadNR trailer (1, only if another block follows)
		// perSyscallLen = insns to skip to LAND on the trailer (or on
		// the final retAllow if this is the last block).
		trailerLen := uint8(0)
		if !isLast {
			trailerLen = 1
		}
		perSyscallLen := uint8(len(allowed)) + 3 + trailerLen
		prog = append(prog, jne(uint32(n), perSyscallLen))
		prog = append(prog, loadArgLow32(argIdx))
		for i, af := range allowed {
			jt := uint8(len(allowed) - i) // past all remaining JEQ + retKill, land on retAllow
			prog = append(prog, unix.SockFilter{
				Code: bpfJMP | bpfJEQ | bpfK, K: uint32(af), Jt: jt, Jf: 0,
			})
		}
		prog = append(prog, retInsn(ActionKill))
		prog = append(prog, retInsn(ActionAllow))
		if !isLast {
			prog = append(prog, loadNR())
		}
	}
	emitCheck("socket", 0, false)
	emitCheck("socketpair", 0, true)

	prog = append(prog, retInsn(ActionAllow))
	return prog, nil
}

// CompileRestrictFileSystems builds a filter that blanket-denies every
// mount-family syscall. Classic seccomp-BPF cannot inspect the fsname
// string that mount(2) receives, so we cannot honour a curated
// filesystem allow-list the way systemd's BPF-LSM implementation does.
// Denying the full mount surface is the most-restrictive interpretation
// available in classic BPF and matches what an operator asking for
// RestrictFileSystems=~ (deny-all) expects.
func CompileRestrictFileSystems() ([]unix.SockFilter, error) {
	names := []string{
		"mount", "umount2", "fsopen", "fsconfig", "fsmount", "fspick",
		"move_mount", "open_tree",
	}

	prog := archHeader()
	for _, name := range names {
		n, ok := SyscallNumber(name)
		if !ok {
			continue
		}
		prog = append(prog,
			jne(uint32(n), 1),
			retInsn(ActionKill),
		)
	}
	prog = append(prog, retInsn(ActionAllow))
	return prog, nil
}

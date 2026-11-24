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

// alu_and emits A = A & K.
func aluAnd(k uint32) unix.SockFilter {
	return unix.SockFilter{Code: bpfALU | bpfAND | bpfK, K: k}
}

// archHeader emits the standard arch-check prelude used by every
// helper here: reject on wrong arch, and on x86-64 reject an x32 call,
// then leave A pointing at nr for the caller's per-syscall dispatch.
func archHeader() []unix.SockFilter {
	prog := []unix.SockFilter{
		loadArch(),
		// If arch matches native, jump past the reject.
		{Code: bpfJMP | bpfJEQ | bpfK, K: archNumber(NativeArch()), Jt: 1, Jf: 0},
		retInsn(ActionKill),
		loadNR(),
	}
	if NativeArch() == "x86-64" {
		prog = append(prog, x32Reject()...)
	}
	return prog
}

// The Restrict*/Lock* filters below answer like systemd's
// (src/shared/seccomp-util.c): a refused call fails with an error and the
// process carries on — EPERM for a policy refusal, EAFNOSUPPORT for an
// address family, ENOSYS where the caller should fall back to an older
// call that can be checked. They used to kill instead, which took down
// any program that probes and copes: glibc's pthread_create tries clone3
// first, so restrict-namespaces killed every threaded service.
//
// A foreign-architecture syscall still kills (archHeader), and so does an
// x32 call on x86-64: these filters only describe the native ABI.

// cond is one test on a syscall argument's low 32 bits: the bits in k are
// set (bpfJSET), or the argument equals k (bpfJEQ).
type cond struct {
	arg  int
	code uint16
	k    uint32
}

func bitsSet(arg int, mask uint32) cond { return cond{arg, bpfJSET, mask} }
func equals(arg int, v uint32) cond     { return cond{arg, bpfJEQ, v} }

// rule refuses a syscall with action when every one of its conds holds;
// with no conds it refuses the syscall outright.
type rule struct {
	syscall string
	conds   []cond
	action  Action
}

// exceptRule refuses a syscall with action unless args[arg]&mask is one of
// allowed.
type exceptRule struct {
	syscall string
	arg     int
	mask    uint32
	allowed []uint32
	action  Action
}

// Every block below has the same shape and ends on a loadNR, so a block
// that does not match — wrong syscall, or a condition that fails — lands
// on that reload and the next block sees the syscall number again. The
// jump offsets are derived here once rather than by hand per filter:
//
//	rule:   jne nr ; (load arg ; test)×k ; ret action ; loadNR
//	except: jne nr ; load arg ; [and mask] ; (jeq v)×n ; ret action ; loadNR
func appendRule(prog []unix.SockFilter, nr int, r rule) []unix.SockFilter {
	k := len(r.conds)
	prog = append(prog, jne(uint32(nr), uint8(2*k+1)))
	for i, c := range r.conds {
		prog = append(prog,
			loadArgLow32(c.arg),
			unix.SockFilter{Code: bpfJMP | c.code | bpfK, K: c.k, Jt: 0, Jf: uint8(2*(k-1-i) + 1)},
		)
	}
	return append(prog, retInsn(r.action), loadNR())
}

func appendExcept(prog []unix.SockFilter, nr int, r exceptRule) []unix.SockFilter {
	n := len(r.allowed)
	masked := r.mask != ^uint32(0)
	body := 1 + n + 1 // load, the jeqs, ret
	if masked {
		body++
	}
	prog = append(prog, jne(uint32(nr), uint8(body)), loadArgLow32(r.arg))
	if masked {
		prog = append(prog, aluAnd(r.mask))
	}
	for i, v := range r.allowed {
		// A match skips the remaining jeqs and the ret, onto the loadNR.
		prog = append(prog, unix.SockFilter{Code: bpfJMP | bpfJEQ | bpfK, K: v, Jt: uint8(n - i), Jf: 0})
	}
	return append(prog, retInsn(r.action), loadNR())
}

// compileRules builds one filter from rules and exceptRules. A syscall the
// native table does not know is skipped: the legacy calls (open, chmod,
// mkdir, creat, mknod) do not exist on arm64, and a call that does not
// exist cannot be made. restrict_semantics_test checks that every name
// resolves on amd64, so a misspelt name cannot fail open unnoticed there.
func compileRules(rules []rule, excepts []exceptRule) ([]unix.SockFilter, error) {
	prog := archHeader()
	for _, r := range excepts {
		if nr, ok := SyscallNumber(r.syscall); ok {
			prog = appendExcept(prog, nr, r)
		}
	}
	for _, r := range rules {
		if nr, ok := SyscallNumber(r.syscall); ok {
			prog = appendRule(prog, nr, r)
		}
	}
	prog = append(prog, retInsn(ActionAllow))
	if len(prog) > 4096 {
		return nil, fmt.Errorf("seccomp: program exceeds 4096 instructions (got %d)", len(prog))
	}
	return prog, nil
}

var (
	errEPERM        = ErrnoAction(unix.EPERM)
	errENOSYS       = ErrnoAction(unix.ENOSYS)
	errEAFNOSUPPORT = ErrnoAction(unix.EAFNOSUPPORT)
)

// realtimeRules: sched_setscheduler may only select SCHED_OTHER, SCHED_BATCH
// or SCHED_IDLE, as systemd allows; every other policy, present or future,
// is refused. SCHED_RESET_ON_FORK is masked off first: it changes nothing
// about whether the policy is realtime. sched_setattr carries the policy in
// a struct the filter cannot read, and is the only way to SCHED_DEADLINE,
// so it is refused outright, as systemd does.
var (
	realtimeExcepts = []exceptRule{{
		syscall: "sched_setscheduler", arg: 1, mask: ^uint32(0x40000000),
		allowed: []uint32{0 /* OTHER */, 3 /* BATCH */, 5 /* IDLE */}, action: errEPERM,
	}}
	realtimeRules = []rule{{syscall: "sched_setattr", action: errEPERM}}
)

// CompileRestrictRealtime builds the restrict-realtime filter.
func CompileRestrictRealtime() ([]unix.SockFilter, error) {
	return compileRules(realtimeRules, realtimeExcepts)
}

// CompileLockPersonality builds the filter for lock-personality: it
// refuses personality() calls that would change the execution domain.
// The query (0xffffffff) and setting the current value again are allowed,
// as systemd allows them (d21d96fe77): libraries query the personality,
// and refusing that broke them.
func CompileLockPersonality(current uint32) ([]unix.SockFilter, error) {
	if _, ok := SyscallNumber("personality"); !ok {
		return nil, fmt.Errorf("lock-personality: personality unknown")
	}
	return compileRules(nil, []exceptRule{{
		syscall: "personality", arg: 0, mask: ^uint32(0),
		allowed: []uint32{0xffffffff, current}, action: errEPERM,
	}})
}

// cloneNewMask is every CLONE_NEW* flag.
const cloneNewMask = uint32(0 |
	0x00000080 | // CLONE_NEWTIME
	0x00020000 | // CLONE_NEWNS
	0x02000000 | // CLONE_NEWCGROUP
	0x04000000 | // CLONE_NEWUTS
	0x08000000 | // CLONE_NEWIPC
	0x10000000 | // CLONE_NEWUSER
	0x20000000 | // CLONE_NEWPID
	0x40000000) // CLONE_NEWNET

// namespaceRules refuses creating or joining any namespace, as systemd's
// RestrictNamespaces=yes does. clone3 passes its flags in a struct, so it
// is refused with ENOSYS: glibc and others then fall back to clone, whose
// flags are checked. setns with nstype 0 joins whatever namespace the fd
// names, so it is refused too.
var namespaceRules = []rule{
	{syscall: "clone3", action: errENOSYS},
	{syscall: "unshare", conds: []cond{bitsSet(0, cloneNewMask)}, action: errEPERM},
	{syscall: "clone", conds: []cond{bitsSet(0, cloneNewMask)}, action: errEPERM},
	{syscall: "setns", conds: []cond{bitsSet(1, cloneNewMask)}, action: errEPERM},
	{syscall: "setns", conds: []cond{equals(1, 0)}, action: errEPERM},
}

// CompileRestrictNamespaces builds the restrict-namespaces filter.
func CompileRestrictNamespaces() ([]unix.SockFilter, error) {
	return compileRules(namespaceRules, nil)
}

const (
	suidSGIDMask = uint32(0o4000 | 0o2000) // S_ISUID | S_ISGID
	// oCreatOrTmpfile is O_CREAT plus the bit of O_TMPFILE that is not
	// O_DIRECTORY: either one makes open(2) create a file whose mode is
	// taken from the mode argument.
	oCreatOrTmpfile = uint32(unix.O_CREAT | (unix.O_TMPFILE &^ unix.O_DIRECTORY))
)

// suidSGIDRules refuses every call that can give a file or directory the
// set-user-ID or set-group-ID bit, the set systemd's RestrictSUIDSGID=
// covers: the chmod family, and creating with such a mode through open,
// openat, creat, mkdir and mknod. O_TMPFILE creates too, and is checked
// with O_CREAT (systemd's seccomp rule checks O_CREAT only; its io_uring
// rule checks both). openat2 keeps its flags in a struct the filter
// cannot read, and is refused with ENOSYS so callers use openat.
var suidSGIDRules = []rule{
	{syscall: "chmod", conds: []cond{bitsSet(1, suidSGIDMask)}, action: errEPERM},
	{syscall: "fchmod", conds: []cond{bitsSet(1, suidSGIDMask)}, action: errEPERM},
	{syscall: "fchmodat", conds: []cond{bitsSet(2, suidSGIDMask)}, action: errEPERM},
	{syscall: "fchmodat2", conds: []cond{bitsSet(2, suidSGIDMask)}, action: errEPERM},
	{syscall: "mkdir", conds: []cond{bitsSet(1, suidSGIDMask)}, action: errEPERM},
	{syscall: "mkdirat", conds: []cond{bitsSet(2, suidSGIDMask)}, action: errEPERM},
	{syscall: "mknod", conds: []cond{bitsSet(1, suidSGIDMask)}, action: errEPERM},
	{syscall: "mknodat", conds: []cond{bitsSet(2, suidSGIDMask)}, action: errEPERM},
	{syscall: "open", conds: []cond{bitsSet(1, oCreatOrTmpfile), bitsSet(2, suidSGIDMask)}, action: errEPERM},
	{syscall: "openat", conds: []cond{bitsSet(2, oCreatOrTmpfile), bitsSet(3, suidSGIDMask)}, action: errEPERM},
	{syscall: "creat", conds: []cond{bitsSet(1, suidSGIDMask)}, action: errEPERM},
	{syscall: "openat2", action: errENOSYS},
}

// CompileRestrictSUIDSGID builds the restrict-suidsgid filter.
func CompileRestrictSUIDSGID() ([]unix.SockFilter, error) {
	return compileRules(suidSGIDRules, nil)
}

// CompileRestrictAddressFamilies builds a filter that refuses socket(2)
// with EAFNOSUPPORT unless the address family is in allowed, as systemd
// does. An empty list refuses every family. socketpair(2) is left alone,
// as systemd leaves it: it only makes connected AF_UNIX pairs, which
// programs use internally whatever network families they are allowed.
// Values outside [0, 65535] are rejected.
func CompileRestrictAddressFamilies(allowed []int) ([]unix.SockFilter, error) {
	afs := make([]uint32, 0, len(allowed))
	for _, af := range allowed {
		if af < 0 || af > 0xffff {
			return nil, fmt.Errorf("restrict-address-families: family %d out of range [0,65535]", af)
		}
		afs = append(afs, uint32(af))
	}
	if len(afs) > 200 {
		return nil, fmt.Errorf("restrict-address-families: %d families, more than a filter can list", len(afs))
	}
	return compileRules(nil, []exceptRule{{
		syscall: "socket", arg: 0, mask: ^uint32(0), allowed: afs, action: errEAFNOSUPPORT,
	}})
}

// fileSystemRules refuses every mount-family syscall. Classic seccomp-BPF
// cannot read the fsname passed to mount(2), which is what systemd's
// RestrictFileSystems= inspects through BPF-LSM; refusing the whole mount
// surface is the most restrictive reading, matching what a deny-all
// RestrictFileSystems=~ expects.
var fileSystemRules = []rule{
	{syscall: "mount", action: errEPERM},
	{syscall: "umount2", action: errEPERM},
	{syscall: "fsopen", action: errEPERM},
	{syscall: "fsconfig", action: errEPERM},
	{syscall: "fsmount", action: errEPERM},
	{syscall: "fspick", action: errEPERM},
	{syscall: "move_mount", action: errEPERM},
	{syscall: "open_tree", action: errEPERM},
}

// CompileRestrictFileSystems builds the restrict-file-systems filter.
func CompileRestrictFileSystems() ([]unix.SockFilter, error) {
	return compileRules(fileSystemRules, nil)
}

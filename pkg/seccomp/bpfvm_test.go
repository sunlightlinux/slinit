//go:build linux

package seccomp

import (
	"encoding/binary"
	"testing"

	"golang.org/x/sys/unix"
)

// runBPF executes a classic BPF program against a seccomp_data built from
// nr, the native arch and args, the way the kernel would, and returns the
// action word. It supports exactly the instructions the compilers in this
// package emit, and fails the test on anything else, so a new instruction
// cannot slip past it unexamined.
//
// This runs the compiled bytes, not a model of what the compiler meant: a
// wrong jump offset changes the answer here just as it would in the kernel.
func runBPF(t *testing.T, prog []unix.SockFilter, nr int, args ...uint64) Action {
	t.Helper()
	return runBPFArch(t, prog, archNumber(NativeArch()), uint32(nr), args...)
}

// runBPFArch is runBPF for a syscall made through the ABI arch (an
// AUDIT_ARCH_* value) with the raw number nr.
func runBPFArch(t *testing.T, prog []unix.SockFilter, arch, nr uint32, args ...uint64) Action {
	t.Helper()
	var data [64]byte // struct seccomp_data: nr, arch, ip, args[6]
	binary.LittleEndian.PutUint32(data[0:], nr)
	binary.LittleEndian.PutUint32(data[4:], arch)
	for i, a := range args {
		binary.LittleEndian.PutUint64(data[16+8*i:], a)
	}
	var acc uint32
	for pc := 0; pc < len(prog); pc++ {
		in := prog[pc]
		switch in.Code {
		case bpfLD | bpfW | bpfABS:
			if int(in.K)+4 > len(data) {
				t.Fatalf("pc %d: load past seccomp_data at %d", pc, in.K)
			}
			acc = binary.LittleEndian.Uint32(data[in.K:])
		case bpfALU | bpfAND | bpfK:
			acc &= in.K
		case bpfJMP | bpfJEQ | bpfK:
			if acc == in.K {
				pc += int(in.Jt)
			} else {
				pc += int(in.Jf)
			}
		case bpfJMP | bpfJGE | bpfK:
			if acc >= in.K {
				pc += int(in.Jt)
			} else {
				pc += int(in.Jf)
			}
		case bpfJMP | bpfJSET | bpfK:
			if acc&in.K != 0 {
				pc += int(in.Jt)
			} else {
				pc += int(in.Jf)
			}
		case bpfRET | bpfK:
			return Action(in.K)
		default:
			t.Fatalf("pc %d: instruction 0x%x not understood by the test VM", pc, in.Code)
		}
	}
	t.Fatalf("program ran off its end")
	return 0
}

// nrOf resolves a syscall name the test relies on, failing loudly rather
// than testing nothing when the table lacks it.
func nrOf(t *testing.T, name string) int {
	t.Helper()
	n, ok := SyscallNumber(name)
	if !ok {
		t.Fatalf("syscall %q unknown on %s", name, NativeArch())
	}
	return n
}

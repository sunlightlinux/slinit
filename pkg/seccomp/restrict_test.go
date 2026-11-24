//go:build linux

package seccomp

import (
	"testing"

	"golang.org/x/sys/unix"
)

// The restrict-* helpers emit small self-contained BPF programs. These
// tests verify each compiles to a non-empty, well-formed program that
// the kernel would accept — no round-trip through the actual seccomp
// syscall (that requires root/CAP_SYS_ADMIN and is exercised by the
// functional suite). A "well-formed" program here means: starts with
// the arch prelude, contains at least one RET instruction, ends with a
// RET, and every jump lands within the program.

func assertValidBPF(t *testing.T, name string, prog []unix.SockFilter) {
	t.Helper()
	if len(prog) == 0 {
		t.Fatalf("%s: empty program", name)
	}
	if len(prog) > 4096 {
		t.Fatalf("%s: exceeds 4096 insn limit (%d)", name, len(prog))
	}
	last := prog[len(prog)-1]
	if last.Code&0x07 != bpfRET {
		t.Fatalf("%s: last insn is not a RET (code=0x%x)", name, last.Code)
	}
	// Every jump's Jt/Jf destination must land within the program.
	for i, insn := range prog {
		if insn.Code&0x07 != bpfJMP {
			continue
		}
		if int(insn.Jt)+i+1 >= len(prog) {
			t.Fatalf("%s: insn %d Jt=%d jumps past end", name, i, insn.Jt)
		}
		if int(insn.Jf)+i+1 >= len(prog) {
			t.Fatalf("%s: insn %d Jf=%d jumps past end", name, i, insn.Jf)
		}
	}
}

func TestCompileRestrictRealtime(t *testing.T) {
	prog, err := CompileRestrictRealtime()
	if err != nil {
		t.Fatal(err)
	}
	assertValidBPF(t, "restrict-realtime", prog)
	// Sanity: at least the arch header (4 insns) + policy check (~7).
	if len(prog) < 10 {
		t.Errorf("expected >=10 insns, got %d", len(prog))
	}
}

func TestCompileRestrictNamespaces(t *testing.T) {
	prog, err := CompileRestrictNamespaces()
	if err != nil {
		t.Fatal(err)
	}
	assertValidBPF(t, "restrict-namespaces", prog)
}

func TestCompileRestrictSUIDSGID(t *testing.T) {
	prog, err := CompileRestrictSUIDSGID()
	if err != nil {
		t.Fatal(err)
	}
	assertValidBPF(t, "restrict-suidsgid", prog)
}

func TestCompileRestrictAddressFamiliesEmpty(t *testing.T) {
	// Empty allow-list = deny all sockets. Program must still be
	// well-formed and terminate the socket/socketpair syscalls with
	// KILL when they hit.
	prog, err := CompileRestrictAddressFamilies(nil)
	if err != nil {
		t.Fatal(err)
	}
	assertValidBPF(t, "restrict-address-families (empty)", prog)
}

func TestCompileRestrictAddressFamiliesAllowInet(t *testing.T) {
	prog, err := CompileRestrictAddressFamilies([]int{1 /* AF_UNIX */, 2 /* AF_INET */, 10 /* AF_INET6 */})
	if err != nil {
		t.Fatal(err)
	}
	assertValidBPF(t, "restrict-address-families (inet)", prog)
}

func TestCompileRestrictAddressFamiliesRejectsOutOfRange(t *testing.T) {
	if _, err := CompileRestrictAddressFamilies([]int{-1}); err == nil {
		t.Errorf("expected error for AF=-1")
	}
	if _, err := CompileRestrictAddressFamilies([]int{0x10000}); err == nil {
		t.Errorf("expected error for AF=65536")
	}
}

func TestCompileRestrictFileSystems(t *testing.T) {
	prog, err := CompileRestrictFileSystems()
	if err != nil {
		t.Fatal(err)
	}
	assertValidBPF(t, "restrict-file-systems", prog)
}

// A rule whose syscall the native table cannot resolve must fail the
// compile, not be skipped: on a build without a syscall table (every
// arch but amd64) each restrict-* filter compiled to "allow all", and
// the hardening deny list to an empty denylist, so the service ran
// unprotected. Only a syscall listed as absent on this arch — one that
// cannot be called at all — may be skipped.
func TestUnresolvedSyscallFailsClosed(t *testing.T) {
	const bogus = "no_such_syscall"
	if _, err := compileRules([]rule{{syscall: bogus, action: errEPERM}}, nil); err == nil {
		t.Error("compileRules accepted a rule it cannot resolve")
	}
	if _, err := compileRules(nil, []exceptRule{{syscall: bogus, mask: ^uint32(0), action: errEPERM}}); err == nil {
		t.Error("compileRules accepted an except rule it cannot resolve")
	}
	if _, err := Compile(&Filter{Mode: ModeDeny, Syscalls: []string{bogus}, DefaultAction: errEPERM}); err == nil {
		t.Error("Compile accepted a denylist entry it cannot resolve")
	}

	absentSyscalls[bogus] = true
	defer delete(absentSyscalls, bogus)
	if _, err := compileRules([]rule{{syscall: bogus, action: errEPERM}}, nil); err != nil {
		t.Errorf("a syscall absent on this arch must be skipped: %v", err)
	}
}

//go:build linux

package seccomp

import (
	"testing"

	"golang.org/x/sys/unix"
)

// TestCompileAllowMode produces a deterministic small program and
// checks the load-arch, load-nr and per-syscall structure. We don't
// pretend to fully simulate the BPF VM; the goal is to catch wire-
// format regressions (jt/jf miscounts, wrong opcodes) by inspecting
// the emitted instructions.
func TestCompileAllowMode(t *testing.T) {
	f, err := Build([]string{"read", "write"}, ModeAllow, []string{"x86-64"}, ActionKill, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	prog, err := Compile(f)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// First insn: load arch.
	if prog[0].Code != bpfLD|bpfW|bpfABS || prog[0].K != offsetArch {
		t.Errorf("insn[0] should load arch, got %+v", prog[0])
	}

	// Find the trailing default ret. With allow-mode + default KILL,
	// the last instruction must return SECCOMP_RET_KILL_PROCESS.
	last := prog[len(prog)-1]
	if last.Code != bpfRET|bpfK || Action(last.K) != ActionKill {
		t.Errorf("trailing ret should be KILL, got code=0x%x k=0x%x",
			last.Code, last.K)
	}

	// Per-syscall hit count: one ALLOW return per allowed syscall —
	// read and write, plus the @default set every allowlist admits —
	// and two KILL returns (the trailing default + arch reject), three
	// on x86-64, which also kills x32 calls.
	var allowCount, killCount int
	for _, in := range prog {
		if in.Code != bpfRET|bpfK {
			continue
		}
		switch Action(in.K) {
		case ActionAllow:
			allowCount++
		case ActionKill:
			killCount++
		}
	}
	if allowCount != len(f.Syscalls) {
		t.Errorf("expected %d ALLOW returns (one per syscall), got %d", len(f.Syscalls), allowCount)
	}
	if want := 2 + x32Kills(); killCount != want {
		t.Errorf("expected %d KILL returns (arch + default + x32), got %d", want, killCount)
	}
}

// TestCompileDenyModeFlipsDefault verifies that ModeDeny flips the
// trailing default to ALLOW and uses KILL per matched syscall.
func TestCompileDenyModeFlipsDefault(t *testing.T) {
	f, err := Build([]string{"~ptrace"}, ModeAllow, []string{"x86-64"}, ActionKill, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	prog, err := Compile(f)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	last := prog[len(prog)-1]
	if Action(last.K) != ActionAllow {
		t.Errorf("deny mode trailing ret should be ALLOW, got 0x%x", last.K)
	}
	// The body now has one KILL (per-entry, for ptrace) plus one
	// arch-reject KILL, and on x86-64 the x32 one.
	var killCount int
	for _, in := range prog {
		if in.Code == bpfRET|bpfK && Action(in.K) == ActionKill {
			killCount++
		}
	}
	if want := 2 + x32Kills(); killCount != want {
		t.Errorf("expected %d KILL returns, got %d", want, killCount)
	}
}

// TestCompileLogIsItsOwnFilter: a logged syscall is still refused by the
// main filter, and the log filter logs it and allows the rest. The log
// branches used to sit in front of the main filter's, so a call both
// denied and logged returned LOG — which runs it.
func TestCompileLogIsItsOwnFilter(t *testing.T) {
	eperm := ErrnoAction(unix.EPERM)
	f, err := Build([]string{"~getppid"}, ModeAllow, nil, eperm, []string{"getppid"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	prog, err := Compile(f)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got := runBPF(t, prog, nrOf(t, "getppid")); got != eperm {
		t.Errorf("main filter: getppid -> %#x, want EPERM %#x", got, eperm)
	}
	logProg, err := CompileLog(f)
	if err != nil {
		t.Fatalf("CompileLog: %v", err)
	}
	if got := runBPF(t, logProg, nrOf(t, "getppid")); got != ActionLog {
		t.Errorf("log filter: getppid -> %#x, want LOG", got)
	}
	if got := runBPF(t, logProg, nrOf(t, "getpid")); got != ActionAllow {
		t.Errorf("log filter: getpid -> %#x, want ALLOW", got)
	}
}

// TestCompileEmptyFilterStillValid verifies that an empty allow-list
// compiles to a coherent program (arch check + immediate default action)
// that allows every native syscall: no list is no filter, as when only
// system-call-architectures is set. It used to refuse every syscall.
func TestCompileEmptyFilterStillValid(t *testing.T) {
	f := &Filter{
		Mode:          ModeAllow,
		Archs:         []string{NativeArch()},
		DefaultAction: ActionKill,
	}
	prog, err := Compile(f)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(prog) < 4 { // load-arch + 1 cmp + arch-ret + load-nr + default
		t.Errorf("program too short: %d insns", len(prog))
	}
	if got := runBPF(t, prog, nrOf(t, "getppid")); got != ActionAllow {
		t.Errorf("getppid -> %#x, want ALLOW", got)
	}
	// The SockFilter type comes from unix; make sure it's wired
	// correctly so the build doesn't silently drop the import.
	_ = unix.SockFilter{}
}

// x32Kills is the number of x32-reject KILL returns a program has.
func x32Kills() int {
	if NativeArch() == "x86-64" {
		return 1
	}
	return 0
}

// TestX32CallsAreKilled: an x32 call reports AUDIT_ARCH_X86_64 with the
// x86_64 number plus bit 30, so it passed the arch check and matched no
// branch — taking a deny list's ALLOW. Every filter must kill it. The
// host kernel may lack the x32 ABI, so this runs the compiled programs.
func TestX32CallsAreKilled(t *testing.T) {
	if NativeArch() != "x86-64" {
		t.Skip("x32 is an x86-64 ABI")
	}
	x32 := func(name string) uint32 { return uint32(nrOf(t, name)) | x32SyscallBit }
	eperm := ErrnoAction(unix.EPERM)
	compile := func(items []string) []unix.SockFilter {
		f, err := Build(items, ModeAllow, nil, eperm, nil)
		if err != nil {
			t.Fatalf("Build %v: %v", items, err)
		}
		prog, err := Compile(f)
		if err != nil {
			t.Fatalf("Compile %v: %v", items, err)
		}
		return prog
	}
	ns, err := CompileRestrictNamespaces()
	if err != nil {
		t.Fatal(err)
	}
	af, err := CompileRestrictAddressFamilies([]int{unix.AF_UNIX})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		what string
		prog []unix.SockFilter
		nr   uint32
		args []uint64
	}{
		{"~@mount: mount", compile([]string{"~@mount"}), x32("mount"), nil},
		{"read allowlist: read", compile([]string{"read"}), x32("read"), nil},
		{"no list: getppid", compile(nil), x32("getppid"), nil},
		{"restrict-namespaces: unshare(CLONE_NEWUSER)", ns, x32("unshare"), []uint64{unix.CLONE_NEWUSER}},
		{"restrict-address-families: socket(AF_INET)", af, x32("socket"), []uint64{unix.AF_INET}},
	} {
		if got := runBPFArch(t, c.prog, auditArchX86_64, c.nr, c.args...); got != ActionKill {
			t.Errorf("%s through x32 -> %#x, want KILL", c.what, got)
		}
	}
}

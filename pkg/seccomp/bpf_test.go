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

// nr -1 is what a tracer writes to cancel a syscall (strace fault
// injection, a debugger) and what probes use; the kernel answers it with
// ENOSYS. As an unsigned number it is above the x32 bit, so the x32 kill
// took it too and every filtered service died of SIGSYS under strace.
// It must reach the filter's ordinary rules, as before the x32 check.
func TestSyscallMinusOneIsNotTakenForX32(t *testing.T) {
	if NativeArch() != "x86-64" {
		t.Skip("x32 is an x86-64 ABI")
	}
	f, err := Build([]string{"~@mount"}, ModeAllow, nil, ErrnoAction(unix.EPERM), nil)
	if err != nil {
		t.Fatal(err)
	}
	prog, err := Compile(f)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := CompileRestrictNamespaces()
	if err != nil {
		t.Fatal(err)
	}
	for what, p := range map[string][]unix.SockFilter{"~@mount": prog, "restrict-namespaces": ns} {
		if got := runBPFArch(t, p, auditArchX86_64, 0xffffffff); got == ActionKill {
			t.Errorf("%s: syscall(-1) -> KILL, want it passed to the kernel (ENOSYS)", what)
		}
	}
}

// system-call-architectures alone (no filter list) only says which
// arches may call: an accepted foreign arch must not be refused then.
// The foreign-arch refusal took the filter's action even with nothing to
// filter, killing i386 calls the setting had just accepted.
func TestForeignArchAllowedWithNoList(t *testing.T) {
	if NativeArch() != "x86-64" {
		t.Skip("i386 is the foreign arch tested on x86-64")
	}
	f, err := Build(nil, ModeAllow, []string{"native", "x86"}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	prog, err := Compile(f)
	if err != nil {
		t.Fatal(err)
	}
	if got := runBPFArch(t, prog, auditArchX86, 20); got != ActionAllow {
		t.Errorf("i386 getpid with no list -> %#x, want ALLOW", got)
	}
}

// TestForeignArchIsRefused: with system-call-architectures=native x86, an
// i386 call used to be dispatched through the x86_64-numbered branches —
// i386 mount (21) is x86_64 access, i386 getpid (20) is x86_64 writev —
// so a denylist missed it and an allowlist admitted the wrong call. The
// filter has no i386 table, so every i386 call now takes the filter's
// refusal action, and the native calls are filtered as before.
func TestForeignArchIsRefused(t *testing.T) {
	if NativeArch() != "x86-64" {
		t.Skip("i386 is foreign to an x86-64 build")
	}
	eperm := ErrnoAction(unix.EPERM)
	compile := func(items ...string) []unix.SockFilter {
		f, err := Build(items, ModeAllow, []string{"native", "x86"}, eperm, nil)
		if err != nil {
			t.Fatalf("Build %v: %v", items, err)
		}
		prog, err := Compile(f)
		if err != nil {
			t.Fatalf("Compile %v: %v", items, err)
		}
		return prog
	}
	deny, allow, none := compile("~@mount", "getpid"), compile("read"), compile()
	for _, c := range []struct {
		what string
		prog []unix.SockFilter
		arch uint32
		nr   uint32
		want Action
	}{
		{"~@mount: i386 mount(21)", deny, auditArchX86, 21, eperm},
		{"~getpid: i386 getpid(20)", deny, auditArchX86, 20, eperm},
		{"read allowlist: i386 restart_syscall(0)", allow, auditArchX86, 0, eperm},
		{"no list: i386 read(3)", none, auditArchX86, 3, ActionAllow},
		{"~@mount: x86_64 mount", deny, auditArchX86_64, uint32(nrOf(t, "mount")), eperm},
		{"~@mount: x86_64 access", deny, auditArchX86_64, uint32(nrOf(t, "access")), ActionAllow},
		{"read allowlist: x86_64 read", allow, auditArchX86_64, uint32(nrOf(t, "read")), ActionAllow},
		{"no list: x86_64 getppid", none, auditArchX86_64, uint32(nrOf(t, "getppid")), ActionAllow},
		{"unlisted arm64", deny, auditArchARM64, 0, ActionKill},
	} {
		if got := runBPFArch(t, c.prog, c.arch, c.nr); got != c.want {
			t.Errorf("%s -> %#x, want %#x", c.what, got, c.want)
		}
	}
}

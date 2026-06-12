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

	// Per-syscall hit count: we expect exactly two ALLOW returns
	// (one per syscall) and one KILL (the trailing default + arch
	// reject = 2 KILL returns total).
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
	if allowCount != 2 {
		t.Errorf("expected 2 ALLOW returns, got %d", allowCount)
	}
	if killCount != 2 {
		t.Errorf("expected 2 KILL returns (arch + default), got %d", killCount)
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
	// arch-reject KILL.
	var killCount int
	for _, in := range prog {
		if in.Code == bpfRET|bpfK && Action(in.K) == ActionKill {
			killCount++
		}
	}
	if killCount != 2 {
		t.Errorf("expected 2 KILL returns, got %d", killCount)
	}
}

// TestCompileLogSyscallsEmitsLogReturn verifies a syscall on the log
// list produces a SECCOMP_RET_LOG branch that takes precedence over
// any later ALLOW branch.
func TestCompileLogSyscallsEmitsLogReturn(t *testing.T) {
	f, err := Build([]string{"read"}, ModeAllow, []string{"x86-64"}, ActionKill, []string{"openat"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	prog, err := Compile(f)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	var sawLog bool
	for _, in := range prog {
		if in.Code == bpfRET|bpfK && Action(in.K) == ActionLog {
			sawLog = true
			break
		}
	}
	if !sawLog {
		t.Error("expected a SECCOMP_RET_LOG return in the program")
	}
}

// TestCompileEmptyFilterStillValid verifies that even an empty
// allow-list compiles to a coherent program (arch check + immediate
// default action). Such a filter is effectively "kill on every
// syscall" — an unusual but legal config.
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
	// The SockFilter type comes from unix; make sure it's wired
	// correctly so the build doesn't silently drop the import.
	_ = unix.SockFilter{}
}

package bpfdev

import (
	"io"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// run interprets the program for one access, over the subset of eBPF
// Program emits, as the kernel would run it; it fails on anything
// else, a jump out of the program or a fall off its end.
func run(t *testing.T, p []Insn, char bool, acc uint32, major, minor uint32) bool {
	t.Helper()
	typ := uint32(unix.BPF_DEVCG_DEV_BLOCK)
	if char {
		typ = unix.BPF_DEVCG_DEV_CHAR
	}
	ctx := []uint32{typ | acc<<16, major, minor}
	var r [11]uint64
	for pc, steps := 0, 0; ; pc, steps = pc+1, steps+1 {
		if pc < 0 || pc >= len(p) || steps > 10000 {
			t.Fatalf("pc %d out of the program (len %d)", pc, len(p))
		}
		in := p[pc]
		dst, src := in.Regs&0xf, in.Regs>>4
		switch in.Code {
		case opLdxMemW:
			if src != 1 || in.Off%4 != 0 || in.Off/4 >= 3 {
				t.Fatalf("bad load %+v", in)
			}
			r[dst] = uint64(ctx[in.Off/4])
		case opAnd32K:
			r[dst] = uint64(uint32(r[dst]) & uint32(in.Imm))
		case opRsh32K:
			r[dst] = uint64(uint32(r[dst]) >> uint32(in.Imm))
		case opMov32X:
			r[dst] = uint64(uint32(r[src]))
		case opMov64K:
			r[dst] = uint64(int64(in.Imm))
		case opJneX:
			if r[dst] != r[src] {
				pc += int(in.Off)
			}
		case opJneK:
			if r[dst] != uint64(int64(in.Imm)) {
				pc += int(in.Off)
			}
		case opJa:
			pc += int(in.Off)
		case opExit:
			return r[0] == 1
		default:
			t.Fatalf("opcode %#x", in.Code)
		}
	}
}

func TestProgramDecides(t *testing.T) {
	rules := []Rule{
		{'c', 1, 3, accAll},                // /dev/null
		{'c', 1, 11, AccRead},              // /dev/kmsg, read only
		{'b', 8, -1, AccRead},              // sd*, read
		{'c', 136, -1, AccRead | AccWrite}, // pts
	}
	p := Program(rules)
	for _, c := range []struct {
		char     bool
		acc      uint32
		maj, min uint32
		want     bool
		what     string
	}{
		{true, AccRead | AccWrite, 1, 3, true, "null rw"},
		{true, AccMknod, 1, 3, true, "null mknod"},
		{true, AccRead, 1, 11, true, "kmsg r"},
		{true, AccWrite, 1, 11, false, "kmsg w"},
		{true, AccRead | AccWrite, 1, 11, false, "kmsg rw"},
		{false, AccRead, 1, 3, false, "a block 1:3 is not /dev/null"},
		{false, AccRead, 8, 0, true, "sda r"},
		{false, AccRead, 8, 17, true, "sdb1 r"},
		{false, AccWrite, 8, 0, false, "sda w"},
		{true, AccRead, 8, 0, false, "char 8:0"},
		{true, AccRead | AccWrite, 136, 4, true, "pts rw"},
		{true, AccMknod, 136, 4, false, "pts mknod"},
		{true, AccRead, 4, 1, false, "tty1"},
		{true, AccRead, 1, 1, false, "mem"},
	} {
		if got := run(t, p, c.char, c.acc, c.maj, c.min); got != c.want {
			t.Errorf("%s: allowed %v, want %v", c.what, got, c.want)
		}
	}
	// A class rule; no rule at all denies everything.
	if !run(t, Program([]Rule{{'c', -1, -1, accAll}}), true, AccRead, 4, 1) {
		t.Error("char-* did not allow a char device")
	}
	if run(t, Program([]Rule{{'c', -1, -1, accAll}}), false, AccRead, 8, 0) {
		t.Error("char-* allowed a block device")
	}
	if run(t, Program(nil), true, AccRead, 1, 3) {
		t.Error("an empty rule set allowed /dev/null")
	}
}

func testEnv(nodes map[string][3]uint32) Env {
	return Env{
		Stat: func(p string) (bool, uint32, uint32, bool, error) {
			n, ok := nodes[p]
			return n[0] == 1, n[1], n[2], ok, nil
		},
		ProcDevices: func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("Character devices:\n  1 mem\n  4 tty\n  4 ttyS\n136 pts\n180 usb\n\nBlock devices:\n  8 sd\n259 blkext\n")), nil
		},
	}
}

func TestResolve(t *testing.T) {
	env := testEnv(map[string][3]uint32{"/dev/null": {1, 1, 3}, "/dev/zero": {1, 1, 5}, "/dev/sda": {0, 8, 0}})
	must := func(s string) Allow {
		a, err := ParseAllow(s)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	// auto with nothing allowed: no filter.
	if _, filter, _, _ := Resolve(PolicyAuto, nil, env); filter {
		t.Error("auto with no device-allow made a filter")
	}
	// strict: only what is allowed; a missing node is skipped with a note.
	rules, filter, notes, err := Resolve(PolicyStrict, []Allow{must("/dev/sda r"), must("/dev/nope")}, env)
	if err != nil || !filter || len(rules) != 1 || rules[0] != (Rule{'b', 8, 0, AccRead}) || len(notes) != 1 {
		t.Errorf("strict: %v %v %v %v", rules, filter, notes, err)
	}
	// closed: the pseudo devices present, pts by name, nothing else.
	rules, _, _, _ = Resolve(PolicyClosed, nil, env)
	want := []Rule{{'c', 1, 3, accAll}, {'c', 1, 5, accAll}, {'c', 136, -1, AccRead | AccWrite}}
	if len(rules) != len(want) {
		t.Fatalf("closed: %v", rules)
	}
	for i := range want {
		if rules[i] != want[i] {
			t.Errorf("closed rule %d: %v, want %v", i, rules[i], want[i])
		}
	}
	// auto with an allow adds the pseudo devices too; a glob matches
	// every driver it names; /dev/char/M:N is parsed, not stat'ed.
	rules, _, _, _ = Resolve(PolicyAuto, []Allow{must("char-tty* rw"), must("/dev/char/10:200 r")}, env)
	if n := len(rules); n != 3+2+1 {
		t.Errorf("auto + char-tty* + /dev/char: %d rules %v", n, rules)
	}
	if rules[len(rules)-1] != (Rule{'c', 10, 200, AccRead}) {
		t.Errorf("/dev/char/10:200 -> %v", rules[len(rules)-1])
	}
	rules, _, _, _ = Resolve(PolicyStrict, []Allow{must("block-*"), must("char-180")}, env)
	if len(rules) != 2 || rules[0] != (Rule{'b', -1, -1, accAll}) || rules[1] != (Rule{'c', 180, -1, accAll}) {
		t.Errorf("block-* char-180: %v", rules)
	}
	// Everything allowed resolved to nothing: a filter that denies all.
	rules, filter, _, _ = Resolve(PolicyStrict, []Allow{must("char-nosuch")}, env)
	if !filter || len(rules) != 0 {
		t.Errorf("nothing matched: %v %v", rules, filter)
	}
}

func TestParseAllow(t *testing.T) {
	for _, bad := range []string{"", "sda", "/dev/sda rx", "/dev/../etc/x", "char-", "/dev/a r w"} {
		if _, err := ParseAllow(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if a, _ := ParseAllow("/dev/fuse"); a.Access != accAll {
		t.Errorf("default access %v", a.Access)
	}
	if a, _ := ParseAllow("block-loop rm"); a.Access != AccRead|AccMknod {
		t.Errorf("rm -> %v", a.Access)
	}
}

// Loading and attaching for real needs CAP_SYS_ADMIN and a cgroup v2
// directory of one's own; functional case 342 does it under PID 1.
func TestAttachForReal(t *testing.T) {
	cg := os.Getenv("SLINIT_TEST_CGROUP")
	if cg == "" {
		t.Skip("SLINIT_TEST_CGROUP not set")
	}
	f, err := os.Open(cg)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := Attach(int(f.Fd()), Program([]Rule{{'c', 1, 3, accAll}})); err != nil {
		t.Fatal(err)
	}
}

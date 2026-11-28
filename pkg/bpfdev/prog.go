package bpfdev

import (
	"github.com/sunlightlinux/slinit/pkg/ebpf"
	"golang.org/x/sys/unix"
)

// Insn is one eBPF instruction.
type Insn = ebpf.Insn

// The opcodes the program uses.
const (
	opLdxMemW  = 0x61 // BPF_LDX | BPF_MEM | BPF_W
	opAnd32K   = 0x54 // BPF_ALU | BPF_AND | BPF_K
	opRsh32K   = 0x74 // BPF_ALU | BPF_RSH | BPF_K
	opMov32X   = 0xbc // BPF_ALU | BPF_MOV | BPF_X
	opMov64K   = 0xb7 // BPF_ALU64 | BPF_MOV | BPF_K
	opJneX     = 0x5d // BPF_JMP | BPF_JNE | BPF_X
	opJneK     = 0x55 // BPF_JMP | BPF_JNE | BPF_K
	opJa       = 0x05 // BPF_JMP | BPF_JA
	opExit     = 0x95 // BPF_JMP | BPF_EXIT
	passJumpOf = 4096 // a jump to "allow", fixed up at the end
)

func regs(dst, src uint8) uint8 { return ebpf.Regs(dst, src) }

// Program assembles the filter for rules (Resolve's), as systemd's
// bpf_devices_cgroup_init + allow_list_* + apply_policy do: r2 the
// device type, r3 the access, r4 the major, r5 the minor; each rule
// jumps to "allow" when it matches; past the last, deny. With no rule
// at all the program denies everything.
func Program(rules []Rule) []Insn {
	if len(rules) == 0 {
		return []Insn{{Code: opMov64K, Regs: regs(0, 0), Imm: 0}, {Code: opExit}}
	}
	p := []Insn{
		{Code: opLdxMemW, Regs: regs(2, 1), Off: 0}, // access_type
		{Code: opAnd32K, Regs: regs(2, 0), Imm: 0xFFFF},
		{Code: opLdxMemW, Regs: regs(3, 1), Off: 0},
		{Code: opRsh32K, Regs: regs(3, 0), Imm: 16},
		{Code: opLdxMemW, Regs: regs(4, 1), Off: 4}, // major
		{Code: opLdxMemW, Regs: regs(5, 1), Off: 8}, // minor
	}
	for _, r := range rules {
		devType := int32(unix.BPF_DEVCG_DEV_BLOCK)
		if r.Type == 'c' {
			devType = unix.BPF_DEVCG_DEV_CHAR
		}
		// The compares after the access one, each jumping over the
		// rest of the rule when it fails.
		var cmp []Insn
		cmp = append(cmp, Insn{Code: opJneK, Regs: regs(2, 0), Imm: devType})
		if r.Major >= 0 {
			cmp = append(cmp, Insn{Code: opJneK, Regs: regs(4, 0), Imm: int32(r.Major)})
			if r.Minor >= 0 {
				cmp = append(cmp, Insn{Code: opJneK, Regs: regs(5, 0), Imm: int32(r.Minor)})
			}
		}
		var body []Insn
		if r.Access&accAll != accAll {
			// The access asked for must be within the rule's.
			body = append(body,
				Insn{Code: opMov32X, Regs: regs(1, 3)},
				Insn{Code: opAnd32K, Regs: regs(1, 0), Imm: int32(r.Access)},
				Insn{Code: opJneX, Regs: regs(1, 3)}, // offset set below
			)
		}
		body = append(body, cmp...)
		// Each compare jumps past the rule's final "ja allow".
		for i := range body {
			if body[i].Code == opJneX || body[i].Code == opJneK {
				body[i].Off = int16(len(body) - i)
			}
		}
		body = append(body, Insn{Code: opJa, Off: passJumpOf})
		p = append(p, body...)
	}
	p = append(p,
		Insn{Code: opMov64K, Regs: regs(0, 0), Imm: 0}, // deny
		Insn{Code: opJa, Off: 1},
		Insn{Code: opMov64K, Regs: regs(0, 0), Imm: 1}, // allow
		Insn{Code: opExit},
	)
	allow := len(p) - 2
	for i := range p {
		if p[i].Code == opJa && p[i].Off == passJumpOf {
			p[i].Off = int16(allow - i - 1)
		}
	}
	return p
}

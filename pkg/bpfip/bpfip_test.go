package bpfip

import (
	"encoding/binary"
	"net/netip"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/ebpf"
)

// A fake kernel for the program: maps by fd, LPM-matched; one packet.
type fakeKernel struct {
	maps map[int][]netip.Prefix
}

const (
	stackBase = uint64(1) << 40 // r10 points at stackBase+512
	ctxBase   = uint64(1) << 41
)

// run interprets prog for one packet (proto 4 or 6, with the given
// source and destination) and returns whether it passes. It fails on an
// opcode the emitter does not use and on a read of the stack nothing
// wrote.
func (k *fakeKernel) run(t *testing.T, prog []ebpf.Insn, src, dst netip.Addr) bool {
	t.Helper()
	var pkt []byte
	proto := uint16(ethPIP)
	if src.Is4() {
		pkt = make([]byte, 20)
		copy(pkt[ip4Src:], src.AsSlice())
		copy(pkt[ip4Dst:], dst.AsSlice())
	} else {
		proto = ethPIPv6
		pkt = make([]byte, 40)
		copy(pkt[ip6Src:], src.AsSlice())
		copy(pkt[ip6Dst:], dst.AsSlice())
	}
	var stack [512]byte
	var written [512]bool
	var r [11]uint64
	r[1], r[10] = ctxBase, stackBase+512
	stackAt := func(addr uint64, n int) int {
		off := int(addr - stackBase)
		if addr < stackBase || off+n > 512 {
			t.Fatalf("stack access %#x+%d out of range", addr, n)
		}
		return off
	}
	for pc := 0; ; pc++ {
		if pc < 0 || pc >= len(prog) {
			t.Fatalf("pc %d out of the program", pc)
		}
		in := prog[pc]
		dst, srcR := in.Regs&0xf, in.Regs>>4
		switch in.Code {
		case 0xbf:
			r[dst] = r[srcR]
		case 0x61:
			if r[srcR] != ctxBase || in.Off != skbProtocol {
				t.Fatalf("load %+v", in)
			}
			r[dst] = uint64(htons(proto)) & 0xffff
		case 0xb4:
			r[dst] = uint64(uint32(in.Imm))
		case 0xb7:
			r[dst] = uint64(int64(in.Imm))
		case 0x07:
			r[dst] += uint64(int64(in.Imm))
		case 0x44:
			r[dst] = uint64(uint32(r[dst]) | uint32(in.Imm))
		case 0x55:
			if r[dst] != uint64(int64(in.Imm)) {
				pc += int(in.Off)
			}
		case 0x15:
			if r[dst] != uint64(int64(in.Imm)) {
				break
			}
			pc += int(in.Off)
		case 0x18:
			if srcR != 1 || prog[pc+1] != (ebpf.Insn{}) {
				t.Fatalf("ld_imm64 %+v", in)
			}
			r[dst] = uint64(in.Imm)
			pc++
		case 0x62:
			o := stackAt(r[dst]+uint64(int64(in.Off)), 4)
			binary.NativeEndian.PutUint32(stack[o:], uint32(in.Imm))
			for i := 0; i < 4; i++ {
				written[o+i] = true
			}
		case 0x85:
			switch in.Imm {
			case fnSkbLoadBytes:
				if r[1] != ctxBase {
					t.Fatal("skb_load_bytes without the skb")
				}
				off, n := int(r[2]), int(r[4])
				o := stackAt(r[3], n)
				copy(stack[o:o+n], pkt[off:off+n])
				for i := 0; i < n; i++ {
					written[o+i] = true
				}
				r[0] = 0
			case fnMapLookupElem:
				ps, ok := k.maps[int(r[1])]
				if !ok {
					t.Fatalf("lookup in map %d, not one made", r[1])
				}
				o := stackAt(r[2], 4)
				bits := int(binary.NativeEndian.Uint32(stack[o:]))
				n := bits / 8
				for i := 0; i < 4+n; i++ {
					if !written[o+i] {
						t.Fatal("lookup key not written")
					}
				}
				a, _ := netip.AddrFromSlice(stack[o+4 : o+4+n])
				r[0] = 0
				for _, p := range ps {
					if p.Contains(a) {
						r[0] = 1
					}
				}
			default:
				t.Fatalf("helper %d", in.Imm)
			}
		case 0x95:
			return r[0] == 1
		default:
			t.Fatalf("opcode %#x", in.Code)
		}
	}
}

// build gives program's map fds for the lists, as Apply would.
func build(t *testing.T, allow, deny string) (*fakeKernel, lists) {
	t.Helper()
	a, err := ParseList(allow)
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseList(deny)
	if err != nil {
		t.Fatal(err)
	}
	k := &fakeKernel{maps: map[int][]netip.Prefix{}}
	l := lists{allow4: -1, allow6: -1, deny4: -1, deny6: -1, allowAny: isAny(a), denyAny: isAny(d)}
	next := 100
	add := func(ps []netip.Prefix, four bool, fd *int) {
		var mine []netip.Prefix
		for _, p := range ps {
			if p.Addr().Is4() == four {
				mine = append(mine, p)
			}
		}
		if len(mine) > 0 {
			next++
			k.maps[next], *fd = mine, next
		}
	}
	if !l.allowAny {
		add(a, true, &l.allow4)
		add(a, false, &l.allow6)
	}
	if !l.denyAny {
		add(d, true, &l.deny4)
		add(d, false, &l.deny6)
	}
	return k, l
}

func TestFilterDecides(t *testing.T) {
	A := netip.MustParseAddr
	for _, c := range []struct {
		allow, deny string
		peer        string
		pass        bool
	}{
		// systemd's usual pair: deny everything but the loopback.
		{"localhost", "any", "127.0.0.1", true},
		{"localhost", "any", "127.8.9.10", true},
		{"localhost", "any", "::1", true},
		{"localhost", "any", "10.0.2.2", false},
		{"localhost", "any", "2001:db8::1", false},
		// A deny list alone: what is not in it passes.
		{"", "10.0.0.0/8 fd00::/8", "10.1.2.3", false},
		{"", "10.0.0.0/8 fd00::/8", "fd12::1", false},
		{"", "10.0.0.0/8 fd00::/8", "192.168.1.1", true},
		{"", "10.0.0.0/8 fd00::/8", "2001:db8::1", true},
		// Allow beats deny.
		{"10.1.0.0/16", "10.0.0.0/8", "10.1.9.9", true},
		{"10.1.0.0/16", "10.0.0.0/8", "10.2.9.9", false},
		// An allow list alone denies nothing.
		{"192.168.0.0/16", "", "8.8.8.8", true},
		// One family's /0 is a map entry, not "any".
		{"", "0.0.0.0/0", "1.2.3.4", false},
		{"", "0.0.0.0/0", "2001:db8::1", true},
		{"link-local multicast", "any", "169.254.3.4", true},
		{"link-local multicast", "any", "ff02::1", true},
		{"link-local multicast", "any", "8.8.4.4", false},
	} {
		k, l := build(t, c.allow, c.deny)
		peer := A(c.peer)
		local := A("127.0.0.1")
		if peer.Is6() {
			local = A("::1")
		}
		// Ingress judges the source, egress the destination.
		if got := k.run(t, program(true, l), peer, local); got != c.pass {
			t.Errorf("allow %q deny %q: in from %s passes %v, want %v", c.allow, c.deny, c.peer, got, c.pass)
		}
		if got := k.run(t, program(false, l), local, peer); got != c.pass {
			t.Errorf("allow %q deny %q: out to %s passes %v, want %v", c.allow, c.deny, c.peer, got, c.pass)
		}
	}
}

func TestParseList(t *testing.T) {
	for _, bad := range []string{"10.0.0.0/33", "foo", "fe80::1%eth0", "1.2.3"} {
		if _, err := ParseList(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	ps, _ := ParseList("10.1.2.3/8 ::1")
	if ps[0].String() != "10.0.0.0/8" || ps[1].String() != "::1/128" {
		t.Errorf("%v", ps)
	}
	for _, w := range []string{"LOCALHOST", "localhost.", "localhost.localdomain", "foo.localhost"} {
		if ps, err := ParseList(w); err != nil || len(ps) != 2 || ps[0].String() != "127.0.0.0/8" {
			t.Errorf("%q: %v %v", w, ps, err)
		}
	}
	if ps, _ := ParseList("any"); !isAny(ps) {
		t.Error("any is not any")
	}
}

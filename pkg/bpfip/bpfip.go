// Package bpfip is slinit's ip-address-allow / ip-address-deny
// (systemd's IPAddressAllow= / IPAddressDeny=): two programs of type
// BPF_PROG_TYPE_CGROUP_SKB on the service's cgroup, one for packets in
// (judged by their source address) and one for packets out (by their
// destination), shaped as systemd's bpf-firewall.c builds them:
//
//   - a packet whose address is in the allow list passes;
//   - otherwise one whose address is in the deny list is dropped;
//   - otherwise it passes.
//
// The lists are longest-prefix-match maps, one per family and list;
// "any" in a list is a flag instead of a map, as in systemd. Unlike a
// network namespace, the filter leaves netlink, uevents and Unix
// sockets alone: it sees IP packets only.
package bpfip

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/ebpf"
	"golang.org/x/sys/unix"
)

// ParseList parses one ip-address-allow/-deny value: addresses and
// prefixes (10.0.0.0/8, ::1), and systemd's words any, localhost,
// link-local and multicast.
func ParseList(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, w := range strings.Fields(s) {
		if isLocalhost(w) {
			w = "localhost"
		}
		switch w {
		case "any":
			out = append(out, netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0"))
		case "localhost":
			out = append(out, netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128"))
		case "link-local":
			out = append(out, netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("fe80::/64"))
		case "multicast":
			out = append(out, netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("ff00::/8"))
		default:
			var p netip.Prefix
			var err error
			if strings.Contains(w, "/") {
				p, err = netip.ParsePrefix(w)
			} else {
				var a netip.Addr
				a, err = netip.ParseAddr(w)
				if err == nil && a.Zone() != "" {
					err = fmt.Errorf("zone")
				}
				p = netip.PrefixFrom(a, a.BitLen())
			}
			if err != nil || !p.IsValid() {
				return nil, fmt.Errorf("ip address %q: want an address, a prefix, any, localhost, link-local or multicast", w)
			}
			// An IPv4-mapped IPv6 address stays IPv6, as systemd keeps it.
			out = append(out, p.Masked())
		}
	}
	return out, nil
}

// isLocalhost is systemd's is_localhost(): localhost, localhost.,
// localhost.localdomain and any name under .localhost, in any case.
func isLocalhost(w string) bool {
	w = strings.TrimSuffix(strings.ToLower(w), ".")
	return w == "localhost" || w == "localhost.localdomain" || strings.HasSuffix(w, ".localhost")
}

// Verdict bits a lookup sets in r8, as systemd's ACCESS_ALLOWED /
// ACCESS_DENIED.
const (
	accessAllowed = 1
	accessDenied  = 2
)

// lists is what one program consults: a map fd per family and list (-1
// for none), and "any" per list.
type lists struct {
	allow4, allow6, deny4, deny6 int
	allowAny, denyAny            bool
}

const (
	ethPIP   = 0x0800
	ethPIPv6 = 0x86DD
	// The __sk_buff field and the header offsets the program reads.
	skbProtocol = 16
	ip4Src      = 12
	ip4Dst      = 16
	ip6Src      = 8
	ip6Dst      = 24
	// The helpers it calls.
	fnMapLookupElem = 1
	fnSkbLoadBytes  = 26
)

// htons gives v in network order as the host reads it, as systemd's
// htobe16: skb->protocol holds the big-endian ethertype.
func htons(v uint16) int32 {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	return int32(binary.NativeEndian.Uint16(b[:]))
}

// Program assembles one direction's program over l.
func program(ingress bool, l lists) []ebpf.Insn {
	r := ebpf.Regs
	p := []ebpf.Insn{
		{Code: 0xbf, Regs: r(6, 1)},                   // r6 = skb
		{Code: 0x61, Regs: r(7, 6), Off: skbProtocol}, // r7 = skb->protocol
		{Code: 0xb4, Regs: r(8, 0), Imm: 0},           // w8 = 0
	}
	lookup := func(fd int, proto uint16, verdict int32) {
		size, off := int32(4), int32(ip4Dst)
		if ingress {
			off = ip4Src
		}
		if proto == ethPIPv6 {
			size, off = 16, ip6Dst
			if ingress {
				off = ip6Src
			}
		}
		block := []ebpf.Insn{
			{Code: 0x55, Regs: r(7, 0), Imm: htons(proto)}, // if r7 != proto: skip the block
			{Code: 0xbf, Regs: r(1, 6)},                    // r1 = skb
			{Code: 0xb4, Regs: r(2, 0), Imm: off},          // w2 = the address' offset
			{Code: 0xbf, Regs: r(3, 10)},                   // r3 = fp
			{Code: 0x07, Regs: r(3, 0), Imm: -size},        // r3 -= size
			{Code: 0xb4, Regs: r(4, 0), Imm: size},         // w4 = size
			{Code: 0x85, Imm: fnSkbLoadBytes},
			{Code: 0x18, Regs: r(1, unix.BPF_PSEUDO_MAP_FD), Imm: int32(fd)}, // r1 = map
			{},
			{Code: 0xbf, Regs: r(2, 10)}, // r2 = fp
			{Code: 0x07, Regs: r(2, 0), Imm: -size - 4}, // r2 -= size + 4: the key
			{Code: 0x62, Regs: r(2, 0), Imm: size * 8},  // key.prefixlen = all the bits
			{Code: 0x85, Imm: fnMapLookupElem},
			{Code: 0x15, Regs: r(0, 0), Off: 1},       // if r0 == 0: no match
			{Code: 0x44, Regs: r(8, 0), Imm: verdict}, // w8 |= verdict
		}
		block[0].Off = int16(len(block) - 1)
		p = append(p, block...)
	}
	if l.deny4 >= 0 {
		lookup(l.deny4, ethPIP, accessDenied)
	}
	if l.deny6 >= 0 {
		lookup(l.deny6, ethPIPv6, accessDenied)
	}
	if l.allow4 >= 0 {
		lookup(l.allow4, ethPIP, accessAllowed)
	}
	if l.allow6 >= 0 {
		lookup(l.allow6, ethPIPv6, accessAllowed)
	}
	if l.allowAny {
		p = append(p, ebpf.Insn{Code: 0x44, Regs: r(8, 0), Imm: accessAllowed})
	}
	if l.denyAny {
		p = append(p, ebpf.Insn{Code: 0x44, Regs: r(8, 0), Imm: accessDenied})
	}
	return append(p,
		ebpf.Insn{Code: 0xb7, Regs: r(0, 0), Imm: 1},                    // r0 = pass
		ebpf.Insn{Code: 0x55, Regs: r(8, 0), Imm: accessDenied, Off: 1}, // if r8 != denied only: keep
		ebpf.Insn{Code: 0xb7, Regs: r(0, 0), Imm: 0},                    // r0 = drop
		ebpf.Insn{Code: 0x95},
	)
}

// isAny reports whether a list holds both 0.0.0.0/0 and ::/0.
func isAny(ps []netip.Prefix) bool {
	var v4, v6 bool
	for _, p := range ps {
		if p.Bits() == 0 {
			if p.Addr().Is4() {
				v4 = true
			} else {
				v6 = true
			}
		}
	}
	return v4 && v6
}

package bpfip

import (
	"net/netip"

	"github.com/sunlightlinux/slinit/pkg/ebpf"
	"golang.org/x/sys/unix"
)

// Program names, to replace slinit's own on a restart.
const (
	nameIn  = "slinit_ip_in"
	nameOut = "slinit_ip_out"
)

// Apply attaches the filter for the allow and deny lists to the cgroup
// open at cgroupFD, replacing the one a previous start attached. Both
// lists empty: the previous filter, if any, is taken away.
func Apply(cgroupFD int, allow, deny []netip.Prefix) error {
	if len(allow) == 0 && len(deny) == 0 {
		err := ebpf.Detach(cgroupFD, unix.BPF_CGROUP_INET_INGRESS, nameIn)
		if err2 := ebpf.Detach(cgroupFD, unix.BPF_CGROUP_INET_EGRESS, nameOut); err == nil {
			err = err2
		}
		return err
	}
	l := lists{allow4: -1, allow6: -1, deny4: -1, deny6: -1}
	var fds []int
	defer func() {
		for _, fd := range fds {
			unix.Close(fd)
		}
	}()
	fill := func(ps []netip.Prefix, four bool, name string) (int, error) {
		var mine []netip.Prefix
		for _, p := range ps {
			if p.Addr().Is4() == four {
				mine = append(mine, p)
			}
		}
		if len(mine) == 0 {
			return -1, nil
		}
		size := 16
		if four {
			size = 4
		}
		fd, err := ebpf.NewLPMTrie(name, size, len(mine))
		if err != nil {
			return -1, err
		}
		fds = append(fds, fd)
		one := []byte{1, 0, 0, 0, 0, 0, 0, 0}
		for _, p := range mine {
			if err := ebpf.MapUpdate(fd, ebpf.LPMKey(p.Bits(), p.Addr().AsSlice()), one); err != nil {
				return -1, err
			}
		}
		return fd, nil
	}
	var err error
	if l.allowAny = isAny(allow); !l.allowAny {
		if l.allow4, err = fill(allow, true, "slinit_allow4"); err != nil {
			return err
		}
		if l.allow6, err = fill(allow, false, "slinit_allow6"); err != nil {
			return err
		}
	}
	if l.denyAny = isAny(deny); !l.denyAny {
		if l.deny4, err = fill(deny, true, "slinit_deny4"); err != nil {
			return err
		}
		if l.deny6, err = fill(deny, false, "slinit_deny6"); err != nil {
			return err
		}
	}
	for _, d := range []struct {
		ingress bool
		at      uint32
		name    string
	}{{true, unix.BPF_CGROUP_INET_INGRESS, nameIn}, {false, unix.BPF_CGROUP_INET_EGRESS, nameOut}} {
		fd, err := ebpf.Load(unix.BPF_PROG_TYPE_CGROUP_SKB, d.at, d.name, program(d.ingress, l))
		if err != nil {
			return err
		}
		fds = append(fds, fd)
		if err := ebpf.Replace(cgroupFD, d.at, d.name, fd); err != nil {
			return err
		}
	}
	return nil
}

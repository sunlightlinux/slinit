package process

import (
	"fmt"
	"net/netip"
	"os"

	"github.com/sunlightlinux/slinit/pkg/bpfip"
)

// applyIPFilter attaches ip-address-allow / -deny to the service's
// cgroup, open at cgroupFD. Like the device filter it confines, so one
// that cannot be put in place fails the start.
func applyIPFilter(params ExecParams, cgroupFD *os.File) error {
	parse := func(vals []string) ([]netip.Prefix, error) {
		var out []netip.Prefix
		for _, v := range vals {
			ps, err := bpfip.ParseList(v)
			if err != nil {
				return nil, err
			}
			out = append(out, ps...)
		}
		return out, nil
	}
	allow, err := parse(params.IPAddressAllow)
	if err != nil {
		return err
	}
	deny, err := parse(params.IPAddressDeny)
	if err != nil {
		return err
	}
	if len(allow) == 0 && len(deny) == 0 {
		// A reload took it away: best effort, as for the device filter.
		if cgroupFD != nil && params.DeviceFilterOwnCgroup {
			if err := bpfip.Apply(int(cgroupFD.Fd()), nil, nil); err != nil {
				fmt.Fprintf(os.Stderr, "slinit: %s: removing the previous ip filter: %v\n", params.CgroupPath, err)
			}
		}
		return nil
	}
	if !params.DeviceFilterOwnCgroup {
		return fmt.Errorf("ip filter: the service has no cgroup of its own to attach it to (set slice or cgroup)")
	}
	if cgroupFD == nil {
		return fmt.Errorf("ip filter: the service's cgroup %s could not be made or opened", params.CgroupPath)
	}
	if err := bpfip.Apply(int(cgroupFD.Fd()), allow, deny); err != nil {
		return fmt.Errorf("ip filter: %w", err)
	}
	return nil
}

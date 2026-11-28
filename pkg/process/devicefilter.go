package process

import (
	"fmt"
	"io"
	"os"

	"github.com/sunlightlinux/slinit/pkg/bpfdev"
	"golang.org/x/sys/unix"
)

// deviceEnv is what bpfdev.Resolve reads at start time: the nodes the
// entries name, and /proc/devices.
var deviceEnv = bpfdev.Env{
	Stat: func(p string) (bool, uint32, uint32, bool, error) {
		var st unix.Stat_t
		if err := unix.Stat(p, &st); err != nil {
			if err == unix.ENOENT {
				return false, 0, 0, false, nil
			}
			return false, 0, 0, false, err
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFCHR, unix.S_IFBLK:
		default:
			return false, 0, 0, false, fmt.Errorf("not a device node")
		}
		return st.Mode&unix.S_IFMT == unix.S_IFCHR, unix.Major(st.Rdev), unix.Minor(st.Rdev), true, nil
	},
	ProcDevices: func() (io.ReadCloser, error) { return os.Open("/proc/devices") },
}

// applyDeviceFilter resolves device-policy / device-allow and attaches
// the filter to the service's cgroup, open at cgroupFD.
func applyDeviceFilter(params ExecParams, cgroupFD *os.File) error {
	policy := bpfdev.PolicyAuto
	if params.DevicePolicy != "" {
		p, err := bpfdev.ParsePolicy(params.DevicePolicy)
		if err != nil {
			return err
		}
		policy = p
	}
	allows := make([]bpfdev.Allow, 0, len(params.DeviceAllow))
	for _, s := range params.DeviceAllow {
		a, err := bpfdev.ParseAllow(s)
		if err != nil {
			return err
		}
		allows = append(allows, a)
	}
	rules, filter, notes, err := bpfdev.Resolve(policy, allows, deviceEnv)
	if err != nil {
		return err
	}
	for _, n := range notes {
		fmt.Fprintf(os.Stderr, "slinit: %s\n", n)
	}
	if !filter {
		return nil
	}
	if cgroupFD == nil || !params.DeviceFilterOwnCgroup {
		return fmt.Errorf("device filter: the service has no cgroup of its own to attach it to (set slice or cgroup)")
	}
	if err := bpfdev.Attach(int(cgroupFD.Fd()), bpfdev.Program(rules)); err != nil {
		return fmt.Errorf("device filter: %w", err)
	}
	return nil
}

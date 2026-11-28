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
	if !params.DeviceFilterOwnCgroup {
		if !filter {
			return nil
		}
		return fmt.Errorf("device filter: the service has no cgroup of its own to attach it to (set slice or cgroup)")
	}
	if !filter {
		// None now (a reload took it away): one an earlier start
		// attached to a cgroup that outlived the stop goes. Best
		// effort: a filter left there is only stricter, and failing
		// the start for it would keep a service down after its filter
		// was removed because bpf(2) is not available (why it was).
		if cgroupFD != nil {
			if err := bpfdev.Detach(int(cgroupFD.Fd())); err != nil {
				fmt.Fprintf(os.Stderr, "slinit: %s: removing the previous device filter: %v\n", params.CgroupPath, err)
			}
		}
		return nil
	}
	if cgroupFD == nil {
		return fmt.Errorf("device filter: the service's cgroup %s could not be made or opened", params.CgroupPath)
	}
	if err := bpfdev.Attach(int(cgroupFD.Fd()), bpfdev.Program(rules)); err != nil {
		return fmt.Errorf("device filter: %w", err)
	}
	return nil
}

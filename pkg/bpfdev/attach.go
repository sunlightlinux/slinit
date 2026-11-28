package bpfdev

import (
	"github.com/sunlightlinux/slinit/pkg/ebpf"
	"golang.org/x/sys/unix"
)

// progName tells slinit's device programs apart from any other on the
// cgroup, so a restart replaces its own and nothing else.
const progName = "slinit_dev"

// Attach installs the filter on the cgroup open at cgroupFD, replacing
// the one slinit attached there before (a restart, a reload that
// changed it): the new one first, so the cgroup is never without one.
func Attach(cgroupFD int, p []Insn) error {
	fd, err := ebpf.Load(unix.BPF_PROG_TYPE_CGROUP_DEVICE, unix.BPF_CGROUP_DEVICE, progName, p)
	if err != nil {
		return err
	}
	defer unix.Close(fd) // the attachment keeps the program
	return ebpf.Replace(cgroupFD, unix.BPF_CGROUP_DEVICE, progName, fd)
}

// Detach takes away the filter slinit attached to the cgroup: a
// reload that removed it.
func Detach(cgroupFD int) error {
	return ebpf.Detach(cgroupFD, unix.BPF_CGROUP_DEVICE, progName)
}

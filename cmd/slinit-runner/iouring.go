package main

import (
	"fmt"

	"golang.org/x/sys/unix"

	"github.com/sunlightlinux/slinit/pkg/seccomp"
)

// ioUringSpec is what the io_uring filters are derived from: the same
// settings that filter system calls, since an io_uring request does the
// work of a system call without making one.
type ioUringSpec struct {
	syscallFilter []string
	syscallArchs  []string
	syscallAction string
	restrictAF    bool
	afs           []int
	restrictSUID  bool
	opFilter      []string // io-uring-filter assignments
}

func (s ioUringSpec) active() bool {
	return len(s.syscallFilter) > 0 || s.restrictAF || s.restrictSUID || len(s.opFilter) > 0
}

// ioUringSupported is the kernel probe, a variable so a test can stand in
// for a kernel without io_uring filters.
var ioUringSupported = seccomp.IOUringFilterSupported

// applyIOUring registers the io_uring filters that stop a service from
// doing through a ring what system-call-filter, restrict-address-families
// or restrict-suidsgid refuse it, as systemd does since v263 (exec-invoke,
// apply_io_uring_*). It runs before installSeccomp, because the system
// call filter may refuse io_uring_register() itself.
//
// io-uring-filter restricts io_uring operations by name. It is the one
// setting that exists only for io_uring, so where the kernel cannot filter
// io_uring (before Linux 7.0) it is enforced by refusing io_uring_setup()
// with ENOSYS, and programs fall back to plain system calls, as systemd
// does. For the others the gap stays open on such kernels, as under
// systemd: refusing io_uring_setup() there would break every io_uring user
// that asked only for, say, an address family restriction. A filter that
// refuses io_uring_setup() already closes it, and then nothing is
// registered: the service cannot create a ring.
func applyIOUring(s ioUringSpec) error {
	if !s.active() {
		return nil
	}
	ops, allowList, err := seccomp.ParseIOUringFilter(s.opFilter)
	if err != nil {
		return err
	}
	opFilterSet := ops != nil
	var filter *seccomp.Filter
	if len(s.syscallFilter) > 0 {
		def, err := seccomp.ParseAction(s.syscallAction)
		if err != nil {
			return fmt.Errorf("io_uring: seccomp action: %w", err)
		}
		if filter, err = seccomp.Build(s.syscallFilter, seccomp.ModeAllow, s.syscallArchs, def, nil); err != nil {
			return fmt.Errorf("io_uring: seccomp build: %w", err)
		}
		if filter.Denies("io_uring_setup") {
			return nil
		}
	}
	// Task filters need no_new_privs, like seccomp; the probe answers
	// "unsupported" without it.
	if err := seccomp.EnsureNoNewPrivs(); err != nil {
		return fmt.Errorf("io_uring prerequisite: %w", err)
	}
	if !ioUringSupported() {
		if opFilterSet {
			return refuseIOUringSetup()
		}
		return nil
	}
	if s.restrictAF {
		if err := seccomp.RestrictIOUringAddressFamilies(s.afs); err != nil {
			return err
		}
	}
	if s.restrictSUID {
		if err := seccomp.RestrictIOUringSUIDSGID(); err != nil {
			return err
		}
	}
	if opFilterSet {
		if err := seccomp.RestrictIOUringOps(ops, allowList); err != nil {
			return err
		}
	}
	if filter != nil {
		if ops := seccomp.IOUringOpsDeniedBy(filter); len(ops) > 0 {
			if err := seccomp.RestrictIOUringOps(ops, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// refuseIOUringSetup installs a filter that fails io_uring_setup() with
// ENOSYS, as if the kernel had no io_uring, so programs fall back to
// plain system calls.
func refuseIOUringSetup() error {
	prog, err := seccomp.Compile(&seccomp.Filter{
		Mode:          seccomp.ModeDeny,
		Syscalls:      []string{"io_uring_setup"},
		Archs:         []string{seccomp.NativeArch()},
		DefaultAction: seccomp.ErrnoAction(unix.ENOSYS),
	})
	if err != nil {
		return fmt.Errorf("io-uring-filter: %w", err)
	}
	if err := seccomp.Install(prog); err != nil {
		return fmt.Errorf("io-uring-filter: refuse io_uring_setup: %w", err)
	}
	return nil
}

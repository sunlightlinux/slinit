package main

import (
	"fmt"

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
}

func (s ioUringSpec) active() bool {
	return len(s.syscallFilter) > 0 || s.restrictAF || s.restrictSUID
}

// applyIOUring registers the io_uring filters that stop a service from
// doing through a ring what system-call-filter, restrict-address-families
// or restrict-suidsgid refuse it, as systemd does since v263 (exec-invoke,
// apply_io_uring_*). It runs before installSeccomp, because the system
// call filter may refuse io_uring_register() itself.
//
// Where the kernel cannot filter io_uring (before Linux 7.0), the gap
// stays open, as under systemd: refusing io_uring_setup() there would
// break every io_uring user that asked only for, say, an address family
// restriction. A filter that refuses io_uring_setup() already closes it,
// and then nothing is registered: the service cannot create a ring.
func applyIOUring(s ioUringSpec) error {
	if !s.active() {
		return nil
	}
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
	if !seccomp.IOUringFilterSupported() {
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
	if filter != nil {
		if ops := seccomp.IOUringOpsDeniedBy(filter); len(ops) > 0 {
			if err := seccomp.RestrictIOUringOps(ops, false); err != nil {
				return err
			}
		}
	}
	return nil
}

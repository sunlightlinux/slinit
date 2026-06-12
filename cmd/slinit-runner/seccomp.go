package main

import (
	"fmt"

	"github.com/sunlightlinux/slinit/pkg/seccomp"
)

// seccompSpec captures the seccomp flags parsed by main(). Empty Filter
// and empty LogFilter together mean "no seccomp" — installSeccomp
// returns nil in that case so the runner does not touch
// PR_SET_NO_NEW_PRIVS or the seccomp syscall unnecessarily.
type seccompSpec struct {
	filter    []string
	archs     []string
	errorAct  string
	logFilter []string
}

func (s seccompSpec) active() bool {
	return len(s.filter) > 0 || len(s.logFilter) > 0 ||
		s.errorAct != "" || len(s.archs) > 0
}

// installSeccomp builds the filter, ensures PR_SET_NO_NEW_PRIVS is set
// (required for non-root install) and installs the BPF program. The
// install must happen after every other privileged operation the
// runner does (mount setup, mempolicy, mlockall) because PR_SET_NO_NEW_PRIVS
// suppresses setuid effects on any subsequent execve. The AppArmor
// onexec transition still works because the kernel applies the profile
// to the *exec call*, not the prctl that preceded it.
func installSeccomp(s seccompSpec) error {
	if !s.active() {
		return nil
	}
	defAction, err := seccomp.ParseAction(s.errorAct)
	if err != nil {
		return fmt.Errorf("seccomp action: %w", err)
	}
	filter, err := seccomp.Build(s.filter, seccomp.ModeAllow, s.archs, defAction, s.logFilter)
	if err != nil {
		return fmt.Errorf("seccomp build: %w", err)
	}
	prog, err := seccomp.Compile(filter)
	if err != nil {
		return fmt.Errorf("seccomp compile: %w", err)
	}
	if err := seccomp.EnsureNoNewPrivs(); err != nil {
		return fmt.Errorf("seccomp prerequisite: %w", err)
	}
	if err := seccomp.Install(prog); err != nil {
		return fmt.Errorf("seccomp install: %w", err)
	}
	return nil
}

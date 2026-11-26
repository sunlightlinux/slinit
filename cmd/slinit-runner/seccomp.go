package main

import (
	"fmt"

	"github.com/sunlightlinux/slinit/pkg/seccomp"
	"golang.org/x/sys/unix"
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
// installSeccomp builds and installs the service's filters. beforeInstall
// runs once everything is compiled, just before the first install (or at
// once when there is no filter): the last point where the runner may
// still allocate and make system calls a filter could refuse.
func installSeccomp(s seccompSpec, beforeInstall func() error) error {
	if !s.active() {
		return beforeInstall()
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
	// system-call-log is a filter of its own, as under systemd, so the
	// kernel's most-restrictive rule keeps a logged call refused when
	// the main filter refuses it. It goes in first: the main filter may
	// refuse seccomp(2) itself.
	var logProg []unix.SockFilter
	if len(filter.LogSyscalls) > 0 {
		logProg, err = seccomp.CompileLog(filter)
		if err != nil {
			return fmt.Errorf("seccomp log compile: %w", err)
		}
	}
	if err := beforeInstall(); err != nil {
		return err
	}
	if err := seccomp.EnsureNoNewPrivs(); err != nil {
		return fmt.Errorf("seccomp prerequisite: %w", err)
	}
	if logProg != nil {
		if err := seccomp.Install(logProg); err != nil {
			return fmt.Errorf("seccomp log install: %w", err)
		}
	}
	if err := seccomp.Install(prog); err != nil {
		return fmt.Errorf("seccomp install: %w", err)
	}
	return nil
}

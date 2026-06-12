package main

import (
	"fmt"
	"os"

	"github.com/sunlightlinux/slinit/pkg/seccomp"
	"golang.org/x/sys/unix"
)

// hardeningSpec is the parsed set of Restrict*/Protect* flags from
// main(). Each is yes/no in slinit's config; the runner translates the
// active ones into a fixed deny syscall list (installed as a second
// seccomp filter alongside the user's main one) and a small set of
// mount operations.
//
// The arg-checking variants (RestrictRealtime, RestrictSUIDSGID,
// MemoryDenyWriteExecute, RestrictNamespaces, RestrictAddressFamilies)
// are deferred — they need a BPF compiler extension to inspect
// seccomp_data.args.
type hardeningSpec struct {
	protectKernelTunables bool
	protectKernelModules  bool
	protectKernelLogs     bool
	protectClock          bool
	protectControlGroups  bool
	protectHostname       bool
	lockPersonality       bool
}

func (h hardeningSpec) active() bool {
	return h.protectKernelTunables || h.protectKernelModules ||
		h.protectKernelLogs || h.protectClock ||
		h.protectControlGroups || h.protectHostname ||
		h.lockPersonality
}

// needsMountNS reports whether any active knob requires a private
// mount namespace to apply its mount operation.
func (h hardeningSpec) needsMountNS() bool {
	return h.protectKernelTunables || h.protectControlGroups ||
		h.protectKernelLogs
}

// applyHardening enforces the active Restrict*/Protect* knobs:
//
//  1. Mount operations on /proc/sys, /sys/fs/cgroup and /dev/kmsg are
//     applied first; they require the runner to be in a private mount
//     namespace (the loader auto-implies CLONE_NEWNS when any of these
//     three knobs is active).
//  2. A second seccomp filter is built containing the union of all
//     denied syscalls and installed via seccomp(2). The kernel runs
//     every loaded filter on each syscall and picks the most
//     restrictive action, so this composes cleanly with the operator's
//     own system-call-filter.
//
// Both halves fail closed: a partial application would leave the
// service running with weaker protection than the operator asked for.
func applyHardening(h hardeningSpec) error {
	if !h.active() {
		return nil
	}

	if h.needsMountNS() {
		// Ensure / is detached from host propagation. This is
		// idempotent — if applySandbox already did it, the second
		// call is a cheap no-op.
		if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
			return fmt.Errorf("hardening make / private: %w", err)
		}
	}

	if h.protectKernelTunables {
		// /proc/sys holds the kernel.* / vm.* / net.* sysctls. A
		// service has no business writing to them; ro-remount keeps
		// reads intact (the kernel exposes many useful read-only
		// counters there) while blocking writes.
		if err := remountROIfMount("/proc/sys"); err != nil {
			return fmt.Errorf("protect-kernel-tunables: %w", err)
		}
	}
	if h.protectControlGroups {
		if err := remountROIfMount("/sys/fs/cgroup"); err != nil {
			return fmt.Errorf("protect-control-groups: %w", err)
		}
	}
	if h.protectKernelLogs {
		// /dev/kmsg is the only writable handle the operator-facing
		// kmsg system has — bind /dev/null over it to neutralise it
		// without depending on /proc to exist.
		if _, err := os.Stat("/dev/kmsg"); err == nil {
			if err := unix.Mount("/dev/null", "/dev/kmsg", "", unix.MS_BIND, ""); err != nil {
				return fmt.Errorf("protect-kernel-logs bind /dev/null over /dev/kmsg: %w", err)
			}
		}
	}

	// Build the seccomp deny filter. We construct it directly rather
	// than going through seccomp.Build because the input is a fixed
	// curated list — there's no user expansion to do, and the deny
	// semantics (kill on match, allow everything else) match
	// ModeDeny with default ActionAllow.
	denyList := collectDenyList(h)
	if len(denyList) == 0 {
		return nil
	}
	filter := &seccomp.Filter{
		Mode:          seccomp.ModeDeny,
		Syscalls:      denyList,
		Archs:         []string{seccomp.NativeArch()},
		DefaultAction: seccomp.ActionKill,
	}
	prog, err := seccomp.Compile(filter)
	if err != nil {
		return fmt.Errorf("hardening seccomp compile: %w", err)
	}
	if err := seccomp.EnsureNoNewPrivs(); err != nil {
		return fmt.Errorf("hardening prerequisite: %w", err)
	}
	if err := seccomp.Install(prog); err != nil {
		return fmt.Errorf("hardening seccomp install: %w", err)
	}
	return nil
}

// collectDenyList returns the syscall names blocked by each active
// knob. Order is stable and grouped per-knob so failure messages from
// the BPF compiler can be traced back to a specific protect-* setting
// when something is added or removed here.
func collectDenyList(h hardeningSpec) []string {
	var out []string
	if h.protectKernelTunables {
		// iopl/ioperm grant raw port I/O — a pre-cgroup era escape
		// route. swapon/swapoff aren't tunables per se but are widely
		// grouped with them as "the kernel surface a service should
		// never touch".
		out = append(out, "iopl", "ioperm", "swapon", "swapoff")
	}
	if h.protectKernelModules {
		out = append(out, "init_module", "finit_module", "delete_module")
	}
	if h.protectKernelLogs {
		out = append(out, "syslog")
	}
	if h.protectClock {
		out = append(out, "clock_settime", "clock_adjtime",
			"settimeofday", "adjtimex")
	}
	if h.protectHostname {
		out = append(out, "sethostname", "setdomainname")
	}
	if h.lockPersonality {
		// systemd's LockPersonality= actually arg-checks the
		// personality() syscall and only denies arg != current. In v1
		// we blanket-deny; almost no service legitimately calls
		// personality() and a blanket deny is easy to audit.
		out = append(out, "personality")
	}
	return out
}

// remountROIfMount ro-remounts target when it is already a mount
// point. The first bind is a no-op if it's already mounted but ensures
// the kernel will accept the MS_REMOUNT flag below.
func remountROIfMount(path string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if err := unix.Mount(path, path, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("bind %s: %w", path, err)
	}
	if err := unix.Mount("", path, "",
		unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("remount-ro %s: %w", path, err)
	}
	return nil
}

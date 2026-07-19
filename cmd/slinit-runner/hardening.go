package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/seccomp"
	"golang.org/x/sys/unix"
)

// parseAFList converts operator-facing AF_* tokens ("AF_INET",
// "AF_UNIX", "AF_INET6", or bare numbers) into the numeric values the
// BPF compiler expects. Case-insensitive, tolerant of an "AF_" prefix
// or its absence. Empty or all-whitespace input yields the empty
// allow-list — the operator asked for "no families", which is what
// gets enforced.
func parseAFList(toks []string) ([]int, error) {
	names := map[string]int{
		"UNIX":   1, "LOCAL": 1,
		"INET":   2,
		"AX25":   3,
		"IPX":    4,
		"APPLETALK": 5,
		"NETLINK": 16,
		"PACKET": 17,
		"INET6":  10,
		"BLUETOOTH": 31,
		"VSOCK":  40,
	}
	var out []int
	for _, raw := range toks {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		v = strings.ToUpper(strings.TrimPrefix(strings.ToUpper(v), "AF_"))
		if n, ok := names[v]; ok {
			out = append(out, n)
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("restrict-address-families: unknown family %q", raw)
		}
		out = append(out, n)
	}
	return out, nil
}

// hardeningSpec is the parsed set of Restrict*/Protect* flags from
// main(). Each is yes/no in slinit's config; the runner translates the
// active ones into seccomp filters (installed alongside the user's
// main system-call-filter) and a small set of mount operations.
//
// Two seccomp filter families are used:
//   1. A single deny-mode filter with the union of all "block-outright"
//      syscall lists (protect-kernel-*, protect-clock, protect-hostname,
//      lock-personality). Cheapest to build; one filter, many syscalls.
//   2. Per-restriction arg-checking BPF programs (restrict-realtime,
//      restrict-namespaces, restrict-suidsgid, restrict-address-families,
//      restrict-file-systems). Each is its own tiny filter; the kernel
//      stacks them and applies the most-restrictive result.
//
// memory-deny-write-execute is a straight prctl, not seccomp.
type hardeningSpec struct {
	protectKernelTunables bool
	protectKernelModules  bool
	protectKernelLogs     bool
	protectClock          bool
	protectControlGroups  bool
	protectHostname       bool
	lockPersonality       bool
	// Arg-checking variants — see pkg/seccomp/restrict_linux.go for the
	// per-directive BPF programs.
	restrictRealtime         bool
	restrictNamespaces       bool
	restrictSUIDSGID         bool
	restrictFileSystems      bool
	restrictAddressFamilies  []int // empty when the directive is not set; explicit "no families" is len == 0 sentinel below
	restrictAFEnabled        bool  // true when the directive appeared, distinguishes "unset" from "empty allow-list"
	memoryDenyWriteExecute   bool
}

func (h hardeningSpec) active() bool {
	return h.protectKernelTunables || h.protectKernelModules ||
		h.protectKernelLogs || h.protectClock ||
		h.protectControlGroups || h.protectHostname ||
		h.lockPersonality ||
		h.restrictRealtime || h.restrictNamespaces ||
		h.restrictSUIDSGID || h.restrictFileSystems ||
		h.restrictAFEnabled || h.memoryDenyWriteExecute
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

	// memory-deny-write-execute is a straight prctl — no seccomp
	// needed. Runs BEFORE the seccomp install so a filter that blocks
	// prctl doesn't lock us out. PR_SET_MDWE first appeared in kernel
	// 6.3; on older kernels the prctl returns EINVAL and we surface
	// it as an error (fail-closed to match the operator's stated
	// intent).
	if h.memoryDenyWriteExecute {
		const prSetMDWE = 65
		const mdweRefuseExecGain = 1
		if err := unix.Prctl(prSetMDWE, mdweRefuseExecGain, 0, 0, 0); err != nil {
			return fmt.Errorf("memory-deny-write-execute: %w", err)
		}
	}

	// PR_SET_NO_NEW_PRIVS is the shared prerequisite for every seccomp
	// install below. Set it once; each install() is a no-op on the
	// prctl side after the first.
	seccompNeeded := len(collectDenyList(h)) > 0 ||
		h.restrictRealtime || h.restrictNamespaces ||
		h.restrictSUIDSGID || h.restrictFileSystems || h.restrictAFEnabled
	if seccompNeeded {
		if err := seccomp.EnsureNoNewPrivs(); err != nil {
			return fmt.Errorf("hardening prerequisite: %w", err)
		}
	}

	// Build the seccomp deny filter for the block-outright cluster.
	// Constructed directly rather than via seccomp.Build because the
	// input is a curated list — no user expansion to do.
	if denyList := collectDenyList(h); len(denyList) > 0 {
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
		if err := seccomp.Install(prog); err != nil {
			return fmt.Errorf("hardening seccomp install: %w", err)
		}
	}

	// Arg-checking filters. Each is a small standalone program; the
	// kernel stacks them and takes the most-restrictive result.
	installArg := func(name string, build func() ([]unix.SockFilter, error)) error {
		prog, err := build()
		if err != nil {
			return fmt.Errorf("%s compile: %w", name, err)
		}
		if err := seccomp.Install(prog); err != nil {
			return fmt.Errorf("%s install: %w", name, err)
		}
		return nil
	}
	if h.restrictRealtime {
		if err := installArg("restrict-realtime", seccomp.CompileRestrictRealtime); err != nil {
			return err
		}
	}
	if h.restrictNamespaces {
		if err := installArg("restrict-namespaces", seccomp.CompileRestrictNamespaces); err != nil {
			return err
		}
	}
	if h.restrictSUIDSGID {
		if err := installArg("restrict-suidsgid", seccomp.CompileRestrictSUIDSGID); err != nil {
			return err
		}
	}
	if h.restrictFileSystems {
		if err := installArg("restrict-file-systems", seccomp.CompileRestrictFileSystems); err != nil {
			return err
		}
	}
	if h.restrictAFEnabled {
		afs := h.restrictAddressFamilies
		if err := installArg("restrict-address-families", func() ([]unix.SockFilter, error) {
			return seccomp.CompileRestrictAddressFamilies(afs)
		}); err != nil {
			return err
		}
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

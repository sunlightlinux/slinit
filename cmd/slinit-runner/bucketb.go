package main

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// bucketBSpec captures the small-runner-side directives from the
// legacy-safe niches batch. Each field is zero-value = "leave
// untouched"; the runner applies only what was explicitly set.
//
// Ordering considerations enforced in applyBucketB:
//   - personality() must run BEFORE any exec that depends on the new
//     domain being active (which is basically every exec). We can't do
//     it after the seccomp install because a filter that blocks
//     personality would lock out the runner.
//   - PR_SET_TIMERSLACK survives execve; same story — set early.
//   - PR_SET_MEMORY_MERGE (KSM opt-in) applies to VMAs that exist NOW;
//     madvise(MADV_MERGEABLE) on future allocations from the child is
//     implied by this prctl on kernel 6.4+.
//   - coredump-filter writes /proc/self/coredump_filter — inherited
//     across fork+exec, so setting it in the runner covers the child.
//   - SIGPIPE handler: SIG_IGN survives execve; the shell default is
//     SIG_DFL which terminates on write-to-closed-pipe. systemd's
//     default is IGN; slinit follows suit but only when the operator
//     opts in (--ignore-sigpipe) OR does not opt out (default). The
//     --no-ignore-sigpipe flag exists so a service that WANTS to die
//     on broken pipe can restore the shell behaviour.
type bucketBSpec struct {
	coredumpFilter   string
	timerSlackNsec   int64
	memoryKSM        bool
	ignoreSigpipeYes bool
	ignoreSigpipeNo  bool
	personality      string
}

func applyBucketB(s bucketBSpec) error {
	// personality(2) — set the exec domain. Sub-32-bit domains are
	// niche; we accept the common four names. Additional flags (e.g.
	// ADDR_LIMIT_32BIT) are not exposed at config level — an operator
	// who needs them can supply a raw numeric value.
	if s.personality != "" {
		p, err := parsePersonality(s.personality)
		if err != nil {
			return err
		}
		if _, _, errno := unix.Syscall(unix.SYS_PERSONALITY, uintptr(p), 0, 0); errno != 0 && errno != unix.EAGAIN {
			return fmt.Errorf("personality(%s=0x%x): %w", s.personality, p, errno)
		}
	}

	if s.timerSlackNsec > 0 {
		if err := unix.Prctl(unix.PR_SET_TIMERSLACK, uintptr(s.timerSlackNsec), 0, 0, 0); err != nil {
			return fmt.Errorf("timer-slack-nsec=%d: %w", s.timerSlackNsec, err)
		}
	}

	if s.memoryKSM {
		// PR_SET_MEMORY_MERGE = 67 (kernel 6.4+). On older kernels the
		// prctl returns EINVAL — fail-closed to match the operator's
		// stated intent. The kernel then applies MADV_MERGEABLE
		// automatically to every anon VMA created in this process
		// (and inherited by execve).
		const prSetMemoryMerge = 67
		if err := unix.Prctl(prSetMemoryMerge, 1, 0, 0, 0); err != nil {
			return fmt.Errorf("memory-ksm: %w", err)
		}
	}

	if s.coredumpFilter != "" {
		if err := writeCoredumpFilter(s.coredumpFilter); err != nil {
			return err
		}
	}

	// SIGPIPE: default (no flags) → set SIG_IGN. Explicit --ignore-
	// sigpipe → same. Explicit --no-ignore-sigpipe → do nothing (leave
	// SIG_DFL inherited from PID 1, which is the runit/OpenRC-style
	// behaviour). SIG_IGN survives execve, so the child sees the
	// masked signal.
	if !s.ignoreSigpipeNo {
		signal.Ignore(syscall.SIGPIPE)
	}

	return nil
}

// parsePersonality maps a config-side name to the personality(2) value.
// Names match systemd's Personality= vocabulary. A bare numeric value
// (decimal or hex) is passed through unchanged so operators can opt
// into flags we don't expose.
func parsePersonality(s string) (uint32, error) {
	switch strings.ToLower(s) {
	case "x86-64", "x86_64", "arm64", "aarch64":
		return 0, nil // PER_LINUX
	case "x86", "linux32", "arm":
		return 0x0008, nil // PER_LINUX32
	}
	n, err := strconv.ParseUint(s, 0, 32)
	if err != nil {
		return 0, fmt.Errorf("personality: unknown domain %q", s)
	}
	return uint32(n), nil
}

// writeCoredumpFilter accepts either a bare hex mask ("0x33") or a
// decimal integer and writes it to /proc/self/coredump_filter. The
// value is inherited across fork+exec (per Documentation/filesystems/
// proc.rst), so setting it in the runner covers the child.
func writeCoredumpFilter(value string) error {
	v := strings.TrimSpace(value)
	if _, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(v), "0x"), 16, 32); err != nil {
		if _, derr := strconv.ParseUint(v, 10, 32); derr != nil {
			return fmt.Errorf("coredump-filter: expected hex or decimal, got %q", value)
		}
	}
	return os.WriteFile("/proc/self/coredump_filter", []byte(v+"\n"), 0)
}


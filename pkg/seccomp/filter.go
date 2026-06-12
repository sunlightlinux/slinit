package seccomp

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Mode controls how the syscall list is interpreted.
type Mode uint8

const (
	// ModeAllow: listed syscalls are ALLOWed, everything else takes the
	// default action (KILL_PROCESS unless errno is set). systemd's
	// default for SystemCallFilter=.
	ModeAllow Mode = iota

	// ModeDeny: listed syscalls take the default action, everything
	// else is ALLOWed. Matches systemd's SystemCallFilter=~ form.
	ModeDeny
)

// Action is the seccomp action to take when a syscall matches the
// "non-allowed" side of the filter (the default action). Allowed
// syscalls always return SECCOMP_RET_ALLOW.
type Action uint32

const (
	// ActionKill returns SECCOMP_RET_KILL_PROCESS — the standard
	// default.  Linux >= 4.14 supports SECCOMP_RET_KILL_PROCESS; on
	// older kernels SECCOMP_RET_KILL_THREAD has the same effect for
	// single-threaded children before exec.
	ActionKill Action = 0x80000000

	// ActionAllow returns SECCOMP_RET_ALLOW (only used internally for
	// the per-syscall ALLOW branch — not a valid default).
	ActionAllow Action = 0x7fff0000

	// actionErrnoBase is the SECCOMP_RET_ERRNO action; the low 16 bits
	// hold the errno value.
	actionErrnoBase Action = 0x00050000

	// ActionLog returns SECCOMP_RET_LOG; the call still executes but
	// is logged via the audit subsystem. Used as a non-blocking
	// inspection default.
	ActionLog Action = 0x7ffc0000

	// ActionTrap returns SECCOMP_RET_TRAP — the kernel raises SIGSYS
	// on the calling task. Useful for debugging filters.
	ActionTrap Action = 0x00030000
)

// ParseAction interprets a user-facing string ("kill" | "log" | errno
// name | numeric errno) into an Action value. Empty string returns
// ActionKill (the default).
func ParseAction(s string) (Action, error) {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "", "kill", "kill-process", "kill_process":
		return ActionKill, nil
	case "log":
		return ActionLog, nil
	case "trap":
		return ActionTrap, nil
	}
	// Allow common errno names and numeric forms. The kernel masks the
	// errno to the low 16 bits; we refuse > 4095 to keep typos visible.
	if n, ok := errnoByName(s); ok {
		return actionErrnoBase | Action(n&0xffff), nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > 4095 {
		return 0, fmt.Errorf("unknown action %q (expected kill|log|trap|errno-name|errno-number)", s)
	}
	return actionErrnoBase | Action(n&0xffff), nil
}

// errnoByName maps a small set of Linux errno names to their numeric
// values. The list covers the cases that actually appear in systemd
// SystemCallErrorNumber= settings in the wild; an exhaustive table is
// not worth the maintenance burden when callers can fall back to a
// numeric literal.
func errnoByName(s string) (int, bool) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "EPERM":
		return 1, true
	case "ENOENT":
		return 2, true
	case "EACCES":
		return 13, true
	case "EINVAL":
		return 22, true
	case "ENOSYS":
		return 38, true
	}
	return 0, false
}

// Filter is a fully-resolved seccomp specification ready to compile to
// BPF and install. All group expansion and validation has already
// happened — every Syscalls entry is a known syscall name on this arch.
type Filter struct {
	Mode          Mode
	Syscalls      []string
	Archs         []string // canonical arch names; empty means "current arch only"
	DefaultAction Action   // action for the non-allowed side
	LogSyscalls   []string // syscalls that always trigger SECCOMP_RET_LOG (orthogonal to Mode)
}

// Build converts a user-facing spec into a validated Filter. items is
// the post-parse list of syscall names and @group tokens, optionally
// prefixed with `~` to switch into deny mode. Unknown syscall names
// fail with a descriptive error.
//
// Recognised item forms:
//   - "name"      — syscall name
//   - "@group"    — predefined group (see groups.go)
//   - "~..."      — only legal as the very first item; flips Mode
//   - "" or " "   — silently skipped (config helpers may emit them)
//
// The expansion is deterministic and order-preserving; the BPF builder
// later sorts and dedupes so wire-format diffs stay stable.
func Build(items []string, mode Mode, archs []string, def Action, logList []string) (*Filter, error) {
	seen := make(map[string]struct{})
	var out []string
	for i, it := range items {
		it = strings.TrimSpace(it)
		if it == "" {
			continue
		}
		if strings.HasPrefix(it, "~") {
			if i != 0 {
				return nil, fmt.Errorf("seccomp: ~ prefix only allowed on first item, got %q at position %d", it, i)
			}
			mode = ModeDeny
			it = strings.TrimPrefix(it, "~")
			if it == "" {
				continue
			}
		}
		if err := appendItem(it, seen, &out); err != nil {
			return nil, err
		}
	}
	for _, it := range logList {
		// LogSyscalls are validated the same way but stored separately
		// so the compiler can emit a distinct ret-log branch for them.
		if it == "" {
			continue
		}
		if _, ok := syscallNumbers[it]; !ok {
			if expansion, gOK := syscallGroups[it]; gOK {
				_ = expansion // expand below
			} else {
				return nil, fmt.Errorf("seccomp: log entry %q is not a known syscall or group", it)
			}
		}
	}
	// Normalise the LogSyscalls list separately: groups expand, names
	// stay. We don't pre-dedupe between the two — the compiler handles
	// it (a log syscall takes precedence over an allow/deny branch).
	logOut, err := expandList(logList)
	if err != nil {
		return nil, fmt.Errorf("seccomp: log list: %w", err)
	}

	canonArchs := make([]string, 0, len(archs))
	for _, a := range archs {
		canon, err := ResolveArch(a)
		if err != nil {
			return nil, err
		}
		if archNumber(canon) == 0 {
			return nil, fmt.Errorf("seccomp: arch %q has no AUDIT_ARCH_* constant on this build", canon)
		}
		canonArchs = append(canonArchs, canon)
	}

	return &Filter{
		Mode:          mode,
		Syscalls:      out,
		Archs:         canonArchs,
		DefaultAction: def,
		LogSyscalls:   logOut,
	}, nil
}

func appendItem(it string, seen map[string]struct{}, out *[]string) error {
	if strings.HasPrefix(it, "@") {
		expansion, ok := syscallGroups[it]
		if !ok {
			return fmt.Errorf("seccomp: unknown group %q", it)
		}
		for _, name := range expansion {
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			*out = append(*out, name)
		}
		return nil
	}
	if _, ok := syscallNumbers[it]; !ok {
		return fmt.Errorf("seccomp: unknown syscall %q on this arch", it)
	}
	if _, dup := seen[it]; dup {
		return nil
	}
	seen[it] = struct{}{}
	*out = append(*out, it)
	return nil
}

// expandList expands @group tokens but otherwise treats entries as raw
// syscall names; unknown items fail.
func expandList(items []string) ([]string, error) {
	seen := make(map[string]struct{})
	var out []string
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it == "" {
			continue
		}
		if strings.HasPrefix(it, "@") {
			expansion, ok := syscallGroups[it]
			if !ok {
				return nil, fmt.Errorf("unknown group %q", it)
			}
			for _, name := range expansion {
				if _, dup := seen[name]; dup {
					continue
				}
				seen[name] = struct{}{}
				out = append(out, name)
			}
			continue
		}
		if _, ok := syscallNumbers[it]; !ok {
			return nil, fmt.Errorf("unknown syscall %q", it)
		}
		if _, dup := seen[it]; dup {
			continue
		}
		seen[it] = struct{}{}
		out = append(out, it)
	}
	return out, nil
}

// SortedSyscallNumbers returns the syscall numbers from f.Syscalls in
// ascending order with duplicates removed. Sorted output keeps the
// generated BPF deterministic across runs, which matters for testing
// and audit trails.
func (f *Filter) SortedSyscallNumbers() []int {
	seen := make(map[int]struct{}, len(f.Syscalls))
	var out []int
	for _, name := range f.Syscalls {
		n, ok := syscallNumbers[name]
		if !ok {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// SortedLogNumbers returns log-only syscall numbers in ascending order.
func (f *Filter) SortedLogNumbers() []int {
	seen := make(map[int]struct{}, len(f.LogSyscalls))
	var out []int
	for _, name := range f.LogSyscalls {
		n, ok := syscallNumbers[name]
		if !ok {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

package seccomp

import (
	"sort"
	"strings"
	"testing"
)

// TestGroupsResolve ensures every entry in every predefined group has a
// matching syscall number in the table. A typo here is silently broken
// — the group expansion succeeds but the BPF builder drops the unknown
// name, leaving a gap in the filter. This test is the safety net.
func TestGroupsResolve(t *testing.T) {
	for name, members := range syscallGroups {
		for _, m := range members {
			if _, ok := syscallNumbers[m]; !ok {
				t.Errorf("%s lists %q which is not in syscallNumbers", name, m)
			}
		}
	}
}

// TestExpandGroupSystemService spot-checks the broad default group.
func TestExpandGroupSystemService(t *testing.T) {
	s, ok := ExpandGroup("@system-service")
	if !ok {
		t.Fatal("@system-service must exist")
	}
	wantAtLeast := []string{"read", "write", "openat", "close", "exit_group"}
	in := func(name string) bool {
		for _, x := range s {
			if x == name {
				return true
			}
		}
		return false
	}
	for _, w := range wantAtLeast {
		if !in(w) {
			t.Errorf("@system-service should include %q", w)
		}
	}
}

// TestExpandGroupUnknown ensures a typo'd group surfaces as an error
// rather than silently expanding to an empty allowlist.
func TestExpandGroupUnknown(t *testing.T) {
	if _, ok := ExpandGroup("@bogus"); ok {
		t.Fatal("expected unknown group to fail")
	}
}

// TestBuildAllowMode exercises the parse-time expansion: a mix of
// names and groups resolves into a deduped sorted list with no
// surprises.
func TestBuildAllowMode(t *testing.T) {
	f, err := Build([]string{"@network-io", "read", "write", "read"},
		ModeAllow, []string{"native"}, ActionKill, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if f.Mode != ModeAllow {
		t.Errorf("Mode = %d, want %d", f.Mode, ModeAllow)
	}
	// "read" appears twice in the input; the dedupe must keep one.
	count := 0
	for _, s := range f.Syscalls {
		if s == "read" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("'read' should appear exactly once, saw %d", count)
	}
	if len(f.Archs) == 0 {
		t.Error("'native' should resolve to a non-empty arch list")
	}
}

// TestBuildDenyPrefix verifies the `~` prefix flips into deny mode and
// is only legal at the head of the list.
func TestBuildDenyPrefix(t *testing.T) {
	f, err := Build([]string{"~kill", "tgkill"}, ModeAllow,
		nil, ActionKill, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if f.Mode != ModeDeny {
		t.Error("`~` prefix should switch to deny mode")
	}
	wantSC := []string{"kill", "tgkill"}
	if !sliceEq(f.Syscalls, wantSC) {
		t.Errorf("Syscalls = %v, want %v", f.Syscalls, wantSC)
	}

	if _, err := Build([]string{"read", "~kill"}, ModeAllow, nil, ActionKill, nil); err == nil {
		t.Error("`~` mid-list must be rejected")
	}
}

// TestBuildUnknownSyscall surfaces an error at parse time, so the
// service never starts with an incomplete filter.
func TestBuildUnknownSyscall(t *testing.T) {
	_, err := Build([]string{"this_is_not_a_syscall"}, ModeAllow, nil, ActionKill, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown syscall") {
		t.Fatalf("expected unknown-syscall error, got %v", err)
	}
}

// TestParseAction covers the three named actions plus an errno name and
// a numeric value.
func TestParseAction(t *testing.T) {
	cases := []struct {
		in       string
		wantBase Action
		hasErrno bool
	}{
		{"", ActionKill, false},
		{"kill", ActionKill, false},
		{"log", ActionLog, false},
		{"trap", ActionTrap, false},
		{"EPERM", actionErrnoBase | 1, true},
		{"22", actionErrnoBase | 22, true},
	}
	for _, c := range cases {
		got, err := ParseAction(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got != c.wantBase {
			t.Errorf("%q: got 0x%x, want 0x%x", c.in, got, c.wantBase)
		}
	}
	bad := []string{"murder", "0", "-1", "99999"}
	for _, s := range bad {
		if _, err := ParseAction(s); err == nil {
			t.Errorf("%q should have been rejected", s)
		}
	}
}

// TestSortedSyscallNumbers verifies the deterministic ordering needed
// for stable BPF programs.
func TestSortedSyscallNumbers(t *testing.T) {
	f, err := Build([]string{"write", "read", "close"}, ModeAllow, nil, ActionKill, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	nums := f.SortedSyscallNumbers()
	if !sort.IntsAreSorted(nums) {
		t.Errorf("SortedSyscallNumbers not sorted: %v", nums)
	}
}

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

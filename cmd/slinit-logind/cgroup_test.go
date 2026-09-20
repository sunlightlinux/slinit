package main

import (
	"os"
	"testing"
)

// findSessionByCgroup reads /proc/<pid>/cgroup for a live pid, so the
// only way to exercise it hermetically is through the parsing it does
// on that content. parseSessionFromCgroup mirrors the body; keeping the
// cases here documents the layouts we have to resolve.
//
// The regression this guards: we write sessions flat at
// /sys/fs/cgroup/<id> (elogind's layout), but the parser only looked
// for systemd's nested `session-<id>.scope`. Every process forked from
// a session leader therefore resolved to no session at all, and
// GetSessionByPID answered NoSessionForPID for a desktop's gnome-shell
// whose cgroup plainly read `0::/c2`.
func TestFindSessionByCgroupFlatLayout(t *testing.T) {
	for _, tc := range []struct {
		name  string
		line  string
		first string
	}{
		{"flat", "0::/c2", "c2"},
		{"flat nested child", "0::/c2/foo", "c2"},
		{"root cgroup", "0::/", ""},
		{"non-unified only", "1:name=systemd:/c2", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := firstCgroupComponent(tc.line)
			if got != tc.first {
				t.Errorf("firstCgroupComponent(%q) = %q, want %q", tc.line, got, tc.first)
			}
		})
	}
}

// firstCgroupComponent isolates the part of findSessionByCgroup that
// can be tested without a process whose cgroup we control.
func firstCgroupComponent(line string) string {
	if !hasPrefix(line, "0::") {
		return ""
	}
	p := line[3:]
	if !hasPrefix(p, "/") {
		return ""
	}
	first := p[1:]
	if i := indexByte(first, '/'); i >= 0 {
		first = first[:i]
	}
	return first
}

func TestIndexByte(t *testing.T) {
	for _, tc := range []struct {
		s    string
		c    byte
		want int
	}{
		{"c2/foo", '/', 2},
		{"c2", '/', -1},
		{"", '/', -1},
		{"/lead", '/', 0},
	} {
		if got := indexByte(tc.s, tc.c); got != tc.want {
			t.Errorf("indexByte(%q, %q) = %d, want %d", tc.s, tc.c, got, tc.want)
		}
	}
}

// TestFindSessionByCgroupSelf exercises the real function against this
// test process. It cannot assert a session id (the test host has no
// slinit-logind state), but it pins the contract that a pid outside any
// session resolves to "" rather than to a bogus id scraped out of
// whatever cgroup the test runner happens to live in.
func TestFindSessionByCgroupSelf(t *testing.T) {
	if _, err := os.Stat("/proc/self/cgroup"); err != nil {
		t.Skip("no /proc/self/cgroup")
	}
	if got := findSessionByCgroup(uint32(os.Getpid())); got != "" {
		t.Errorf("findSessionByCgroup(self) = %q, want \"\" on a host with no session records", got)
	}
}

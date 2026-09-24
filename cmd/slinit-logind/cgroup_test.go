package main

import (
	"os"
	"testing"
)

// sessionSet turns a list of ids into the `exists` callback
// sessionFromCgroup takes, standing in for the records on disk.
func sessionSet(ids ...string) func(string) bool {
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	return func(id string) bool { return set[id] }
}

// TestSessionFromCgroup drives the real parser against content we
// choose. It used to drive a copy of the parser kept in this file,
// which is how the bug below survived: the copy was right and the
// original was not.
//
// Two regressions are pinned here.
//
// We write sessions flat at /sys/fs/cgroup/<id>, elogind's layout, but
// the parser once looked only for systemd's nested
// `session-<id>.scope`. Every process forked from a session leader
// resolved to no session, and GetSessionByPID answered NoSessionForPID
// for a desktop's gnome-shell whose cgroup plainly read `0::/c2`.
//
// Then the nested form, kept for compatibility, returned whatever it
// scraped out of the path without checking that the session existed.
// On a systemd host every process lives under .../session-<id>.scope,
// so any pid resolved to an id this daemon had never heard of — which
// is what a CI agent hit, getting "c15" from a daemon with no sessions
// at all.
func TestSessionFromCgroup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		known   []string
		want    string
	}{
		{
			name:    "flat layout, session on disk",
			content: "0::/c2\n",
			known:   []string{"c2"},
			want:    "c2",
		},
		{
			name:    "flat layout, child cgroup of the session",
			content: "0::/c2/foo\n",
			known:   []string{"c2"},
			want:    "c2",
		},
		{
			name:    "flat layout, no such session",
			content: "0::/c2\n",
			known:   nil,
			want:    "",
		},
		{
			name:    "root cgroup is not a session",
			content: "0::/\n",
			known:   []string{"c2"},
			want:    "",
		},
		{
			name:    "no unified line",
			content: "1:name=systemd:/c2\n",
			known:   []string{"c2"},
			want:    "",
		},
		{
			name:    "systemd nesting, session on disk",
			content: "0::/user.slice/user-1000.slice/session-c2.scope\n",
			known:   []string{"c2"},
			want:    "c2",
		},
		{
			// The CI failure, exactly.
			name:    "systemd nesting, session not ours",
			content: "0::/user.slice/user-0.slice/session-c15.scope\n",
			known:   nil,
			want:    "",
		},
		{
			name:    "systemd nesting, a different session is ours",
			content: "0::/user.slice/user-0.slice/session-c15.scope\n",
			known:   []string{"c2"},
			want:    "",
		},
		{
			name:    "unified line among others",
			content: "3:devices:/\n0::/c7\n",
			known:   []string{"c7"},
			want:    "c7",
		},
		{
			name:    "empty content",
			content: "",
			known:   []string{"c2"},
			want:    "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionFromCgroup(tc.content, sessionSet(tc.known...))
			if got != tc.want {
				t.Errorf("sessionFromCgroup(%q) = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}

// TestFindSessionByCgroupSelf exercises the whole function against this
// test process. It cannot assert a session id — the test host has no
// slinit-logind state — but it pins the contract that a pid outside any
// session resolves to "" rather than to a bogus id scraped out of
// whatever cgroup the test runner happens to live in. On a host running
// systemd that cgroup is a session scope, so before the guard above
// this test failed exactly where it was most likely to run: CI.
func TestFindSessionByCgroupSelf(t *testing.T) {
	if _, err := os.Stat("/proc/self/cgroup"); err != nil {
		t.Skip("no /proc/self/cgroup")
	}
	if got := findSessionByCgroup(uint32(os.Getpid())); got != "" {
		t.Errorf("findSessionByCgroup(self) = %q, want \"\" on a host with no session records", got)
	}
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

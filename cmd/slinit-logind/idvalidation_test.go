package main

import "testing"

// A session id becomes part of a filesystem path, and it arrives as a
// raw D-Bus string from a caller this daemon does not authenticate —
// the shipped login1 bus policy allows ReleaseSession, TerminateSession
// and KillSession from the default context, i.e. every local user,
// because elogind and systemd authorise inside the method instead. So
// the id is the only gate, and these are the strings that must not get
// through.
func TestValidSessionIDRejectsAnythingThatCanNameAPath(t *testing.T) {
	for _, id := range []string{
		"",                             // no session
		"../../../tmp/evil",            // the traversal filepath.Join cleans but does not confine
		"..",                           // the parent of the sessions dir
		"a/b",                          // any separator at all
		"c1/../../etc/shadow",          // traversal after a plausible prefix
		"c1\x00",                       // NUL, which truncates a C path
		"c1.json",                      // the suffix sessionFile appends itself
		".",                            // the sessions dir
		"c" + string(make([]byte, 64)), // over the length bound
	} {
		if validSessionID(id) {
			t.Errorf("validSessionID(%q) = true; it can name a path", id)
		}
	}
}

// And the ids that legitimately occur. slinit generates "c<N>", but
// ListSessions hands ids back to callers and elogind's own state uses
// bare decimals, so the check is a charset and not a format.
func TestValidSessionIDAcceptsRealIDs(t *testing.T) {
	for _, id := range []string{"c1", "c12345", "3", "4", "seat0", "a-b_c"} {
		if !validSessionID(id) {
			t.Errorf("validSessionID(%q) = false; this form occurs in practice", id)
		}
	}
}

// Scope is read back from JSON, and the readJSON gate can be satisfied
// by a planted file, so it is untrusted even though this daemon is its
// only writer. Unconfined it was a root rmdir and a root
// create-or-truncate of <path>/cgroup.kill.
func TestScopeMustBeUnderCgroupFS(t *testing.T) {
	bad := []string{
		"", "/etc", "/etc/shadow", "/sys/fs/cgroup",
		"/sys/fs/cgroup/../../etc", "/tmp/evil", "sys/fs/cgroup/c1",
	}
	for _, s := range bad {
		if scopeUnderCgroupFS(s) {
			t.Errorf("scopeUnderCgroupFS(%q) = true", s)
		}
	}
	for _, s := range []string{"/sys/fs/cgroup/c1", "/sys/fs/cgroup/user.slice/user-1000.slice"} {
		if !scopeUnderCgroupFS(s) {
			t.Errorf("scopeUnderCgroupFS(%q) = false; CreateSession builds exactly this shape", s)
		}
	}
}

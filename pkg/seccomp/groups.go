package seccomp

// Predefined syscall groups: syscallGroups, in zgroups.go, is generated
// by tools/seccompgroups from systemd's seccomp-util.c, so the names and
// their members are systemd's and a unit's SystemCallFilter= ports
// verbatim. Regenerate rather than edit; filter_test.go checks every
// member resolves in the syscall table.

// ExpandGroup returns the syscall name list for a single @group token.
// ok is false when the name is unknown so the parser can surface a
// typo'd group at parse time. Names are NOT validated here; the parser
// validates after expansion so an out-of-group syscall (added by name)
// is caught the same way.
func ExpandGroup(name string) (syscalls []string, ok bool) {
	s, ok := syscallGroups[name]
	return s, ok
}

// Groups returns the names of all predefined groups. Useful for help
// output and tests.
func Groups() []string {
	out := make([]string, 0, len(syscallGroups))
	for name := range syscallGroups {
		out = append(out, name)
	}
	return out
}

package main

import (
	"testing"
)

// FuzzSysusersParseLine fuzzes systemd-sysusers.d(5) directive lines.
// slinit-sysusers reads /usr/lib/sysusers.d + /etc/sysusers.d + /run/
// at PID-1 setup time; a parser panic would abort user provisioning
// on boot. The parser accepts four kinds ('u' user, 'g' group, 'm'
// member, 'r' range) plus tab/space-separated fields with optional
// double-quoted GECOS strings and container-relative UID/GID specs.
func FuzzSysusersParseLine(f *testing.F) {
	f.Add(`u root 0 "root" /root /bin/bash`)
	f.Add(`u daemon 1 "daemon user" /`)
	f.Add(`g wheel 10 -`)
	f.Add(`m myuser wheel`)
	f.Add(`r - 100-999`)
	f.Add(`u appuser - "App User" /var/lib/appuser /sbin/nologin`)
	f.Add("u\tappuser\t500\t\"App\"\t/var/lib/appuser\t/sbin/nologin")
	f.Add("# comment")
	f.Add("")
	f.Add("u")
	f.Add("u username")
	f.Add(`u user 65536 "gecos" /home /sh`)
	f.Add(`u user - "with \"escaped\"" - -`)
	f.Add(`u user - "unterminated`)
	f.Add("unknown_kind foo bar")
	f.Add("u user 100:1000 - - -")

	f.Fuzz(func(t *testing.T, data string) {
		e, err := parseLine(data)
		if err != nil {
			return
		}
		_ = e.kind
		_ = e.name
		_ = e.idOrGid
		_ = e.gecos
		_ = e.home
		_ = e.shell
		_ = e.arg
	})
}

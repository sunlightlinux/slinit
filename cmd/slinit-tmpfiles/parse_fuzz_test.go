package main

import (
	"testing"
)

// FuzzTmpfilesParseLine fuzzes systemd-tmpfiles.d(5) directive lines.
// slinit-tmpfiles walks /usr/lib/tmpfiles.d, /etc/tmpfiles.d, and
// /run/tmpfiles.d at boot; any single malformed line can be seen by
// PID 1 during early setup, and a parser panic here would abort
// system-init.
//
// Invariant: parseLine must not panic on any input. The seed corpus
// covers the systemd operators slinit-tmpfiles handles (d, D, f, F,
// L, r, R, x, X, z, Z, w, C, etc.) plus a handful of adversarial
// shapes (only-whitespace, only-type, quoted fields with escapes).
func FuzzTmpfilesParseLine(f *testing.F) {
	// Real-world tmpfiles.d directives.
	f.Add("d /run/user 0755 root root -")
	f.Add("D /var/cache/foo 0755 daemon daemon 30d")
	f.Add("f /var/log/boot 0644 root root - -")
	f.Add("L /etc/foo - - - - /usr/lib/foo")
	f.Add("r! /run/lock/subsys/*")
	f.Add("z /run/wpa 0770 root wheel -")
	f.Add("Z /var/log/foo 0640 root adm -")
	f.Add("w /proc/sys/kernel/panic - - - - 10")
	f.Add("C /var/lib/skel 0755 - - - /etc/skel")
	// Comments + blanks (should not crash but parse to skip).
	f.Add("# comment")
	f.Add("")
	f.Add("   ")
	// Adversarial malformed shapes.
	f.Add("d")                   // only type char
	f.Add("d /path")             // missing mode
	f.Add("badtype /path 0755")  // unknown type
	f.Add("d /path '0755' u g -") // quoted mode
	f.Add("d \"/path with spaces\" 0755 u g -")
	f.Add("d /path 07777 root root -") // invalid mode
	f.Add("!d /path 0755 root root -") // only-boot ! prefix
	f.Add("d+ /path 0755 root root -") // + suffix
	f.Add("d- /path 0755 root root -") // - suffix

	f.Fuzz(func(t *testing.T, data string) {
		// parseLine returns (entry, error) — either is fine, but
		// must not panic. The entry struct's fields are accessed
		// later by apply() so we probe the accessors here to catch
		// partial-init nil-derefs.
		e, err := parseLine(data)
		if err != nil {
			return
		}
		_ = e.kind
		_ = e.path
		_ = e.mode
		_ = e.uid
		_ = e.gid
		_ = e.arg
		_ = e.force
	})
}

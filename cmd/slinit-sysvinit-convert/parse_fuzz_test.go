package main

import (
	"strings"
	"testing"
)

// FuzzParseInittab fuzzes the inittab reader. The input is an arbitrary
// /etc/inittab, which on a migrating machine is a file nobody has looked
// at in years — truncated lines, DOS endings, stray colons inside
// commands. A panic here aborts the conversion of a whole machine, and
// the operator is mid-migration when it happens.
//
// convert() runs on whatever parsed, because the interesting crashes are
// downstream: a zero-length id reaching sanitise, a command whose first
// field is empty reaching filepath.Base, a runlevel column of unexpected
// runes reaching splitLevels.
func FuzzParseInittab(f *testing.F) {
	f.Add("id:3:initdefault:\n")
	f.Add("1:2345:respawn:/sbin/agetty 38400 tty1 linux\n")
	f.Add("si::sysinit:/etc/init.d/rcS\n")
	f.Add("x:2:respawn:/bin/sh -c \"echo a:b:c\"\n")
	f.Add("# comment only\n\n   \n")
	f.Add(":::\n")
	f.Add("a:b:c\n")
	f.Add("::::::::\n")
	f.Add("tty1::askfirst:/bin/sh\n")
	f.Add("\x00:\x00:\x00:\x00\n")

	f.Fuzz(func(t *testing.T, data string) {
		entries, _ := parseInittab(strings.NewReader(data))
		res := convert(entries)
		// Every emitted name has to be usable as a filename: the
		// converter writes one file per service, and a name carrying a
		// slash or a NUL would escape the output directory or be
		// rejected by the kernel.
		for name := range res.services {
			if name == "" {
				t.Fatal("emitted a service with an empty name")
			}
			if strings.ContainsAny(name, "/\x00") {
				t.Fatalf("emitted an unusable service name %q", name)
			}
		}
	})
}

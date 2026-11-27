package main

import (
	"io"
	"strings"
	"testing"
)

// FuzzScanSysArgs throws arbitrary kernel command lines at the PID-1
// scan. As PID 1 nothing on that line may end init, and a stray word
// must not become a boot service: scanning the whole line once let a
// `-s` anywhere turn `splash` and `auto` into services, which sent a
// headless machine into the rescue menu and its 60 s auto-reboot, a
// reboot loop (5f1ff3d, reverted; redone dinit-exact in 1c36f97), and
// the binary before that panicked the kernel on `-s auto -s`.
//
// The line is split on spaces, as the kernel splits it, so an empty
// word (two spaces) reaches the scan too.
func FuzzScanSysArgs(f *testing.F) {
	for _, s := range []string{
		"",
		"splash quiet",
		"splash -s",
		"-s auto -s",
		"splash -t rescue",
		"splash -m kcmd-target",
		"-o true web -t boot",
		"-m=false x -o y",
		"--version",
		"-F abc -t boot",
		"single",
		"-t  -m",
		"--container=1 .hidden @x ok",
		"-- -t boot",
		"KEY=VALUE -b -z -1",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		args := strings.Split(line, " ")
		fl := newSysArgsFlags()
		words := scanSysArgs(fl.fs, args, io.Discard)

		// Properties that hold whatever the scan's internals: only an
		// -m or -o (in any spelling) can make naked words services; until
		// one appears on the line, "single" is the only word taken. The
		// check does not re-derive where processing stops, so it cannot
		// share a mistake with the code it checks.
		mo := false
		in := map[string]bool{}
		for _, a := range args {
			in[a] = true
			if len(a) > 1 && a[0] == '-' {
				name, _, _ := strings.Cut(strings.TrimPrefix(a[1:], "-"), "=")
				switch name { // spelled out, not sysArgsOff: the check must not move with the code
				case "m", "system-mgr", "o", "container":
					mo = true
				}
			}
		}
		for _, w := range words {
			if w == "" || w[0] == '-' || w[0] == '.' || w[0] == '@' {
				t.Fatalf("scan of %q returned the word %q, which is no service name", line, w)
			}
		}
		for _, w := range words {
			if !in[w] {
				t.Fatalf("scan of %q returned %q, which is not on the line", line, w)
			}
			if !mo && w != "single" {
				t.Fatalf("scan of %q made the stray word %q a service with no -m/-o on the line", line, w)
			}
		}
	})
}

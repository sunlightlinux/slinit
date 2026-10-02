package main

import "testing"

// FuzzParseStat fuzzes the /proc/<pid>/stat reader.
//
// This is a real untrusted-input surface, not a formality: field 2 of
// that line is the executable's name, and any user can choose it. A
// process called ") (" or "0 0 0 0" is legal, and if a crafted name
// shifted the field indexing then this tool — which signals every
// process on the machine — could spare something it should kill or,
// worse, mistake a session id and sweep the shell that called it.
//
// The invariant asserted is the one that matters: a parse either fails,
// or returns the pid it was asked about. It must never report a
// different process.
func FuzzParseStat(f *testing.F) {
	f.Add("600 (nginx) S 1 600 600 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 4194304 4857856")
	f.Add("7 (kworker/0:1) S 2 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0")
	f.Add("1 (slinit) S 0 1 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 4194304 4857856")
	// A name that lies about the shape of the line.
	f.Add("600 () (evil) 0 0 0) S 1 600 600 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 4194304 4857856")
	f.Add("600 (Z) Z 1 600 600 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 4194304 4857856")
	f.Add("")
	f.Add(")")
	f.Add("600 (x)")
	f.Add("600 (x) \x00 1 2 3")

	f.Fuzz(func(t *testing.T, stat string) {
		const asked = 600
		p, ok := parseStat(asked, stat)
		if !ok {
			return
		}
		if p.pid != asked {
			t.Fatalf("parseStat(%d, ...) reported pid %d", asked, p.pid)
		}
		// skip() must stay decidable for whatever came back: a panic here
		// would abort a shutdown sweep halfway through.
		_ = skip(p, 1, 1, nil)
	})
}

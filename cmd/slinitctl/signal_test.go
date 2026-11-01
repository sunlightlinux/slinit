package main

import (
	"syscall"
	"testing"
)

// Every name `signal --list` offers must be one `signal` accepts. The
// list and the parser used to be separate tables: the list showed 31
// names and the parser knew 9, so `slinitctl signal WINCH svc` failed.
func TestParseSignalAcceptsEveryListedName(t *testing.T) {
	for _, s := range signalTable {
		for _, in := range []string{s.name, "SIG" + s.name, "sig" + s.name} {
			got, err := parseSignal(in)
			if err != nil {
				t.Errorf("parseSignal(%q): %v, but `signal --list` offers SIG%s", in, err, s.name)
				continue
			}
			if got != syscall.Signal(s.num) {
				t.Errorf("parseSignal(%q) = %d, want %d", in, got, s.num)
			}
		}
	}
}

func TestParseSignal(t *testing.T) {
	if got, err := parseSignal("28"); err != nil || got != syscall.SIGWINCH {
		t.Errorf("parseSignal(\"28\") = %d, %v; want SIGWINCH", got, err)
	}
	if _, err := parseSignal("BOGUS"); err == nil {
		t.Error("parseSignal(\"BOGUS\") succeeded, want an error")
	}
}

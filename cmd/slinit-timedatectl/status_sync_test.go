package main

import "testing"

// "System clock synchronized" is the kernel's answer, as in systemd, not
// whether the NTP daemon runs: as a user the daemon's state cannot be
// had, and a running daemon has not necessarily synchronized.
func TestStatusSynchronizedComesFromTheKernel(t *testing.T) {
	orig := ntpSynchronized
	t.Cleanup(func() { ntpSynchronized = orig })
	for _, want := range []bool{true, false} {
		ntpSynchronized = func() bool { return want }
		if got := collectStatus().SystemClockSynchronized; got != want {
			t.Errorf("kernel says %v, status says %v", want, got)
		}
	}
}

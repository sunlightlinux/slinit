package main

import "testing"

// stubNTP replaces every outside source detectNTPService and
// collectStatus consult, so no test reaches the host's slinit or bus.
func stubNTP(t *testing.T, svc string, running, known, busRunning, busKnown, synced bool) *int {
	busCalls := 0
	of, os_, ob, on := findNTPServiceFunc, serviceStateFunc, ntpOverBus, ntpSynchronized
	findNTPServiceFunc = func() string { return svc }
	serviceStateFunc = func(string) (bool, bool) { return running, known }
	ntpOverBus = func() (bool, bool) { busCalls++; return busRunning, busKnown }
	ntpSynchronized = func() bool { return synced }
	t.Cleanup(func() { findNTPServiceFunc, serviceStateFunc, ntpOverBus, ntpSynchronized = of, os_, ob, on })
	return &busCalls
}

// "System clock synchronized" is the kernel's answer, as in systemd, not
// whether the NTP daemon runs: as a user the daemon's state cannot be
// had, and a running daemon has not necessarily synchronized.
func TestStatusSynchronizedComesFromTheKernel(t *testing.T) {
	for _, want := range []bool{true, false} {
		stubNTP(t, "chronyd", true, true, false, false, want)
		if got := collectStatus().SystemClockSynchronized; got != want {
			t.Errorf("kernel says %v, status says %v", want, got)
		}
	}
}

// Without the control socket — any user but root — the NTP state comes
// from slinit-timedated over the bus, as systemd's timedatectl gets it
// from timedated; only if that fails too is it "unknown". The bus is not
// asked when slinitctl answered.
func TestStatusNTPStateFallsBackToTheBus(t *testing.T) {
	cases := []struct {
		name                              string
		running, known, busRunning, busOK bool
		wantNTP                           string
		wantRunning                       bool
		wantBusCalls                      int
	}{
		{"root, running", true, true, false, false, "active", true, 0},
		{"root, stopped", false, true, true, true, "inactive", false, 0},
		{"user, bus says running", false, false, true, true, "active", true, 1},
		{"user, bus says stopped", false, false, false, true, "inactive", false, 1},
		{"user, no bus", false, false, false, false, "unknown", false, 1},
	}
	for _, c := range cases {
		calls := stubNTP(t, "chronyd", c.running, c.known, c.busRunning, c.busOK, true)
		s := collectStatus()
		if s.NTP != c.wantNTP || s.NTPServiceRunning != c.wantRunning || *calls != c.wantBusCalls {
			t.Errorf("%s: NTP=%q running=%v bus calls=%d; want %q %v %d",
				c.name, s.NTP, s.NTPServiceRunning, *calls, c.wantNTP, c.wantRunning, c.wantBusCalls)
		}
	}
}

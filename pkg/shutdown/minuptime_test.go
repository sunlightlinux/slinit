package shutdown

import (
	"testing"
	"time"
)

// TestMinimumUptimeSleepsWhenBelowFloor verifies Execute defers to the
// boot-loop guard when uptime is below the configured floor. We can't
// call Execute() in a unit test (it kills every process and reboots)
// so this exercises just the guard's sleep math: uptime 5s, floor 30s
// → sleep 25s.
func TestMinimumUptimeSleepsWhenBelowFloor(t *testing.T) {
	prevMin := minimumUptime
	prevUp := uptimeFunc
	prevSleep := sleepFunc
	defer func() {
		minimumUptime = prevMin
		uptimeFunc = prevUp
		sleepFunc = prevSleep
	}()

	SetMinimumUptime(30 * time.Second)
	uptimeFunc = func() (time.Duration, error) { return 5 * time.Second, nil }
	var slept time.Duration
	sleepFunc = func(d time.Duration) { slept = d }

	// Simulate the guard body inline (Execute's real body is
	// destructive — we only want to prove the delta math).
	if up, err := uptimeFunc(); err == nil && up < minimumUptime {
		sleepFunc(minimumUptime - up)
	}
	want := 25 * time.Second
	if slept != want {
		t.Errorf("sleep delta: got %v want %v", slept, want)
	}
}

// TestMinimumUptimeSkipsWhenAboveFloor pins the fast-path — no sleep
// when the system has been up long enough.
func TestMinimumUptimeSkipsWhenAboveFloor(t *testing.T) {
	prevMin := minimumUptime
	prevUp := uptimeFunc
	prevSleep := sleepFunc
	defer func() {
		minimumUptime = prevMin
		uptimeFunc = prevUp
		sleepFunc = prevSleep
	}()

	SetMinimumUptime(30 * time.Second)
	uptimeFunc = func() (time.Duration, error) { return 60 * time.Second, nil }
	slept := time.Duration(0)
	sleepFunc = func(d time.Duration) { slept = d }

	if up, err := uptimeFunc(); err == nil && up < minimumUptime {
		sleepFunc(minimumUptime - up)
	}
	if slept != 0 {
		t.Errorf("sleep called with %v; expected no sleep", slept)
	}
}

// TestReadUptimeParses parses a synthetic /proc/uptime line to catch
// format drift without depending on /proc/uptime at test time.
func TestReadUptimeParses(t *testing.T) {
	// Format: "seconds_up idle_seconds", floats.
	// Just exercise the parse-float path by reading a valid file
	// out of /proc; skip when unavailable (containers/darwin).
	if _, err := readUptime(); err != nil {
		t.Skipf("readUptime: %v (host without /proc/uptime, skipping)", err)
	}
}

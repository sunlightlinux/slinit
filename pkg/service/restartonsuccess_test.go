package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// restart = on-success is the inverse of on-failure: keep going while
// the program exits cleanly, stop at the first failure. Adapted from
// launchd's KeepAlive{SuccessfulExit: true}.
//
// The counter file is how the test observes repetition without racing
// the state machine: each run appends a byte, so its size is the number
// of runs that happened.
func runCountingService(t *testing.T, name string, script string, mode AutoRestartMode) (*ServiceSet, *ProcessService, string) {
	t.Helper()
	set, _ := newTestSet()
	counter := filepath.Join(t.TempDir(), "runs")
	svc := NewProcessService(set, name)
	svc.SetCommand([]string{"/bin/sh", "-c",
		"printf x >> " + counter + "; " + script})
	svc.SetAutoRestart(mode)
	svc.SetRestartDelay(20 * time.Millisecond)
	svc.SetRestartLimits(time.Minute, 20)
	set.AddService(svc)
	return set, svc, counter
}

func runCount(t *testing.T, counter string) int {
	t.Helper()
	b, err := os.ReadFile(counter)
	if err != nil {
		return 0
	}
	return len(b)
}

// Exit 0 repeatedly: on-success must keep restarting.
func TestRestartOnSuccessRepeatsOnCleanExit(t *testing.T) {
	set, svc, counter := runCountingService(t, "ros-clean", "exit 0", RestartOnSuccess)
	set.StartService(svc)

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && runCount(t, counter) < 3 {
		time.Sleep(25 * time.Millisecond)
	}
	if n := runCount(t, counter); n < 3 {
		t.Errorf("ran %d times, expected repeated restarts on exit 0", n)
	}
}

// First non-zero exit ends the cycle. This is the half that on-failure
// cannot express.
func TestRestartOnSuccessStopsOnFailure(t *testing.T) {
	set, svc, counter := runCountingService(t, "ros-fail", "exit 3", RestartOnSuccess)
	set.StartService(svc)

	mustReachState(t, svc, StateStopped)
	first := runCount(t, counter)
	if first != 1 {
		t.Errorf("ran %d times, expected exactly 1 before giving up", first)
	}
	time.Sleep(300 * time.Millisecond)
	if n := runCount(t, counter); n != first {
		t.Errorf("ran again after a failure: %d then %d", first, n)
	}
}

// A signal is not a clean exit, whatever the signal: launchd's wording
// is "exits and with an exit status of zero", and a killed job has not
// finished its work.
func TestRestartOnSuccessDoesNotRepeatAfterSignal(t *testing.T) {
	set, svc, counter := runCountingService(t, "ros-sig",
		"kill -TERM $$; sleep 5", RestartOnSuccess)
	set.StartService(svc)

	mustReachState(t, svc, StateStopped)
	first := runCount(t, counter)
	time.Sleep(300 * time.Millisecond)
	if n := runCount(t, counter); n != first {
		t.Errorf("restarted after a signal: %d then %d", first, n)
	}
}

// on-failure must behave as before: the inverse of the first two cases.
func TestRestartOnFailureUnchangedByTheNewMode(t *testing.T) {
	set, svc, counter := runCountingService(t, "rof-clean", "exit 0", RestartOnFailure)
	set.StartService(svc)
	mustReachState(t, svc, StateStopped)
	first := runCount(t, counter)
	time.Sleep(300 * time.Millisecond)
	if n := runCount(t, counter); n != first {
		t.Errorf("on-failure restarted after a clean exit: %d then %d", first, n)
	}
}

func TestRestartModeStrings(t *testing.T) {
	for mode, want := range map[AutoRestartMode]string{
		RestartNever:     "never",
		RestartAlways:    "always",
		RestartOnFailure: "on-failure",
		RestartOnSuccess: "on-success",
	} {
		if got := mode.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", mode, got, want)
		}
	}
}

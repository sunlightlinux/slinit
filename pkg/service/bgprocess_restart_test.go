package service

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A bgprocess daemon that is killed must come back under
// `restart = on-failure`, exactly as an equivalent `process` service does.
// Open since 2026-09-27: in the demo VM, nginx (bgprocess, on-failure)
// stayed STOPPED after kill -9 while hello (process, same policy)
// restarted.
//
// The two restart gates are textually identical, so the difference is what
// the policy is shown. A bgprocess daemon is not slinit's child — it is
// read from a pidfile and watched with kill(pid, 0) — so there is no wait
// status for it, and exitStatus still held the LAUNCHER's: the start
// command that forked the daemon and exited 0 to report success.
// on-failure then asked "was it signalled?" (no, it exited) and "did it
// exit non-zero?" (no, it exited 0), so neither branch fired.
//
// The daemon here is long-lived and killed deliberately, which is the
// scenario that was reported. An earlier version of this test used a
// daemon that died after a second, and that cannot answer the question: a
// service whose daemon dies every second restart-loops until the limiter
// stops it, so sampling the state later says more about where the loop had
// got to than about whether a restart ever happened.

func waitForState(t *testing.T, svc Service, want ServiceState, within time.Duration) ServiceState {
	t.Helper()
	deadline := time.Now().Add(within)
	var got ServiceState
	for time.Now().Before(deadline) {
		got = svc.State()
		if got == want {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	return got
}

// waitForDaemonPID waits for the pidfile to name a pid other than `not`,
// which is how a restart is recognised: a new daemon, not the old one.
func waitForDaemonPID(t *testing.T, pidFile string, not int, within time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(pidFile)
		if err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil &&
				pid > 0 && pid != not {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return 0
}

// bgTestLongDaemon forks a daemon that stays up until it is killed.
func bgTestLongDaemon(pidFile string) []string {
	return []string{"/bin/sh", "-c",
		"sleep 300 & echo $! > " + pidFile + "; exit 0"}
}

func TestBGProcessRestartsOnFailureAfterDaemonKilled(t *testing.T) {
	set, _ := newTestSet()
	pidFile := filepath.Join(t.TempDir(), "daemon.pid")

	svc := NewBGProcessService(set, "bg-onfailure")
	svc.SetCommand(bgTestLongDaemon(pidFile))
	svc.SetPIDFile(pidFile)
	svc.SetAutoRestart(RestartOnFailure)
	set.AddService(svc)

	set.StartService(svc)
	if got := waitForState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("expected STARTED, got %v", got)
	}
	first := waitForDaemonPID(t, pidFile, 0, 5*time.Second)
	if first == 0 {
		t.Fatal("no daemon pid in the pidfile")
	}
	t.Cleanup(func() {
		if p := waitForDaemonPID(t, pidFile, 0, time.Second); p > 0 {
			_ = syscall.Kill(p, syscall.SIGKILL)
		}
	})

	// The reported scenario, exactly: an external kill of the daemon.
	if err := syscall.Kill(first, syscall.SIGKILL); err != nil {
		t.Fatalf("kill daemon %d: %v", first, err)
	}

	// A restart means a *different* daemon. Checking the state alone would
	// also pass for a service that never went down.
	second := waitForDaemonPID(t, pidFile, first, 15*time.Second)
	if second == 0 {
		t.Errorf("the daemon was not restarted after being killed — state %v, "+
			"desired %v. `restart = on-failure` never fired.",
			svc.State(), svc.Record().desired.Load())
		return
	}
	if got := waitForState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Errorf("state = %v after the restart, want STARTED", got)
	}
}

// The same for `restart = yes`, which reaches the policy by a different
// branch — it only asks whether the exit was "normal", where on-failure
// additionally wants to see a signal or a non-zero code. Both have to work.
func TestBGProcessRestartsAlwaysAfterDaemonKilled(t *testing.T) {
	set, _ := newTestSet()
	pidFile := filepath.Join(t.TempDir(), "daemon.pid")

	svc := NewBGProcessService(set, "bg-always")
	svc.SetCommand(bgTestLongDaemon(pidFile))
	svc.SetPIDFile(pidFile)
	svc.SetAutoRestart(RestartAlways)
	set.AddService(svc)

	set.StartService(svc)
	if got := waitForState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("expected STARTED, got %v", got)
	}
	first := waitForDaemonPID(t, pidFile, 0, 5*time.Second)
	if first == 0 {
		t.Fatal("no daemon pid in the pidfile")
	}
	t.Cleanup(func() {
		if p := waitForDaemonPID(t, pidFile, 0, time.Second); p > 0 {
			_ = syscall.Kill(p, syscall.SIGKILL)
		}
	})

	if err := syscall.Kill(first, syscall.SIGKILL); err != nil {
		t.Fatalf("kill daemon %d: %v", first, err)
	}

	if second := waitForDaemonPID(t, pidFile, first, 15*time.Second); second == 0 {
		t.Errorf("the daemon was not restarted after being killed — state %v, "+
			"desired %v", svc.State(), svc.Record().desired.Load())
	}
}

// `restart = no` (RestartNever) must stay down. The fix makes a vanished daemon count as a
// failure, and this is the guard that it did not turn into "always
// restart": an operator who said no still gets no.
func TestBGProcessWithRestartNoStaysDown(t *testing.T) {
	set, _ := newTestSet()
	pidFile := filepath.Join(t.TempDir(), "daemon.pid")

	svc := NewBGProcessService(set, "bg-norestart")
	svc.SetCommand(bgTestLongDaemon(pidFile))
	svc.SetPIDFile(pidFile)
	svc.SetAutoRestart(RestartNever)
	set.AddService(svc)

	set.StartService(svc)
	if got := waitForState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("expected STARTED, got %v", got)
	}
	first := waitForDaemonPID(t, pidFile, 0, 5*time.Second)
	if first == 0 {
		t.Fatal("no daemon pid in the pidfile")
	}

	if err := syscall.Kill(first, syscall.SIGKILL); err != nil {
		t.Fatalf("kill daemon %d: %v", first, err)
	}

	if got := waitForState(t, svc, StateStopped, 15*time.Second); got != StateStopped {
		t.Errorf("state = %v, want STOPPED — `restart = no` must not restart", got)
	}
	if p := waitForDaemonPID(t, pidFile, first, 2*time.Second); p != 0 {
		t.Errorf("a new daemon %d appeared despite `restart = no`", p)
	}
}

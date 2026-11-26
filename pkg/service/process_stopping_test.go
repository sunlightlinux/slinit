package service

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// stubbornShell ignores the polite stop signals, so a service running it
// stays STOPPING until its stop-timeout escalates to SIGKILL.
const stubbornShell = "trap '' TERM INT; while :; do sleep 0.05; done"

// TestCrashWithSlowHardDependentRestartsThroughStopped kills a restart=always
// service while a hard dependent of it takes its time to stop. The service is
// left STOPPING and waiting for that dependent; dinit's
// handle_unexpected_termination (proc-service.cc) calls stopped() here, which
// "will restart us": force_stop is cleared, the restart is counted, and
// bring_up honours restart-delay. Calling initiateStart directly skipped all
// three, so the relaunch came at once and was stopped again by the stale
// forceStop.
func TestCrashWithSlowHardDependentRestartsThroughStopped(t *testing.T) {
	set, _ := newTestSet()
	a := NewProcessService(set, "a")
	a.SetCommand([]string{"/bin/sleep", "3600"})
	a.SetAutoRestart(RestartAlways)
	a.SetRestartDelay(2 * time.Second)
	set.AddService(a)

	b := NewProcessService(set, "b")
	b.SetCommand([]string{"/bin/sh", "-c", stubbornShell})
	b.SetStopTimeout(1 * time.Second)
	b.Record().AddDep(a, DepRegular)
	set.AddService(b)
	defer func() {
		set.StopAllServices(ShutdownHalt)
		waitState(t, b, StateStopped, 5*time.Second)
		waitState(t, a, StateStopped, 5*time.Second)
	}()

	set.StartService(a)
	set.StartService(b)
	mustReachState(t, b, StateStarted)
	mustReachState(t, a, StateStarted)
	time.Sleep(200 * time.Millisecond) // let b's shell install its trap
	pid1 := lockedPID(set, a)
	if err := syscall.Kill(pid1, syscall.SIGKILL); err != nil {
		t.Fatalf("kill %d: %v", pid1, err)
	}

	killed := time.Now()
	var relaunched time.Duration
	for time.Since(killed) < 5*time.Second {
		if p := lockedPID(set, a); p > 0 && p != pid1 {
			relaunched = time.Since(killed)
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if relaunched == 0 {
		t.Fatalf("a was never relaunched; state %v", a.State())
	}
	if relaunched < 1500*time.Millisecond {
		t.Errorf("a relaunched %v after the crash, inside its 2s restart-delay", relaunched)
	}

	mustReachState(t, a, StateStarted)
	// b has no restart of its own: the crash takes it down for good.
	mustReachState(t, b, StateStopped)
	// Settled: the relaunched process must not have been stopped again.
	time.Sleep(300 * time.Millisecond)
	if st := a.State(); st != StateStarted {
		t.Errorf("a = %v after its restart, want STARTED", st)
	}
	if p := lockedPID(set, a); p <= 0 || syscall.Kill(p, 0) != nil {
		t.Errorf("a has no live process after its restart (pid %d)", p)
	}
	if n := a.RestartCount(); n != 1 {
		t.Errorf("a restart count = %d, want 1", n)
	}
}

// lockedDaemonPID reads a bgprocess's daemon pid under queueMu.
func lockedDaemonPID(set *ServiceSet, s *BGProcessService) int {
	set.queueMu.RLock()
	defer set.queueMu.RUnlock()
	return s.daemonPID
}

// TestBgDaemonCrashWithSlowHardDependentRestartsThroughStopped is the
// bgprocess side of TestCrashWithSlowHardDependentRestartsThroughStopped:
// restarting with initiateStart left forceStop set, so the relaunched
// daemon was brought down again as soon as it started.
func TestBgDaemonCrashWithSlowHardDependentRestartsThroughStopped(t *testing.T) {
	set, _ := newTestSet()
	pf := t.TempDir() + "/d.pid"
	a := NewBGProcessService(set, "a")
	a.SetCommand([]string{"/bin/sh", "-c", "sleep 3600 & echo $! > " + pf + "; exit 0"})
	a.SetPIDFile(pf)
	a.SetAutoRestart(RestartAlways)
	set.AddService(a)

	b := NewProcessService(set, "b")
	b.SetCommand([]string{"/bin/sh", "-c", stubbornShell})
	b.SetStopTimeout(1 * time.Second)
	b.Record().AddDep(a, DepRegular)
	set.AddService(b)
	defer func() {
		set.StopAllServices(ShutdownHalt)
		waitState(t, b, StateStopped, 5*time.Second)
		waitState(t, a, StateStopped, 5*time.Second)
	}()

	set.StartService(a)
	set.StartService(b)
	mustReachState(t, b, StateStarted)
	mustReachState(t, a, StateStarted)
	time.Sleep(200 * time.Millisecond) // let b's shell install its trap
	d1 := lockedDaemonPID(set, a)
	if err := syscall.Kill(d1, syscall.SIGKILL); err != nil {
		t.Fatalf("kill %d: %v", d1, err)
	}

	mustReachState(t, b, StateStopped)
	time.Sleep(500 * time.Millisecond)
	if st := a.State(); st != StateStarted {
		t.Errorf("a = %v after its restart, want STARTED", st)
	}
	if d := lockedDaemonPID(set, a); d <= 0 || d == d1 || syscall.Kill(d, 0) != nil {
		t.Errorf("a has no live new daemon after its restart (pid %d, old %d)", d, d1)
	}
	if n := a.RestartCount(); n != 1 {
		t.Errorf("a restart count = %d, want 1", n)
	}
}

// TestExitWhileWaitingForDependentsWaits stops a service whose hard
// dependent is slow to stop, and lets its process exit by itself while the
// stop is waiting for that dependent. The service must not report STOPPED
// underneath a dependent that is still running: dinit's
// process_service::handle_exit_status calls stopped() only when
// !waiting_for_deps, and otherwise leaves bring_down (pid gone) to finish
// the stop once the dependents are down.
func TestExitWhileWaitingForDependentsWaits(t *testing.T) {
	set, _ := newTestSet()
	a := NewProcessService(set, "a")
	a.SetCommand([]string{"/bin/sleep", "3600"})
	set.AddService(a)

	b := NewProcessService(set, "b")
	b.SetCommand([]string{"/bin/sh", "-c", stubbornShell})
	b.SetStopTimeout(1 * time.Second)
	b.Record().AddDep(a, DepRegular)
	set.AddService(b)

	set.StartService(b)
	mustReachState(t, b, StateStarted)
	time.Sleep(200 * time.Millisecond) // let b's shell install its trap
	set.StopService(a)
	pid := lockedPID(set, a)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill %d: %v", pid, err)
	}
	time.Sleep(300 * time.Millisecond)
	if as, bs := a.State(), b.State(); as == StateStopped && bs != StateStopped {
		t.Errorf("a reported STOPPED while its hard dependent b is still %v", bs)
	}

	mustReachState(t, b, StateStopped)
	mustReachState(t, a, StateStopped)
	if n := set.CountActiveServices(); n != 0 {
		t.Errorf("active services = %d after the stop, want 0", n)
	}
}

// TestBgDaemonExitWhileWaitingForDependentsWaits is the bgprocess side of
// TestExitWhileWaitingForDependentsWaits: the daemon dying while the stop
// waits for a hard dependent must not end the stop early either.
func TestBgDaemonExitWhileWaitingForDependentsWaits(t *testing.T) {
	set, _ := newTestSet()
	pf := t.TempDir() + "/d.pid"
	a := NewBGProcessService(set, "a")
	a.SetCommand([]string{"/bin/sh", "-c", "sleep 3600 & echo $! > " + pf + "; exit 0"})
	a.SetPIDFile(pf)
	set.AddService(a)

	b := NewProcessService(set, "b")
	b.SetCommand([]string{"/bin/sh", "-c", stubbornShell})
	b.SetStopTimeout(3 * time.Second)
	b.Record().AddDep(a, DepRegular)
	set.AddService(b)

	set.StartService(b)
	mustReachState(t, b, StateStarted)
	time.Sleep(200 * time.Millisecond) // let b's shell install its trap
	set.StopService(a)
	d := lockedDaemonPID(set, a)
	if err := syscall.Kill(d, syscall.SIGKILL); err != nil {
		t.Fatalf("kill %d: %v", d, err)
	}
	// The daemon monitor polls once a second; give it two polls.
	time.Sleep(2 * time.Second)
	if as, bs := a.State(), b.State(); as == StateStopped && bs != StateStopped {
		t.Errorf("a reported STOPPED while its hard dependent b is still %v", bs)
	}

	mustReachState(t, b, StateStopped)
	mustReachState(t, a, StateStopped)
	if n := set.CountActiveServices(); n != 0 {
		t.Errorf("active services = %d after the stop, want 0", n)
	}
}

// TestBgStopCommandOutlivesDaemon runs a bgprocess stop-command that kills
// the daemon and then keeps working. The daemon's death made the service
// STOPPED at once, while the stop-command was still cleaning up; and when
// the stop-command finished it signalled whatever s.daemonPID was by then
// — the daemon of the next start. dinit's bgproc_service calls stopped()
// only if stop_pid == -1 (proc-service.cc), as ProcessService already does.
func TestBgStopCommandOutlivesDaemon(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	pf := dir + "/d.pid"
	done := dir + "/cleaned"
	a := NewBGProcessService(set, "a")
	a.SetCommand([]string{"/bin/sh", "-c", "sleep 60 & echo $! > " + pf + "; exit 0"})
	a.SetPIDFile(pf)
	a.SetStopCommand([]string{"/bin/sh", "-c",
		"kill $(cat " + pf + "); sleep 1.5; touch " + done})
	set.AddService(a)
	defer func() {
		set.StopAllServices(ShutdownHalt)
		waitState(t, a, StateStopped, 5*time.Second)
	}()

	set.StartService(a)
	mustReachState(t, a, StateStarted)
	set.StopService(a)
	mustReachState(t, a, StateStopped)
	if _, err := os.Stat(done); err != nil {
		t.Errorf("a STOPPED before its stop-command finished cleaning up")
	}

	set.StartService(a)
	mustReachState(t, a, StateStarted)
	d2 := lockedDaemonPID(set, a)
	time.Sleep(2 * time.Second) // past the end of any stale stop-command
	if syscall.Kill(d2, 0) != nil || procIsZombie(d2) {
		t.Errorf("the new daemon %d was killed by the previous stop's stop-command", d2)
	}
	if st := a.State(); st != StateStarted {
		t.Errorf("a = %v, want STARTED", st)
	}
}

// TestBgStopCommandTimeoutAfterDaemonGone: once the daemon has died, the
// stop waits for the stop-command, so a wedged stop-command must still be
// bounded by stop-timeout (its timer died with the daemon monitor).
func TestBgStopCommandTimeoutAfterDaemonGone(t *testing.T) {
	set, _ := newTestSet()
	pf := t.TempDir() + "/d.pid"
	a := NewBGProcessService(set, "a")
	a.SetCommand([]string{"/bin/sh", "-c", "sleep 60 & echo $! > " + pf + "; exit 0"})
	a.SetPIDFile(pf)
	a.SetStopCommand([]string{"/bin/sh", "-c", "kill $(cat " + pf + "); exec sleep 60"})
	a.SetStopTimeout(2 * time.Second)
	set.AddService(a)

	set.StartService(a)
	mustReachState(t, a, StateStarted)
	set.StopService(a)
	if got := waitState(t, a, StateStopped, 4*time.Second); got != StateStopped {
		t.Errorf("a = %v 4s into a stop with stop-timeout 2s, want STOPPED", got)
	}
}

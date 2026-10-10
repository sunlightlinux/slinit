package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// startStranger launches a process slinit knows nothing about and writes
// its pid where a follow-pid service will look. It is deliberately not a
// child of anything slinit started: that is the whole point of follow-pid,
// and it is what makes wait4(2) unavailable and the poll necessary.
func startStranger(t *testing.T, pidFile string, seconds int) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf("sleep %d", seconds))
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting stranger: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	if pidFile != "" {
		if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", cmd.Process.Pid)), 0o644); err != nil {
			t.Fatalf("writing pid file: %v", err)
		}
	}
	return cmd
}

func TestFollowPIDAdoptsRunningProcess(t *testing.T) {
	set, _ := newTestSet()
	pidFile := filepath.Join(t.TempDir(), "stranger.pid")
	stranger := startStranger(t, pidFile, 60)

	svc := NewBGProcessService(set, "follow-svc")
	svc.SetFollowPID(pidFile)
	set.AddService(svc)
	set.StartService(svc)

	mustReachState(t, svc, StateStarted)

	if got := svc.PID(); got != stranger.Process.Pid {
		t.Errorf("adopted PID = %d, want the stranger's %d", got, stranger.Process.Pid)
	}
}

// A follow-pid service must not need a command: there is nothing to run.
// Before this existed, bgprocess refused to start without one.
func TestFollowPIDNeedsNoCommand(t *testing.T) {
	set, logger := newTestSet()
	pidFile := filepath.Join(t.TempDir(), "stranger.pid")
	startStranger(t, pidFile, 60)

	svc := NewBGProcessService(set, "follow-nocmd")
	svc.SetFollowPID(pidFile)
	set.AddService(svc)
	set.StartService(svc)

	mustReachState(t, svc, StateStarted)
	if len(logger.started) != 1 || logger.started[0] != "follow-nocmd" {
		t.Errorf("expected a ServiceStarted notification, got %v", logger.started)
	}
}

// The file arriving late is the normal case, not a failure: slinit may be
// asked to follow something that has not been started yet. A launched
// bgprocess fails fast on a missing pid file; following waits.
func TestFollowPIDWaitsForPIDFileToAppear(t *testing.T) {
	set, _ := newTestSet()
	pidFile := filepath.Join(t.TempDir(), "late.pid")

	svc := NewBGProcessService(set, "follow-late")
	svc.SetFollowPID(pidFile)
	svc.SetStartTimeout(5 * time.Second)
	set.AddService(svc)
	set.StartService(svc)

	if svc.State() == StateStarted {
		t.Fatal("started before the pid file existed")
	}

	time.Sleep(300 * time.Millisecond)
	startStranger(t, pidFile, 60)

	mustReachState(t, svc, StateStarted)
}

// A stale file naming a dead process must also wait rather than fail: it is
// indistinguishable from one about to be rewritten. It fails at the
// start-timeout, which is the only honest boundary.
func TestFollowPIDStaleFileWaitsThenFails(t *testing.T) {
	set, _ := newTestSet()
	pidFile := filepath.Join(t.TempDir(), "stale.pid")

	// A pid that is gone: start a process, reap it, reuse its number.
	dead := startStranger(t, "", 0)
	_, _ = dead.Process.Wait()
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", dead.Process.Pid)), 0o644); err != nil {
		t.Fatalf("writing stale pid file: %v", err)
	}

	svc := NewBGProcessService(set, "follow-stale")
	svc.SetFollowPID(pidFile)
	svc.SetStartTimeout(1500 * time.Millisecond)
	set.AddService(svc)
	set.StartService(svc)

	// It must still be trying a moment later, not have failed instantly.
	time.Sleep(400 * time.Millisecond)
	if st := svc.State(); st == StateStopped {
		t.Fatal("failed immediately on a stale pid file; should wait for the start-timeout")
	}

	mustReachState(t, svc, StateStopped)
}

// Stopping an adopted process signals it. The group is only signalled when
// the adopted process leads its own group, since otherwise the group
// belongs to whoever started it.
func TestFollowPIDStopSignalsAdoptedProcess(t *testing.T) {
	set, _ := newTestSet()
	pidFile := filepath.Join(t.TempDir(), "stranger.pid")
	stranger := startStranger(t, pidFile, 60)

	svc := NewBGProcessService(set, "follow-stop")
	svc.SetFollowPID(pidFile)
	set.AddService(svc)
	set.StartService(svc)
	mustReachState(t, svc, StateStarted)

	set.StopService(svc)
	mustReachState(t, svc, StateStopped)

	// kill(pid, 0) is NOT the check here: the stranger is this test's own
	// child, so until it is reaped it is a zombie, and kill(pid, 0)
	// succeeds for a zombie exactly as it does for a running process —
	// which is why slinit's own monitor pairs it with procIsZombie. Reap
	// it and read the wait status instead, which also proves the signal
	// was ours rather than the process simply having finished.
	state, err := stranger.Process.Wait()
	if err != nil {
		t.Fatalf("reaping the adopted process: %v", err)
	}
	ws, ok := state.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("no wait status for the adopted process")
	}
	if !ws.Signaled() {
		t.Errorf("adopted process exited on its own (status %v), expected a signal from the stop", state)
	} else if ws.Signal() != syscall.SIGTERM {
		t.Errorf("adopted process got %v, expected SIGTERM", ws.Signal())
	}
}

// signalProcessOnly is the guard that keeps a service stop from killing an
// operator's shell job. A process that does not lead its own group must be
// signalled alone.
func TestFollowPIDSignalsGroupOnlyWhenLeader(t *testing.T) {
	set, _ := newTestSet()
	svc := NewBGProcessService(set, "follow-sig")

	// Not following: the group is one slinit set up, so group-signal.
	if svc.signalProcessOnly(os.Getpid()) {
		t.Error("a launched bgprocess should signal its group")
	}

	svc.SetFollowPID("/nonexistent")
	// This test process does not lead its own group under `go test`, so
	// following it must fall back to the process alone.
	pgid, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Skipf("getpgid: %v", err)
	}
	wantOnly := pgid != os.Getpid()
	if got := svc.signalProcessOnly(os.Getpid()); got != wantOnly {
		t.Errorf("signalProcessOnly = %v, want %v (pid=%d pgid=%d)",
			got, wantOnly, os.Getpid(), pgid)
	}
}

// With a restart policy, a vanished adopted process means re-reading the
// pid file: there is nothing to exec, so "restart" can only mean looking
// again at what the file names. The man page says so, which is why it is
// tested rather than left to follow from BringUp being re-entered.
func TestFollowPIDRestartReadsPIDFileAgain(t *testing.T) {
	set, _ := newTestSet()
	pidFile := filepath.Join(t.TempDir(), "stranger.pid")
	first := startStranger(t, pidFile, 60)

	svc := NewBGProcessService(set, "follow-restart")
	svc.SetFollowPID(pidFile)
	svc.SetAutoRestart(RestartAlways)
	svc.SetRestartDelay(50 * time.Millisecond)
	svc.SetStartTimeout(5 * time.Second)
	set.AddService(svc)
	set.StartService(svc)
	mustReachState(t, svc, StateStarted)

	if got := svc.PID(); got != first.Process.Pid {
		t.Fatalf("adopted %d, want the first stranger %d", got, first.Process.Pid)
	}

	// The owner replaces the process and rewrites the file, as a daemon
	// doing a binary upgrade would.
	_ = first.Process.Kill()
	_, _ = first.Process.Wait()
	second := startStranger(t, pidFile, 60)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if svc.State() == StateStarted && svc.PID() == second.Process.Pid {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("never re-adopted the new pid: state=%v pid=%d, want %d",
		svc.State(), svc.PID(), second.Process.Pid)
}

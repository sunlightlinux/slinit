package service

import (
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

// killSpy records the pids KillActiveServices signals, in place of the
// real syscall.
type killSpy struct {
	mu   sync.Mutex
	pids []int
}

func (k *killSpy) kill(pid int, sig syscall.Signal) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.pids = append(k.pids, pid)
	return nil
}

func (k *killSpy) seen() []int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]int(nil), k.pids...)
}

func withKillSpy(t *testing.T, grace time.Duration) *killSpy {
	t.Helper()
	spy := &killSpy{}
	oldKill, oldGrace := killFunc, stopCommandGrace
	killFunc = spy.kill
	stopCommandGrace = grace
	t.Cleanup(func() {
		killFunc = oldKill
		stopCommandGrace = oldGrace
	})
	return spy
}

func containsPID(pids []int, want int) bool {
	for _, p := range pids {
		if p == want {
			return true
		}
	}
	return false
}

// TestKillActiveServicesSparesStopCommand is the regression that
// matters: a scripted service whose start command is gone reports its
// stop-command as its PID, so the immediate-kill path used to SIGKILL
// the cleanup script. For a service that manages a detached daemon
// (start-stop-daemon, supervise-daemon) that script is the only thing
// that will ever stop the daemon, and killing it orphaned the daemon
// with its pidfile intact — which then broke every subsequent boot.
func TestKillActiveServicesSparesStopCommand(t *testing.T) {
	spy := withKillSpy(t, time.Hour) // long enough that the grace never expires

	set, _ := newTestSet()
	cleaning := NewScriptedService(set, "ssd-demo")
	cleaning.startPID = 0
	cleaning.stopPID = 4242
	set.AddService(cleaning)

	set.KillActiveServices()

	if containsPID(spy.seen(), 4242) {
		t.Errorf("stop-command pid 4242 was killed immediately; signalled pids: %v", spy.seen())
	}
}

// TestKillActiveServicesKillsWorkload: everything that is not a
// stop-command still dies at once, which is what `now` promises.
func TestKillActiveServicesKillsWorkload(t *testing.T) {
	spy := withKillSpy(t, time.Hour)

	set, _ := newTestSet()
	running := NewScriptedService(set, "worker")
	running.startPID = 1111
	set.AddService(running)

	set.KillActiveServices()

	if !containsPID(spy.seen(), 1111) {
		t.Errorf("workload pid 1111 was not killed; signalled pids: %v", spy.seen())
	}
}

// TestKillActiveServicesGraceExpires: the grace is a reprieve, not a
// pardon. A stop-command still running when it expires is killed, so a
// hung cleanup script cannot hold the machine up either.
//
// Uses a real child rather than a spy, because killStopCommand goes
// through process.SignalProcess and the only honest evidence that the
// grace fired is a process that is no longer there.
func TestKillActiveServicesGraceExpires(t *testing.T) {
	withKillSpy(t, 10*time.Millisecond)

	cmd := exec.Command("/bin/sleep", "30")
	// Its own process group, as slinit gives every service it spawns:
	// killStopCommand signals the group, not the bare pid.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	// close(done) rather than a value on a channel: both the assertion
	// below and the cleanup below wait on it, and a closed channel can
	// be received from twice.
	var waitErr error
	done := make(chan struct{})
	go func() { waitErr = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})

	set, _ := newTestSet()
	cleaning := NewScriptedService(set, "hung")
	cleaning.startPID = 0
	cleaning.stopPID = cmd.Process.Pid
	set.AddService(cleaning)

	set.KillActiveServices()

	select {
	case <-done:
		// SIGKILL shows up as a signal death, not an exit status.
		ee, ok := waitErr.(*exec.ExitError)
		if !ok || ee.ProcessState.ExitCode() != -1 {
			t.Errorf("child ended as %v, want death by signal", waitErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stop-command survived its grace period")
	}
}

// TestKillActiveServicesSparesStopCommandForReal is the mirror image,
// also against a live child: within the grace, the stop-command is
// still running.
func TestKillActiveServicesSparesStopCommandForReal(t *testing.T) {
	withKillSpy(t, 10*time.Second)

	cmd := exec.Command("/bin/sleep", "30")
	// Its own process group, as slinit gives every service it spawns:
	// killStopCommand signals the group, not the bare pid.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	// close(done) rather than a value on a channel: both the assertion
	// below and the cleanup below wait on it, and a closed channel can
	// be received from twice.
	var waitErr error
	done := make(chan struct{})
	go func() { waitErr = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})

	set, _ := newTestSet()
	cleaning := NewScriptedService(set, "cleaning")
	cleaning.startPID = 0
	cleaning.stopPID = cmd.Process.Pid
	set.AddService(cleaning)

	set.KillActiveServices()

	select {
	case <-done:
		t.Fatalf("stop-command was killed inside its grace period: %v", waitErr)
	case <-time.After(300 * time.Millisecond):
		// Still running, which is the point.
	}
}

// TestRunningStopCommand pins the predicate the shutdown path branches
// on, including the case that must not be mistaken for cleanup: a
// service whose start command is still running.
func TestRunningStopCommand(t *testing.T) {
	set, _ := newTestSet()
	svc := NewScriptedService(set, "svc")

	if svc.runningStopCommand() {
		t.Error("idle service reported a stop-command")
	}

	svc.startPID = 100
	svc.stopPID = 200
	if svc.runningStopCommand() {
		t.Error("service with a live start command reported a stop-command")
	}

	svc.startPID = 0
	if !svc.runningStopCommand() {
		t.Error("service running only a stop-command did not report one")
	}
}

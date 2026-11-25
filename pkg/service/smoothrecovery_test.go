package service

import (
	"syscall"
	"testing"
	"time"
)

// lockedPID reads a process service's pid under queueMu.
func lockedPID(set *ServiceSet, s *ProcessService) int {
	set.queueMu.Lock()
	defer set.queueMu.Unlock()
	return s.PID()
}

// waitForNewPID polls until the service runs a process other than old.
func waitForNewPID(set *ServiceSet, s *ProcessService, old int, within time.Duration) int {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if p := lockedPID(set, s); p > 0 && p != old {
			return p
		}
		time.Sleep(20 * time.Millisecond)
	}
	return 0
}

// A crash inside restart-delay makes smooth recovery wait before the
// relaunch. That wait was armed on processTimer, which only
// monitorProcess reads — and monitorProcess had already returned with
// the process, so the relaunch never happened: the service sat STARTED
// with pid 0 for good.
func TestSmoothRecoveryRelaunchesAfterRestartDelay(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "smooth-delay")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	svc.SetSmoothRecovery(true)
	svc.SetRestartDelay(500 * time.Millisecond)
	set.AddService(svc)
	set.StartService(svc)
	first := waitForNewPID(set, svc, 0, 2*time.Second)
	if first == 0 {
		t.Fatalf("not started: %v", svc.State())
	}
	t.Cleanup(func() {
		set.StopService(svc)
		waitForState(t, svc, StateStopped, 3*time.Second)
	})

	syscall.Kill(first, syscall.SIGKILL) // well inside the 500ms delay
	second := waitForNewPID(set, svc, first, 3*time.Second)
	if second == 0 {
		t.Fatalf("smooth recovery never relaunched: state=%v pid=%d",
			svc.State(), lockedPID(set, svc))
	}
	if st := svc.State(); st != StateStarted {
		t.Errorf("state after recovery = %v, want STARTED", st)
	}
}

// A stop while the recovery is still waiting out restart-delay calls the
// relaunch off: nothing may start once the service is STOPPED.
func TestSmoothRecoveryDelayCancelledByStop(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "smooth-cancel")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	svc.SetSmoothRecovery(true)
	svc.SetRestartDelay(500 * time.Millisecond)
	set.AddService(svc)
	set.StartService(svc)
	first := waitForNewPID(set, svc, 0, 2*time.Second)
	if first == 0 {
		t.Fatalf("not started: %v", svc.State())
	}
	syscall.Kill(first, syscall.SIGKILL)
	time.Sleep(100 * time.Millisecond)
	set.StopService(svc)
	if got := waitForState(t, svc, StateStopped, 2*time.Second); got != StateStopped {
		t.Fatalf("stop during recovery delay: state %v", got)
	}
	time.Sleep(700 * time.Millisecond) // past the delay
	if p := lockedPID(set, svc); p != 0 || svc.State() != StateStopped {
		syscall.Kill(p, syscall.SIGKILL)
		t.Errorf("relaunched after stop: state=%v pid=%d", svc.State(), p)
	}
}

// The bgprocess twin: the delay was armed on processTimer, read only by
// monitorDaemon, which had returned when the daemon vanished.
func TestBGSmoothRecoveryRelaunchesAfterRestartDelay(t *testing.T) {
	set, _ := newTestSet()
	pidFile := t.TempDir() + "/daemon.pid"
	svc := NewBGProcessService(set, "bg-smooth")
	svc.SetCommand(bgTestLongDaemon(pidFile))
	svc.SetPIDFile(pidFile)
	svc.SetSmoothRecovery(true)
	svc.SetRestartDelay(3 * time.Second)
	set.AddService(svc)
	set.StartService(svc)
	if got := waitForState(t, svc, StateStarted, 5*time.Second); got != StateStarted {
		t.Fatalf("not started: %v", got)
	}
	first := waitForDaemonPID(t, pidFile, 0, 5*time.Second)
	t.Cleanup(func() { stopBGTestService(t, set, svc, pidFile) })
	syscall.Kill(first, syscall.SIGKILL)
	if second := waitForDaemonPID(t, pidFile, first, 8*time.Second); second == 0 {
		t.Errorf("bgprocess smooth recovery never relaunched (state %v)", svc.State())
	}
}

// A smooth recovery of a ready-notification service relaunches the
// process with a fresh start-timeout, and the new process signals ready
// while the service is already STARTED. That ready was dropped as
// meaningless, so the start-timeout was never cancelled: it fired later,
// SIGINTed the healthy process and failed a STARTED service.
func TestSmoothRecoveryReadyCancelsStartTimeout(t *testing.T) {
	set, lg := newTestSet()
	svc := NewProcessService(set, "smooth-notify")
	svc.SetCommand([]string{"/bin/sh", "-c", "echo ready >&3; exec sleep 60"})
	svc.SetReadyNotification(3, "")
	svc.SetSmoothRecovery(true)
	svc.SetRestartDelay(10 * time.Millisecond)
	svc.SetStartTimeout(1 * time.Second)
	set.AddService(svc)
	set.StartService(svc)
	if waitForState(t, svc, StateStarted, 2*time.Second) != StateStarted {
		t.Fatal("not started")
	}
	t.Cleanup(func() {
		set.StopService(svc)
		waitForState(t, svc, StateStopped, 3*time.Second)
	})
	first := lockedPID(set, svc)
	syscall.Kill(first, syscall.SIGKILL)
	second := waitForNewPID(set, svc, first, 2*time.Second)
	if second == 0 {
		t.Fatalf("no recovery: state=%v", svc.State())
	}
	time.Sleep(1500 * time.Millisecond) // past the start-timeout
	set.queueMu.Lock()
	pid, failed := svc.PID(), len(lg.failed)
	set.queueMu.Unlock()
	if st := svc.State(); st != StateStarted || pid != second || failed != 0 {
		t.Errorf("recovered process killed by a stale start-timeout: state=%v pid %d -> %d, %d failure(s)",
			st, second, pid, failed)
	}
}

// After a smooth recovery the new process's keepalives must find a
// watchdog listening, and a pipe to write to: the dropped ready closed the
// pipe, so the first keepalive killed the process with SIGPIPE, over and
// over until the restart limit stopped the service.
func TestSmoothRecoveryRearmsWatchdog(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "smooth-wd")
	svc.SetCommand([]string{"/bin/sh", "-c",
		"echo ready >&3; while :; do echo k >&3; sleep 0.1; done"})
	svc.SetReadyNotification(3, "")
	svc.SetWatchdogTimeout(1 * time.Second)
	svc.SetSmoothRecovery(true)
	svc.SetRestartDelay(10 * time.Millisecond)
	svc.SetStartTimeout(0)
	set.AddService(svc)
	set.StartService(svc)
	if waitForState(t, svc, StateStarted, 2*time.Second) != StateStarted {
		t.Fatal("not started")
	}
	t.Cleanup(func() {
		set.StopService(svc)
		waitForState(t, svc, StateStopped, 3*time.Second)
	})
	time.Sleep(300 * time.Millisecond)
	first := lockedPID(set, svc)
	syscall.Kill(first, syscall.SIGKILL)
	second := waitForNewPID(set, svc, first, 2*time.Second)
	time.Sleep(500 * time.Millisecond)
	set.queueMu.Lock()
	armed, pid, es := svc.watchdogStop != nil, svc.PID(), svc.GetExitStatus()
	set.queueMu.Unlock()
	if second == 0 || pid != second || !armed || svc.State() != StateStarted {
		t.Errorf("after recovery: pid %d -> %d (now %d), watchdog armed=%v, state=%v, last exit signal=%v",
			first, second, pid, armed, svc.State(), es.Signal())
	}
}

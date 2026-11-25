package service

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
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

// bgSmoothRecoveryScenario starts a smooth-recovery bgprocess "a" whose
// launcher leaves the pid file to its daemon, written 300ms after the
// launcher exits, with an internal service "b" depending on it. It kills
// the daemon and returns once the recovery has had time to settle, with
// the daemon pid it killed. With staleFile the launcher leaves the dead
// daemon's pid file in place until the new one overwrites it.
func bgSmoothRecoveryScenario(t *testing.T, staleFile bool) (set *ServiceSet, a *BGProcessService, b *InternalService, pf string, d1 int) {
	t.Helper()
	set, _ = newTestSet()
	pf = t.TempDir() + "/d.pid"
	rm := "rm -f " + pf + "; "
	if staleFile {
		rm = ""
	}
	a = NewBGProcessService(set, "a")
	a.SetCommand([]string{"/bin/sh", "-c",
		rm + "sleep 60 & D=$!; ( sleep 0.3; echo $D > " + pf + " ) & exit 0"})
	a.SetPIDFile(pf)
	a.SetAutoRestart(RestartAlways)
	a.SetSmoothRecovery(true)
	set.AddService(a)
	b = NewInternalService(set, "b")
	b.Record().AddDep(a, DepRegular)
	set.AddService(b)

	set.StartService(b)
	mustReachState(t, b, StateStarted)
	d1 = lockedDaemonPID(set, a)
	if err := syscall.Kill(d1, syscall.SIGKILL); err != nil {
		t.Fatalf("kill %d: %v", d1, err)
	}
	// One daemon poll (1s) to notice, 300ms for the new pid file.
	time.Sleep(2500 * time.Millisecond)
	return set, a, b, pf, d1
}

// checkRecoveredDaemon asserts that a smooth recovery left "a" STARTED and
// supervising the daemon its pid file names, and that the shutdown which
// follows takes that daemon down.
func checkRecoveredDaemon(t *testing.T, set *ServiceSet, a *BGProcessService, b *InternalService, pf string, d1 int) {
	t.Helper()
	data, _ := os.ReadFile(pf)
	d2, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if d2 <= 0 || d2 == d1 {
		t.Fatalf("pid file never named a new daemon: %q", data)
	}
	defer syscall.Kill(d2, syscall.SIGKILL)
	if st := a.State(); st != StateStarted {
		t.Errorf("a = %v after smooth recovery, want STARTED", st)
	}
	if st := b.State(); st != StateStarted {
		t.Errorf("hard dependent b = %v after smooth recovery, want STARTED", st)
	}
	if got := lockedDaemonPID(set, a); got != d2 {
		t.Errorf("a tracks daemon pid %d, want the recovered daemon %d", got, d2)
	}

	set.StopAllServices(ShutdownHalt)
	mustReachState(t, a, StateStopped)
	time.Sleep(200 * time.Millisecond)
	if syscall.Kill(d2, 0) == nil && !procIsZombie(d2) {
		t.Errorf("recovered daemon %d survived the shutdown", d2)
	}
}

// TestBGSmoothRecoveryLatePIDFile: the relaunched daemon writes its pid
// file after its launcher has exited, as forking daemons routinely do. The
// poll that waits for it gave up at once because the service was STARTED,
// not STARTING, leaving the service STARTED with no tracked pid and the new
// daemon outliving the shutdown. dinit re-reads the pid file in the STARTED
// branch of bgproc_service::handle_exit_status (proc-service.cc).
func TestBGSmoothRecoveryLatePIDFile(t *testing.T) {
	set, a, b, pf, d1 := bgSmoothRecoveryScenario(t, false)
	checkRecoveredDaemon(t, set, a, b, pf, d1)
}

// TestBGSmoothRecoveryStopWhilePIDFilePending stops the service while a
// smooth recovery is still waiting for its new daemon's pid file. With no
// pid to signal, the stop must wait for the file and bring that daemon
// down, not report STOPPED and leave it running unsupervised.
func TestBGSmoothRecoveryStopWhilePIDFilePending(t *testing.T) {
	bgStopWhilePIDFilePending(t, false)
}

// TestBGSmoothRecoveryStopWhileStalePIDFile is the same stop with the dead
// daemon's pid file still in place. The stop's own poll read that pid back,
// saw it was not running and called the stop done at once, while the new
// daemon went on to write its file and run unsupervised. A pid file naming
// the daemon being replaced is stale, as pollForPIDFile already knew.
func TestBGSmoothRecoveryStopWhileStalePIDFile(t *testing.T) {
	bgStopWhilePIDFilePending(t, true)
}

// bgStopWhilePIDFilePending stops a smooth-recovering bgprocess while its
// new daemon's pid file is pending, and checks that daemon does not outlive
// the stop. With staleFile the launcher leaves the dead daemon's pid file
// in place until the new one overwrites it.
func bgStopWhilePIDFilePending(t *testing.T, staleFile bool) {
	t.Helper()
	set, _ := newTestSet()
	pf := t.TempDir() + "/d.pid"
	rm := "rm -f " + pf + "; "
	if staleFile {
		rm = ""
	}
	a := NewBGProcessService(set, "a")
	a.SetCommand([]string{"/bin/sh", "-c",
		rm + "sleep 60 & D=$!; ( sleep 0.5; echo $D > " + pf + " ) & exit 0"})
	a.SetPIDFile(pf)
	a.SetSmoothRecovery(true)
	set.AddService(a)
	set.StartService(a)
	mustReachState(t, a, StateStarted)
	d1 := lockedDaemonPID(set, a)
	if err := syscall.Kill(d1, syscall.SIGKILL); err != nil {
		t.Fatalf("kill %d: %v", d1, err)
	}

	pending := func() bool {
		set.queueMu.RLock()
		defer set.queueMu.RUnlock()
		return a.recoveryPoll
	}
	deadline := time.Now().Add(3 * time.Second)
	for !pending() {
		if time.Now().After(deadline) {
			t.Fatalf("smooth recovery never waited for the pid file (a=%v)", a.State())
		}
		time.Sleep(10 * time.Millisecond)
	}
	set.StopService(a)
	mustReachState(t, a, StateStopped)

	var d2 int
	for deadline = time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		data, _ := os.ReadFile(pf)
		if d2, _ = strconv.Atoi(strings.TrimSpace(string(data))); d2 > 0 && d2 != d1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if d2 <= 0 || d2 == d1 {
		t.Fatalf("pid file never named a new daemon (read %d)", d2)
	}
	time.Sleep(200 * time.Millisecond)
	if syscall.Kill(d2, 0) == nil && !procIsZombie(d2) {
		syscall.Kill(d2, syscall.SIGKILL)
		t.Errorf("recovered daemon %d outlived the stop", d2)
	}
}

// TestBGSmoothRecoveryStalePIDFile: the dead daemon's pid file is still in
// place when the recovery's launcher exits, and the new daemon overwrites
// it a moment later. Reading the old pid back is not the new daemon dying:
// it was taken as a failed start of a STARTED service, which left the hard
// dependent STARTED and the new daemon unsupervised.
func TestBGSmoothRecoveryStalePIDFile(t *testing.T) {
	set, a, b, pf, d1 := bgSmoothRecoveryScenario(t, true)
	checkRecoveredDaemon(t, set, a, b, pf, d1)
}

// bgLaunchScript returns a launcher that counts its runs in dir and, on
// run 2 only, does what second does instead of forking a daemon into pf.
// PIDFILE in second stands for pf.
func bgLaunchScript(dir, pf, second string) []string {
	second = strings.ReplaceAll(second, "PIDFILE", pf)
	return []string{"/bin/sh", "-c",
		"n=$(cat " + dir + "/n 2>/dev/null || echo 0); n=$((n+1)); echo $n > " + dir + "/n; " +
			"if [ $n = 2 ]; then " + second + "; fi; " +
			"sleep 60 & echo $! > " + pf + "; exit 0"}
}

// bgRecoveryFailureCase kills the daemon of a smooth-recovery bgprocess
// whose relaunch does second, and checks the recovery failure takes the
// service and its hard dependent down instead of failing a STARTED service
// as though it were starting. dinit: hard_failure() in the STARTED branch
// of bgproc_service::handle_exit_status (proc-service.cc).
func bgRecoveryFailureCase(t *testing.T, second string) {
	set, _ := newTestSet()
	dir := t.TempDir()
	pf := dir + "/d.pid"
	a := NewBGProcessService(set, "a")
	a.SetCommand(bgLaunchScript(dir, pf, second))
	a.SetPIDFile(pf)
	a.SetAutoRestart(RestartAlways)
	a.SetSmoothRecovery(true)
	set.AddService(a)
	b := NewInternalService(set, "b")
	b.Record().AddDep(a, DepRegular)
	set.AddService(b)
	defer func() {
		set.StopAllServices(ShutdownHalt)
		waitState(t, a, StateStopped, 5*time.Second)
	}()

	set.StartService(b)
	mustReachState(t, b, StateStarted)
	d1 := lockedDaemonPID(set, a)
	if err := syscall.Kill(d1, syscall.SIGKILL); err != nil {
		t.Fatalf("kill %d: %v", d1, err)
	}
	if got := waitState(t, b, StateStopped, 4*time.Second); got != StateStopped {
		t.Errorf("hard dependent b = %v after a failed smooth recovery, want STOPPED (a=%v)",
			got, a.State())
	}
	if got := waitState(t, a, StateStopped, 2*time.Second); got != StateStopped {
		t.Errorf("a = %v after a failed smooth recovery, want STOPPED", got)
	}
	if r := a.StopReason(); r != ReasonFailed {
		t.Errorf("a stop reason = %v, want %v", r, ReasonFailed)
	}
	if n := set.CountActiveServices(); n != 0 {
		t.Errorf("active services = %d, want 0", n)
	}
}

func TestBGSmoothRecoveryLauncherFails(t *testing.T) {
	bgRecoveryFailureCase(t, "exit 3")
}

func TestBGSmoothRecoveryUnreadablePIDFile(t *testing.T) {
	bgRecoveryFailureCase(t, "echo garbage > PIDFILE; exit 0")
}

// TestBGSmoothRecoveryNewDaemonAlreadyDead: the recovery's pid file names a
// new daemon that has already died. That is a daemon death, handled as one
// (dinit: goto begin) — here a second smooth recovery, which succeeds —
// not a failed start of a STARTED service.
func TestBGSmoothRecoveryNewDaemonAlreadyDead(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	pf := dir + "/d.pid"
	a := NewBGProcessService(set, "a")
	a.SetCommand(bgLaunchScript(dir, pf, "(exit 0) & wait $!; echo $! > PIDFILE; exit 0"))
	a.SetPIDFile(pf)
	a.SetAutoRestart(RestartAlways)
	a.SetSmoothRecovery(true)
	set.AddService(a)
	b := NewInternalService(set, "b")
	b.Record().AddDep(a, DepRegular)
	set.AddService(b)
	defer func() {
		set.StopAllServices(ShutdownHalt)
		waitState(t, a, StateStopped, 5*time.Second)
	}()

	set.StartService(b)
	mustReachState(t, b, StateStarted)
	d1 := lockedDaemonPID(set, a)
	if err := syscall.Kill(d1, syscall.SIGKILL); err != nil {
		t.Fatalf("kill %d: %v", d1, err)
	}
	var d3 int
	for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); {
		if data, _ := os.ReadFile(dir + "/n"); strings.TrimSpace(string(data)) == "3" {
			if d3 = lockedDaemonPID(set, a); d3 > 0 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if d3 <= 0 || d3 == d1 || syscall.Kill(d3, 0) != nil {
		t.Errorf("a tracks no live daemon from the second recovery (pid %d)", d3)
	}
	if st := a.State(); st != StateStarted {
		t.Errorf("a = %v, want STARTED", st)
	}
	if st := b.State(); st != StateStarted {
		t.Errorf("hard dependent b = %v, want STARTED", st)
	}
}

// TestBGStaleRecoveryPollStandsDown arms a smooth recovery's pid-file poll,
// ends it elsewhere, and arms the next recovery's poll before the first has
// woken. The first poll checked only that recoveryPoll was set, which the
// second had set again, so it went on to adopt what its file named on the
// newer recovery's behalf, and the two could both finish the discovery:
// two monitorDaemon goroutines for one service. Each poll now owns a
// generation of its own. The polls watch different files here so the
// stale one is the only one that can act.
func TestBGStaleRecoveryPollStandsDown(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	stalePF, currentPF := dir+"/old.pid", dir+"/new.pid"
	a := NewBGProcessService(set, "a")
	a.SetCommand([]string{"/bin/true"})
	a.SetPIDFile(currentPF)
	set.AddService(a)

	daemon := exec.Command("/bin/sleep", "60")
	if err := daemon.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		daemon.Process.Kill()
		daemon.Wait()
	}()
	pid := []byte(strconv.Itoa(daemon.Process.Pid) + "\n")

	// A STARTED service with no tracked pid: a smooth recovery's launcher
	// has exited and its daemon's pid file is pending.
	set.queueMu.Lock()
	a.state.Store(StateStarted)
	a.pollForPIDFile(stalePF, nil) // the first recovery's poll
	a.recoveryPoll = false         // ended elsewhere
	a.pollForPIDFile(currentPF, nil)
	set.queueMu.Unlock()

	if err := os.WriteFile(stalePF, pid, 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * pidFilePollInterval)
	set.queueMu.Lock()
	adopted, pending := a.daemonPID, a.recoveryPoll
	set.queueMu.Unlock()
	if adopted != 0 || !pending {
		t.Errorf("a stale recovery poll acted for the current one (adopted pid %d, current poll still pending: %v)",
			adopted, pending)
	}

	// The current poll still finishes, and only once.
	if err := os.WriteFile(currentPF, pid, 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * pidFilePollInterval)
	if got := lockedDaemonPID(set, a); got != daemon.Process.Pid {
		t.Errorf("a tracks pid %d, want %d", got, daemon.Process.Pid)
	}
	frame := fmt.Sprintf("monitorDaemon(%p", a)
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	if got := strings.Count(string(buf[:n]), frame); got != 1 {
		t.Errorf("%d monitorDaemon goroutines for one daemon, want 1", got)
	}

	set.StopService(a)
	mustReachState(t, a, StateStopped)
}

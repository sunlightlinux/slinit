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

// The stop path of a process service: stop-command, stop-timeout and the
// escalation ladder behind it. Every case uses a main process that records
// the signals it sees in marker files, so a test can tell which of the
// ladder's steps actually ran rather than only where the state ended up.

// pathExists reports whether path exists.
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// waitPIDFile waits for path to hold a pid and returns it.
func waitPIDFile(t *testing.T, path string) int {
	t.Helper()
	s := waitForFileWithin(path, 5*time.Second)
	pid, err := strconv.Atoi(s)
	if err != nil || pid <= 0 {
		t.Fatalf("no pid in %s (%q)", path, s)
	}
	return pid
}

// trappingMain is a main process that installs trap for each of the given
// "SIG:action" pairs, then writes its pid to dir/main.pid and loops. An
// empty action ignores the signal. It is written to a script file so the
// trap actions need no shell quoting.
func trappingMain(t *testing.T, dir string, traps ...string) []string {
	t.Helper()
	var b strings.Builder
	for _, tr := range traps {
		sig, action, _ := strings.Cut(tr, ":")
		b.WriteString("trap '" + action + "' " + sig + "\n")
	}
	b.WriteString("echo $$ > " + filepath.Join(dir, "main.pid") + "\n")
	b.WriteString("while :; do sleep 0.05; done\n")
	script := filepath.Join(dir, "main.sh")
	if err := os.WriteFile(script, []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{"/bin/sh", script}
}

// startStopTest starts svc and waits until its main process has installed
// its traps; it returns that pid. The cleanup kills whatever is left.
func startStopTest(t *testing.T, set *ServiceSet, svc *ProcessService, dir string) int {
	t.Helper()
	set.AddService(svc)
	set.StartService(svc)
	mustReachState(t, svc, StateStarted)
	pid := waitPIDFile(t, filepath.Join(dir, "main.pid"))
	t.Cleanup(func() {
		killIfAlive(pid)
		set.StopService(svc)
		waitState(t, svc, StateStopped, 5*time.Second)
	})
	return pid
}

// stopWithin stops svc and fails the test unless it is STOPPED within d.
// It returns how long the stop took.
func stopWithin(t *testing.T, set *ServiceSet, svc Service, d time.Duration) time.Duration {
	t.Helper()
	began := time.Now()
	set.StopService(svc)
	if got := waitState(t, svc, StateStopped, d); got != StateStopped {
		t.Fatalf("%s = %v %v into its stop, want STOPPED", svc.Name(), got, d)
	}
	return time.Since(began)
}

// The stop-command runs in place of the stop signal: the main process
// never sees SIGTERM while the stop-command is doing the stopping, and the
// stop-command's own exit finishes the stop.
func TestProcessStopCommandReplacesStopSignal(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	svc := NewProcessService(set, "p")
	svc.SetCommand(trappingMain(t, dir, "TERM:touch "+dir+"/termed; exit 0"))
	svc.SetStopCommand([]string{"/bin/sh", "-c",
		"touch " + dir + "/stopran; kill -KILL $(cat " + dir + "/main.pid)"})
	svc.SetStopTimeout(10 * time.Second)
	startStopTest(t, set, svc, dir)

	if took := stopWithin(t, set, svc, 5*time.Second); took > 3*time.Second {
		t.Errorf("stop took %v; the stop-command finished it at once", took)
	}
	if !pathExists(dir + "/stopran") {
		t.Error("the stop-command never ran")
	}
	if pathExists(dir + "/termed") {
		t.Error("the main process got SIGTERM although a stop-command was configured")
	}
}

// A stop-command that leaves the main process running is followed by the
// stop signal, whether the stop-command succeeded or failed: the stop does
// not sit out the stop-timeout either way.
func TestProcessStopCommandExitThenSignalsMain(t *testing.T) {
	for _, tc := range []struct{ name, code string }{
		{"succeeds", "0"},
		{"fails", "3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set, _ := newTestSet()
			dir := t.TempDir()
			svc := NewProcessService(set, "p")
			svc.SetCommand(trappingMain(t, dir, "TERM:touch "+dir+"/termed; exit 0"))
			svc.SetStopCommand([]string{"/bin/sh", "-c",
				"touch " + dir + "/stopran; exit " + tc.code})
			svc.SetStopTimeout(10 * time.Second)
			startStopTest(t, set, svc, dir)

			stopWithin(t, set, svc, 4*time.Second)
			if !pathExists(dir + "/stopran") {
				t.Error("the stop-command never ran")
			}
			if !pathExists(dir + "/termed") {
				t.Error("the main process was not sent SIGTERM after the stop-command")
			}
		})
	}
}

// A stop-command that hangs, for a main process that ignores SIGTERM: the
// stop-timeout SIGKILLs both, and the service still reaches STOPPED.
func TestProcessStopCommandHangsStopTimeoutKillsAll(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	svc := NewProcessService(set, "p")
	svc.SetCommand(trappingMain(t, dir, "TERM:", "INT:"))
	svc.SetStopCommand([]string{"/bin/sh", "-c",
		"echo $$ > " + dir + "/stop.pid; exec sleep 60"})
	svc.SetStopTimeout(400 * time.Millisecond)
	main := startStopTest(t, set, svc, dir)

	stopWithin(t, set, svc, 4*time.Second)
	stopPID := waitPIDFile(t, dir+"/stop.pid")
	defer killIfAlive(stopPID)
	if processAlive(main) {
		t.Errorf("main process %d survived the stop-timeout", main)
	}
	if processAlive(stopPID) {
		t.Errorf("stop-command %d survived the stop-timeout", stopPID)
	}
}

// The usual stop-command kills the main process and then hangs. The main
// process's monitor, which reads the stop-timeout, is gone at that point:
// the stop-command's own timeout must still end it.
func TestProcessStopCommandHangsAfterMainGone(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	svc := NewProcessService(set, "p")
	svc.SetCommand(trappingMain(t, dir))
	svc.SetStopCommand([]string{"/bin/sh", "-c",
		"echo $$ > " + dir + "/stop.pid; kill -KILL $(cat " + dir + "/main.pid); exec sleep 60"})
	svc.SetStopTimeout(400 * time.Millisecond)
	startStopTest(t, set, svc, dir)

	stopWithin(t, set, svc, 4*time.Second)
	stopPID := waitPIDFile(t, dir+"/stop.pid")
	defer killIfAlive(stopPID)
	if processAlive(stopPID) {
		t.Errorf("stop-command %d survived its stop-timeout", stopPID)
	}
}

// The main process exiting while the stop-command is still running does not
// finish the stop: the stop-command's cleanup does (mainGoneDuringStop).
func TestProcessMainGoneDuringStopWaitsForStopCommand(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	svc := NewProcessService(set, "p")
	svc.SetCommand(trappingMain(t, dir))
	svc.SetStopCommand([]string{"/bin/sh", "-c",
		"kill -KILL $(cat " + dir + "/main.pid); sleep 0.7; touch " + dir + "/cleaned"})
	svc.SetStopTimeout(10 * time.Second)
	main := startStopTest(t, set, svc, dir)

	set.StopService(svc)
	deadline := time.Now().Add(3 * time.Second)
	for processAlive(main) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if processAlive(main) {
		t.Fatalf("the stop-command did not kill the main process")
	}
	mustReachState(t, svc, StateStopped)
	if !pathExists(dir + "/cleaned") {
		t.Error("STOPPED while the stop-command was still cleaning up after the main process")
	}
}

// timeout-abort-sec puts a SIGABRT step before the final SIGKILL.
// SignalProcessOnly keeps the SIGABRT off the shell's sleep child, which
// would otherwise dump core.
func TestProcessStopTimeoutAbortSecSendsSIGABRTFirst(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	svc := NewProcessService(set, "p")
	svc.SetCommand(trappingMain(t, dir, "TERM:", "INT:", "ABRT:touch "+dir+"/aborted"))
	svc.Flags.SignalProcessOnly = true
	svc.SetStopTimeout(300 * time.Millisecond)
	svc.SetTimeoutAbortSec(300 * time.Millisecond)
	main := startStopTest(t, set, svc, dir)

	stopWithin(t, set, svc, 4*time.Second)
	if !pathExists(dir + "/aborted") {
		t.Error("no SIGABRT before the final kill")
	}
	if processAlive(main) {
		t.Errorf("main process %d survived the abort timeout", main)
	}
}

// timeout-stop-failure-mode=abort: SIGABRT at the stop-timeout, then the
// final kill once timeout-abort-sec has passed too.
func TestProcessStopFailureModeAbort(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	svc := NewProcessService(set, "p")
	svc.SetCommand(trappingMain(t, dir, "TERM:", "INT:", "ABRT:touch "+dir+"/aborted"))
	svc.Flags.SignalProcessOnly = true
	svc.SetStopTimeout(300 * time.Millisecond)
	svc.SetTimeoutAbortSec(300 * time.Millisecond)
	svc.Record().SetTimeoutStopFailureMode(TimeoutFailureAbort)
	main := startStopTest(t, set, svc, dir)

	stopWithin(t, set, svc, 4*time.Second)
	if !pathExists(dir + "/aborted") {
		t.Error("stop-failure-mode=abort sent no SIGABRT")
	}
	if processAlive(main) {
		t.Errorf("main process %d survived the abort timeout", main)
	}
}

// timeout-stop-failure-mode=kill goes straight to the final kill at the
// stop-timeout, skipping the SIGABRT step timeout-abort-sec would add.
func TestProcessStopFailureModeKillSkipsAbort(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	svc := NewProcessService(set, "p")
	svc.SetCommand(trappingMain(t, dir, "TERM:", "INT:", "ABRT:touch "+dir+"/aborted"))
	svc.Flags.SignalProcessOnly = true
	svc.SetStopTimeout(300 * time.Millisecond)
	svc.SetTimeoutAbortSec(10 * time.Second)
	svc.Record().SetTimeoutStopFailureMode(TimeoutFailureKill)
	startStopTest(t, set, svc, dir)

	stopWithin(t, set, svc, 3*time.Second)
	if pathExists(dir + "/aborted") {
		t.Error("stop-failure-mode=kill sent SIGABRT")
	}
}

// final-kill-signal replaces SIGKILL at the end of the ladder.
func TestProcessFinalKillSignalIsUsed(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	svc := NewProcessService(set, "p")
	svc.SetCommand(trappingMain(t, dir, "TERM:", "INT:", "HUP:touch "+dir+"/hupped; exit 0"))
	svc.SetStopTimeout(300 * time.Millisecond)
	svc.Record().SetFinalKillSignal(syscall.SIGHUP)
	startStopTest(t, set, svc, dir)

	stopWithin(t, set, svc, 4*time.Second)
	if !pathExists(dir + "/hupped") {
		t.Error("the final kill was not final-kill-signal (SIGHUP)")
	}
}

// survive-final-kill-signal: the stop-timeout escalates no further; the
// service stays STOPPING until its process goes by other means.
func TestProcessSurviveFinalKillSignalDoesNotEscalate(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	svc := NewProcessService(set, "p")
	svc.SetCommand(trappingMain(t, dir, "TERM:", "INT:"))
	svc.SetStopTimeout(200 * time.Millisecond)
	svc.Record().SetSurviveFinalKillSignal(true)
	main := startStopTest(t, set, svc, dir)

	set.StopService(svc)
	time.Sleep(800 * time.Millisecond) // four stop-timeouts
	if st := svc.State(); st != StateStopping {
		t.Errorf("state = %v past the stop-timeout, want STOPPING", st)
	}
	if !processAlive(main) {
		t.Fatalf("main process was killed despite survive-final-kill-signal")
	}
	killIfAlive(main)
	mustReachState(t, svc, StateStopped)
}

// timeout-start-failure-mode=kill: a start that overruns start-timeout is
// SIGKILLed, so a process ignoring SIGINT (the default start-timeout signal)
// does not hold the failed start until a long stop-timeout.
func TestProcessStartTimeoutFailureModeKill(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	svc := NewProcessService(set, "p")
	svc.SetCommand(trappingMain(t, dir, "TERM:", "INT:"))
	svc.SetReadyNotification(3, "")
	svc.SetStartTimeout(300 * time.Millisecond)
	svc.SetStopTimeout(30 * time.Second)
	svc.SetTimeoutStartFailureMode(TimeoutFailureKill)
	set.AddService(svc)
	set.StartService(svc)
	main := waitPIDFile(t, filepath.Join(dir, "main.pid"))
	defer killIfAlive(main)

	if got := waitState(t, svc, StateStopped, 3*time.Second); got != StateStopped {
		t.Fatalf("state = %v after the start-timeout, want STOPPED", got)
	}
	if !svc.DidStartFail() {
		t.Error("a timed-out start was not reported as failed")
	}
	if processAlive(main) {
		t.Errorf("main process %d survived start-failure-mode=kill", main)
	}
}

// exit-type=cgroup: the main process exiting does not finish the stop while
// the service's cgroup still holds processes; it finishes once the cgroup
// reports itself empty. The cgroup is a directory with a cgroup.events file
// standing in for the kernel's, set after the start so the launch itself
// does not try to use it.
func TestProcessExitTypeCgroupWaitsForDrain(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	cg := filepath.Join(dir, "cg")
	if err := os.Mkdir(cg, 0o755); err != nil {
		t.Fatal(err)
	}
	events := filepath.Join(cg, "cgroup.events")
	if err := os.WriteFile(events, []byte("populated 1\nfrozen 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := NewProcessService(set, "p")
	svc.SetCommand(trappingMain(t, dir))
	svc.SetExitType(ExitTypeCgroup)
	main := startStopTest(t, set, svc, dir)
	set.queueMu.Lock()
	svc.Record().SetCgroupPath(cg)
	set.queueMu.Unlock()

	set.StopService(svc)
	deadline := time.Now().Add(3 * time.Second)
	for processAlive(main) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if processAlive(main) {
		t.Fatalf("main process %d not stopped", main)
	}
	time.Sleep(600 * time.Millisecond) // three drain polls
	if st := svc.State(); st != StateStopping {
		t.Errorf("state = %v with the cgroup still populated, want STOPPING", st)
	}
	if err := os.WriteFile(events, []byte("populated 0\nfrozen 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := waitState(t, svc, StateStopped, 2*time.Second); got != StateStopped {
		t.Errorf("state = %v after the cgroup drained, want STOPPED", got)
	}
}

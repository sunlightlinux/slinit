package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The bgprocess side of stopcommand_test.go: the daemon is forked off a
// launcher and found through its pid file.

// trappingDaemon writes a daemon script that installs the given
// "SIG:action" traps, writes its own pid to dir/d.pid and loops, and
// returns a launcher that backgrounds it and exits 0. The pid file is
// written by the daemon after its traps are in place, so a STARTED
// service always has a daemon ready for the signals a test sends.
func trappingDaemon(t *testing.T, dir string, traps ...string) []string {
	t.Helper()
	var b strings.Builder
	for _, tr := range traps {
		sig, action, _ := strings.Cut(tr, ":")
		b.WriteString("trap '" + action + "' " + sig + "\n")
	}
	b.WriteString("echo $$ > " + filepath.Join(dir, "d.pid") + "\n")
	b.WriteString("while :; do sleep 0.05; done\n")
	script := filepath.Join(dir, "daemon.sh")
	if err := os.WriteFile(script, []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{"/bin/sh", "-c", "/bin/sh " + script + " & exit 0"}
}

// startBgStopTest starts svc and returns its daemon's pid. The cleanup
// kills whatever is left.
func startBgStopTest(t *testing.T, set *ServiceSet, svc *BGProcessService, dir string) int {
	t.Helper()
	svc.SetPIDFile(filepath.Join(dir, "d.pid"))
	set.AddService(svc)
	set.StartService(svc)
	mustReachState(t, svc, StateStarted)
	pid := lockedDaemonPID(set, svc)
	if pid <= 0 {
		t.Fatalf("STARTED with no daemon pid")
	}
	t.Cleanup(func() {
		killIfAlive(pid)
		set.StopService(svc)
		waitState(t, svc, StateStopped, 5*time.Second)
	})
	return pid
}

// A stop-command that leaves the daemon running is followed by the stop
// signal to the daemon, whether the stop-command succeeded or failed.
func TestBgStopCommandExitThenSignalsDaemon(t *testing.T) {
	for _, tc := range []struct{ name, code string }{
		{"succeeds", "0"},
		{"fails", "3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set, _ := newTestSet()
			dir := t.TempDir()
			svc := NewBGProcessService(set, "d")
			svc.SetCommand(trappingDaemon(t, dir, "TERM:touch "+dir+"/termed; exit 0"))
			svc.SetStopCommand([]string{"/bin/sh", "-c",
				"touch " + dir + "/stopran; exit " + tc.code})
			svc.SetStopTimeout(20 * time.Second)
			startBgStopTest(t, set, svc, dir)

			// The daemon monitor polls once a second.
			stopWithin(t, set, svc, 5*time.Second)
			if !pathExists(dir + "/stopran") {
				t.Error("the stop-command never ran")
			}
			if !pathExists(dir + "/termed") {
				t.Error("the daemon was not sent SIGTERM after the stop-command")
			}
		})
	}
}

// deadPID returns the pid of a process that has run and been reaped.
func deadPID(t *testing.T) int {
	t.Helper()
	c := exec.Command("/bin/true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	return c.Process.Pid
}

// A launcher that exits 0 leaving a pid file naming a process that is no
// longer running has not started anything: the start fails.
func TestBGProcessPIDFileNamesDeadProcessFails(t *testing.T) {
	set, _ := newTestSet()
	pf := filepath.Join(t.TempDir(), "d.pid")
	svc := NewBGProcessService(set, "d")
	svc.SetCommand([]string{"/bin/sh", "-c",
		"echo " + strconv.Itoa(deadPID(t)) + " > " + pf + "; exit 0"})
	svc.SetPIDFile(pf)
	set.AddService(svc)
	set.StartService(svc)

	if got := waitState(t, svc, StateStopped, 3*time.Second); got != StateStopped {
		t.Fatalf("state = %v, want STOPPED", got)
	}
	if !svc.DidStartFail() {
		t.Error("a pid file naming a dead process was not a failed start")
	}
}

// A launcher that exits with a failure status fails the start, and
// whatever it left in its process group is killed: nothing is going to be
// adopted from a failed launch.
func TestBGProcessFailedLauncherFailsAndKillsItsGroup(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	svc := NewBGProcessService(set, "d")
	svc.SetCommand([]string{"/bin/sh", "-c",
		"sleep 60 & echo $! > " + dir + "/litter.pid; exit 4"})
	svc.SetPIDFile(dir + "/d.pid")
	set.AddService(svc)
	set.StartService(svc)

	if got := waitState(t, svc, StateStopped, 3*time.Second); got != StateStopped {
		t.Fatalf("state = %v, want STOPPED", got)
	}
	if !svc.DidStartFail() {
		t.Error("a launcher exiting 4 was not a failed start")
	}
	litter := waitPIDFile(t, dir+"/litter.pid")
	defer killIfAlive(litter)
	deadline := time.Now().Add(2 * time.Second)
	for processAlive(litter) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processAlive(litter) {
		t.Errorf("the failed launcher's child %d was left running", litter)
	}
}

// required-files is checked before the launcher runs: a missing one fails
// the start and nothing is launched.
func TestBGProcessRequiredPathMissingLaunchesNothing(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	svc := NewBGProcessService(set, "d")
	svc.SetCommand([]string{"/bin/sh", "-c", "touch " + dir + "/launched"})
	svc.SetPIDFile(dir + "/d.pid")
	svc.Record().SetRequiredPaths([]string{dir + "/missing"}, nil)
	set.AddService(svc)
	set.StartService(svc)

	if got := waitState(t, svc, StateStopped, 3*time.Second); got != StateStopped {
		t.Fatalf("state = %v, want STOPPED", got)
	}
	if !svc.DidStartFail() {
		t.Error("a missing required file was not a failed start")
	}
	time.Sleep(100 * time.Millisecond)
	if pathExists(dir + "/launched") {
		t.Error("the launcher ran although a required file was missing")
	}
}

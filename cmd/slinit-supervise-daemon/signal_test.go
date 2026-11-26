package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// unrelatedProcess starts a sleep that no supervisor owns: what a
// pidfile's pid names after it is reused.
func unrelatedProcess(t *testing.T) (pid int, exited <-chan struct{}) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("no sleep: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})
	return cmd.Process.Pid, done
}

func writePid(t *testing.T, path string, pid int) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertNotSignalled(t *testing.T, what string, exited <-chan struct{}) {
	t.Helper()
	select {
	case <-exited:
		t.Errorf("%s: unrelated process was signalled", what)
	case <-time.After(200 * time.Millisecond):
	}
}

// --signal reaches the supervised daemon.
func TestCmdSignalHitsDaemon(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	bin := buildBinary(t)
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "svc.pid")
	marker := filepath.Join(dir, "usr1")
	_, daemon := startSupervised(t, bin, pidfile, "--exec", sh, "--", "-c",
		`trap ': > "$0"' USR1; while :; do sleep 0.1; done`, marker)
	time.Sleep(200 * time.Millisecond)

	if code, out := runBin(t, bin, nil, "svc", "--signal", "USR1", "--pidfile", pidfile); code != exitOK {
		t.Fatalf("--signal: exit %d: %s", code, out)
	}
	waitFile(t, marker)
	if !processAlive(daemon) {
		t.Errorf("daemon pid %d died on USR1", daemon)
	}
}

// PIDFILE.daemon outlives its daemon. --signal used to signal whatever
// it named, so after pid reuse it hit an unrelated process; it now
// requires the pid to be the running supervisor's child.
func TestCmdSignalRefusesReusedDaemonPid(t *testing.T) {
	bin := buildBinary(t)
	pidfile := filepath.Join(t.TempDir(), "svc.pid")
	startSupervised(t, bin, pidfile, "--exec", "sleep", "--", "60")

	victim, exited := unrelatedProcess(t)
	writePid(t, pidfile+".daemon", victim)
	code, _ := runBin(t, bin, nil, "svc", "--signal", "KILL", "--pidfile", pidfile)
	if code != exitStalePidfile {
		t.Errorf("--signal at a reused daemon pid: exit %d, want %d", code, exitStalePidfile)
	}
	assertNotSignalled(t, "--signal at a reused daemon pid", exited)
}

// With no live supervisor behind it the daemon pidfile is stale even
// if the pid it names runs.
func TestCmdSignalNeedsLiveSupervisor(t *testing.T) {
	notSuper, _ := unrelatedProcess(t)
	victim, exited := unrelatedProcess(t)
	pidfile := filepath.Join(t.TempDir(), "svc.pid")
	writePid(t, pidfile, notSuper)
	writePid(t, pidfile+".daemon", victim)
	if code := cmdSignal(Options{PidFile: pidfile, Signal: syscall.SIGKILL}); code == exitOK {
		t.Errorf("cmdSignal signalled without a supervisor")
	}
	assertNotSignalled(t, "supervisor pidfile naming a non-supervisor", exited)

	pidfile = filepath.Join(t.TempDir(), "svc.pid")
	writePid(t, pidfile+".daemon", victim)
	if code := cmdSignal(Options{PidFile: pidfile, Signal: syscall.SIGKILL}); code == exitOK {
		t.Errorf("cmdSignal signalled with no supervisor pidfile")
	}
	assertNotSignalled(t, "no supervisor pidfile", exited)
}

// A pidfile whose pid now runs something other than a supervise-daemon
// is stale; it used to make --start report "already running".
func TestStartIgnoresReusedSupervisorPid(t *testing.T) {
	bin := buildBinary(t)
	pidfile := filepath.Join(t.TempDir(), "svc.pid")
	victim, exited := unrelatedProcess(t)
	writePid(t, pidfile, victim)

	code, out := runBin(t, bin, nil, "svc", "--start", "--pidfile", pidfile, "--exec", "sleep", "--", "60")
	if code != exitOK {
		t.Fatalf("--start over a reused pid: exit %d: %s", code, out)
	}
	waitFile(t, pidfile+".daemon")
	super, daemon := pidOf(t, pidfile), pidOf(t, pidfile+".daemon")
	t.Cleanup(func() {
		_ = syscall.Kill(super, syscall.SIGKILL)
		_ = syscall.Kill(daemon, syscall.SIGKILL)
	})
	if super == victim {
		t.Errorf("pidfile still names the unrelated pid")
	}
	assertNotSignalled(t, "--start", exited)

	// A real supervisor still blocks a second --start.
	if code, _ := runBin(t, bin, nil, "svc", "--start", "--pidfile", pidfile, "--exec", "sleep", "--", "60"); code != exitAlready {
		t.Errorf("second --start: exit %d, want %d", code, exitAlready)
	}
}

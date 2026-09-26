package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// startTestDaemon returns a live process that outlives the launcher.
//
// The daemon cannot come from the service's own shell. slinit SIGKILLs the
// launcher's process group as soon as the launcher is reaped
// (process.KillProcessGroup), so a background child survives only if it
// escapes the group first — real daemons do that with setsid() in the
// first instants, but a shell helper cannot win that race. One that tried
// would die for its own reasons and look exactly like the bug under test,
// which is how an hour goes missing. Owning the process here keeps the
// test about slinit's side of the contract: the launcher has exited, and a
// pid file naming a live process appears afterwards.
func startTestDaemon(t *testing.T) int {
	t.Helper()
	daemon := exec.Command("sleep", "60")
	if err := daemon.Start(); err != nil {
		t.Fatalf("starting test daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = daemon.Process.Kill()
		_, _ = daemon.Process.Wait()
	})
	return daemon.Process.Pid
}

// writePIDFileAfter writes pid into path once delay has passed.
func writePIDFileAfter(path string, pid int, delay time.Duration) {
	go func() {
		time.Sleep(delay)
		_ = os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o644)
	}()
}

// TestBGProcessWaitsForLatePIDFile is the regression for the pid-file
// race. The read used to happen exactly once, right after the launcher
// exited, so a daemon that had not written its file yet was declared
// failed. Measured with nginx: absent at that instant in 7 of 10 starts.
func TestBGProcessWaitsForLatePIDFile(t *testing.T) {
	set, _ := newTestSet()

	pidFile := filepath.Join(t.TempDir(), "late.pid")
	pid := startTestDaemon(t)
	writePIDFileAfter(pidFile, pid, 300*time.Millisecond)

	svc := NewBGProcessService(set, "late-svc")
	svc.SetCommand([]string{"/bin/sh", "-c", "exit 0"})
	svc.SetPIDFile(pidFile)
	set.AddService(svc)

	set.StartService(svc)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && svc.State() != StateStarted {
		time.Sleep(20 * time.Millisecond)
	}

	if svc.State() != StateStarted {
		t.Fatalf("service did not wait for the pid file: state %v, want STARTED", svc.State())
	}
	if svc.PID() != pid {
		t.Errorf("adopted PID = %d, want the pid from the file (%d)", svc.PID(), pid)
	}
}

// Waiting must stay bounded. A pid file that never arrives has to fail the
// start rather than hold the service in STARTING with nothing watching it:
// the armed start-timeout timer cannot do that job here, because it only
// acts when a PID is already known (it reads launcherPID, then daemonPID)
// and at this point both are zero.
func TestBGProcessPIDFileWaitIsBounded(t *testing.T) {
	set, _ := newTestSet()

	pidFile := filepath.Join(t.TempDir(), "never.pid")

	svc := NewBGProcessService(set, "never-svc")
	svc.SetCommand([]string{"/bin/sh", "-c", "exit 0"})
	svc.SetPIDFile(pidFile)
	svc.SetStartTimeout(300 * time.Millisecond)
	set.AddService(svc)

	set.StartService(svc)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && svc.State() == StateStarting {
		time.Sleep(20 * time.Millisecond)
	}

	switch svc.State() {
	case StateStarting:
		t.Fatal("service is wedged in STARTING: the wait for the pid file is unbounded")
	case StateStarted:
		t.Fatal("service reached STARTED with no pid file")
	}
}

// Polling runs on its own goroutine while the service sits in STARTING. A
// stop arriving in that window must win, and must not be overtaken by a
// late pid file quietly completing the start behind it.
func TestBGProcessStopDuringPIDFileWait(t *testing.T) {
	set, _ := newTestSet()

	pidFile := filepath.Join(t.TempDir(), "slow.pid")
	pid := startTestDaemon(t)
	writePIDFileAfter(pidFile, pid, 1*time.Second)

	svc := NewBGProcessService(set, "slow-svc")
	svc.SetCommand([]string{"/bin/sh", "-c", "exit 0"})
	svc.SetPIDFile(pidFile)
	set.AddService(svc)

	set.StartService(svc)

	// Let the launcher exit so the poll loop is really running, then stop
	// while the pid file is still most of a second away.
	time.Sleep(200 * time.Millisecond)
	set.StopService(svc)

	// Well past the moment the file appears.
	time.Sleep(1500 * time.Millisecond)

	if svc.State() == StateStarted {
		t.Errorf("a late pid file completed the start after a stop was requested")
	}
}

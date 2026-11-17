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

// sleepBinary is the sleep executable, with symlinks resolved so that it is
// the path /proc/<pid>/exe will report. Where sleep in PATH is a symlink (a
// busybox applet, or a usr-merge /bin that ubuntu's CI runner has),
// exec.LookPath returns the link but the kernel names the target it was
// exec'd from, and the match would never see the two as equal.
func sleepBinary(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// waitExe blocks until /proc/<pid>/exe resolves to want, the same symlink
// the match code reads. Start()/spawn() return after the fork but before the
// child execs, so the symlink points at the test binary for a while; under
// load a fixed sleep raced the exec and the running/status probes then found
// "no matching process" and started a second daemon. Polling removes the
// guessed duration.
func waitExe(t *testing.T, pid int, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if exe, _ := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe"); exe == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("/proc/%d/exe never became %s", pid, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestE2EStartBackgroundStop exercises the real spawn → pidfile → stop
// cycle against /bin/sleep, the smallest widely-available "hangs until
// killed" binary. Uses spawn() and cmdStop() directly rather than
// shelling out to the compiled binary so tests stay hermetic.
func TestE2EStartBackgroundStop(t *testing.T) {
	sleep := sleepBinary(t)

	dir := t.TempDir()
	pidfile := filepath.Join(dir, "sleep.pid")

	opts := Options{
		Mode:        "start",
		Exec:        sleep,
		PidFile:     pidfile,
		Background:  true,
		MakePidfile: true,
		Args:        []string{"60"},
		Signal:      syscall.SIGTERM,
	}
	binary, argv, err := resolveExec(opts)
	if err != nil {
		t.Fatal(err)
	}
	pid, _, err := spawn(binary, argv, opts)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	})

	if err := writePIDFile(pidfile, pid); err != nil {
		t.Fatal(err)
	}
	if !processAlive(pid) {
		t.Fatalf("pid %d exited before we could probe", pid)
	}

	// Wait for the exec to complete so /proc/PID/exe points at sleep and
	// not our test binary.
	waitExe(t, pid, sleep)

	// Status: should report running.
	statusOpts := Options{PidFile: pidfile}
	if code := cmdStatus(statusOpts); code != exitOK {
		t.Errorf("status: got %d, want %d", code, exitOK)
	}

	// Stop with retry: TERM, then KILL if it lingers.
	stopOpts := Options{
		Mode:    "stop",
		PidFile: pidfile,
		Signal:  syscall.SIGTERM,
		Retry:   "TERM/2/KILL/2",
	}
	if code := cmdStop(stopOpts); code != exitOK {
		t.Errorf("stop: got %d, want %d", code, exitOK)
	}
	// Confirm process is gone.
	if processAlive(pid) {
		t.Errorf("pid %d still alive after stop", pid)
	}
	// Pidfile should be gone.
	if _, err := os.Stat(pidfile); !os.IsNotExist(err) {
		t.Errorf("pidfile still present after successful stop: %v", err)
	}
}

func TestE2EStopStalePidFile(t *testing.T) {
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "stale.pid")
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(999_999)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Stale pidfile: cmdStop reports exitStalePidfile per LSB, unless oknodo.
	opts := Options{
		Mode:    "stop",
		PidFile: pidfile,
	}
	if code := cmdStop(opts); code != exitStalePidfile {
		t.Errorf("stop stale: got %d, want %d", code, exitStalePidfile)
	}
	opts.OKnodo = true
	if code := cmdStop(opts); code != exitOK {
		t.Errorf("stop stale --oknodo: got %d, want %d", code, exitOK)
	}
}

func TestE2EStartAlreadyRunning(t *testing.T) {
	sleep := sleepBinary(t)
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "sleep.pid")

	// Prime: spawn once, drop pidfile.
	first := exec.Command(sleep, "60")
	if err := first.Start(); err != nil {
		t.Fatalf("prime spawn: %v", err)
	}
	pid := first.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		_ = first.Wait()
	})
	if err := writePIDFile(pidfile, pid); err != nil {
		t.Fatal(err)
	}
	// Wait for /proc/PID/exe to reflect sleep.
	waitExe(t, pid, sleep)

	// Second start with same pidfile should refuse.
	opts := Options{
		Mode:    "start",
		Exec:    sleep,
		PidFile: pidfile,
		Args:    []string{"60"},
		Signal:  syscall.SIGTERM,
	}
	if code := cmdStart(opts); code != exitAlready {
		t.Errorf("second start: got %d, want %d", code, exitAlready)
	}
	opts.OKnodo = true
	if code := cmdStart(opts); code != exitOK {
		t.Errorf("second start --oknodo: got %d, want %d", code, exitOK)
	}
}

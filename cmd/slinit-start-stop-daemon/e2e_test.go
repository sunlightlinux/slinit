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

// TestE2EStartBackgroundStop exercises the real spawn → pidfile → stop
// cycle against /bin/sleep, the smallest widely-available "hangs until
// killed" binary. Uses spawn() and cmdStop() directly rather than
// shelling out to the compiled binary so tests stay hermetic.
func TestE2EStartBackgroundStop(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary: %v", err)
	}

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
	pid, err := spawn(binary, argv, opts)
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

	// Give the exec syscall a moment to complete so /proc/PID/exe points
	// at sleep and not our test binary.
	time.Sleep(100 * time.Millisecond)

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
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
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
	time.Sleep(100 * time.Millisecond)

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

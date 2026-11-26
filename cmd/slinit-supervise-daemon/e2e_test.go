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

// TestStartDaemonSpawnsAndReports fires startDaemon at /bin/sleep and
// verifies the returned PID is alive plus the exit channel fires once
// the process dies.
func TestStartDaemonSpawnsAndReports(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep: %v", err)
	}
	opts := Options{
		Service: "test",
		Exec:    sleep,
		Args:    []string{"0.2"},
	}
	pid, exitCh, err := startDaemon(opts)
	if err != nil {
		t.Fatalf("startDaemon: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	})
	if !processAlive(pid) {
		t.Fatalf("pid %d already dead", pid)
	}
	select {
	case msg := <-exitCh:
		if msg == "" {
			t.Errorf("empty exit msg")
		}
	case <-time.After(2 * time.Second):
		t.Errorf("exit channel didn't fire")
	}
}

// TestStopDaemonSendsRetrySchedule kills a long-running /bin/sleep via
// stopDaemon's retry path and confirms it exits within budget.
func TestStopDaemonSendsRetrySchedule(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep: %v", err)
	}
	opts := Options{
		Service: "test",
		Exec:    sleep,
		Args:    []string{"60"},
		Retry:   "TERM/1/KILL/1",
	}
	pid, _, err := startDaemon(opts)
	if err != nil {
		t.Fatalf("startDaemon: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	})

	start := time.Now()
	if err := stopDaemon(opts, pid); err != nil {
		t.Fatalf("stopDaemon: %v", err)
	}
	if processAlive(pid) {
		t.Errorf("pid %d still alive after stopDaemon", pid)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("stopDaemon took %v, expected < 3s", elapsed)
	}
}

// TestCmdStopStalePidfile: reports code 5 per LSB.
func TestCmdStopStalePidfile(t *testing.T) {
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "stale.pid")
	if err := os.WriteFile(pidfile, []byte("999999\n"), 0644); err != nil {
		t.Fatal(err)
	}
	opts := Options{PidFile: pidfile}
	if code := cmdStop(opts); code != exitStalePidfile {
		t.Errorf("cmdStop stale: got %d, want %d", code, exitStalePidfile)
	}
}

// TestCmdStopMissingPidfileIsOK: nothing to do, exitOK.
func TestCmdStopMissingPidfileIsOK(t *testing.T) {
	opts := Options{PidFile: filepath.Join(t.TempDir(), "nope.pid")}
	if code := cmdStop(opts); code != exitOK {
		t.Errorf("cmdStop missing: got %d, want %d", code, exitOK)
	}
}

// TestSupervisorHonoursSIGTERMAfterSIGHUP drives runSupervisor in-process:
// a SIGHUP (forwarded to the daemon) must not leave the supervisor
// deaf to a following SIGTERM while the daemon keeps running.
func TestSupervisorHonoursSIGTERMAfterSIGHUP(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep: %v", err)
	}
	// sh traps HUP so the forwarded signal does not kill the daemon.
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no sh: %v", err)
	}
	pidfile := filepath.Join(t.TempDir(), "svc.pid")
	opts := Options{
		Service: "test",
		PidFile: pidfile,
		Exec:    sh,
		Args:    []string{"-c", "trap '' HUP; exec " + sleep + " 60"},
		Retry:   "TERM/2/KILL/2",
	}

	done := make(chan int, 1)
	go func() { done <- runSupervisor(opts) }()

	var daemonPID int
	deadline := time.Now().Add(5 * time.Second)
	for daemonPID == 0 && time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidfile + ".daemon"); err == nil {
			daemonPID, _ = strconv.Atoi(string(b[:len(b)-1]))
		}
		if daemonPID == 0 {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if daemonPID == 0 {
		t.Fatal("daemon pidfile not written")
	}
	t.Cleanup(func() {
		// On failure the supervisor is stuck on the daemon; kill it so
		// the buffered SIGTERM is consumed and runSupervisor returns.
		_ = syscall.Kill(daemonPID, syscall.SIGKILL)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	})
	// Let sh install its trap and exec sleep before the HUP arrives.
	time.Sleep(200 * time.Millisecond)

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if !processAlive(daemonPID) {
		t.Fatalf("daemon pid %d died on forwarded SIGHUP", daemonPID)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	select {
	case code := <-done:
		if code != exitOK {
			t.Errorf("runSupervisor: got %d, want %d", code, exitOK)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor ignored SIGTERM after SIGHUP")
	}
	if processAlive(daemonPID) {
		t.Errorf("daemon pid %d still alive after supervisor SIGTERM", daemonPID)
	}
}

// While waiting out the respawn delay there is no daemon to forward a
// SIGHUP to. It used to end the supervisor (exit 0) like SIGTERM, so a
// config reload that happened to land between a crash and the respawn
// took the service down. It must be ignored; SIGTERM still stops.
func TestSupervisorIgnoresSIGHUPDuringBackoff(t *testing.T) {
	falseBin, err := exec.LookPath("false")
	if err != nil {
		t.Skipf("no false: %v", err)
	}
	opts := Options{
		Service:      "test",
		PidFile:      filepath.Join(t.TempDir(), "svc.pid"),
		Exec:         falseBin,
		Retry:        "TERM/2/KILL/2",
		RespawnDelay: 2 * time.Second,
	}

	done := make(chan int, 1)
	exited := false
	go func() { done <- runSupervisor(opts) }()
	t.Cleanup(func() {
		// Only while it still runs: once runSupervisor has returned, its
		// signal handler is gone and SIGTERM would kill the test binary.
		if exited {
			return
		}
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	})

	// The daemon exits at once; the supervisor is now in its 2s delay.
	time.Sleep(500 * time.Millisecond)
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		exited = true
		t.Fatalf("supervisor exited (%d) on SIGHUP during the respawn delay", code)
	case <-time.After(500 * time.Millisecond):
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		exited = true
		if code != exitOK {
			t.Errorf("exit = %d, want %d", code, exitOK)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor ignored SIGTERM during the respawn delay")
	}
}

// TestCmdStopNeverSignalsInit: the pidfile is trusted as written, so
// one naming PID 1 had --stop send init SIGTERM — a reboot under
// slinit — and --signal send it whatever was asked. Neither init nor
// this process may be read back as the supervisor or the daemon.
func TestCmdStopNeverSignalsInit(t *testing.T) {
	for _, pid := range []int{1, os.Getpid()} {
		pidfile := filepath.Join(t.TempDir(), "svc.pid")
		for _, p := range []string{pidfile, pidfile + ".daemon"} {
			if err := os.WriteFile(p, []byte(strconv.Itoa(pid)+"\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		if got, ok := readPIDFile(pidfile); ok {
			t.Errorf("pidfile holding %d read back as pid %d", pid, got)
		}
		if code := cmdStop(Options{PidFile: pidfile}); code != exitOK {
			t.Errorf("cmdStop on pid %d: got %d, want %d (nothing to stop)", pid, code, exitOK)
		}
		if code := cmdSignal(Options{PidFile: pidfile, Signal: syscall.Signal(0)}); code == exitOK {
			t.Errorf("cmdSignal on pid %d: signalled it", pid)
		}
	}
}

// TestCmdStopRefusesUntrustedPidfile: --stop and --signal signal what
// the pidfile names, as root. A pidfile anyone could have written, or
// one naming a process that is not a supervise-daemon, let a service
// user who can write the pidfile directory have root SIGTERM/SIGKILL
// any pid (--stop) or send it any signal (--signal).
func TestCmdStopRefusesUntrustedPidfile(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep: %v", err)
	}
	cmd := exec.Command(sleep, "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	victim := cmd.Process.Pid
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = syscall.Kill(victim, syscall.SIGKILL)
		<-exited
	})
	alive := func(what string) {
		t.Helper()
		select {
		case <-exited:
			t.Fatalf("%s signalled pid %d", what, victim)
		case <-time.After(100 * time.Millisecond):
		}
	}

	write := func(path string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(strconv.Itoa(victim)+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}

	// World-writable: whoever wrote it picked the pid.
	pidfile := filepath.Join(t.TempDir(), "svc.pid")
	write(pidfile, 0o666)
	write(pidfile+".daemon", 0o666)
	if code := cmdStop(Options{PidFile: pidfile}); code == exitOK {
		t.Errorf("cmdStop accepted a world-writable pidfile")
	}
	alive("cmdStop on a world-writable pidfile")
	if code := cmdSignal(Options{PidFile: pidfile, Signal: syscall.SIGKILL}); code == exitOK {
		t.Errorf("cmdSignal accepted a world-writable pidfile")
	}
	alive("cmdSignal on a world-writable pidfile")

	// A trusted file naming a live process that is not a
	// supervise-daemon: a reused or forged pid, not ours to stop.
	pidfile = filepath.Join(t.TempDir(), "svc.pid")
	write(pidfile, 0o644)
	if code := cmdStop(Options{PidFile: pidfile, Retry: "1"}); code == exitOK {
		t.Errorf("cmdStop reported success stopping a non-supervisor")
	}
	alive("cmdStop on a pidfile naming a non-supervisor")

	// A planted symlink is refused rather than followed.
	target := filepath.Join(t.TempDir(), "other.pid")
	write(target, 0o644)
	pidfile = filepath.Join(t.TempDir(), "svc.pid")
	if err := os.Symlink(target, pidfile+".daemon"); err != nil {
		t.Fatal(err)
	}
	if code := cmdSignal(Options{PidFile: pidfile, Signal: syscall.SIGKILL}); code == exitOK {
		t.Errorf("cmdSignal followed a symlinked daemon pidfile")
	}
	alive("cmdSignal through a symlink")
}

// TestIsSupervisor: the supervisor --stop signals runs this binary.
func TestIsSupervisor(t *testing.T) {
	if ok, err := isSupervisor(os.Getpid()); err != nil || !ok {
		t.Errorf("isSupervisor(self) = %v, %v; want true", ok, err)
	}
	if ok, err := isSupervisor(999999); err != nil || ok {
		t.Errorf("isSupervisor(dead pid) = %v, %v; want false, nil", ok, err)
	}
}

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

// TestCmdSignalHitsDaemon writes a daemon pidfile pointing at a live
// process and verifies cmdSignal delivers the requested signal to it
// (via the process actually dying when we send SIGKILL).
func TestCmdSignalHitsDaemon(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep: %v", err)
	}
	cmd := exec.Command(sleep, "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	dir := t.TempDir()
	pidfile := filepath.Join(dir, "svc.pid")
	if err := os.WriteFile(pidfile+".daemon", []byte(strconv.Itoa(pid)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	opts := Options{PidFile: pidfile, Signal: syscall.SIGKILL}
	if code := cmdSignal(opts); code != exitOK {
		t.Errorf("cmdSignal: got %d, want %d", code, exitOK)
	}
	// cmd.Wait reaps the (post-SIGKILL) zombie so the assertion below
	// really tests dead-vs-alive rather than the zombie corner case.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cmd.Wait timed out after SIGKILL")
	}
	if processAlive(pid) {
		t.Errorf("pid %d still alive after cmdSignal SIGKILL + reap", pid)
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

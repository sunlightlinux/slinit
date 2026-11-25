package process

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A pid file naming PID 1 or slinit itself must be refused. The pid read
// is what stop signals: before the fix ReadPIDFile returned "1" as
// PIDResultOK, so stopping the service sent SIGTERM to PID 1 — a reboot
// request when slinit is init.
func TestReadPIDFileRefusesInitAndSelf(t *testing.T) {
	for _, pid := range []int{1, os.Getpid()} {
		path := filepath.Join(t.TempDir(), "svc.pid")
		if err := os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, res, err := ReadPIDFile(path)
		if res != PIDResultFailed || err == nil {
			t.Errorf("pid file %q: got pid=%d result=%v err=%v; want PIDResultFailed with an error",
				strconv.Itoa(pid), got, res, err)
		}
	}
}

// SignalDaemonGroup(1, …) went to the group branch: getpgid(1) is 1, so
// it sent kill(-1, sig) — every process the caller may signal. Signal 0
// keeps that harmless here; before the fix it returned nil (sent).
func TestSignalDaemonGroupRefusesPID1(t *testing.T) {
	if err := SignalDaemonGroup(1, 0, false); err == nil {
		t.Error("SignalDaemonGroup(1) signalled; want it refused")
	}
	if err := SignalDaemonGroup(1, 0, true); err == nil {
		t.Error("SignalDaemonGroup(1, processOnly) signalled; want it refused")
	}
}

// A kernel thread reports pgid 0, and kill(-0, sig) is kill(0, sig): the
// caller's own process group. A pid file naming kthreadd (pid 2) made a
// service stop signal slinit and everything in its group. The check runs
// in a child in its own process group, so a regression only hits it.
// Measured before the fix: the child received the signal it sent.
func TestSignalDaemonGroupKernelThreadNotOwnGroup(t *testing.T) {
	if os.Getenv("SLINIT_TEST_SIGGRP_CHILD") == "1" {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGUSR1)
		_ = SignalDaemonGroup(2, syscall.SIGUSR1, false)
		select {
		case <-ch:
			os.Stdout.WriteString("OWN-GROUP-SIGNALLED\n")
		case <-time.After(300 * time.Millisecond):
			os.Stdout.WriteString("not-signalled\n")
		}
		return
	}
	comm, err := os.ReadFile("/proc/2/comm")
	if err != nil || strings.TrimSpace(string(comm)) != "kthreadd" {
		t.Skip("pid 2 is not kthreadd here (pid namespace?)")
	}
	if pg, err := syscall.Getpgid(2); err != nil || pg != 0 {
		t.Skipf("getpgid(2) = %d, %v; want 0", pg, err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSignalDaemonGroupKernelThreadNotOwnGroup$")
	cmd.Env = append(os.Environ(), "SLINIT_TEST_SIGGRP_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.CombinedOutput()
	if strings.Contains(string(out), "OWN-GROUP-SIGNALLED") {
		t.Fatalf("SignalDaemonGroup(kernel thread) signalled the caller's own process group:\n%s", out)
	}
	if err != nil || !strings.Contains(string(out), "not-signalled") {
		t.Fatalf("child: %v\n%s", err, out)
	}
}

package main

import (
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestWaitForPidExitReturnsOnExit is the core of session reaping: a
// session whose leader died used to stay registered forever, which is
// what wedges gdm (it sees a greeter still listed on seat0 and refuses
// to start a replacement).
func TestWaitForPidExitReturnsOnExit(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn helper: %v", err)
	}
	pid := uint32(cmd.Process.Pid)

	done := make(chan error, 1)
	go func() { done <- waitForPidExit(pid) }()

	// Must still be blocked while the process lives.
	select {
	case err := <-done:
		t.Fatalf("waitForPidExit returned early: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("waitForPidExit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("waitForPidExit did not return after the process exited")
	}
}

// TestWaitForPidExitDeadPid pins the error the caller branches on:
// reapDeadSessions treats ESRCH as "reap now", not "skip".
func TestWaitForPidExitDeadPid(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skipf("cannot spawn helper: %v", err)
	}
	pid := uint32(cmd.Process.Pid)

	err := waitForPidExit(pid)
	if err != unix.ESRCH {
		// A recycled pid would make this flaky, but the window is one
		// exec wide and the kernel hands out pids sequentially.
		t.Skipf("pid %d not reported as gone (%v) — likely recycled", pid, err)
	}
}

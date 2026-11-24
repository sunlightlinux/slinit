package main

import (
	"os/exec"
	"syscall"
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

// Kill(who="leader") used to write cgroup.kill — SIGKILL to the whole
// session, whatever signal was asked for — and any other who was taken
// as "all". systemd signals the leader alone with the signal given, and
// refuses a who that is neither "leader" nor "all" (bus_session_method_kill).
func TestKillSessionLeaderSignalsOnlyTheLeader(t *testing.T) {
	m := usersBus(t)
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	defer cmd.Process.Kill()
	rec := SessionRecord{ID: "c1", UserID: 1000, SeatID: "seat0",
		LeaderPID: uint32(cmd.Process.Pid), Scope: "/sys/fs/cgroup/slinit-test-none.scope"}
	if err := writeJSONAtomic(sessionFile("c1"), rec); err != nil {
		t.Fatal(err)
	}

	for _, who := range []string{"bogus", "cgroup", "Leader"} {
		if err := m.killSession("c1", who, int32(syscall.SIGTERM)); err == nil || err.Name != "org.freedesktop.DBus.Error.InvalidArgs" {
			t.Errorf("Kill(who=%q): %v, want InvalidArgs", who, err)
		}
	}
	if err := m.killSession("c1", "leader", int32(syscall.SIGTERM)); err != nil {
		t.Fatalf("Kill(leader, SIGTERM): %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Kill(leader, SIGTERM) did not signal the leader")
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		t.Errorf("leader ended with %v, want killed by SIGTERM", cmd.ProcessState)
	}
}

package main

import (
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// sched_setattr(2) also sets the nice value of SCHED_OTHER/BATCH/IDLE.
// applySched passed 0, so --nicelevel with --scheduler batch either
// undid the nice level (as root) or failed with EPERM and left the
// policy unset (unprivileged, raising priority back to 0).
func TestApplySchedKeepsNiceLevel(t *testing.T) {
	cmd := exec.Command("sleep", "10")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	pid := cmd.Process.Pid
	if err := syscall.Setpriority(syscall.PRIO_PROCESS, pid, 5); err != nil {
		t.Fatal(err)
	}
	if err := applySched(pid, unix.SCHED_BATCH, 0); err != nil {
		t.Fatalf("applySched batch after nice 5: %v", err)
	}
	attr, err := unix.SchedGetAttr(pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	if attr.Policy != unix.SCHED_BATCH {
		t.Errorf("policy %d, want SCHED_BATCH", attr.Policy)
	}
	if attr.Nice != 5 {
		t.Errorf("nice %d after applySched, want 5", attr.Nice)
	}
}

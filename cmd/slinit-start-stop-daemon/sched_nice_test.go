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
	// Relative to the nice we run at: unprivileged, only raising works,
	// and a test run under `nice -n 10` would get EPERM for an absolute 5.
	cur, err := unix.Getpriority(unix.PRIO_PROCESS, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := int32(20-cur) + 4
	if want > 19 {
		want = 19
	}
	if err := syscall.Setpriority(syscall.PRIO_PROCESS, pid, int(want)); err != nil {
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
	if attr.Nice != want {
		t.Errorf("nice %d after applySched, want %d", attr.Nice, want)
	}
}

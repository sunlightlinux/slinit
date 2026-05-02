package process

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// TestApplySchedFifoLive forks /bin/sleep, programs SCHED_FIFO/50, then
// reads /proc/PID/sched to confirm the policy stuck. Requires
// CAP_SYS_NICE — skipped (not failed) when running unprivileged.
func TestApplySchedFifoLive(t *testing.T) {
	if !canSetRTPolicy(t) {
		t.Skip("no CAP_SYS_NICE / RLIMIT_RTPRIO; skipping live sched test")
	}

	cmd := exec.Command("/bin/sleep", "10")
	if err := cmd.Start(); err != nil {
		t.Fatalf("sleep start: %v", err)
	}
	defer cmd.Process.Kill()

	pid := cmd.Process.Pid
	params := ExecParams{
		SchedPolicy:      unix.SCHED_FIFO,
		SchedPriority:    50,
		SchedResetOnFork: true,
	}
	if err := applySched(pid, params); err != nil {
		t.Fatalf("applySched: %v", err)
	}

	// Verify via SchedGetAttr round-trip.
	got, err := unix.SchedGetAttr(pid, 0)
	if err != nil {
		t.Fatalf("SchedGetAttr: %v", err)
	}
	if got.Policy != unix.SCHED_FIFO {
		t.Errorf("Policy = %d, want SCHED_FIFO (%d)", got.Policy, unix.SCHED_FIFO)
	}
	if got.Priority != 50 {
		t.Errorf("Priority = %d, want 50", got.Priority)
	}
	if got.Flags&unix.SCHED_FLAG_RESET_ON_FORK == 0 {
		t.Error("SCHED_FLAG_RESET_ON_FORK not set")
	}
}

func TestApplySchedFifoOutOfRange(t *testing.T) {
	params := ExecParams{
		SchedPolicy:   unix.SCHED_FIFO,
		SchedPriority: 0, // out of 1..99
	}
	// pid=os.Getpid() so the syscall path is exercised, not just validation
	if err := applySched(os.Getpid(), params); err == nil {
		t.Fatal("expected error for priority=0 with FIFO")
	}
}

func TestApplySchedDeadlineRejectsMissingFields(t *testing.T) {
	params := ExecParams{
		SchedPolicy:  unix.SCHED_DEADLINE,
		SchedRuntime: 1000,
		// missing deadline + period
	}
	if err := applySched(os.Getpid(), params); err == nil {
		t.Fatal("expected error for incomplete DEADLINE params")
	}
}

func TestApplySchedDeadlineInvariant(t *testing.T) {
	// runtime > deadline must be caught even before sched_setattr.
	params := ExecParams{
		SchedPolicy:   unix.SCHED_DEADLINE,
		SchedRuntime:  10_000_000,
		SchedDeadline: 5_000_000,
		SchedPeriod:   20_000_000,
	}
	if err := applySched(os.Getpid(), params); err == nil {
		t.Fatal("expected error for runtime > deadline")
	}
}

func TestApplySchedNormalCanBeAppliedBySelf(t *testing.T) {
	// SCHED_NORMAL is the default; setting it to itself should always
	// succeed without privilege. Acts as a smoke test that the syscall
	// path works at all.
	params := ExecParams{SchedPolicy: unix.SCHED_NORMAL}
	if err := applySched(os.Getpid(), params); err != nil {
		t.Fatalf("applySched(SCHED_NORMAL): %v", err)
	}
}

// canSetRTPolicy probes whether the current process can set SCHED_FIFO.
// Used to gate the live RT test on systems without CAP_SYS_NICE.
func canSetRTPolicy(t *testing.T) bool {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "1")
	if err := cmd.Start(); err != nil {
		return false
	}
	defer func() {
		cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	// Give Linux a moment to let the child enter sleep.
	time.Sleep(20 * time.Millisecond)

	attr := unix.SchedAttr{
		Size:     uint32(unsafe.Sizeof(unix.SchedAttr{})),
		Policy:   unix.SCHED_FIFO,
		Priority: 1,
	}
	err := unix.SchedSetAttr(cmd.Process.Pid, &attr, 0)
	if err == nil {
		return true
	}
	// EPERM is the canonical "no privilege" answer.
	if strings.Contains(err.Error(), "permission denied") ||
		strings.Contains(err.Error(), "operation not permitted") {
		return false
	}
	// Anything else (e.g. ENOSYS on a stripped kernel) — treat as "no".
	return false
}


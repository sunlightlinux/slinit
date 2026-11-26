package process

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// startSleeper starts a child that the test may change the attributes
// of, and reaps it when the test ends.
func startSleeper(t *testing.T) int {
	t.Helper()
	c := exec.Command("sleep", "60")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
	return c.Process.Pid
}

func niceOf(t *testing.T, pid int) int {
	t.Helper()
	prio, err := unix.Getpriority(unix.PRIO_PROCESS, pid)
	if err != nil {
		t.Fatal(err)
	}
	return 20 - prio // the raw syscall returns 20 - nice
}

func readProcInt(t *testing.T, pid int, file string) int {
	t.Helper()
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/" + file)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Every attribute an unprivileged parent may set on its own child lands
// on the child, with no error reported.
func TestApplyPostForkAttrsApplies(t *testing.T) {
	pid := startSleeper(t)

	nice := niceOf(t, pid) + 3
	if nice > 19 {
		nice = 19
	}
	oom := 1000 // raising is always allowed
	var cur unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &cur); err != nil {
		t.Fatal(err)
	}
	if cur.Max < 128 {
		t.Skipf("RLIMIT_NOFILE hard limit %d too low", cur.Max)
	}
	var mine unix.CPUSet
	if err := unix.SchedGetaffinity(0, &mine); err != nil {
		t.Fatal(err)
	}
	cpu := -1
	for i := 0; i < 1024; i++ {
		if mine.IsSet(i) {
			cpu = i
			break
		}
	}

	errs := applyPostForkAttrs(pid, ExecParams{
		Nice:        &nice,
		OOMScoreAdj: &oom,
		Rlimits:     []Rlimit{{Resource: unix.RLIMIT_NOFILE, Soft: 64, Hard: 128}},
		IOPrioClass: 2, IOPrioLevel: 7, // best-effort, lowest
		NoNewPrivs: true, RunnerPath: "/usr/libexec/slinit-runner", // the runner does it
		CPUAffinity: []uint{uint(cpu)},
	}, false)
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}

	if got := niceOf(t, pid); got != nice {
		t.Errorf("nice = %d, want %d", got, nice)
	}
	if got := readProcInt(t, pid, "oom_score_adj"); got != oom {
		t.Errorf("oom_score_adj = %d, want %d", got, oom)
	}
	var lim unix.Rlimit
	if err := unix.Prlimit(pid, unix.RLIMIT_NOFILE, nil, &lim); err != nil {
		t.Fatal(err)
	}
	if lim.Cur != 64 || lim.Max != 128 {
		t.Errorf("RLIMIT_NOFILE = %d/%d, want 64/128", lim.Cur, lim.Max)
	}
	prio, _, e := syscall.Syscall(unix.SYS_IOPRIO_GET, ioprioWhoProcess, uintptr(pid), 0)
	if e != 0 {
		t.Fatal(e)
	}
	if prio != 2<<13|7 {
		t.Errorf("ioprio = %#x, want %#x", prio, 2<<13|7)
	}
	var aff unix.CPUSet
	if err := unix.SchedGetaffinity(pid, &aff); err != nil {
		t.Fatal(err)
	}
	if aff.Count() != 1 || !aff.IsSet(cpu) {
		t.Errorf("affinity has %d CPUs, want only CPU %d", aff.Count(), cpu)
	}
}

// One failing attribute does not stop the rest: every failure comes back,
// each tagged with the setting it belongs to, in the order applied.
func TestApplyPostForkAttrsReportsEveryFailure(t *testing.T) {
	pid := startSleeper(t)
	oom := 5000 // outside -1000..1000
	errs := applyPostForkAttrs(pid, ExecParams{
		OOMScoreAdj:    &oom,
		Rlimits:        []Rlimit{{Resource: unix.RLIMIT_NOFILE, Soft: 10, Hard: 5}},
		IOPrioClass:    9,
		CgroupPath:     "/not-cgroupfs/x",
		CgroupSettings: []CgroupSetting{{File: "memory.max", Value: "1"}},
		NoNewPrivs:     true, // no runner: refused, not silently dropped
		CPUAffinity:    []uint{1023},
		SchedPolicy:    99,
	}, false)
	want := []string{"oom_score_adj(5000): ", "rlimits: resource 7: ", "ioprio(9,0): ",
		"cgroup-settings: ", "cgroup(/not-cgroupfs/x): ", "no_new_privs: ", "sched-policy: "}
	// CPU 1023 is out of range on any machine with fewer CPUs; on one
	// with 1024 the affinity call succeeds, which is not this test's point.
	if len(errs) == len(want)+1 {
		want = append(want[:6:6], "cpu-affinity: ", "sched-policy: ")
	}
	if len(errs) != len(want) {
		t.Fatalf("got %d errors, want %d: %v", len(errs), len(want), errs)
	}
	for i, e := range errs {
		if !strings.HasPrefix(e.Error(), want[i]) {
			t.Errorf("error %d = %q, want prefix %q", i, e, want[i])
		}
	}
}

// A zero-valued ExecParams touches nothing and reports nothing.
func TestApplyPostForkAttrsNothingSet(t *testing.T) {
	pid := startSleeper(t)
	before := niceOf(t, pid)
	if errs := applyPostForkAttrs(pid, ExecParams{}, false); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if niceOf(t, pid) != before {
		t.Error("nice changed")
	}
}

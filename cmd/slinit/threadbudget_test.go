package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The reserve must fit the task limits, or making it is the
// pthread_create failure it exists to prevent: unconditionally
// GOMAXPROCS+16, it killed slinit at start in `docker run --pids-limit
// 25` on a 12-CPU host (tests/container case 18). When it is cut,
// GOMAXPROCS comes down with it, so every P can still run on a reserved
// thread.
func TestCapThreadReserve(t *testing.T) {
	const procs = 12
	const want = procs + threadReserveExtra // 28
	for _, c := range []struct {
		name             string
		want             int
		known            bool
		pidsMax, pidsCur int64
		pidsOK           bool
		nproc            uint64
		nprocOK          bool
		used             uint64 // tasks the uid already has
		usedOK           bool
		reserve, procs   int
	}{
		{"PID 1 of a machine, no limits readable", want, true, 0, 0, false, 0, false, 0, false, want, procs},
		{"nothing readable (hidden cgroup, or a root manager with no pids controller)", want, false, 0, 0, false, 0, false, 0, false, threadReserveUnknown, procs},
		{"roomy pids cgroup", want, true, 4096, 10, true, 0, false, 0, false, want, procs},
		{"docker --pids-limit 25", want, true, 25, 6, true, 0, false, 0, false, 4, 2},
		{"pids cgroup already full", want, true, 25, 25, true, 0, false, 0, false, 0, 1},
		{"pids cgroup over its max", want, true, 25, 30, true, 0, false, 0, false, 0, 1},
		{"tight RLIMIT_NPROC, tasks not counted", want, true, 0, 0, false, 100, true, 0, false, 12, 6},
		{"tight RLIMIT_NPROC, 20 tasks used", want, true, 0, 0, false, 100, true, 20, true, 12, 6},
		{"RLIMIT_NPROC, a count that came out low", want, true, 0, 0, false, 160, true, 0, true, 20, 10},
		{"roomy RLIMIT_NPROC (overnight harness)", want, true, 0, 0, false, 1260, true, 460, true, want, procs},
		{"RLIMIT_NPROC nearly used up by the uid", want, true, 0, 0, false, 4096, true, 4000, true, 24, 12},
		{"RLIMIT_NPROC already exceeded by the uid", want, true, 0, 0, false, 100, true, 150, true, 0, 1},
		{"pids.max reads max (user session, no limit)", want, true, 0, 0, false, 0, false, 0, false, want, procs},
		{"64-CPU host, the tighter of the two limits", 80, true, 1000, 0, true, 400, true, 0, false, 50, procs},
	} {
		r, p := capThreadReserve(c.want, procs, c.known, c.pidsMax, c.pidsCur, c.pidsOK, c.nproc, c.nprocOK, c.used, c.usedOK)
		if r != c.reserve || p != c.procs {
			t.Errorf("%s: reserve %d GOMAXPROCS %d, want %d and %d", c.name, r, p, c.reserve, c.procs)
		}
	}
}

func TestHasController(t *testing.T) {
	for _, c := range []struct {
		list string
		want bool
	}{
		{"pids", true}, {"cpu,cpuacct", false}, {"net_cls,pids", true}, {"", false}, {"pidsx", false},
	} {
		if got := hasController(c.list, "pids"); got != c.want {
			t.Errorf("hasController(%q) = %v", c.list, got)
		}
	}
}

// The pids limit is found on a cgroup v2 host, and on v1 and hybrid hosts
// (where Docker enforces --pids-limit through the v1 pids controller and
// the "0::" line names a controller-less unified tree) too; the tightest
// level wins; a path outside the cgroup namespace reads nothing.
func TestPidsLimitFrom(t *testing.T) {
	mk := func(t *testing.T, root, rel, max, cur string) {
		t.Helper()
		d := filepath.Join(root, rel)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(d, "pids.max"), []byte(max+"\n"), 0o644)
		os.WriteFile(filepath.Join(d, "pids.current"), []byte(cur+"\n"), 0o644)
	}
	t.Run("v2 container root", func(t *testing.T) {
		r := t.TempDir()
		mk(t, r, "", "25", "6")
		max, cur, ok, seen := pidsLimitFrom("0::/\n", r)
		if !ok || !seen || max != 25 || cur != 6 {
			t.Fatalf("got %d %d %v %v", max, cur, ok, seen)
		}
	})
	t.Run("v2 tightest ancestor", func(t *testing.T) {
		r := t.TempDir()
		mk(t, r, "", "max", "100")
		mk(t, r, "user.slice", "1000", "990")
		mk(t, r, "user.slice/session-1.scope", "max", "5")
		max, cur, ok, _ := pidsLimitFrom("0::/user.slice/session-1.scope\n", r)
		if !ok || max != 1000 || cur != 990 {
			t.Fatalf("got %d %d %v", max, cur, ok)
		}
	})
	t.Run("v2 no limit anywhere is seen but not ok", func(t *testing.T) {
		r := t.TempDir()
		mk(t, r, "", "max", "100")
		_, _, ok, seen := pidsLimitFrom("0::/\n", r)
		if ok || !seen {
			t.Fatalf("ok %v seen %v, want false true", ok, seen)
		}
	})
	t.Run("hybrid: v1 pids line, empty unified tree", func(t *testing.T) {
		r := t.TempDir()
		os.MkdirAll(filepath.Join(r, "unified"), 0o755)
		mk(t, r, "pids/docker/abc", "25", "4")
		pc := "12:pids:/docker/abc\n5:cpu,cpuacct:/docker/abc\n0::/docker/abc\n"
		max, cur, ok, _ := pidsLimitFrom(pc, r)
		if !ok || max != 25 || cur != 4 {
			t.Fatalf("got %d %d %v", max, cur, ok)
		}
	})
	t.Run("outside the cgroup namespace", func(t *testing.T) {
		r := t.TempDir()
		mk(t, r, "x", "25", "4")
		_, _, ok, seen := pidsLimitFrom("0::/../x\n", r)
		if ok || seen {
			t.Fatalf("read %v %v for a path outside the namespace", ok, seen)
		}
	})
	t.Run("nothing mounted", func(t *testing.T) {
		_, _, ok, seen := pidsLimitFrom("0::/\n", filepath.Join(t.TempDir(), "none"))
		if ok || seen {
			t.Fatalf("ok %v seen %v with no cgroup mount", ok, seen)
		}
	})
}

func TestStatusUIDThreads(t *testing.T) {
	st := "Name:\tslinit\nUmask:\t0022\nState:\tS (sleeping)\nUid:\t1000\t1000\t1000\t1000\nGid:\t1000\t1000\t1000\t1000\nThreads:\t7\n"
	if u, n, ok := statusUIDThreads(st); !ok || u != 1000 || n != 7 {
		t.Fatalf("got uid %d threads %d ok %v", u, n, ok)
	}
	if _, _, ok := statusUIDThreads("Name:\tx\nUid:\t0\t0\t0\t0\n"); ok {
		t.Fatal("ok without a Threads line")
	}
}

// uidTasks finds at least this process's own threads.
func TestUIDTasksCountsThisProcess(t *testing.T) {
	n, ok := uidTasks(os.Getuid())
	if !ok || n < 1 {
		t.Fatalf("uidTasks = %d, %v", n, ok)
	}
}

// One thread short of the want costs one P, not half of them (64 CPUs:
// the old cut gave 39); a deep cut still halves the reserve at most.
func TestReserveCutLowersGOMAXPROCSGradually(t *testing.T) {
	for _, c := range []struct {
		free         int64
		reserve, gmp int
	}{
		{316, 79, 63}, // (320-4)/4 = 79 of 80 wanted
		{200, 50, 34},
		{40, 10, 5},
		{4, 1, 1},
	} {
		r, p := capThreadReserve(80, 64, true, c.free, 0, true, 0, false, 0, false)
		if r != c.reserve || p != c.gmp {
			t.Errorf("free %d: reserve %d GOMAXPROCS %d, want %d and %d", c.free, r, p, c.reserve, c.gmp)
		}
	}
}

// --sys naming a container runtime marks a container even without -o or
// the runtime's own marks (the holistic review's F4).
func TestLooksLikeContainerHonoursSys(t *testing.T) {
	for _, s := range []string{"docker", "lxc", "podman", "Docker", "systemd-nspawn", "openvz", "vserver", "rkt"} {
		if !looksLikeContainer(s) {
			t.Errorf("--sys %s not taken for a container", s)
		}
	}
}

func TestLooksLikeContainerNotForVMs(t *testing.T) {
	t.Setenv("container", "")
	for _, s := range []string{"wsl", "xen0", "xenu", "none"} {
		if looksLikeContainer(s) && os.Getenv("container") == "" {
			if _, err := os.Stat("/.dockerenv"); err != nil {
				t.Errorf("--sys %s taken for a container", s)
			}
		}
	}
}

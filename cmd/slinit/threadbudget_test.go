package main

import "testing"

// The reserve must fit the task limits, or making it is the
// pthread_create failure it exists to prevent: unconditionally
// GOMAXPROCS+16, it killed slinit at start in `docker run --pids-limit
// 25` on a 12-CPU host (tests/container case 18).
func TestCapThreadReserve(t *testing.T) {
	for _, c := range []struct {
		name             string
		want             int
		pidsMax, pidsCur int64
		pidsOK           bool
		nproc            uint64
		nprocOK          bool
		got              int
	}{
		{"no limits known", 28, 0, 0, false, 0, false, 28},
		{"roomy pids cgroup", 28, 4096, 10, true, 0, false, 28},
		{"docker --pids-limit 25", 28, 25, 6, true, 0, false, 4},
		{"pids cgroup already full", 28, 25, 25, true, 0, false, 0},
		{"pids cgroup over its max", 28, 25, 30, true, 0, false, 0},
		{"tight RLIMIT_NPROC", 28, 0, 0, false, 100, true, 12},
		{"roomy RLIMIT_NPROC", 28, 0, 0, false, 1260, true, 28},
		{"tighter of the two", 80, 1000, 0, true, 400, true, 50},
	} {
		if got := capThreadReserve(c.want, c.pidsMax, c.pidsCur, c.pidsOK, c.nproc, c.nprocOK); got != c.got {
			t.Errorf("%s: reserve %d, want %d", c.name, got, c.got)
		}
	}
}

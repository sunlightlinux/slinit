package main

import (
	"os"
	"path"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// threadReserveSize caps the startup thread reserve so that making it
// cannot itself run slinit out of tasks.
//
// The reserve exists because the Go runtime aborts the whole process when
// pthread_create fails. Made unconditionally at GOMAXPROCS+16, it turned
// into that very failure in a container with a small pids.max: the reserve
// is sized by the host's CPUs (28 threads with 12 of them, 80 with 64),
// and `docker run --pids-limit 25` killed slinit before its first service
// ("runtime: failed to create new OS thread (have 30 already; errno=11)",
// exit 2) where v4.0.3, with about six threads, ran (tests/container case
// 18). So the reserve takes at most a quarter of the tasks the pids
// cgroup still allows, and an eighth of RLIMIT_NPROC when that applies
// (not to root), leaving the rest for the services. Unknown limits do
// not cap it: PID 1 of a machine runs this before /sys is mounted, and
// has no task limit to fit anyway.
func threadReserveSize(want int) int {
	pidsMax, pidsCur, pidsOK := pidsCgroupLimit()
	nproc, nprocOK := nprocLimit()
	return capThreadReserve(want, pidsMax, pidsCur, pidsOK, nproc, nprocOK)
}

// capThreadReserve is threadReserveSize's arithmetic. pidsMax/pidsCur are
// the tightest pids cgroup limit on the way to the root and its current
// count; nproc is RLIMIT_NPROC when it binds this process.
func capThreadReserve(want int, pidsMax, pidsCur int64, pidsOK bool, nproc uint64, nprocOK bool) int {
	if pidsOK {
		free := pidsMax - pidsCur
		if free < 0 {
			free = 0
		}
		if c := free / 4; c < int64(want) {
			want = int(c)
		}
	}
	if nprocOK {
		if c := nproc / 8; c < uint64(want) {
			want = int(c)
		}
	}
	if want < 0 {
		want = 0
	}
	return want
}

// pidsCgroupLimit returns the pids.max with the least room left, among
// this process's cgroup (v2) and its ancestors, with that cgroup's
// pids.current. ok is false when no limit is set or none can be read.
func pidsCgroupLimit() (max, cur int64, ok bool) {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return 0, 0, false
	}
	var rel string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "0::") {
			rel = strings.TrimPrefix(l, "0::")
			break
		}
	}
	if rel == "" {
		return 0, 0, false
	}
	best := int64(-1)
	for p := path.Clean("/" + rel); ; p = path.Dir(p) {
		dir := path.Join("/sys/fs/cgroup", p)
		m, err1 := os.ReadFile(path.Join(dir, "pids.max"))
		c, err2 := os.ReadFile(path.Join(dir, "pids.current"))
		if err1 == nil && err2 == nil {
			ms := strings.TrimSpace(string(m))
			mv, e1 := strconv.ParseInt(ms, 10, 64)
			cv, e2 := strconv.ParseInt(strings.TrimSpace(string(c)), 10, 64)
			if ms != "max" && e1 == nil && e2 == nil {
				if best < 0 || mv-cv < best {
					best, max, cur, ok = mv-cv, mv, cv, true
				}
			}
		}
		if p == "/" {
			break
		}
	}
	return max, cur, ok
}

// nprocLimit returns RLIMIT_NPROC when it binds this process: the kernel
// does not apply it to root.
func nprocLimit() (uint64, bool) {
	if os.Getuid() == 0 {
		return 0, false
	}
	var rl unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NPROC, &rl); err != nil {
		return 0, false
	}
	if rl.Cur == unix.RLIM_INFINITY {
		return 0, false
	}
	return rl.Cur, true
}

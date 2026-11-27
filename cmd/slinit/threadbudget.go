package main

import (
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// The startup thread reserve (threadreserve.go) must fit the task limits,
// or making it is the very pthread_create failure it exists to prevent.
// Sized by the host's CPUs alone (GOMAXPROCS+16: 28 threads with 12 CPUs,
// 80 with 64) it killed slinit before its first service under `docker run
// --pids-limit 25` ("runtime: failed to create new OS thread (have 30
// already; errno=11)", exit 2; tests/container case 18), where v4.0.3,
// with about six threads, ran.
//
// So the reserve takes at most a quarter of the tasks the pids cgroup
// still allows (cgroup v2, or the v1 pids controller of a v1 or hybrid
// host) and a quarter of what RLIMIT_NPROC leaves the uid when that
// binds (an eighth of the limit if the uid's tasks cannot be counted).
// When it is cut
// that far, GOMAXPROCS is lowered with it, to half the reserve: the
// runtime wants a thread per P running at once (GC workers, a burst of
// work), and Ps it cannot back with reserved threads would bring the
// pthread_create back at the moment the limit is reached.
//
// Limits that cannot be read do not cap the reserve for PID 1 of a
// machine, which runs this before /sys is mounted and has no task limit
// to fit. Anywhere else, when nothing at all can be read (a container
// whose cgroup is not visible), a small fixed reserve is made instead; a
// pids.max that reads "max" is a known absence of a limit, not that.

// threadReserveUnknown is the reserve when no task limit can be read and
// slinit is not PID 1 of a machine.
const threadReserveUnknown = 8

// sizeThreadReserve returns how many threads to reserve and the GOMAXPROCS
// to run with (unchanged unless the reserve had to be cut).
func sizeThreadReserve(procs int, machinePID1 bool) (reserve, newProcs int) {
	want := procs + threadReserveExtra
	pidsMax, pidsCur, pidsOK, pidsSeen := pidsCgroupLimit()
	nproc, nprocOK := nprocLimit()
	var used uint64
	usedOK := false
	if nprocOK {
		used, usedOK = uidTasks(os.Getuid())
	}
	known := machinePID1 || pidsSeen || nprocOK
	return capThreadReserve(want, procs, known, pidsMax, pidsCur, pidsOK, nproc, nprocOK, used, usedOK)
}

// capThreadReserve is sizeThreadReserve's arithmetic. pidsMax/pidsCur are
// the pids cgroup limit with the least room left and its current count;
// nproc is RLIMIT_NPROC when it binds this process, used the tasks the
// uid already has (when they could be counted). known says whether
// anything tells how many tasks there are: PID 1 of a machine, a pids
// cgroup that could be read (if only to say "max"), a finite RLIMIT_NPROC.
func capThreadReserve(want, procs int, known bool, pidsMax, pidsCur int64, pidsOK bool, nproc uint64, nprocOK bool, used uint64, usedOK bool) (reserve, newProcs int) {
	reserve = want
	if pidsOK {
		free := pidsMax - pidsCur
		if free < 0 {
			free = 0
		}
		if c := free / 4; c < int64(reserve) {
			reserve = int(c)
		}
	}
	if nprocOK {
		// RLIMIT_NPROC counts every task of the uid, not this process's:
		// a quarter of what the uid has left, as for the pids cgroup, or
		// an eighth of the limit when the tasks could not be counted.
		c := nproc / 8
		if usedOK {
			c = 0
			if used < nproc {
				c = (nproc - used) / 4
			}
		}
		if c < uint64(reserve) {
			reserve = int(c)
		}
	}
	if !known && reserve > threadReserveUnknown {
		reserve = threadReserveUnknown
	}
	if reserve < 0 {
		reserve = 0
	}
	newProcs = procs
	if reserve < want {
		if p := reserve / 2; p < newProcs {
			newProcs = p
		}
		if newProcs < 1 {
			newProcs = 1
		}
	}
	return reserve, newProcs
}

// applyThreadReserve sizes the reserve, lowers GOMAXPROCS if the reserve
// was cut, and makes the reserve.
func applyThreadReserve(machinePID1 bool) {
	reserve, procs := sizeThreadReserve(runtime.GOMAXPROCS(0), machinePID1)
	if procs != runtime.GOMAXPROCS(0) {
		runtime.GOMAXPROCS(procs)
	}
	reserveThreads(reserve)
}

// pidsCgroupLimit returns the pids.max with the least room left, among
// this process's cgroup and its ancestors (as far as the cgroup namespace
// shows them), with that cgroup's pids.current. cgroup v2 is read from
// the "0::" line, the v1 pids controller (v1 and hybrid hosts) from its
// own line. ok is false when no limit is set or none can be read; seen
// says whether any pids.max could be read at all, "max" included, which
// is a known absence of a limit rather than an unknown one.
func pidsCgroupLimit() (max, cur int64, ok, seen bool) {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return 0, 0, false, false
	}
	return pidsLimitFrom(string(b), "/sys/fs/cgroup")
}

// pidsLimitFrom is pidsCgroupLimit on a /proc/self/cgroup text and the
// cgroup mount root (a parameter for the tests).
func pidsLimitFrom(procCgroup, cgRoot string) (max, cur int64, ok, seen bool) {
	best := int64(-1)
	consider := func(root, rel string) {
		// A path outside the cgroup namespace ("/../...") names no
		// directory under this mount.
		if rel == "" || rel[0] != '/' || strings.HasPrefix(rel, "/..") {
			return
		}
		for p := path.Clean(rel); ; p = path.Dir(p) {
			dir := path.Join(root, p)
			m, err1 := os.ReadFile(path.Join(dir, "pids.max"))
			c, err2 := os.ReadFile(path.Join(dir, "pids.current"))
			if err1 == nil && err2 == nil {
				seen = true
				ms := strings.TrimSpace(string(m))
				mv, e1 := strconv.ParseInt(ms, 10, 64)
				cv, e2 := strconv.ParseInt(strings.TrimSpace(string(c)), 10, 64)
				if ms != "max" && e1 == nil && e2 == nil && (best < 0 || mv-cv < best) {
					best, max, cur, ok = mv-cv, mv, cv, true
				}
			}
			if p == "/" {
				break
			}
		}
	}
	for _, l := range strings.Split(procCgroup, "\n") {
		f := strings.SplitN(l, ":", 3)
		if len(f) != 3 {
			continue
		}
		switch {
		case f[0] == "0" && f[1] == "":
			consider(cgRoot, f[2])
		case hasController(f[1], "pids"):
			consider(path.Join(cgRoot, "pids"), f[2])
		}
	}
	return max, cur, ok, seen
}

func hasController(list, name string) bool {
	for _, c := range strings.Split(list, ",") {
		if c == name {
			return true
		}
	}
	return false
}

// nprocLimit returns RLIMIT_NPROC when it binds this process. The kernel
// exempts only the initial user namespace's root (and holders of
// CAP_SYS_RESOURCE/CAP_SYS_ADMIN there); uid 0 inside a user namespace
// (rootless podman, unshare -r) is an ordinary host uid, so it is exempt
// here only when its uid map is the identity.
func nprocLimit() (uint64, bool) {
	if os.Getuid() == 0 && identityUIDMap() {
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

// identityUIDMap reports whether this process is in the initial user
// namespace, whose uid_map is "0 0 4294967295". An unreadable map (no
// /proc yet, as PID 1 of a machine) counts as the initial namespace.
func identityUIDMap() bool {
	b, err := os.ReadFile("/proc/self/uid_map")
	if err != nil {
		return true
	}
	return strings.Join(strings.Fields(string(b)), " ") == "0 0 4294967295"
}

// uidTasks counts the tasks (threads) of the processes whose real uid is
// uid, which is what RLIMIT_NPROC is checked against. ok is false when
// /proc cannot be read.
func uidTasks(uid int) (n uint64, ok bool) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false
	}
	for _, e := range ents {
		if c := e.Name()[0]; c < '0' || c > '9' {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/status")
		if err != nil {
			continue // gone meanwhile
		}
		if u, t, ok := statusUIDThreads(string(b)); ok && u == uid {
			n += uint64(t)
		}
	}
	return n, true
}

// statusUIDThreads reads the real uid and the thread count from a
// /proc/PID/status text.
func statusUIDThreads(status string) (uid, threads int, ok bool) {
	var haveU, haveT bool
	for _, l := range strings.Split(status, "\n") {
		k, v, found := strings.Cut(l, ":")
		if !found {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		switch k {
		case "Uid":
			if n, err := strconv.Atoi(f[0]); err == nil {
				uid, haveU = n, true
			}
		case "Threads":
			if n, err := strconv.Atoi(f[0]); err == nil {
				threads, haveT = n, true
			}
		}
	}
	return uid, threads, haveU && haveT
}

package process

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Linux syscall numbers not in Go's syscall package.
const (
	sysPrlimit64  = 302 // SYS_prlimit64 (amd64)
	sysIoprioSet  = 251 // SYS_ioprio_set (amd64)
	sysPrctl      = 157 // SYS_prctl (amd64)

	ioprioWhoProcess = 1
	prSetNoNewPrivs  = 38
)

// applyPostForkAttrs applies process attributes after fork.
// These operate on the child PID from the parent process.
// Errors are collected and returned for logging by the caller.
func applyPostForkAttrs(pid int, params ExecParams) []error {
	var errs []error
	if params.Nice != nil {
		if err := applyNice(pid, *params.Nice); err != nil {
			errs = append(errs, fmt.Errorf("nice(%d): %w", *params.Nice, err))
		}
	}
	if params.OOMScoreAdj != nil {
		if err := applyOOMScoreAdj(pid, *params.OOMScoreAdj); err != nil {
			errs = append(errs, fmt.Errorf("oom_score_adj(%d): %w", *params.OOMScoreAdj, err))
		}
	}
	if len(params.Rlimits) > 0 {
		if err := applyRlimits(pid, params.Rlimits); err != nil {
			errs = append(errs, fmt.Errorf("rlimits: %w", err))
		}
	}
	if params.IOPrioClass > 0 {
		if err := applyIOPrio(pid, params.IOPrioClass, params.IOPrioLevel); err != nil {
			errs = append(errs, fmt.Errorf("ioprio(%d,%d): %w", params.IOPrioClass, params.IOPrioLevel, err))
		}
	}
	if params.CgroupPath != "" {
		if err := applyCgroup(pid, params.CgroupPath); err != nil {
			errs = append(errs, fmt.Errorf("cgroup(%s): %w", params.CgroupPath, err))
		}
	}
	if params.NoNewPrivs {
		if err := applyNoNewPrivs(pid); err != nil {
			errs = append(errs, fmt.Errorf("no_new_privs: %w", err))
		}
	}
	if params.Securebits != 0 {
		if err := applySecurebits(params.Securebits); err != nil {
			errs = append(errs, fmt.Errorf("securebits(%d): %w", params.Securebits, err))
		}
	}
	if len(params.CPUAffinity) > 0 {
		if err := applyCPUAffinity(pid, params.CPUAffinity); err != nil {
			errs = append(errs, fmt.Errorf("cpu-affinity: %w", err))
		}
	}
	return errs
}

func applyNice(pid, nice int) error {
	return syscall.Setpriority(syscall.PRIO_PROCESS, pid, nice)
}

func applyOOMScoreAdj(pid, adj int) error {
	path := "/proc/" + strconv.Itoa(pid) + "/oom_score_adj"
	return os.WriteFile(path, strconv.AppendInt(nil, int64(adj), 10), 0200)
}

func applyRlimits(pid int, limits []Rlimit) error {
	for _, rl := range limits {
		lim := syscall.Rlimit{
			Cur: rl.Soft,
			Max: rl.Hard,
		}
		if err := prlimit(pid, rl.Resource, &lim); err != nil {
			return fmt.Errorf("resource %d: %w", rl.Resource, err)
		}
	}
	return nil
}

// prlimit wraps the prlimit64 syscall to set resource limits on another process.
func prlimit(pid, resource int, newLim *syscall.Rlimit) error {
	_, _, errno := syscall.RawSyscall6(
		sysPrlimit64,
		uintptr(pid),
		uintptr(resource),
		uintptr(unsafe.Pointer(newLim)),
		0, // old limit (nil)
		0, 0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func applyIOPrio(pid, class, level int) error {
	// ioprio value = (class << 13) | level
	ioprio := uintptr((class << 13) | level)
	_, _, errno := syscall.Syscall(sysIoprioSet, ioprioWhoProcess, uintptr(pid), ioprio)
	if errno != 0 {
		return errno
	}
	return nil
}

func applyCgroup(pid int, cgroupPath string) error {
	procsPath := cgroupPath + "/cgroup.procs"
	return os.WriteFile(procsPath, strconv.AppendInt(nil, int64(pid), 10), 0200)
}

// KillCgroup sends a signal to every process in a cgroup v2 subtree.
// For SIGKILL it first tries the kernel's cgroup.kill interface
// (available on Linux ≥ 5.14), which is inherently recursive. When
// cgroup.kill is not available or the caller is sending a different
// signal, KillCgroup walks the cgroup subtree and signals every PID
// listed in each cgroup.procs file it encounters.
//
// The recursive walk matters when a service creates its own sub-cgroups
// (worker pools, container runtimes, etc.). A non-recursive kill would
// only reach processes in the leaf cgroup, leaving orphans behind that
// the service manager has no other handle on.
func KillCgroup(cgroupPath string, sig syscall.Signal) error {
	if cgroupPath == "" {
		return nil
	}

	// Try cgroup.kill (cgroup v2, kernel ≥ 5.14) — sends SIGKILL to the
	// whole subtree in one atomic write. Only valid for SIGKILL per the
	// kernel interface contract.
	if sig == syscall.SIGKILL {
		killPath := cgroupPath + "/cgroup.kill"
		if err := os.WriteFile(killPath, []byte("1"), 0200); err == nil {
			return nil
		}
		// cgroup.kill not available or failed; fall through to manual walk
	}

	// Fallback: walk the subtree and signal every PID we find. Errors
	// from individual cgroups are aggregated but never abort the walk —
	// a locked sub-cgroup must not prevent cleanup of its siblings.
	return killCgroupRecursive(cgroupPath, sig)
}

// killCgroupRecursive walks the cgroup v2 subtree rooted at root and sends
// sig to every PID listed in each cgroup.procs file encountered. It is a
// depth-first walk: deepest cgroups are signaled first so parents do not
// re-spawn children into an already-signaled subtree.
func killCgroupRecursive(root string, sig syscall.Signal) error {
	var lastErr error

	// Read direct children first so we can recurse before signaling the
	// current level. A cgroup is a directory; its children (sub-cgroups)
	// are the subdirectories, while cgroup.procs / cgroup.kill etc. are
	// plain files in the same directory.
	entries, err := os.ReadDir(root)
	if err != nil {
		// The cgroup itself may have been removed between our kill calls
		// (this is a benign race — the subtree is already empty).
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read cgroup %s: %w", root, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		child := root + "/" + e.Name()
		if err := killCgroupRecursive(child, sig); err != nil {
			lastErr = err
		}
	}

	// Now signal PIDs at this level.
	if err := killPIDsFromCgroupProcs(root, sig); err != nil {
		lastErr = err
	}
	return lastErr
}

// killPIDsFromCgroupProcs reads <cgroup>/cgroup.procs and sends sig to
// each PID listed. A missing procs file is not an error — it simply means
// there are no processes left in that cgroup.
func killPIDsFromCgroupProcs(cgroupPath string, sig syscall.Signal) error {
	data, err := os.ReadFile(cgroupPath + "/cgroup.procs")
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read cgroup.procs: %w", err)
	}

	var lastErr error
	start := 0
	for i := 0; i <= len(data); i++ {
		if i == len(data) || data[i] == '\n' {
			if i > start {
				pid, perr := strconv.Atoi(string(data[start:i]))
				if perr == nil && pid > 0 {
					// ESRCH just means the PID already exited — perfectly
					// fine during a teardown race.
					if kerr := syscall.Kill(pid, sig); kerr != nil && kerr != syscall.ESRCH {
						lastErr = kerr
					}
				}
			}
			start = i + 1
		}
	}
	return lastErr
}

func applyNoNewPrivs(pid int) error {
	// PR_SET_NO_NEW_PRIVS can only be set on the calling thread, not on
	// another process. The /proc/PID/attr/no_new_privs path does not exist.
	// This must be set in the child process before exec.
	//
	// Since Go's os/exec doesn't provide a pre-exec callback in the child,
	// this is a known limitation. For most use cases, the combination of
	// Credential + AmbientCaps in SysProcAttr provides equivalent security.
	//
	// TODO: implement via Cloneflags or a small C helper.
	return fmt.Errorf("no_new_privs cannot be set from parent process (requires child-side prctl)")
}

func applyCPUAffinity(pid int, cpus []uint) error {
	var set unix.CPUSet
	for _, cpu := range cpus {
		set.Set(int(cpu))
	}
	return unix.SchedSetaffinity(pid, &set)
}

const prSetSecurebits = 28 // PR_SET_SECUREBITS

func applySecurebits(bits uint32) error {
	// NOTE: PR_SET_SECUREBITS affects the calling thread only.
	// Setting it in the parent is intentionally skipped because it would
	// permanently alter slinit's own securebits, affecting ALL future
	// child processes — not just the target service.
	//
	// Securebits are inherited across fork, so the correct approach is
	// to set them in the child before exec. Since Go's os/exec does not
	// expose a pre-exec hook that runs in the child, this is a known
	// limitation. The ambient capabilities mechanism (SysProcAttr.AmbientCaps)
	// handles the most common use case.
	//
	// TODO: implement via a small C helper or clone3+CLONE_CLEAR_SIGHAND
	// to set securebits in the child process.
	return fmt.Errorf("securebits cannot be safely set from parent process (would affect slinit itself)")
}

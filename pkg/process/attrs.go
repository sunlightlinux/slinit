package process

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
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
	return errs
}

func applyNice(pid, nice int) error {
	return syscall.Setpriority(syscall.PRIO_PROCESS, pid, nice)
}

func applyOOMScoreAdj(pid, adj int) error {
	path := fmt.Sprintf("/proc/%d/oom_score_adj", pid)
	return os.WriteFile(path, []byte(fmt.Sprintf("%d", adj)), 0200)
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
	return os.WriteFile(procsPath, []byte(fmt.Sprintf("%d", pid)), 0200)
}

func applyNoNewPrivs(pid int) error {
	// PR_SET_NO_NEW_PRIVS can only be set on the calling thread.
	// For child processes, we write to /proc/PID/attr/no_new_privs
	// as a best-effort approach.
	path := fmt.Sprintf("/proc/%d/attr/no_new_privs", pid)
	return os.WriteFile(path, []byte("1"), 0200)
}

const prSetSecurebits = 28 // PR_SET_SECUREBITS

func applySecurebits(bits uint32) error {
	// PR_SET_SECUREBITS sets securebits for the calling thread.
	// When called from the parent before child exec, this affects
	// the parent's securebits which are inherited across fork.
	// Best-effort: only works if caller has CAP_SETPCAP.
	_, _, errno := syscall.Syscall(sysPrctl, prSetSecurebits, uintptr(bits), 0)
	if errno != 0 {
		return errno
	}
	return nil
}

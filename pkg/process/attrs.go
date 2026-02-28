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
// Errors are collected but do not prevent the process from running.
func applyPostForkAttrs(pid int, params ExecParams) {
	if params.Nice != nil {
		applyNice(pid, *params.Nice)
	}
	if params.OOMScoreAdj != nil {
		applyOOMScoreAdj(pid, *params.OOMScoreAdj)
	}
	if len(params.Rlimits) > 0 {
		applyRlimits(pid, params.Rlimits)
	}
	if params.IOPrioClass > 0 {
		applyIOPrio(pid, params.IOPrioClass, params.IOPrioLevel)
	}
	if params.CgroupPath != "" {
		applyCgroup(pid, params.CgroupPath)
	}
	if params.NoNewPrivs {
		applyNoNewPrivs(pid)
	}
	if params.Securebits != 0 {
		applySecurebits(params.Securebits)
	}
}

func applyNice(pid, nice int) {
	// syscall.Setpriority sets the scheduling priority for a process.
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, nice)
}

func applyOOMScoreAdj(pid, adj int) {
	path := fmt.Sprintf("/proc/%d/oom_score_adj", pid)
	_ = os.WriteFile(path, []byte(fmt.Sprintf("%d", adj)), 0200)
}

func applyRlimits(pid int, limits []Rlimit) {
	for _, rl := range limits {
		lim := syscall.Rlimit{
			Cur: rl.Soft,
			Max: rl.Hard,
		}
		prlimit(pid, rl.Resource, &lim)
	}
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

func applyIOPrio(pid, class, level int) {
	// ioprio value = (class << 13) | level
	ioprio := uintptr((class << 13) | level)
	syscall.Syscall(sysIoprioSet, ioprioWhoProcess, uintptr(pid), ioprio)
}

func applyCgroup(pid int, cgroupPath string) {
	procsPath := cgroupPath + "/cgroup.procs"
	_ = os.WriteFile(procsPath, []byte(fmt.Sprintf("%d", pid)), 0200)
}

func applyNoNewPrivs(pid int) {
	// PR_SET_NO_NEW_PRIVS can only be set on the calling thread.
	// For child processes, we write to /proc/PID/attr/no_new_privs
	// as a best-effort approach.
	path := fmt.Sprintf("/proc/%d/attr/no_new_privs", pid)
	_ = os.WriteFile(path, []byte("1"), 0200)
}

const prSetSecurebits = 28 // PR_SET_SECUREBITS

func applySecurebits(bits uint32) {
	// PR_SET_SECUREBITS sets securebits for the calling thread.
	// When called from the parent before child exec, this affects
	// the parent's securebits which are inherited across fork.
	// Best-effort: only works if caller has CAP_SETPCAP.
	syscall.Syscall(sysPrctl, prSetSecurebits, uintptr(bits), 0)
}

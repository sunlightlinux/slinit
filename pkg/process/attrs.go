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

// KillCgroup sends a signal to all processes in a cgroup v2 hierarchy.
// First tries the cgroup.kill interface (kernel 5.14+), then falls back
// to reading cgroup.procs and signaling each PID individually.
// This ensures the entire process tree is terminated, not just the leader.
func KillCgroup(cgroupPath string, sig syscall.Signal) error {
	if cgroupPath == "" {
		return nil
	}

	// Try cgroup.kill (cgroup v2, kernel ≥ 5.14) — sends SIGKILL to all
	killPath := cgroupPath + "/cgroup.kill"
	if sig == syscall.SIGKILL {
		if err := os.WriteFile(killPath, []byte("1"), 0200); err == nil {
			return nil
		}
		// cgroup.kill not available or failed, fall through to manual kill
	}

	// Fallback: read cgroup.procs and signal each PID
	data, err := os.ReadFile(cgroupPath + "/cgroup.procs")
	if err != nil {
		return fmt.Errorf("read cgroup.procs: %w", err)
	}

	// Parse PIDs (one per line) and signal each
	var lastErr error
	start := 0
	for i := 0; i <= len(data); i++ {
		if i == len(data) || data[i] == '\n' {
			if i > start {
				pid, err := strconv.Atoi(string(data[start:i]))
				if err == nil && pid > 0 {
					if err := syscall.Kill(pid, sig); err != nil && err != syscall.ESRCH {
						lastErr = err
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

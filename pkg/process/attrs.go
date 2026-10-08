package process

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Linux syscall numbers not in Go's syscall package.
const (
	sysPrlimit64 = 302 // SYS_prlimit64 (amd64)
	sysIoprioSet = 251 // SYS_ioprio_set (amd64)
	sysPrctl     = 157 // SYS_prctl (amd64)

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
		// Apply cgroup settings (resource limits) BEFORE moving the process
		// into the cgroup. This ensures limits are in effect from the start.
		if len(params.CgroupSettings) > 0 {
			if err := applyCgroupSettings(params.CgroupPath, params.CgroupSettings); err != nil {
				errs = append(errs, fmt.Errorf("cgroup-settings: %w", err))
			}
		}
		if err := applyCgroup(pid, params.CgroupPath); err != nil {
			errs = append(errs, fmt.Errorf("cgroup(%s): %w", params.CgroupPath, err))
		}
	}
	if params.NoNewPrivs && params.RunnerPath == "" {
		// Runner-wrap (exec.go:69) handles NoNewPrivs end-to-end via
		// --no-new-privs whenever RunnerPath is set; only call the
		// parent-side stub when no runner is configured, so the
		// operator gets a single clear warning instead of one per start.
		if err := applyNoNewPrivs(pid); err != nil {
			errs = append(errs, fmt.Errorf("no_new_privs: %w", err))
		}
	}
	if len(params.CPUAffinity) > 0 {
		if err := applyCPUAffinity(pid, params.CPUAffinity); err != nil {
			errs = append(errs, fmt.Errorf("cpu-affinity: %w", err))
		}
	}
	// Real-time scheduling. Applied AFTER cpu-affinity / cgroup so
	// admission control sees the final placement. SchedSetAttr operates
	// on a remote PID, so the parent can configure the child's policy
	// without an extra child-side hop.
	if params.SchedPolicy != 0 {
		if err := applySched(pid, params); err != nil {
			errs = append(errs, fmt.Errorf("sched-policy: %w", err))
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

// cgroupRoot is the base of the cgroup v2 hierarchy used to validate
// configured cgroup paths. Tests override this to point at a tmpdir.
var cgroupRoot = "/sys/fs/cgroup"

// validateCgroupPath rejects paths that aren't safely within the cgroup v2
// hierarchy. Without this, a service with `cgroup = ../../../var/run`
// would let slinit (root) write a PID into an arbitrary file via the
// auto-MkdirAll + WriteFile sequence below.
func validateCgroupPath(p string) error {
	if p == "" {
		return fmt.Errorf("empty cgroup path")
	}
	clean := filepath.Clean(p)
	if clean != cgroupRoot && !strings.HasPrefix(clean, cgroupRoot+"/") {
		return fmt.Errorf("cgroup path %q is outside %s", p, cgroupRoot)
	}
	return nil
}

// RemoveCgroup deletes a service's cgroup directory. slinit creates
// these directories itself (applyCgroup and PrepareCgroupForFD both
// MkdirAll), and nothing used to delete them, so a workload that keeps
// producing new names — `slinitctl run --slice=NAME`, whose transient
// units are called run-<rand> — left one behind per invocation. They
// survive a soft reboot, because a soft reboot does not touch cgroupfs.
//
// This is an rmdir, never a recursive delete. A cgroup that still holds
// processes or child cgroups fails with ENOTEMPTY and is left exactly
// as it was, which is the outcome worth having: the directory is only
// reclaimed once it is genuinely empty. Callers treat any error as
// "nothing to do".
//
// A directory the operator pre-created is removed too, once empty. That
// is the same directory slinit would have created had it been absent,
// and the next start recreates it and rewrites every setting from the
// service's configuration, so nothing slinit knows about is lost.
func RemoveCgroup(path string) error {
	if err := validateCgroupPath(path); err != nil {
		return err
	}
	if filepath.Clean(path) == cgroupRoot {
		return fmt.Errorf("refusing to remove the cgroup root %s", cgroupRoot)
	}
	return syscall.Rmdir(path)
}

// validateCgroupSettingFile rejects setting filenames that try to escape
// the cgroup directory or address an unrelated file. Cgroup interface
// files are all flat names (e.g. "memory.max", "cpu.weight"); a name
// containing "/" or ".." is always either a typo or an attack.
func validateCgroupSettingFile(name string) error {
	if name == "" {
		return fmt.Errorf("empty cgroup setting name")
	}
	if strings.ContainsAny(name, "/") || name == ".." || strings.Contains(name, "..") {
		return fmt.Errorf("cgroup setting name %q must be a flat filename", name)
	}
	return nil
}

func applyCgroup(pid int, cgroupPath string) error {
	if err := validateCgroupPath(cgroupPath); err != nil {
		return err
	}
	// Auto-create the cgroup directory if it does not exist.
	if err := os.MkdirAll(cgroupPath, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", cgroupPath, err)
	}
	procsPath := cgroupPath + "/cgroup.procs"
	return os.WriteFile(procsPath, strconv.AppendInt(nil, int64(pid), 10), 0200)
}

// PrepareCgroupForFD pre-creates the cgroup directory + writes resource
// settings, then opens the directory and returns the *os.File. The fd is
// suitable as SysProcAttr.CgroupFD with UseCgroupFD=true, which routes
// fork through clone3+CLONE_INTO_CGROUP — the child enters the cgroup
// before exec, so anything it forks afterwards inherits the cgroup.
// Without this, a fast shell child could fork (e.g. setsid'd) grandchildren
// in the root cgroup during the race window between cmd.Start() and the
// post-fork cgroup.procs write.
//
// Caller is responsible for closing the returned fd after Start.
// Requires Linux ≥5.7 for CLONE_INTO_CGROUP at fork time.
func PrepareCgroupForFD(cgroupPath string, settings []CgroupSetting) (*os.File, error) {
	if err := validateCgroupPath(cgroupPath); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cgroupPath, 0755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", cgroupPath, err)
	}
	if len(settings) > 0 {
		// Best-effort: settings write failures here are non-fatal because
		// the post-fork path retries the same writes (idempotent).
		_ = applyCgroupSettings(cgroupPath, settings)
	}
	return os.Open(cgroupPath)
}

// applyCgroupSettings writes resource limit values to the cgroup directory.
// It also enables the required subtree controllers on the parent cgroup so
// that delegation works (e.g., writing "memory.max" requires "+memory" in
// the parent's cgroup.subtree_control).
func applyCgroupSettings(cgroupPath string, settings []CgroupSetting) error {
	if err := validateCgroupPath(cgroupPath); err != nil {
		return err
	}
	for _, s := range settings {
		if err := validateCgroupSettingFile(s.File); err != nil {
			return err
		}
	}
	// Auto-create the cgroup directory if it does not exist.
	if err := os.MkdirAll(cgroupPath, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", cgroupPath, err)
	}

	// Enable required controllers on the parent. Extract controller names
	// from the settings (the part before the dot in the filename).
	enableSubtreeControllers(cgroupPath, settings)

	var lastErr error
	for _, s := range settings {
		path := cgroupPath + "/" + s.File
		if err := os.WriteFile(path, []byte(s.Value), 0200); err != nil {
			lastErr = fmt.Errorf("write %s=%s: %w", s.File, s.Value, err)
		}
	}
	return lastErr
}

// AvailableControllers lists the controllers a cgroup may use, which is
// what its parent has delegated down. An empty result is the normal
// answer for a path whose parent delegated nothing, not an error.
func AvailableControllers(cgroupPath string) []string {
	b, err := os.ReadFile(cgroupPath + "/cgroup.controllers")
	if err != nil {
		return nil
	}
	return strings.Fields(string(b))
}

// DelegateCgroup hands a service's own cgroup subtree over to the service,
// so a payload that manages cgroups itself — a container runtime, a
// per-connection worker pool, a nested service manager — can create and
// configure children inside it without being root over the whole
// hierarchy.
//
// Two kernel rules decide what this can and cannot do, both confirmed by
// probe rather than taken from documentation:
//
//  1. A controller is only usable in a cgroup if the PARENT lists it in
//     cgroup.subtree_control. Until then the child's cgroup.controllers is
//     empty and writing to its subtree_control fails with ENOENT. So the
//     controllers are enabled one level up, which is what makes them
//     available inside the delegated cgroup.
//
//  2. A cgroup that holds processes cannot have controllers enabled in its
//     own subtree_control — the write is refused (EOPNOTSUPP on the kernel
//     this was tested against, EBUSY on others; either way it fails). The
//     service's main process lives in this cgroup, so slinit deliberately
//     does NOT write its subtree_control. That belongs to the delegatee,
//     after it has moved its processes into a child of its own. This is
//     the "no inner processes" rule and it is the delegatee's half of the
//     contract, not something slinit can do on its behalf.
//
// Ownership is the other half. An unprivileged payload cannot create a
// child cgroup in a root-owned directory, so the directory and the three
// interface files it needs are chowned to the service's user. Everything
// else in the cgroup stays root-owned, so the payload can manage its own
// subtree without being able to rewrite the limits slinit set on it —
// which is the whole point of delegating rather than just loosening
// permissions.
//
// uid/gid of -1 means the service runs as root and nothing is chowned.
func DelegateCgroup(cgroupPath string, controllers []string, uid, gid int) error {
	if err := validateCgroupPath(cgroupPath); err != nil {
		return err
	}
	if err := os.MkdirAll(cgroupPath, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", cgroupPath, err)
	}

	// Rule 1: make the controllers available by enabling them one level up.
	parent := filepath.Dir(cgroupPath)
	if parent != cgroupPath && parent != "/" {
		subtreeCtl := parent + "/cgroup.subtree_control"
		for _, ctrl := range controllers {
			if err := validateControllerName(ctrl); err != nil {
				return err
			}
			// Best-effort per controller: a kernel without `misc` should
			// not cost the service its `memory`. What actually reached the
			// child is observable in its cgroup.controllers.
			_ = os.WriteFile(subtreeCtl, []byte("+"+ctrl), 0200)
		}
	}

	if uid < 0 && gid < 0 {
		return nil
	}

	// Rule 2's consequence: these three are what a delegatee needs, and
	// they are all it gets.
	if err := os.Chown(cgroupPath, uid, gid); err != nil {
		return fmt.Errorf("chown %s: %w", cgroupPath, err)
	}
	for _, f := range []string{"cgroup.procs", "cgroup.subtree_control", "cgroup.threads"} {
		// cgroup.threads is absent on a domain cgroup in some kernels;
		// a missing file is not a failure to delegate.
		if err := os.Chown(cgroupPath+"/"+f, uid, gid); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("chown %s/%s: %w", cgroupPath, f, err)
		}
	}
	return nil
}

// validateControllerName keeps a configured controller name from being
// anything but a controller name. The value reaches a write to
// cgroup.subtree_control, where "+x -memory" would silently disable a
// controller the operator asked for.
func validateControllerName(name string) error {
	if name == "" {
		return fmt.Errorf("empty controller name")
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && r != '_' {
			return fmt.Errorf("invalid cgroup controller name %q", name)
		}
	}
	return nil
}

// RemoveCgroupTree removes a cgroup and the children left inside it,
// deepest first.
//
// RemoveCgroup is a plain rmdir on purpose, and for a service whose
// cgroup slinit alone populates that is the right thing: it cannot
// destroy anything that is still in use. But a delegated subtree is
// populated by the payload, and a payload that exits without tidying up
// leaves directories that no rmdir of the parent can ever reclaim — the
// leak compounds for every restart. So for delegated cgroups only, the
// children slinit did not create are removed with it.
//
// Still never recursive in the dangerous sense: each removal is an rmdir,
// so a cgroup that still holds processes fails and is left alone, and the
// walk is bottom-up so a parent is only attempted after its children.
// Call it after the subtree has been killed.
func RemoveCgroupTree(path string) error {
	if err := validateCgroupPath(path); err != nil {
		return err
	}
	if filepath.Clean(path) == cgroupRoot {
		return fmt.Errorf("refusing to remove the cgroup root %s", cgroupRoot)
	}

	var dirs []string
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// A cgroup that vanished under us is one less to remove.
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Deepest first, so each rmdir sees an empty directory.
	for i := len(dirs) - 1; i >= 0; i-- {
		if rmErr := syscall.Rmdir(dirs[i]); rmErr != nil && dirs[i] == path {
			return rmErr
		}
	}
	return nil
}

// enableSubtreeControllers enables the required controllers on the parent
// cgroup's subtree_control file. For example, if we need to write
// "memory.max", the parent must have "+memory" in its cgroup.subtree_control.
// Errors are silently ignored — the subsequent writes will fail with a clear
// error if the controller is not available.
func enableSubtreeControllers(cgroupPath string, settings []CgroupSetting) {
	parent := filepath.Dir(cgroupPath)
	if parent == cgroupPath || parent == "/" {
		return
	}
	subtreeCtl := parent + "/cgroup.subtree_control"

	// Collect unique controller names needed.
	seen := make(map[string]bool)
	for _, s := range settings {
		if idx := strings.IndexByte(s.File, '.'); idx > 0 {
			ctrl := s.File[:idx]
			if !seen[ctrl] {
				seen[ctrl] = true
			}
		}
	}

	for ctrl := range seen {
		// Best-effort: write "+controller" to parent's subtree_control.
		_ = os.WriteFile(subtreeCtl, []byte("+"+ctrl), 0200)
	}
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
	// PR_SET_NO_NEW_PRIVS can only be set on the calling thread; the
	// parent cannot reach into a forked child. The supported path is
	// slinit-runner --no-new-privs (see cmd/slinit-runner/main.go and
	// exec.go:528 needsRunnerWrap). This parent-side function only
	// runs in the degraded path where RunnerPath is empty (runner not
	// installed) — surface that explicitly so the operator sees why
	// the prctl didn't take.
	_ = pid
	return fmt.Errorf("no_new_privs requires slinit-runner (RunnerPath unset on this ServiceSet)")
}

func applyCPUAffinity(pid int, cpus []uint) error {
	var set unix.CPUSet
	for _, cpu := range cpus {
		set.Set(int(cpu))
	}
	return unix.SchedSetaffinity(pid, &set)
}

// applySched programs the scheduling policy and, where applicable,
// priority / deadline-bandwidth parameters of pid via sched_setattr(2).
//
// SCHED_DEADLINE rejects any change that does not fit the system's
// available bandwidth (admission control). SCHED_FIFO/RR require
// CAP_SYS_NICE or a sufficient RLIMIT_RTPRIO. We pass these errors back
// to the caller — the operator sees them in the post-fork warning log
// and the service starts with the kernel's default policy instead.
func applySched(pid int, params ExecParams) error {
	attr := unix.SchedAttr{
		Size:   uint32(unsafe.Sizeof(unix.SchedAttr{})),
		Policy: params.SchedPolicy,
	}
	if params.SchedResetOnFork {
		attr.Flags |= unix.SCHED_FLAG_RESET_ON_FORK
	}

	switch params.SchedPolicy {
	case unix.SCHED_FIFO, unix.SCHED_RR:
		if params.SchedPriority < 1 || params.SchedPriority > 99 {
			return fmt.Errorf("sched-priority %d out of range 1..99", params.SchedPriority)
		}
		attr.Priority = params.SchedPriority

	case unix.SCHED_DEADLINE:
		if params.SchedRuntime == 0 || params.SchedDeadline == 0 || params.SchedPeriod == 0 {
			return fmt.Errorf("SCHED_DEADLINE requires sched-runtime, sched-deadline and sched-period")
		}
		if params.SchedRuntime > params.SchedDeadline {
			return fmt.Errorf("SCHED_DEADLINE: runtime (%d) must be ≤ deadline (%d)",
				params.SchedRuntime, params.SchedDeadline)
		}
		if params.SchedDeadline > params.SchedPeriod {
			return fmt.Errorf("SCHED_DEADLINE: deadline (%d) must be ≤ period (%d)",
				params.SchedDeadline, params.SchedPeriod)
		}
		attr.Runtime = params.SchedRuntime
		attr.Deadline = params.SchedDeadline
		attr.Period = params.SchedPeriod

	case unix.SCHED_NORMAL, unix.SCHED_BATCH, unix.SCHED_IDLE:
		// No priority field; nothing else to fill.

	default:
		return fmt.Errorf("unsupported sched-policy %d", params.SchedPolicy)
	}

	return unix.SchedSetAttr(pid, &attr, 0)
}

// slinit-runner is a tiny exec helper invoked by slinit when a service
// configures mlockall(2) or set_mempolicy(2). Both syscalls operate on
// the *calling* process — the parent cannot apply them to a freshly
// fork()ed child remotely, so slinit prepends this helper to the real
// command and the helper applies the syscalls before exec'ing the
// child binary in place.
//
// Usage (always synthesised by slinit, never invoked by humans):
//
//	slinit-runner [--mlockall=N] [--mempolicy=MODE]
//	              [--numa-nodes=LIST] -- /path/to/svc args...
//
// MODE is one of bind, preferred, interleave, local, default.
// LIST is a comma- or hyphen-spec like "0,2,4" or "0-3".
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-runner: %v\n", err)
		os.Exit(2)
	}
}

func run() error {
	fs := flag.NewFlagSet("slinit-runner", flag.ExitOnError)
	mlockall := fs.Int("mlockall", 0,
		"bitmask passed to mlockall(2): MCL_CURRENT=1, MCL_FUTURE=2, MCL_ONFAULT=4")
	mempolicy := fs.String("mempolicy", "",
		"NUMA memory policy: bind, preferred, interleave, local, default")
	numaNodes := fs.String("numa-nodes", "",
		"NUMA node list for bind/interleave/preferred (e.g. '0-3' or '0,2,4')")
	apparmor := fs.String("apparmor", "",
		"AppArmor profile to transition into on the upcoming exec")
	debug := fs.Bool("debug", false,
		"raise SIGSTOP before exec so a debugger can attach (resume with SIGCONT)")
	privateTmp := fs.Bool("private-tmp", false,
		"mount a fresh tmpfs at /tmp and /var/tmp (systemd PrivateTmp=)")
	protectSystem := fs.String("protect-system", "",
		"remount system paths read-only: yes | full | strict (systemd ProtectSystem=)")
	protectHome := fs.String("protect-home", "",
		"hide /home, /root, /run/user: yes | read-only | tmpfs (systemd ProtectHome=)")
	protectProc := fs.String("protect-proc", "",
		"/proc hidepid= mode: noaccess | invisible | ptraceable (systemd ProtectProc=)")
	procSubset := fs.String("proc-subset", "",
		"/proc subset= filter: pid (systemd ProcSubset=)")
	var readOnlyPaths, readWritePaths, inaccessiblePaths stringList
	var bindPaths, bindROPaths, tmpfsPaths stringList
	fs.Var(&readOnlyPaths, "read-only-path",
		"add a path to be bind-mounted read-only (repeatable)")
	fs.Var(&readWritePaths, "read-write-path",
		"add a path to remain writable when ProtectSystem= would make it read-only (repeatable)")
	fs.Var(&inaccessiblePaths, "inaccessible-path",
		"add a path to be hidden behind an empty inaccessible mount (repeatable)")
	fs.Var(&bindPaths, "bind-path",
		"add a writable bind-mount as src:dst (repeatable)")
	fs.Var(&bindROPaths, "bind-ro-path",
		"add a read-only bind-mount as src:dst (repeatable)")
	fs.Var(&tmpfsPaths, "tmpfs-path",
		"mount a fresh tmpfs at path[:options] (repeatable)")
	var syscallFilter, syscallArchs, syscallLog stringList
	syscallAction := fs.String("syscall-action", "",
		"seccomp default action for non-allowed syscalls (kill|log|trap|errno-name|errno-number)")
	fs.Var(&syscallFilter, "syscall-filter",
		"add a seccomp filter item: syscall name, @group, or ~ prefix on first item (repeatable)")
	fs.Var(&syscallArchs, "syscall-arch",
		"add an accepted architecture for seccomp filtering (repeatable)")
	fs.Var(&syscallLog, "syscall-log",
		"add a syscall (or @group) always logged via SECCOMP_RET_LOG (repeatable)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	args := fs.Args()
	if len(args) == 0 {
		return fmt.Errorf("missing target command after flags")
	}

	if *mempolicy != "" {
		mode, nodes, err := parseMempolicy(*mempolicy, *numaNodes)
		if err != nil {
			return fmt.Errorf("mempolicy: %w", err)
		}
		if err := setMempolicy(mode, nodes); err != nil {
			return fmt.Errorf("set_mempolicy: %w", err)
		}
	} else if *numaNodes != "" {
		return fmt.Errorf("numa-nodes set without mempolicy")
	}

	if *mlockall != 0 {
		if err := unix.Mlockall(*mlockall); err != nil {
			return fmt.Errorf("mlockall(0x%x): %w", *mlockall, err)
		}
	}

	// Filesystem sandbox: must happen before AppArmor transition (since
	// the kernel binds the apparmor onexec change to *this* task and any
	// intervening fork/exec via mount helpers would lose it) but after
	// the mlockall/mempolicy calls above (those are pure per-task
	// syscalls unaffected by the mount setup). The runner already runs
	// inside the fresh mount namespace created by the parent's
	// CLONE_NEWNS, so the mount(2) calls below are confined to it.
	spec := sandboxSpec{
		privateTmp:          *privateTmp,
		protectSystem:       *protectSystem,
		readOnlyPaths:       readOnlyPaths,
		readWritePaths:      readWritePaths,
		protectHome:         *protectHome,
		inaccessiblePaths:   inaccessiblePaths,
		protectProc:         *protectProc,
		procSubset:          *procSubset,
		bindPaths:           bindPaths,
		bindROPaths:         bindROPaths,
		temporaryFilesystem: tmpfsPaths,
	}
	if spec.active() {
		if err := applySandbox(spec); err != nil {
			return fmt.Errorf("sandbox: %w", err)
		}
	}

	// seccomp filter install. Must come after the mount/mempolicy
	// setup above (those are privileged operations the kernel would
	// refuse with NO_NEW_PRIVS set) and before the AppArmor onexec
	// transition (AppArmor only attaches to the next execve, which is
	// our trailing syscall.Exec). The install also sets
	// PR_SET_NO_NEW_PRIVS as a prerequisite so non-root services can
	// use it without CAP_SYS_ADMIN.
	if err := installSeccomp(seccompSpec{
		filter:    syscallFilter,
		archs:     syscallArchs,
		errorAct:  *syscallAction,
		logFilter: syscallLog,
	}); err != nil {
		return err
	}

	// Developer debug stop: all runner setup is done, so freeze here
	// with SIGSTOP. The operator attaches a debugger to this PID and
	// resumes it with SIGCONT, after which the profile transition and
	// exec happen — the debugger lands in the service from its first
	// instruction.
	if *debug {
		if err := syscall.Kill(syscall.Getpid(), syscall.SIGSTOP); err != nil {
			return fmt.Errorf("debug SIGSTOP: %w", err)
		}
	}

	// AppArmor domain transition. This must be the last thing before
	// exec: the kernel attaches the profile change to *this* task's
	// next execve, so no fork may intervene. A failure aborts rather
	// than exec'ing the service unconfined.
	if *apparmor != "" {
		if err := changeOnExec(*apparmor); err != nil {
			return fmt.Errorf("apparmor switch %q: %w", *apparmor, err)
		}
	}

	// Replace ourselves with the target program. exec inherits the
	// locked memory and the active mempolicy, so the bandwidth promise
	// the operator made via the config takes effect from the first
	// instruction of the real service.
	if err := syscall.Exec(args[0], args, os.Environ()); err != nil {
		return fmt.Errorf("exec %s: %w", args[0], err)
	}
	return nil // unreachable
}

// changeOnExec performs an AppArmor onexec transition, the same
// operation as libapparmor's aa_change_onexec(): write "exec <profile>"
// to /proc/self/attr/exec in a single write(2). The kernel applies the
// profile when this task next calls execve, which is the syscall.Exec
// immediately after this returns. Writing requires the AppArmor LSM to
// be active; on a kernel without it the open/write fails and the start
// is aborted (fail closed).
func changeOnExec(profile string) error {
	f, err := os.OpenFile("/proc/self/attr/exec", os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open /proc/self/attr/exec: %w", err)
	}
	defer f.Close()
	if _, err := f.Write([]byte("exec " + profile)); err != nil {
		return fmt.Errorf("write attr/exec: %w", err)
	}
	return nil
}

func parseMempolicy(mode, nodesStr string) (uint32, []uint, error) {
	var (
		modeNum   uint32
		needNodes bool
	)
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "default":
		modeNum = unix.MPOL_DEFAULT
	case "bind":
		modeNum, needNodes = unix.MPOL_BIND, true
	case "preferred":
		modeNum, needNodes = unix.MPOL_PREFERRED, true
	case "interleave":
		modeNum, needNodes = unix.MPOL_INTERLEAVE, true
	case "local":
		modeNum = unix.MPOL_LOCAL
	default:
		return 0, nil, fmt.Errorf("unknown mode %q (expected bind|preferred|interleave|local|default)", mode)
	}
	nodes, err := parseNodeList(nodesStr)
	if err != nil {
		return 0, nil, err
	}
	if needNodes && len(nodes) == 0 {
		return 0, nil, fmt.Errorf("mode %q requires --numa-nodes", mode)
	}
	if !needNodes && len(nodes) > 0 {
		return 0, nil, fmt.Errorf("mode %q does not accept node list", mode)
	}
	return modeNum, nodes, nil
}

// parseNodeList accepts comma-separated single nodes and hyphen ranges,
// e.g. "0,2,4" or "0-3" or "0-1,3,5-7".
func parseNodeList(s string) ([]uint, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	seen := make(map[uint]struct{})
	var out []uint
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			rng := strings.SplitN(part, "-", 2)
			lo, err1 := strconv.ParseUint(strings.TrimSpace(rng[0]), 10, 32)
			hi, err2 := strconv.ParseUint(strings.TrimSpace(rng[1]), 10, 32)
			if err1 != nil || err2 != nil || lo > hi {
				return nil, fmt.Errorf("invalid node range %q", part)
			}
			for n := lo; n <= hi; n++ {
				if _, ok := seen[uint(n)]; !ok {
					seen[uint(n)] = struct{}{}
					out = append(out, uint(n))
				}
			}
			continue
		}
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid node %q", part)
		}
		if _, ok := seen[uint(n)]; !ok {
			seen[uint(n)] = struct{}{}
			out = append(out, uint(n))
		}
	}
	return out, nil
}

// setMempolicy invokes the raw set_mempolicy(2) syscall. Linux exposes
// the system call via SYS_SET_MEMPOLICY; we build the bitmask from the
// node list here. maxnode is "highest node index + 1" rounded up — see
// numa(7) and set_mempolicy(2) for the gnarly mask layout.
func setMempolicy(mode uint32, nodes []uint) error {
	var (
		maskPtr uintptr
		maxnode uintptr
	)
	if len(nodes) > 0 {
		highest := uint(0)
		for _, n := range nodes {
			if n > highest {
				highest = n
			}
		}
		// nodemask is an array of unsigned long. Allocate enough words
		// to cover (highest+1) bits, plus one trailing word so the
		// kernel's bounds check on maxnode passes (it requires
		// maxnode ≤ 8 * sizeof(mask) + 1).
		const bitsPerWord = 64
		words := int(highest/bitsPerWord) + 1
		mask := make([]uint64, words)
		for _, n := range nodes {
			mask[n/bitsPerWord] |= 1 << (n % bitsPerWord)
		}
		maskPtr = uintptr(unsafe.Pointer(&mask[0]))
		maxnode = uintptr(highest + 2) // +1 for inclusive, +1 to clear kernel's off-by-one
	}

	_, _, errno := syscall.Syscall(unix.SYS_SET_MEMPOLICY,
		uintptr(mode), maskPtr, maxnode)
	if errno != 0 {
		return errno
	}
	return nil
}

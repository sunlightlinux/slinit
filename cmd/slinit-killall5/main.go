// slinit-killall5 — drop-in replacement for sysvinit's killall5(8).
//
// init.d scripts call killall5 on their shutdown paths, usually as
// `killall5 -15` followed by `killall5 -9`, and `killall5 -15 -o $$` when
// the script wants to survive its own sweep. That interface — a bare
// signal number and a repeatable -o list — is what this implements, so a
// distro can symlink /sbin/killall5 at it and leave the scripts alone.
//
// slinit-nuke is the neighbouring tool and deliberately not this one: it
// takes no signal argument, has no omit list, and applies a policy
// (SIGTERM, grace, SIGKILL) aimed at an operator rescuing a wedged
// machine. killall5 is a primitive that does exactly what it is told
// once, which is what a script on a shutdown path needs.
//
// Behaviour follows sysvinit's killall5.c rather than its manual, which
// does not describe the session filter or the SIGSTOP freeze:
//
//   - PID 1, this process, every process in this process's session, every
//     kernel thread and every zombie are skipped.
//   - With no -o list the whole system is frozen with kill(-1, SIGSTOP)
//     first and resumed with SIGCONT afterwards, so the process list
//     cannot change while /proc is read. With an -o list upstream does
//     not freeze, and neither does this.
//   - Exit 0 if anything was signalled, 2 if nothing was, 1 if /proc
//     could not be read.
//
// Usage:
//
//	slinit-killall5 -SIGNUM [-o PID[,PID...]] [-o PID[,PID...]] ...
package main

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Seams, so the tests can exercise the selection logic without
// signalling the machine they run on.
var (
	killFunc   = syscall.Kill
	procRoot   = "/proc"
	getpidFunc = os.Getpid
	getsidFunc = func() int {
		s, err := unix.Getsid(0)
		if err != nil {
			return -1
		}
		return s
	}
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

const (
	exitKilled   = 0
	exitNoProc   = 1
	exitNotKille = 2
	exitUsage    = 1 // upstream prints usage and returns 1
)

func run(args []string, stderr *os.File) int {
	sig, omit, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "slinit-killall5: %v\n", err)
		fmt.Fprintln(stderr, "usage: slinit-killall5 -signum [-o omitpid[,omitpid...]] ...")
		return exitUsage
	}

	// A script's sweep must not be cut short by its own signal. SIGSTOP
	// and SIGKILL cannot be caught at all, and upstream's attempt to
	// ignore them is a no-op it keeps for defensiveness — but it does not
	// need to work, because Linux excludes the calling process from
	// kill(-1, sig). Only SIGTERM is worth claiming, and only to survive
	// a signal aimed at us by something else mid-sweep.
	signal.Ignore(syscall.SIGTERM)

	// Stay resident. By the time init.d scripts reach killall5 the
	// filesystems may be read-only and swap gone, and a page fault that
	// needs to read the binary back has nowhere to read it from.
	//
	// MCL_CURRENT only. Upstream adds MCL_FUTURE, which in a Go program
	// would lock every later allocation the runtime makes — a liability
	// here rather than insurance, for a process whose remaining work is
	// one walk of /proc.
	_ = unix.Mlockall(unix.MCL_CURRENT)

	self := getpidFunc()
	sid := getsidFunc()

	// Freeze first, so nothing forks away while /proc is being read.
	// Upstream only does this when there is no omit list, and the reason
	// is visible in the shape of the problem: with -o the caller is
	// usually a script that must keep running, and stopping everything
	// would stop it too.
	froze := false
	if len(omit) == 0 {
		if err := killFunc(-1, syscall.SIGSTOP); err == nil {
			froze = true
		}
	}

	procs, err := readProcs(procRoot)
	if err != nil {
		if froze {
			_ = killFunc(-1, syscall.SIGCONT)
		}
		fmt.Fprintf(stderr, "slinit-killall5: %v\n", err)
		return exitNoProc
	}

	killed := false
	for _, p := range procs {
		if skip(p, self, sid, omit) {
			continue
		}
		if err := killFunc(p.pid, sig); err == nil {
			killed = true
		}
	}

	if froze {
		_ = killFunc(-1, syscall.SIGCONT)
	}

	if killed {
		return exitKilled
	}
	return exitNotKille
}

// parseArgs reads killall5's argument grammar: one bare signal number,
// then any number of -o lists.
//
// Deliberately hand-rolled. Go's flag package cannot express `-15`, and a
// drop-in has to accept exactly what the scripts already pass — no long
// forms, no `-s TERM`, nothing invented. A script that works against
// sysvinit must work against this unchanged.
func parseArgs(args []string) (syscall.Signal, map[int]bool, error) {
	var (
		sig    syscall.Signal
		sawSig bool
		omit   = map[int]bool{}
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-o":
			i++
			if i >= len(args) {
				return 0, nil, fmt.Errorf("-o needs a pid list")
			}
			for _, f := range strings.Split(args[i], ",") {
				f = strings.TrimSpace(f)
				if f == "" {
					continue
				}
				n, err := strconv.Atoi(f)
				if err != nil || n <= 0 {
					return 0, nil, fmt.Errorf("illegal omit pid value %q", f)
				}
				omit[n] = true
			}
		case strings.HasPrefix(a, "-o"):
			// `-o123`, which upstream's getopt-less parser also accepts.
			for _, f := range strings.Split(a[2:], ",") {
				f = strings.TrimSpace(f)
				if f == "" {
					continue
				}
				n, err := strconv.Atoi(f)
				if err != nil || n <= 0 {
					return 0, nil, fmt.Errorf("illegal omit pid value %q", f)
				}
				omit[n] = true
			}
		case strings.HasPrefix(a, "-"):
			if sawSig {
				return 0, nil, fmt.Errorf("more than one signal given")
			}
			n, err := strconv.Atoi(a[1:])
			if err != nil || n <= 0 || n >= 64 {
				return 0, nil, fmt.Errorf("invalid signal %q", a)
			}
			sig = syscall.Signal(n)
			sawSig = true
		default:
			return 0, nil, fmt.Errorf("unexpected argument %q", a)
		}
	}
	if !sawSig {
		return 0, nil, fmt.Errorf("no signal given")
	}
	return sig, omit, nil
}

// procInfo is the little slice of /proc/<pid>/stat the decision needs.
type procInfo struct {
	pid    int
	sid    int
	kernel bool
	zombie bool
}

// readProcs collects every process under root.
//
// A pid whose stat cannot be read or parsed is dropped rather than
// guessed at: it has almost certainly exited between the readdir and the
// open, and signalling a pid that has been recycled in between would hit
// an unrelated process.
func readProcs(root string) ([]procInfo, error) {
	d, err := os.Open(root)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", root, err)
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", root, err)
	}

	var out []procInfo
	for _, name := range names {
		pid, err := strconv.Atoi(name)
		if err != nil || pid <= 0 {
			continue
		}
		b, err := os.ReadFile(root + "/" + name + "/stat")
		if err != nil {
			continue
		}
		p, ok := parseStat(pid, string(b))
		if !ok {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// parseStat pulls state, session, startcode and endcode out of a
// /proc/<pid>/stat line.
//
// Everything is indexed from the LAST ')' because field 2 is the
// executable name, in parentheses, and a process may legally be called
// ") (" — anything that counts fields from the left is wrong for it.
// Positions in the real file are state 3, session 6, startcode 26 and
// endcode 27; after the parenthesis they land at 0, 3, 23 and 24.
//
// A kernel thread has no user address space, so startcode and endcode are
// both zero — which is how upstream recognises one, and cheaper than
// looking for an empty cmdline.
func parseStat(pid int, stat string) (procInfo, bool) {
	close := strings.LastIndexByte(stat, ')')
	if close < 0 || close+2 > len(stat) {
		return procInfo{}, false
	}
	f := strings.Fields(stat[close+1:])
	const (
		idxState     = 0
		idxSession   = 3
		idxStartcode = 23
		idxEndcode   = 24
	)
	if len(f) <= idxEndcode {
		return procInfo{}, false
	}
	sid, err := strconv.Atoi(f[idxSession])
	if err != nil {
		return procInfo{}, false
	}
	startcode, err1 := strconv.ParseUint(f[idxStartcode], 10, 64)
	endcode, err2 := strconv.ParseUint(f[idxEndcode], 10, 64)
	if err1 != nil || err2 != nil {
		return procInfo{}, false
	}
	return procInfo{
		pid:    pid,
		sid:    sid,
		kernel: startcode == 0 && endcode == 0,
		zombie: f[idxState] == "Z",
	}, true
}

// skip is the whole selection rule, in one place so a test can state it.
func skip(p procInfo, self, sid int, omit map[int]bool) bool {
	switch {
	case p.pid == 1:
		// Killing init is the one thing a kill-everything tool must not
		// do; on Linux the kernel would refuse anyway, but counting the
		// attempt as a kill would corrupt the exit status.
		return true
	case p.pid == self:
		return true
	case sid > 0 && p.sid == sid:
		// Our own session: the shell that called us, and anything else it
		// started. Sweeping it would end the script mid-shutdown.
		return true
	case p.kernel:
		return true
	case p.zombie:
		// Already dead and waiting to be reaped. Signalling one always
		// "succeeds" and would make the exit status claim a kill that
		// did not happen.
		return true
	case omit[p.pid]:
		return true
	}
	return false
}

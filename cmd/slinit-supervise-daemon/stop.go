package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// stopGrace is how long --stop allows the supervisor beyond the stop
// schedule's own budget before it treats it as hung.
const stopGrace = 2 * time.Second

// cmdStop reads the supervisor pidfile and asks it to shut down.
// SIGTERM is enough — the supervisor's signal handler stops the daemon
// with the stop schedule and cleans the pidfiles. A --retry given here
// replaces the one --start gave: it is handed over in PIDFILE.retry.
func cmdStop(opts Options) int {
	if opts.PidFile == "" {
		fmt.Fprintln(os.Stderr, "--pidfile is required for --stop")
		return exitBadUsage
	}
	pid, err := readTrustedPIDFile(opts.PidFile)
	if errors.Is(err, errInsecurePIDFile) {
		fmt.Fprintf(os.Stderr, "refusing to stop: %v\n", err)
		return exitInsufficientPri
	}
	if err != nil {
		// No pidfile: nothing to do. OpenRC treats this as success under
		// the "if the pidfile is missing, service is stopped" convention.
		return exitOK
	}
	// A live pid that is not a supervise-daemon is a reused pid, or a
	// forged one: either way not ours to SIGTERM, and SIGKILL below.
	super, err := isSupervisor(pid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot verify supervisor pid=%d: %v\n", pid, err)
		return exitInsufficientPri
	}
	if !processAlive(pid) || !super {
		// Stale pidfile: report distinctly, matching Debian LSB code 5.
		removePidfiles(opts)
		return exitStalePidfile
	}

	// The daemon, identified while the supervisor is still its parent:
	// once the supervisor is gone its pid is nobody's to vouch for.
	daemon, haveDaemon := supervisedDaemon(opts, pid)

	// The schedule the supervisor will run, and so how long to wait.
	spec := ""
	if opts.Retry != "" {
		if _, err := ParseRetry(opts.Retry, syscall.SIGTERM); err != nil {
			fmt.Fprintf(os.Stderr, "--retry: %v\n", err)
			return exitBadUsage
		}
		spec = opts.Retry
		if err := writeFileNoFollow(retryFile(opts), spec+"\n"); err != nil {
			fmt.Fprintf(os.Stderr, "cannot hand --retry to the supervisor: %v\n", err)
			return exitInsufficientPri
		}
	} else {
		spec = stopSchedule(opts)
	}
	steps, _ := ParseRetry(spec, syscall.SIGTERM)
	budget, unbounded := scheduleBudget(steps)
	budget += stopGrace
	if opts.Retry == "" && budget < 30*time.Second {
		// A supervisor from before the retry file existed runs a
		// schedule we cannot see; keep the old 30s wait for it.
		budget = 30 * time.Second
	}

	// SIGTERM the supervisor; its handler will do the rest.
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		fmt.Fprintf(os.Stderr, "kill supervisor pid=%d: %v\n", pid, err)
		return exitInsufficientPri
	}

	exited := false
	if unbounded {
		for processAlive(pid) {
			time.Sleep(50 * time.Millisecond)
		}
		exited = true
	} else {
		exited = waitExit(pid, budget)
	}

	if !exited {
		// The supervisor outlived its own schedule: hung. Killing it
		// alone used to orphan the daemon and report success; take the
		// daemon down with it and drop the files that name them.
		fmt.Fprintf(os.Stderr, "supervisor pid=%d did not stop within %s; killing it\n", pid, budget)
		_ = syscall.Kill(pid, syscall.SIGKILL)
		if haveDaemon && daemon.alive() {
			_ = syscall.Kill(daemon.pid, syscall.SIGKILL)
		}
		removePidfiles(opts)
		if !waitExit(pid, 2*time.Second) {
			fmt.Fprintf(os.Stderr, "supervisor pid=%d refused to die\n", pid)
			return exitInsufficientPri
		}
	}

	// The supervisor is gone; the daemon must be too.
	if haveDaemon {
		deadline := time.Now().Add(2 * time.Second)
		for daemon.alive() && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if daemon.alive() {
			fmt.Fprintf(os.Stderr, "daemon pid=%d is still running after the stop schedule (%s)\n",
				daemon.pid, spec)
			return exitInsufficientPri
		}
	}
	return exitOK
}

// procIdentity is a process as /proc/PID/stat names it: its pid plus
// the start time that tells it apart from a later process given the
// same pid.
type procIdentity struct {
	pid   int
	start uint64
}

// procStat returns pid's parent, start time and state from
// /proc/PID/stat.
func procStat(pid int) (ppid int, start uint64, state byte, err error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, 0, err
	}
	// comm may hold spaces and parentheses: fields start after the last ')'.
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return 0, 0, 0, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	f := strings.Fields(string(b[i+1:]))
	// f[0] is field 3 (state), f[1] field 4 (ppid), f[19] field 22 (starttime).
	if len(f) < 20 {
		return 0, 0, 0, fmt.Errorf("short /proc/%d/stat", pid)
	}
	ppid, err = strconv.Atoi(f[1])
	if err != nil {
		return 0, 0, 0, err
	}
	start, err = strconv.ParseUint(f[19], 10, 64)
	if err != nil {
		return 0, 0, 0, err
	}
	return ppid, start, f[0][0], nil
}

// alive reports whether the identified process still runs: same pid,
// same start time, not a zombie.
func (p procIdentity) alive() bool {
	_, start, state, err := procStat(p.pid)
	return err == nil && start == p.start && state != 'Z' && state != 'X'
}

// supervisedDaemon reads PIDFILE.daemon and returns the daemon it names
// only if that process is a child of the supervisor super: a pid that
// was reused, or written by someone else, is not the daemon.
func supervisedDaemon(opts Options, super int) (procIdentity, bool) {
	pid, err := readTrustedPIDFile(opts.PidFile + ".daemon")
	if err != nil {
		return procIdentity{}, false
	}
	ppid, start, state, err := procStat(pid)
	if err != nil || ppid != super || state == 'Z' || state == 'X' {
		return procIdentity{}, false
	}
	return procIdentity{pid: pid, start: start}, true
}

// cmdSignal reads the daemon pidfile (not the supervisor's) and
// delivers the configured signal directly to the daemon. This lets
// scripts do SIGHUP-reload style operations without going through the
// supervisor.
//
// Caveat documented in the man page: SIGTERM/SIGKILL sent this way
// will just be treated as a crash by the supervisor and trigger a
// respawn.
func cmdSignal(opts Options) int {
	if opts.PidFile == "" {
		fmt.Fprintln(os.Stderr, "--pidfile is required for --signal")
		return exitBadUsage
	}
	daemonPid, err := readTrustedPIDFile(opts.PidFile + ".daemon")
	if errors.Is(err, errInsecurePIDFile) {
		fmt.Fprintf(os.Stderr, "refusing to signal: %v\n", err)
		return exitInsufficientPri
	}
	if err != nil {
		// Fall back to supervisor pidfile: if there's no .daemon
		// file the daemon hasn't launched yet or the supervisor is
		// external; not much we can do.
		fmt.Fprintf(os.Stderr, "no daemon pidfile at %q.daemon\n", opts.PidFile)
		return exitAlready
	}
	if !processAlive(daemonPid) {
		return exitStalePidfile
	}
	if err := syscall.Kill(daemonPid, opts.Signal); err != nil {
		fmt.Fprintf(os.Stderr, "kill daemon pid=%d: %v\n", daemonPid, err)
		return exitInsufficientPri
	}
	return exitOK
}

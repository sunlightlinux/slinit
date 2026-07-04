package main

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// cmdStop reads the supervisor pidfile and asks it to shut down.
// SIGTERM is enough — the supervisor's signal handler tears the daemon
// down and cleans both pidfiles.
func cmdStop(opts Options) int {
	if opts.PidFile == "" {
		fmt.Fprintln(os.Stderr, "--pidfile is required for --stop")
		return exitBadUsage
	}
	pid, ok := readPIDFile(opts.PidFile)
	if !ok {
		// No pidfile: nothing to do. OpenRC treats this as success under
		// the "if the pidfile is missing, service is stopped" convention.
		return exitOK
	}
	if !processAlive(pid) {
		// Stale pidfile: report distinctly, matching Debian LSB code 5.
		_ = os.Remove(opts.PidFile)
		_ = os.Remove(opts.PidFile + ".daemon")
		return exitStalePidfile
	}

	// SIGTERM the supervisor; its handler will do the rest.
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		fmt.Fprintf(os.Stderr, "kill supervisor pid=%d: %v\n", pid, err)
		return exitInsufficientPri
	}

	// Wait for the supervisor to exit. Bound by --retry if given so
	// scripts can enforce a hard timeout; default is a generous 30s.
	timeout := 30 * time.Second
	if opts.Retry != "" {
		steps, err := ParseRetry(opts.Retry, syscall.SIGTERM)
		if err == nil {
			// Sum the timeouts — the last step's Timeout=0 means
			// "forever" so we bail out cleanly with a big number.
			total := time.Duration(0)
			for _, s := range steps {
				if s.Timeout == 0 {
					total = 5 * time.Minute
					break
				}
				total += s.Timeout
			}
			if total > 0 {
				timeout = total
			}
		}
	}
	if waitExit(pid, timeout) {
		return exitOK
	}
	// Escalate to SIGKILL of the supervisor if it hung — that leaves
	// the daemon parented to init (the subreaper) which will reap it
	// eventually.
	_ = syscall.Kill(pid, syscall.SIGKILL)
	if waitExit(pid, 2*time.Second) {
		return exitOK
	}
	fmt.Fprintf(os.Stderr, "supervisor pid=%d refused to die\n", pid)
	return exitInsufficientPri
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
	daemonPid, ok := readPIDFile(opts.PidFile + ".daemon")
	if !ok {
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

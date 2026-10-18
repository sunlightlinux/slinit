package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// cmdStart is the top-level --start path. It re-execs the current
// binary with runnerEnvVar set so the child instance takes the
// runSupervisor() branch, then polls the pidfile until the supervisor
// has written it. This mirrors OpenRC's double-fork daemon layout in
// spirit — we get a detached supervisor and a synchronous signal
// (pidfile appearing) that the child is ready.
func cmdStart(opts Options) int {
	if opts.Exec == "" {
		fmt.Fprintln(os.Stderr, "--exec is required for --start")
		return exitBadUsage
	}
	if opts.PidFile == "" {
		fmt.Fprintln(os.Stderr, "--pidfile is required for --start")
		return exitBadUsage
	}

	// Already-running check: if the supervisor's pidfile is fresh and
	// points at a live PID, refuse rather than start a second copy.
	if pid, ok := readPIDFile(opts.PidFile); ok && processAlive(pid) {
		if opts.Verbose {
			fmt.Fprintf(os.Stderr, "already running (supervisor pid %d)\n", pid)
		}
		return exitAlready
	}

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot resolve own path: %v\n", err)
		return exitInsufficientPri
	}

	// Wipe any stale pidfile so the poll loop below sees only our
	// supervisor's write. Ignore ENOENT.
	_ = os.Remove(opts.PidFile)
	_ = os.Remove(opts.PidFile + ".daemon")

	cmd := exec.Command(self, os.Args[1:]...)
	cmd.Env = append(os.Environ(), runnerEnvVar+"=1")
	// Detach: setsid puts the supervisor in its own session, /dev/null
	// stdio so the caller's terminal isn't tied up.
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open /dev/null: %v\n", err)
		return exitInsufficientPri
	}
	defer devnull.Close()
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = devnull
	// Under --verbose, keep the supervisor's stderr wired into a
	// scratch file so a broken re-exec surfaces something an operator
	// can inspect. Silent by default because the release path is
	// meant to be quiet.
	if opts.Verbose {
		// /run/slinit, not /tmp, and O_NOFOLLOW.
		//
		// This used to be the fixed path /tmp/slinit-supervise-daemon.err
		// opened O_CREATE|O_APPEND. /tmp is world-writable and the name
		// was predictable, so any local user could create it first as a
		// symlink and have this process — running as root — append the
		// supervisor's stderr to whatever it named. Append rather than
		// truncate, so the shape is content injection into a root-owned
		// file rather than destruction of one, which is worse in a
		// sudoers.d or a shell rc than it sounds. A plain file planted
		// there instead leaks the supervisor's stderr to its owner.
		// /tmp's sticky bit does not help: it stops you deleting someone
		// else's entry, not creating one that does not exist yet.
		//
		// /run/slinit is root-owned, which is what actually fixes it;
		// O_NOFOLLOW only states the intent for the final component.
		// Failure stays silent, as before — this is a debugging
		// convenience and not a reason to refuse to start a service.
		_ = os.MkdirAll("/run/slinit", 0755)
		if errLog, err := os.OpenFile("/run/slinit/supervise-daemon.err",
			os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, 0644); err == nil {
			cmd.Stderr = errLog
			defer errLog.Close()
		}
	}
	setDetached(cmd)

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "fork supervisor: %v\n", err)
		return exitInsufficientPri
	}

	// Reap the detached grandparent → child fork so it does not become
	// a zombie under our (top-level) process. Run in a goroutine
	// because we return before the supervisor exits — that's the whole
	// point of daemonising.
	// The wait result is kept rather than discarded, because it is the
	// one fact that distinguishes the two ways this can go wrong: a
	// supervisor that is slow to write its pidfile, and one that died
	// before writing it at all. Without it, both look identical — "did
	// not write the pidfile" after a thirty-second wait — which is what
	// a CI failure of 88-slinit-supervise-daemon reduced to, leaving
	// nothing to tell slowness from a crash.
	supervisorDone := make(chan error, 1)
	go func() { supervisorDone <- cmd.Wait() }()

	// Poll for the supervisor's pidfile. Once present, we know it has
	// started the daemon; return so the init.d start() function
	// completes.
	deadline := time.Now().Add(pidfileReadyTimeout)
	for time.Now().Before(deadline) {
		if pid, ok := readPIDFile(opts.PidFile); ok && processAlive(pid) {
			if opts.Verbose {
				fmt.Fprintf(os.Stderr, "supervisor started (pid %d)\n", pid)
			}
			return exitOK
		}

		// A supervisor that has already exited is never going to write
		// the pidfile, so waiting out the rest of the timeout only
		// delays the verdict — thirty seconds of an init.d start() for
		// an answer that is already known. It also gives up with the
		// reason instead of the symptom: the supervisor logs why it
		// stopped (a daemon crashing past its respawn limit, say), and
		// pointing at that is more use than reporting a missing file.
		select {
		case werr := <-supervisorDone:
			fmt.Fprintf(os.Stderr,
				"supervisor exited before writing %q (%v); "+
					"its own output says why\n",
				opts.PidFile, waitDescription(werr))
			return exitInsufficientPri
		default:
		}

		time.Sleep(50 * time.Millisecond)
	}

	// Timed out with the supervisor still running: genuinely slow or
	// stuck, which is a different problem from having died, and the
	// message now says which of the two happened.
	fmt.Fprintf(os.Stderr,
		"supervisor did not write %q within %s and is still running "+
			"(pid %d); giving up\n",
		opts.PidFile, pidfileReadyTimeout, cmd.Process.Pid)
	return exitInsufficientPri
}

// waitDescription renders a wait result for an operator: "exit status N"
// or "no error", rather than a bare <nil>.
func waitDescription(err error) string {
	if err == nil {
		return "exited successfully"
	}
	return err.Error()
}

// readPIDFile parses PATH's integer contents. ok=false on missing or
// malformed files, same shape as start-stop-daemon uses.
func readPIDFile(path string) (int, bool) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	// Strip whitespace / trailing newlines.
	trimmed := trimSpace(string(buf))
	if trimmed == "" {
		return 0, false
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func trimSpace(s string) string {
	start := 0
	end := len(s)
	for start < end {
		c := s[start]
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			break
		}
		start++
	}
	for end > start {
		c := s[end-1]
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			break
		}
		end--
	}
	return s[start:end]
}

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
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
		if errLog, err := os.OpenFile("/tmp/slinit-supervise-daemon.err",
			os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644); err == nil {
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
	go func() { _ = cmd.Wait() }()

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
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Fprintf(os.Stderr,
		"supervisor did not write %q within %s; giving up\n",
		opts.PidFile, pidfileReadyTimeout)
	return exitInsufficientPri
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

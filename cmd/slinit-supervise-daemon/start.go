package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
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

// readPIDFile parses PATH's integer contents. ok=false on missing,
// malformed or untrusted files; readTrustedPIDFile says which.
func readPIDFile(path string) (int, bool) {
	pid, err := readTrustedPIDFile(path)
	return pid, err == nil
}

// errBadPIDFile is a pidfile that is there but names no pid we may
// signal: empty, not a number, or init, this process or its parent.
var errBadPIDFile = errors.New("malformed pidfile")

// readTrustedPIDFile reads a pidfile whose pid --stop or --signal is
// about to signal, checked as slinit-start-stop-daemon checks one.
//
// Both files — PATH naming the supervisor, PATH.daemon naming the
// daemon — are written by the supervisor, as the user --stop runs as,
// and never by the daemon. So each is held to the strict check: a
// world-writable file, or one owned (or group-writable) by anyone but
// root or us, was written by someone who may not pick what root
// signals. A service user who can write the pidfile directory could
// otherwise have --stop SIGTERM/SIGKILL, and --signal send anything
// to, any pid on the box. O_NOFOLLOW for the same reason: the
// supervisor never writes these through a symlink, so a link is
// planted, and Fstat would check the file it names.
//
// A file naming PID 1, this process or its parent is errBadPIDFile:
// SIGTERM to PID 1 is a reboot under slinit.
func readTrustedPIDFile(path string) (int, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return 0, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return 0, fmt.Errorf("pidfile %s is not a regular file: %w", path, errInsecurePIDFile)
	}
	if why := process.PIDFileInsecure(&st, os.Getuid(), os.Getgid(), true); why != "" {
		return 0, fmt.Errorf("%s pidfile %s: %w", why, path, errInsecurePIDFile)
	}
	buf, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return 0, err
	}
	// Strip whitespace / trailing newlines.
	trimmed := trimSpace(string(buf))
	if trimmed == "" {
		return 0, errBadPIDFile
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil || n <= 1 || n == os.Getpid() || n == os.Getppid() {
		return 0, errBadPIDFile
	}
	return n, nil
}

// errInsecurePIDFile is a pidfile someone other than root or us could
// have written; see readTrustedPIDFile.
var errInsecurePIDFile = errors.New("is insecure")

// isSupervisor reports whether pid runs this same executable — a
// supervise-daemon, rather than whatever a reused or forged pid names.
// A binary replaced by a package upgrade reads "<path> (deleted)" in
// /proc/PID/exe, so the suffix is stripped on both sides. A pid that
// is gone reads as false; one we may not inspect is an error, since
// we could not signal it either.
func isSupervisor(pid int) (bool, error) {
	self, err := os.Readlink("/proc/self/exe")
	if err != nil {
		return false, err
	}
	exe, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.TrimSuffix(exe, " (deleted)") == strings.TrimSuffix(self, " (deleted)"), nil
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

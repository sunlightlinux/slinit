package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func cmdStart(opts Options) int {
	crit, err := matchCriteriaFrom(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitBadUsage
	}

	// Guard: don't start if a matching process already runs.
	pids, err := FindMatchingPIDs(crit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scanning /proc: %v\n", err)
		return exitInsufficientPri
	}
	if len(pids) > 0 {
		writeMsg(opts, "already running (pid %v)", pids)
		if opts.OKnodo {
			return exitOK
		}
		return exitAlready
	}

	binary, argv, err := resolveExec(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitBadUsage
	}

	if opts.Test {
		writeMsg(opts, "would start %s %v", binary, argv[1:])
		return exitOK
	}

	pid, err := spawn(binary, argv, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "spawn: %v\n", err)
		return exitInsufficientPri
	}

	if opts.MakePidfile && opts.PidFile != "" {
		if err := writePIDFile(opts.PidFile, pid); err != nil {
			fmt.Fprintf(os.Stderr, "write pidfile %q: %v\n", opts.PidFile, err)
			// non-fatal for compat with Debian
		}
	}

	if opts.Wait > 0 {
		waitWithProgress(time.Duration(opts.Wait)*time.Millisecond, opts)
	}
	if opts.Notify != "" {
		if code := applyNotify(opts, pid); code != exitOK {
			return code
		}
	}
	if opts.Verbose {
		writeMsg(opts, "started pid=%d", pid)
	}
	return exitOK
}

// applyNotify implements the subset of --notify readiness modes that
// makes sense for a one-shot start command:
//   - readiness=none: don't wait, treat exec success as ready.
//   - readiness=pidfile: poll for --pidfile to appear (bounded by
//     --wait if given, otherwise a 30s default).
//
// The richer OpenRC modes (fd, stderr, signal) are rejected loudly.
func applyNotify(opts Options, pid int) int {
	spec := strings.TrimSpace(opts.Notify)
	kv := strings.SplitN(spec, "=", 2)
	if len(kv) != 2 || strings.TrimSpace(kv[0]) != "readiness" {
		fmt.Fprintf(os.Stderr, "--notify: only 'readiness=' form is supported (got %q)\n", spec)
		return exitUnsupported
	}
	mode := strings.TrimSpace(kv[1])
	switch mode {
	case "none":
		return exitOK
	case "pidfile":
		if opts.PidFile == "" {
			fmt.Fprintln(os.Stderr, "--notify readiness=pidfile requires --pidfile")
			return exitBadUsage
		}
		timeout := 30 * time.Second
		if opts.Wait > 0 {
			timeout = time.Duration(opts.Wait) * time.Millisecond
		}
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(opts.PidFile); err == nil {
				return exitOK
			}
			if !processAlive(pid) {
				fmt.Fprintf(os.Stderr, "--notify pidfile: child exited before writing %q\n", opts.PidFile)
				return exitInsufficientPri
			}
			time.Sleep(100 * time.Millisecond)
		}
		fmt.Fprintf(os.Stderr, "--notify pidfile: %q did not appear within %s\n", opts.PidFile, timeout)
		return exitInsufficientPri
	default:
		fmt.Fprintf(os.Stderr, "--notify readiness=%s is not supported\n", mode)
		return exitUnsupported
	}
}

// waitWithProgress sleeps and, if opts.Progress is set, prints a dot to
// stderr every second. Matches OpenRC's --progress behaviour.
func waitWithProgress(d time.Duration, opts Options) {
	if !opts.Progress || d < time.Second {
		time.Sleep(d)
		return
	}
	deadline := time.Now().Add(d)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for time.Now().Before(deadline) {
		select {
		case <-tick.C:
			fmt.Fprint(os.Stderr, ".")
		default:
			remaining := time.Until(deadline)
			if remaining <= 0 {
				fmt.Fprintln(os.Stderr)
				return
			}
			// Sub-second remaining tail: sleep it out.
			if remaining < time.Second {
				time.Sleep(remaining)
				fmt.Fprintln(os.Stderr)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	fmt.Fprintln(os.Stderr)
}

// spawn is the fork/exec plumbing. In --background mode we return the
// child's PID and detach; otherwise we wait for it and propagate its
// exit status.
func spawn(binary string, argv []string, opts Options) (int, error) {
	// Runner-wrap for hardening flags. When --capabilities/--secbits/
	// --no-new-privs are set we prepend slinit-runner so the syscalls
	// happen child-side before exec (they cannot be applied to a peer
	// task from the parent).
	if runnerBin, runnerArgv, wrapped, err := runnerWrapArgs(opts, binary, argv); err != nil {
		return 0, err
	} else if wrapped {
		binary = runnerBin
		argv = runnerArgv
	}

	cmd := exec.Command(binary, argv[1:]...)
	cmd.Args = argv // preserves --startas ARG0 override

	if opts.ChDir != "" {
		cmd.Dir = opts.ChDir
	}

	// Environment: start from ours, overlay --env additions.
	if len(opts.Env) > 0 {
		env := append(os.Environ(), opts.Env...)
		cmd.Env = env
	}

	sysattr := &syscall.SysProcAttr{}
	if opts.Chroot != "" {
		sysattr.Chroot = opts.Chroot
	}
	if opts.ChUID != "" || opts.MatchUser != "" || opts.Group != "" {
		uid, gid, err := resolveCredentials(opts)
		if err != nil {
			return 0, err
		}
		sysattr.Credential = &syscall.Credential{
			Uid: uint32(uid),
			Gid: uint32(gid),
		}
	}
	if opts.Background {
		sysattr.Setsid = true
	}
	cmd.SysProcAttr = sysattr

	if err := setupStdio(cmd, opts); err != nil {
		return 0, err
	}
	if opts.Umask != nil {
		old := syscall.Umask(int(*opts.Umask))
		defer syscall.Umask(old)
	}

	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid

	// Parent-applicable attrs go on after fork so the child inherits them.
	if opts.Nice != nil {
		if err := syscall.Setpriority(syscall.PRIO_PROCESS, pid, *opts.Nice); err != nil && opts.Verbose {
			fmt.Fprintf(os.Stderr, "warning: nice(%d): %v\n", *opts.Nice, err)
		}
	}
	if opts.OOMScoreAdj != nil {
		if err := setOOMScoreAdj(pid, *opts.OOMScoreAdj); err != nil && opts.Verbose {
			fmt.Fprintf(os.Stderr, "warning: oom_score_adj(%d): %v\n", *opts.OOMScoreAdj, err)
		}
	}
	if opts.IOClass != 0 {
		if err := setIOPrio(pid, opts.IOClass, opts.IOLevel); err != nil && opts.Verbose {
			fmt.Fprintf(os.Stderr, "warning: ioprio_set: %v\n", err)
		}
	}
	if opts.Scheduler != "" {
		policy, err := parseSchedPolicy(opts.Scheduler)
		if err != nil {
			return 0, err
		}
		if err := applySched(pid, policy, uint32(opts.SchedulerPriority)); err != nil && opts.Verbose {
			fmt.Fprintf(os.Stderr, "warning: sched_setattr(%s,%d): %v\n",
				opts.Scheduler, opts.SchedulerPriority, err)
		}
	}

	if opts.Background {
		// Reap so we don't leave a zombie, but detach — parent returns
		// immediately.
		go func() {
			_ = cmd.Wait()
		}()
		return pid, nil
	}

	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// Propagate exit code from foreground child.
			if code := exitErr.ExitCode(); code >= 0 {
				os.Exit(code)
			}
		}
		return pid, err
	}
	return pid, nil
}

func setupStdio(cmd *exec.Cmd, opts Options) error {
	if opts.Stdin != "" {
		f, err := os.Open(opts.Stdin)
		if err != nil {
			return fmt.Errorf("open --stdin: %w", err)
		}
		cmd.Stdin = f
	}
	// --stdout-logger takes precedence over --stdout when both are set
	// (matches OpenRC behaviour where logger overrides file redirection).
	if opts.StdoutLogger != "" {
		w, err := startLogger(opts.StdoutLogger)
		if err != nil {
			return fmt.Errorf("--stdout-logger: %w", err)
		}
		cmd.Stdout = w
	} else if opts.Stdout != "" {
		f, err := os.OpenFile(opts.Stdout, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("open --stdout: %w", err)
		}
		cmd.Stdout = f
	} else if opts.Background {
		cmd.Stdout, _ = os.Open(os.DevNull)
	} else {
		cmd.Stdout = os.Stdout
	}
	if opts.StderrLogger != "" {
		w, err := startLogger(opts.StderrLogger)
		if err != nil {
			return fmt.Errorf("--stderr-logger: %w", err)
		}
		cmd.Stderr = w
	} else if opts.Stderr != "" {
		f, err := os.OpenFile(opts.Stderr, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("open --stderr: %w", err)
		}
		cmd.Stderr = f
	} else if opts.Background {
		cmd.Stderr, _ = os.Open(os.DevNull)
	} else {
		cmd.Stderr = os.Stderr
	}
	return nil
}

// resolveCredentials handles the "who runs the child" question, honouring
// --chuid (user[:group]), --user (as chuid fallback for --start), and
// --group. --chuid takes precedence when both are set.
func resolveCredentials(opts Options) (int, int, error) {
	spec := opts.ChUID
	if spec == "" {
		spec = opts.MatchUser
	}
	var userSpec, groupSpec string
	if spec != "" {
		if idx := indexColon(spec); idx >= 0 {
			userSpec = spec[:idx]
			groupSpec = spec[idx+1:]
		} else {
			userSpec = spec
		}
	}
	if opts.Group != "" {
		groupSpec = opts.Group
	}

	uid := os.Getuid()
	gid := os.Getgid()
	if userSpec != "" {
		u, err := lookupUID(userSpec)
		if err != nil {
			return 0, 0, err
		}
		uid = u
		// If no explicit group, take user's primary GID.
		if groupSpec == "" {
			if pw, err := lookupPrimaryGID(userSpec); err == nil {
				gid = pw
			}
		}
	}
	if groupSpec != "" {
		g, err := lookupGID(groupSpec)
		if err != nil {
			return 0, 0, err
		}
		gid = g
	}
	return uid, gid, nil
}

func indexColon(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}

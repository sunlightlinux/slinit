package main

import (
	"fmt"
	"os"
	"os/exec"
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

	// Pre-flight notify validation. The pipe/signal modes need the
	// parent to survive past exec to observe readiness, so --background
	// is required (otherwise cmd.Wait() blocks and we never reach the
	// wait step).
	proto, perr := parseNotify(opts.Notify)
	if perr != nil {
		fmt.Fprintf(os.Stderr, "--notify: %v\n", perr)
		return exitBadUsage
	}
	if !opts.Background {
		switch proto.mode {
		case "fd", "stderr", "signal":
			fmt.Fprintf(os.Stderr, "--notify readiness=%s requires --background\n", proto.mode)
			return exitBadUsage
		}
	}

	if opts.Test {
		writeMsg(opts, "would start %s %v", binary, argv[1:])
		return exitOK
	}

	pid, state, err := spawn(binary, argv, opts)
	if err != nil {
		if state != nil {
			state.closeAll()
		}
		fmt.Fprintf(os.Stderr, "spawn: %v\n", err)
		return exitInsufficientPri
	}

	if opts.MakePidfile && opts.PidFile != "" {
		if err := writePIDFile(opts.PidFile, pid); err != nil {
			fmt.Fprintf(os.Stderr, "write pidfile %q: %v\n", opts.PidFile, err)
			// non-fatal for compat with Debian
		}
	}

	// --wait is a pre-sleep only when no real readiness protocol is
	// configured; the protocol's own timeout otherwise supersedes it.
	if opts.Notify == "" && opts.Wait > 0 {
		waitWithProgress(time.Duration(opts.Wait)*time.Millisecond, opts)
	}
	if state != nil && proto.mode != "none" {
		if code := state.wait(opts, pid); code != exitOK {
			return code
		}
	}
	if opts.Verbose {
		writeMsg(opts, "started pid=%d", pid)
	}
	return exitOK
}

// applyNotify is the legacy state-less entry point retained for tests
// that exercise pidfile/none/manual directly. The pre-fork modes (fd,
// stderr, signal) route through spawn() → notifyState instead.
func applyNotify(opts Options, pid int) int {
	proto, err := parseNotify(opts.Notify)
	if err != nil {
		fmt.Fprintf(os.Stderr, "--notify: %v\n", err)
		return exitUnsupported
	}
	switch proto.mode {
	case "none", "manual":
		return exitOK
	case "pidfile":
		return waitPidfile(opts, pid)
	case "fd", "stderr", "signal":
		// These require pre-fork wiring performed by spawn().
		fmt.Fprintf(os.Stderr,
			"--notify readiness=%s must be applied via spawn's pre-fork state\n",
			proto.mode)
		return exitUnsupported
	default:
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
// exit status. The returned notifyState carries pre-fork resources
// (pipe read side, signal channel) the caller uses to observe
// readiness; it is nil-safe.
func spawn(binary string, argv []string, opts Options) (int, *notifyState, error) {
	// Runner-wrap for hardening flags. When --capabilities/--secbits/
	// --no-new-privs are set we prepend slinit-runner so the syscalls
	// happen child-side before exec (they cannot be applied to a peer
	// task from the parent).
	if runnerBin, runnerArgv, wrapped, err := runnerWrapArgs(opts, binary, argv); err != nil {
		return 0, nil, err
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
			return 0, nil, err
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
		return 0, nil, err
	}
	// Notify wiring: must happen AFTER setupStdio (so readiness=stderr
	// can override cmd.Stderr) and BEFORE cmd.Start() (pipes must land
	// in the child's fd table on exec).
	state, err := prepareNotify(cmd, opts)
	if err != nil {
		return 0, nil, err
	}
	if opts.Umask != nil {
		old := syscall.Umask(int(*opts.Umask))
		defer syscall.Umask(old)
	}

	if err := cmd.Start(); err != nil {
		if state != nil {
			state.closeAll()
		}
		return 0, nil, err
	}
	pid := cmd.Process.Pid
	// Release parent's copy of child-side pipe fds so EOFs are visible
	// once the child (or its wrapper) closes its own copy.
	state.postFork()

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
			return 0, state, err
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
		return pid, state, nil
	}

	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// Propagate exit code from foreground child.
			if code := exitErr.ExitCode(); code >= 0 {
				os.Exit(code)
			}
		}
		return pid, state, err
	}
	return pid, state, nil
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

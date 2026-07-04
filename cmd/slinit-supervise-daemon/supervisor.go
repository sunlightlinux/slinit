package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// runSupervisor is the entry into the detached branch. It owns the
// supervisor pidfile, spawns the daemon, and loops on respawn until
// the rate limiter (RespawnMax within RespawnPeriod) says stop, or
// SIGTERM lands.
func runSupervisor(opts Options) int {
	// Write our supervisor pidfile so cmdStart's poll loop unblocks.
	if err := writePIDFile(opts.PidFile, os.Getpid()); err != nil {
		superLogf(opts, "write pidfile %q: %v", opts.PidFile, err)
		return exitInsufficientPri
	}
	// Both files are removed on clean exit so stale content does not
	// linger for the next start.
	defer func() {
		_ = os.Remove(opts.PidFile)
		_ = os.Remove(opts.PidFile + ".daemon")
	}()

	// Signal channel: SIGTERM / SIGINT tell the supervisor to shut the
	// daemon down and exit. SIGHUP is forwarded to the daemon (config
	// reload convention).
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigs)

	// Respawn rate limiter. Zero max = unlimited.
	limiter := newRespawnLimiter(opts.RespawnMax, opts.RespawnPeriod)

	respawn := 0
	for {
		daemonPID, waitCh, err := startDaemon(opts)
		if err != nil {
			superLogf(opts, "spawn daemon: %v", err)
			return exitInsufficientPri
		}
		// Record daemon PID so --signal can find it.
		if err := writePIDFile(opts.PidFile+".daemon", daemonPID); err != nil {
			superLogf(opts, "write daemon pidfile: %v", err)
		}

		// Wait for either the daemon to exit or a supervisor-directed
		// signal.
		select {
		case exitInfo := <-waitCh:
			if opts.Verbose {
				superLogf(opts, "daemon pid=%d exited: %s",
					daemonPID, exitInfo)
			}
			// Rate check: if we've crashed too much too fast, give up.
			if !limiter.allowRespawn(time.Now()) {
				superLogf(opts,
					"daemon crashed %d times within %s; giving up",
					limiter.count, opts.RespawnPeriod)
				return exitInsufficientPri
			}
			respawn++
			delay := backoffDelay(opts, respawn)
			if delay > 0 {
				select {
				case <-time.After(delay):
				case sig := <-sigs:
					// Shutdown request during backoff: propagate exit.
					superLogf(opts, "shutdown during backoff (%s)", sig)
					return exitOK
				}
			}
			// Loop → respawn.
		case sig := <-sigs:
			switch sig {
			case syscall.SIGTERM, syscall.SIGINT:
				// Clean shutdown: kill the daemon and exit.
				superLogf(opts, "shutdown signal %s → stopping daemon pid=%d",
					sig, daemonPID)
				if err := stopDaemon(opts, daemonPID); err != nil {
					superLogf(opts, "stop daemon: %v", err)
				}
				return exitOK
			case syscall.SIGHUP:
				// Forward to daemon. We do NOT respawn — the daemon
				// stays alive and re-reads its config on HUP.
				_ = syscall.Kill(daemonPID, syscall.SIGHUP)
				// Continue supervising the same daemon.
				<-waitCh
			}
		}
	}
}

// startDaemon fork/execs the child under the configured attrs and
// returns its PID plus a channel that signals daemon exit. The
// channel receives a short description string once so callers can log
// it verbatim.
func startDaemon(opts Options) (int, <-chan string, error) {
	binary := opts.Exec
	if !filepath.IsAbs(binary) {
		abs, err := exec.LookPath(binary)
		if err != nil {
			return 0, nil, fmt.Errorf("cannot resolve %q: %w", binary, err)
		}
		binary = abs
	}
	argv := append([]string{binary}, opts.Args...)

	// Runner-wrap for hardening flags — same shape as start-stop-daemon.
	if runnerBin, runnerArgv, wrapped, err := runnerWrapArgs(opts, binary, argv); err != nil {
		return 0, nil, err
	} else if wrapped {
		binary = runnerBin
		argv = runnerArgv
	}

	cmd := exec.Command(binary, argv[1:]...)
	cmd.Args = argv

	if opts.ChDir != "" {
		cmd.Dir = opts.ChDir
	}
	if len(opts.Env) > 0 {
		cmd.Env = append(os.Environ(), opts.Env...)
	}

	sys := &syscall.SysProcAttr{}
	if opts.Chroot != "" {
		sys.Chroot = opts.Chroot
	}
	if opts.User != "" || opts.Group != "" {
		uid, gid, err := resolveCredentials(opts)
		if err != nil {
			return 0, nil, err
		}
		sys.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
	}
	cmd.SysProcAttr = sys

	if err := setupStdio(cmd, opts); err != nil {
		return 0, nil, err
	}
	if opts.Umask != nil {
		old := syscall.Umask(int(*opts.Umask))
		defer syscall.Umask(old)
	}

	if err := cmd.Start(); err != nil {
		return 0, nil, err
	}
	pid := cmd.Process.Pid

	// Best-effort post-fork attrs (parent-applicable, mirroring
	// start-stop-daemon's ordering).
	if opts.Nice != nil {
		_ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, *opts.Nice)
	}
	if opts.OOMScoreAdj != nil {
		_ = setOOMScoreAdj(pid, *opts.OOMScoreAdj)
	}
	if opts.IOClass != 0 {
		_ = setIOPrio(pid, opts.IOClass, opts.IOLevel)
	}

	exitCh := make(chan string, 1)
	go func() {
		err := cmd.Wait()
		if err == nil {
			exitCh <- "exit status 0"
			return
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCh <- exitErr.String()
			return
		}
		exitCh <- err.Error()
	}()
	return pid, exitCh, nil
}

// stopDaemon shuts the daemon down using --retry when provided,
// otherwise SIGTERM/5/KILL/5. Waits for the process to actually exit.
func stopDaemon(opts Options, pid int) error {
	spec := opts.Retry
	if spec == "" {
		spec = "TERM/5/KILL/5"
	}
	steps, err := ParseRetry(spec, syscall.SIGTERM)
	if err != nil {
		return err
	}
	for _, step := range steps {
		if step.Signal != 0 {
			_ = syscall.Kill(pid, step.Signal)
		}
		if step.Timeout == 0 {
			// "wait forever" tail.
			for processAlive(pid) {
				time.Sleep(50 * time.Millisecond)
			}
			return nil
		}
		if waitExit(pid, step.Timeout) {
			return nil
		}
	}
	return fmt.Errorf("daemon pid=%d still alive after retry schedule", pid)
}

func setupStdio(cmd *exec.Cmd, opts Options) error {
	if opts.Stdin != "" {
		f, err := os.Open(opts.Stdin)
		if err != nil {
			return fmt.Errorf("open --stdin: %w", err)
		}
		cmd.Stdin = f
	} else {
		f, _ := os.Open(os.DevNull)
		cmd.Stdin = f
	}
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
	} else {
		f, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		cmd.Stdout = f
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
	} else {
		f, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		cmd.Stderr = f
	}
	return nil
}

func resolveCredentials(opts Options) (int, int, error) {
	uid := os.Getuid()
	gid := os.Getgid()
	if opts.User != "" {
		u, err := lookupUID(opts.User)
		if err != nil {
			return 0, 0, err
		}
		uid = u
		if opts.Group == "" {
			if g, err := lookupPrimaryGID(opts.User); err == nil {
				gid = g
			}
		}
	}
	if opts.Group != "" {
		g, err := lookupGID(opts.Group)
		if err != nil {
			return 0, 0, err
		}
		gid = g
	}
	return uid, gid, nil
}

func lookupUID(s string) (int, error) {
	if n, err := strconv.Atoi(s); err == nil {
		return n, nil
	}
	u, err := user.Lookup(s)
	if err != nil {
		return -1, fmt.Errorf("user %q: %w", s, err)
	}
	return strconv.Atoi(u.Uid)
}

func lookupGID(s string) (int, error) {
	if n, err := strconv.Atoi(s); err == nil {
		return n, nil
	}
	g, err := user.LookupGroup(s)
	if err != nil {
		return -1, fmt.Errorf("group %q: %w", s, err)
	}
	return strconv.Atoi(g.Gid)
}

func lookupPrimaryGID(u string) (int, error) {
	usr, err := user.Lookup(u)
	if err != nil {
		return -1, err
	}
	return strconv.Atoi(usr.Gid)
}

func processAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err == nil {
		return true
	} else if err != syscall.ESRCH {
		return true
	}
	return false
}

func waitExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return !processAlive(pid)
}

func writePIDFile(path string, pid int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0644)
}

// superLogf writes verbose supervisor diagnostics through syslog when
// available, falling back to stderr. Kept minimal — the actual log
// destination is /dev/null in the detached branch so operators rely on
// external logging (--stdout-logger etc.).
func superLogf(opts Options, format string, args ...any) {
	if !opts.Verbose {
		return
	}
	fmt.Fprintf(os.Stderr, "supervise-daemon["+opts.Service+"]: "+format+"\n", args...)
}

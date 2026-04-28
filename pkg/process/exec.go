package process

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// StartProcess starts a child process with the given parameters.
// It returns the PID and a channel that will receive exactly one ChildExit
// when the process terminates. The caller must read from the channel.
//
// If the command cannot be started at all (e.g., binary not found),
// an error is returned and no channel/PID is produced.
func StartProcess(params ExecParams) (int, <-chan ChildExit, error) {
	if len(params.Command) == 0 {
		return 0, nil, &ExecError{Stage: StageDoExec, Err: os.ErrInvalid}
	}

	cmd := exec.Command(params.Command[0], params.Command[1:]...)

	// Working directory
	if params.WorkingDir != "" {
		cmd.Dir = params.WorkingDir
	}

	// Environment: cache os.Environ() once, reuse for all env additions
	baseEnv := os.Environ()
	if len(params.Env) > 0 {
		cmd.Env = make([]string, 0, len(baseEnv)+len(params.Env)+3)
		cmd.Env = append(cmd.Env, baseEnv...)
		cmd.Env = append(cmd.Env, params.Env...)
	}

	// Set process group so we can signal the group later
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	// Credential setup (run as different user/group)
	if params.RunAsUID != 0 || params.RunAsGID != 0 {
		cmd.SysProcAttr.Credential = &syscall.Credential{
			Uid: params.RunAsUID,
			Gid: params.RunAsGID,
		}
	}

	// Ambient capabilities (applied in child between fork and exec)
	if len(params.AmbientCaps) > 0 {
		cmd.SysProcAttr.AmbientCaps = params.AmbientCaps
	}

	// Chroot support
	if params.Chroot != "" {
		cmd.SysProcAttr.Chroot = params.Chroot
	}

	// New session (setsid) — overrides default Setpgid
	if params.NewSession && !params.OnConsole {
		cmd.SysProcAttr.Setpgid = false
		cmd.SysProcAttr.Setsid = true
	}

	// Namespace isolation via clone flags
	if params.Cloneflags != 0 {
		cmd.SysProcAttr.Cloneflags = params.Cloneflags

		// User namespace requires UID/GID mappings
		if params.Cloneflags&syscall.CLONE_NEWUSER != 0 {
			if len(params.UidMappings) > 0 {
				cmd.SysProcAttr.UidMappings = params.UidMappings
			} else {
				cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{
					{ContainerID: 0, HostID: os.Getuid(), Size: 1},
				}
			}
			if len(params.GidMappings) > 0 {
				cmd.SysProcAttr.GidMappings = params.GidMappings
			} else {
				cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{
					{ContainerID: 0, HostID: os.Getgid(), Size: 1},
				}
			}
		}
	}

	// Lock file: acquire exclusive non-blocking flock before exec.
	// O_NOFOLLOW prevents an attacker from pre-creating the path as a
	// symlink to a system file — slinit runs as root so following the
	// link would let any local user influence which file gets locked
	// (DoS by holding a lock on a real lockfile elsewhere).
	var lockFD *os.File
	if params.LockFile != "" {
		var err error
		lockFD, err = os.OpenFile(params.LockFile, os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return 0, nil, &ExecError{Stage: StageDoExec, Err: fmt.Errorf("lock-file open: %w", err)}
		}
		if err := syscall.Flock(int(lockFD.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			lockFD.Close()
			return 0, nil, &ExecError{Stage: StageDoExec, Err: fmt.Errorf("lock-file already locked: %s", params.LockFile)}
		}
		// lockFD stays open for the lifetime of the process (flock released on close)
	}

	// Virtual TTY: open slave PTY as stdin/stdout/stderr, create new session
	var ptySlaveFd *os.File
	if params.PTYSlave != "" {
		var err error
		ptySlaveFd, err = os.OpenFile(params.PTYSlave, os.O_RDWR|syscall.O_NOCTTY, 0)
		if err == nil {
			cmd.Stdin = ptySlaveFd
			cmd.Stdout = ptySlaveFd
			cmd.Stderr = ptySlaveFd
			cmd.SysProcAttr.Setpgid = false
			cmd.SysProcAttr.Setsid = true
			cmd.SysProcAttr.Setctty = true
			cmd.SysProcAttr.Ctty = 0 // fd 0 (stdin) = pty slave
		}
	}

	// Console handling: open /dev/console, create new session, set controlling terminal
	var consoleFd *os.File
	if params.PTYSlave == "" && params.OnConsole {
		var err error
		consoleFd, err = os.OpenFile("/dev/console", os.O_RDWR, 0)
		if err != nil {
			// Fallback to inherited stdin/stdout/stderr
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
		} else {
			cmd.Stdin = consoleFd
			cmd.Stdout = consoleFd
			cmd.Stderr = consoleFd
			// Create new session so the child is session leader.
			cmd.SysProcAttr.Setpgid = false // Setsid implies new pgid
			cmd.SysProcAttr.Setsid = true
			// Only set /dev/console as controlling terminal when unmask-intr
			// is enabled. With a controlling terminal, the child receives
			// terminal-generated signals (SIGINT from Ctrl+C, SIGQUIT, SIGTSTP).
			// Without it, the child can still read/write the console via fds
			// but is shielded from keyboard signals — matching dinit's default
			// behavior of masking SIGINT for console services.
			if params.UnmaskSigint {
				cmd.SysProcAttr.Setctty = true
				cmd.SysProcAttr.Ctty = 0 // fd 0 (stdin) = /dev/console
			}
		}
	} else if params.OutputPipe != nil {
		// Capture stdout/stderr to a pipe for log buffering or piping.
		// When ErrorPipe is set, stderr goes to a separate pipe (used by
		// the error-logger feature for piping stderr to a different command).
		cmd.Stdout = params.OutputPipe
		if params.ErrorPipe != nil {
			cmd.Stderr = params.ErrorPipe
		} else {
			cmd.Stderr = params.OutputPipe
		}
	}

	// Wire stdin from input pipe (consumer-of)
	if params.InputPipe != nil && !params.OnConsole {
		cmd.Stdin = params.InputPipe
	}

	// Close stdin/stdout/stderr: redirect to /dev/null (runit -0/-1/-2 style)
	if params.CloseStdin && cmd.Stdin == nil {
		devNull, err := os.Open("/dev/null")
		if err == nil {
			cmd.Stdin = devNull
			defer devNull.Close()
		}
	}
	if params.CloseStdout && cmd.Stdout == nil {
		devNull, err := os.OpenFile("/dev/null", os.O_WRONLY, 0)
		if err == nil {
			cmd.Stdout = devNull
			defer devNull.Close()
		}
	}
	if params.CloseStderr && cmd.Stderr == nil {
		devNull, err := os.OpenFile("/dev/null", os.O_WRONLY, 0)
		if err == nil {
			cmd.Stderr = devNull
			defer devNull.Close()
		}
	}

	// Set up extra file descriptors for the child process.
	// ExtraFiles[i] becomes fd 3+i in the child.
	//
	// Ordering: socket activation fd MUST be fd 3 (systemd convention),
	// so socket goes first. Readiness notification pipe follows.
	var extraFdNullFiles []*os.File // /dev/null files to close after start

	// Socket activation: pre-opened listening sockets starting at fd 3
	if params.SocketFD != nil {
		cmd.ExtraFiles = append(cmd.ExtraFiles, params.SocketFD)
		for _, extraFD := range params.ExtraSocketFDs {
			cmd.ExtraFiles = append(cmd.ExtraFiles, extraFD)
		}
		nFDs := 1 + len(params.ExtraSocketFDs)
		listenEnv := fmt.Sprintf("LISTEN_FDS=%d", nFDs)
		if cmd.Env == nil {
			cmd.Env = append(baseEnv[:len(baseEnv):len(baseEnv)], listenEnv)
		} else {
			cmd.Env = append(cmd.Env, listenEnv)
		}
		// LISTEN_PID will be set after cmd.Start() (see below)
	}

	// Readiness notification pipe
	if params.NotifyPipe != nil {
		targetFD := 3 // default: first ExtraFile slot = fd 3
		if params.ForceNotifyFD >= 3 {
			targetFD = params.ForceNotifyFD
		}

		// If socket already occupies fd 3, shift notify target up
		baseOffset := len(cmd.ExtraFiles)
		if targetFD < 3+baseOffset {
			targetFD = 3 + baseOffset
		}

		// Fill ExtraFiles up to the target slot
		slotIndex := targetFD - 3
		for len(cmd.ExtraFiles) < slotIndex {
			devNull, err := os.Open("/dev/null")
			if err != nil {
				return 0, nil, &ExecError{Stage: StageArrangeFDs, Err: err}
			}
			extraFdNullFiles = append(extraFdNullFiles, devNull)
			cmd.ExtraFiles = append(cmd.ExtraFiles, devNull)
		}
		cmd.ExtraFiles = append(cmd.ExtraFiles, params.NotifyPipe)

		// Set environment variable with the actual fd number
		actualFD := 3 + len(cmd.ExtraFiles) - 1
		if params.NotifyVar != "" {
			if cmd.Env == nil {
				cmd.Env = make([]string, len(baseEnv), len(baseEnv)+2)
				copy(cmd.Env, baseEnv)
			}
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%d", params.NotifyVar, actualFD))
		}
	}

	// Control socket fd (pass-cs-fd): append after other extra fds
	if params.ControlSocketFD != nil {
		cmd.ExtraFiles = append(cmd.ExtraFiles, params.ControlSocketFD)
		csFD := 3 + len(cmd.ExtraFiles) - 1
		if cmd.Env == nil {
			cmd.Env = make([]string, len(baseEnv), len(baseEnv)+2)
			copy(cmd.Env, baseEnv)
		}
		cmd.Env = append(cmd.Env, fmt.Sprintf("SLINIT_CS_FD=%d", csFD))
	}

	// Start the process
	if err := cmd.Start(); err != nil {
		if ptySlaveFd != nil {
			ptySlaveFd.Close()
		}
		if consoleFd != nil {
			consoleFd.Close()
		}
		for _, f := range extraFdNullFiles {
			f.Close()
		}
		if lockFD != nil {
			lockFD.Close()
		}
		return 0, nil, &ExecError{Stage: StageDoExec, Err: err}
	}

	// Close our copy of PTY slave and console fd after fork
	if ptySlaveFd != nil {
		ptySlaveFd.Close()
	}
	if consoleFd != nil {
		consoleFd.Close()
	}

	// Close /dev/null filler fds after fork
	for _, f := range extraFdNullFiles {
		f.Close()
	}

	pid := cmd.Process.Pid

	// Apply post-fork process attributes.
	// These are best-effort: failures are logged but don't prevent startup.
	if errs := applyPostForkAttrs(pid, params); len(errs) > 0 {
		for _, err := range errs {
			fmt.Fprintf(os.Stderr, "slinit: pid %d: post-fork attr warning: %v\n", pid, err)
		}
	}

	exitCh := make(chan ChildExit, 1)

	// Goroutine that waits for the process to finish
	go func() {
		defer close(exitCh)
		// Release lock file when process exits
		if lockFD != nil {
			defer lockFD.Close()
		}

		err := cmd.Wait()

		var status syscall.WaitStatus
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				status = exitErr.Sys().(syscall.WaitStatus)
			}
		}

		exitCh <- ChildExit{
			PID:    pid,
			Status: status,
		}
	}()

	return pid, exitCh, nil
}

// SignalProcess sends a signal to a process.
// If signalGroupOnly is false, signals the process group (negative PID).
func SignalProcess(pid int, sig syscall.Signal, processOnly bool) error {
	if pid <= 0 {
		return nil
	}
	if processOnly {
		return syscall.Kill(pid, sig)
	}
	// Signal the process group
	return syscall.Kill(-pid, sig)
}

// KillProcessGroup sends SIGKILL to all remaining processes in a process
// group and reaps their zombie entries. The group leader should already have
// been reaped by cmd.Wait(). Because each service uses Setpgid, the pgid
// equals the leader's PID. Using wait4(-pgid) is safe: it only reaps
// children in this specific group, never other managed service processes.
func KillProcessGroup(pgid int) {
	if pgid <= 0 {
		return
	}
	// Kill remaining group members (ESRCH if group is already empty)
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	// Reap zombies from this specific group
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-pgid, &status, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			break
		}
	}
}

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

	// Environment
	if len(params.Env) > 0 {
		cmd.Env = append(os.Environ(), params.Env...)
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

	// Console handling: open /dev/console, create new session, set controlling terminal
	var consoleFd *os.File
	if params.OnConsole {
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
		// Capture stdout/stderr to a pipe for log buffering or piping
		cmd.Stdout = params.OutputPipe
		cmd.Stderr = params.OutputPipe
	}

	// Wire stdin from input pipe (consumer-of)
	if params.InputPipe != nil && !params.OnConsole {
		cmd.Stdin = params.InputPipe
	}

	// Set up extra file descriptors for the child process.
	// ExtraFiles[i] becomes fd 3+i in the child.
	//
	// Ordering: socket activation fd MUST be fd 3 (systemd convention),
	// so socket goes first. Readiness notification pipe follows.
	var extraFdNullFiles []*os.File // /dev/null files to close after start

	// Socket activation: pre-opened listening socket at fd 3
	if params.SocketFD != nil {
		cmd.ExtraFiles = append(cmd.ExtraFiles, params.SocketFD)
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = append(cmd.Env, "LISTEN_FDS=1")
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
				cmd.Env = os.Environ()
			}
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%d", params.NotifyVar, actualFD))
		}
	}

	// Control socket fd (pass-cs-fd): append after other extra fds
	if params.ControlSocketFD != nil {
		cmd.ExtraFiles = append(cmd.ExtraFiles, params.ControlSocketFD)
		csFD := 3 + len(cmd.ExtraFiles) - 1
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = append(cmd.Env, fmt.Sprintf("SLINIT_CS_FD=%d", csFD))
	}

	// Start the process
	if err := cmd.Start(); err != nil {
		if consoleFd != nil {
			consoleFd.Close()
		}
		for _, f := range extraFdNullFiles {
			f.Close()
		}
		return 0, nil, &ExecError{Stage: StageDoExec, Err: err}
	}

	// Close our copy of the console fd after fork
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
	applyPostForkAttrs(pid, params)

	exitCh := make(chan ChildExit, 1)

	// Goroutine that waits for the process to finish
	go func() {
		defer close(exitCh)

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

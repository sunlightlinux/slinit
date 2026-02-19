package process

import (
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
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd.SysProcAttr.Credential = &syscall.Credential{
			Uid: params.RunAsUID,
			Gid: params.RunAsGID,
		}
	}

	// Console handling
	if params.OnConsole {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	// Start the process
	if err := cmd.Start(); err != nil {
		return 0, nil, &ExecError{Stage: StageDoExec, Err: err}
	}

	pid := cmd.Process.Pid
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

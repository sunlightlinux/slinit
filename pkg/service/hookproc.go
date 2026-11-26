package service

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

// newHookCmd builds the exec.Cmd for a command slinit runs on a
// service's behalf — a hook, a ready-check, the env-generator — and ties
// its lifetime to ctx the way cron-command does: the command gets a
// process group of its own and, when ctx ends, the whole group is
// SIGKILLed.
//
// exec.CommandContext alone kills only the direct child. A hook such as
// `sh -c "sleep 60; :"` that outran hook-timeout lost its shell and left
// the sleep running, reparented to PID 1 with nobody watching it, while
// the log said the hook had been killed. A command that exits by itself
// may still leave a daemon behind; only an ended context sweeps the
// group.
//
// WaitDelay is left to RunAdhoc, whose default bounds how long a child
// that kept the output open can hold the wait.
func newHookCmd(ctx context.Context, argv []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	setHookProcessGroup(cmd)
	return cmd
}

// setHookProcessGroup puts cmd in a process group of its own and makes
// the context's cancellation kill that group. cmd must come from
// exec.CommandContext and not yet be started.
func setHookProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
}

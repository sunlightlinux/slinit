// Package process implements process execution and monitoring for slinit.
package process

import (
	"fmt"
	"syscall"
)

// ExecStage identifies the stage at which process setup failed.
type ExecStage uint8

const (
	StageArrangeFDs ExecStage = iota
	StageReadEnvFile
	StageSetNotifyFDVar
	StageSetupActivationSocket
	StageSetupControlSocket
	StageChdir
	StageSetupStdio
	StageEnterCgroup
	StageSetRLimits
	StageSetUIDGID
	StageOpenLogFile
	StageSetCaps
	StageSetPrio
	StageDoExec
)

func (s ExecStage) String() string {
	descriptions := []string{
		"arranging file descriptors",
		"reading environment file",
		"setting environment variable",
		"setting up activation socket",
		"setting up control socket",
		"changing directory",
		"setting up standard input/output",
		"entering cgroup",
		"setting resource limits",
		"setting user/group ID",
		"opening log file",
		"setting capabilities",
		"setting I/O priority",
		"executing command",
	}
	if int(s) < len(descriptions) {
		return descriptions[s]
	}
	return fmt.Sprintf("ExecStage(%d)", s)
}

// ExecError represents a failure during child process setup or exec.
type ExecError struct {
	Stage ExecStage
	Err   error
}

func (e *ExecError) Error() string {
	return fmt.Sprintf("failed while %s: %v", e.Stage, e.Err)
}

// ExecParams holds the parameters for starting a child process.
type ExecParams struct {
	// Command is the program and arguments to execute.
	Command []string

	// WorkingDir is the working directory for the process.
	WorkingDir string

	// Env holds additional environment variables (key=value).
	Env []string

	// RunAsUID/RunAsGID specify credentials to run as (0 means no change).
	RunAsUID uint32
	RunAsGID uint32

	// Signal to use for stopping the process (default SIGTERM).
	TermSignal syscall.Signal

	// OnConsole indicates the process should run on the console.
	OnConsole bool

	// SignalProcessOnly: if true, signal only the process, not the group.
	SignalProcessOnly bool
}

// ChildExit represents the result of a child process termination.
type ChildExit struct {
	// PID of the terminated process.
	PID int

	// Status is the wait status from the OS.
	Status syscall.WaitStatus

	// ExecErr is set if the process failed during setup (before exec).
	// If nil, the process was exec'd successfully and later terminated.
	ExecErr *ExecError
}

// Exited returns true if the child exited normally.
func (c ChildExit) Exited() bool {
	return c.ExecErr == nil && c.Status.Exited()
}

// ExitedClean returns true if the child exited with code 0.
func (c ChildExit) ExitedClean() bool {
	return c.Exited() && c.Status.ExitStatus() == 0
}

// Signaled returns true if the child was killed by a signal.
func (c ChildExit) Signaled() bool {
	return c.ExecErr == nil && c.Status.Signaled()
}

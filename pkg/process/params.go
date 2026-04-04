// Package process implements process execution and monitoring for slinit.
package process

import (
	"fmt"
	"os"
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

	// UnmaskSigint, when true and OnConsole is true, sets /dev/console as the
	// controlling terminal so the child receives SIGINT from Ctrl+C.
	// When false, the child can read/write the console but terminal-generated
	// signals (SIGINT, SIGQUIT, SIGTSTP) are not delivered to it.
	UnmaskSigint bool

	// SignalProcessOnly: if true, signal only the process, not the group.
	SignalProcessOnly bool

	// OutputPipe, if non-nil, is the write end of a pipe used to capture
	// the child's stdout and stderr. The caller must close it after
	// StartProcess returns. Ignored when OnConsole is true.
	OutputPipe *os.File

	// InputPipe, if non-nil, is the read end of a pipe used as the child's
	// stdin. Used for consumer-of services. The caller should NOT close it
	// after StartProcess (the pipe persists across restarts).
	InputPipe *os.File

	// SocketFD, if non-nil, is a pre-opened listening socket to pass to the
	// child process as fd 3 (systemd socket activation convention).
	// The caller should NOT close it after StartProcess (socket stays open
	// for restarts). Environment variables LISTEN_FDS=N and LISTEN_PID are
	// set automatically.
	SocketFD *os.File

	// ExtraSocketFDs holds additional listening sockets (fd 4, 5, ...).
	// Combined with SocketFD, LISTEN_FDS is set to 1+len(ExtraSocketFDs).
	ExtraSocketFDs []*os.File

	// ControlSocketFD, if non-nil, is the client end of a Unix socketpair
	// connected to the control server. It is passed to the child as an extra
	// fd, and the env var SLINIT_CS_FD is set to its fd number.
	// The caller must close it after StartProcess returns.
	ControlSocketFD *os.File

	// NotifyPipe, if non-nil, is the write end of a readiness notification
	// pipe. It will be passed to the child process as an extra file descriptor.
	// The caller must close it after StartProcess returns.
	NotifyPipe *os.File

	// ForceNotifyFD is the file descriptor number the child expects for
	// the notification pipe (e.g., 3 for pipefd:3). Set to -1 if unused.
	ForceNotifyFD int

	// NotifyVar is the environment variable name to set with the actual
	// notification fd number (for pipevar:VARNAME). Empty if unused.
	NotifyVar string

	// Nice is the process priority (-20 to 19). nil means don't change.
	Nice *int

	// OOMScoreAdj is the OOM killer score adjustment (-1000 to 1000). nil means don't change.
	OOMScoreAdj *int

	// Rlimits holds resource limits to apply after fork.
	Rlimits []Rlimit

	// IOPrioClass is the I/O scheduling class (0=none, 1=RT, 2=BE, 3=IDLE).
	// IOPrioLevel is the priority level within the class (0-7).
	IOPrioClass int
	IOPrioLevel int

	// CgroupPath is the cgroupv2 path to join (e.g., "/sys/fs/cgroup/myservice").
	CgroupPath string

	// NoNewPrivs sets PR_SET_NO_NEW_PRIVS on the child process.
	NoNewPrivs bool

	// AmbientCaps is the list of ambient capabilities (CAP_* numbers)
	// to set on the child process via SysProcAttr.AmbientCaps.
	AmbientCaps []uintptr

	// Securebits is a bitmask of securebits flags to apply post-fork
	// via prctl(PR_SET_SECUREBITS). Best-effort from parent.
	Securebits uint32

	// CPUAffinity is a set of CPU numbers to pin the child process to
	// via sched_setaffinity(). nil/empty means don't change.
	CPUAffinity []uint

	// Chroot is the directory to chroot into before exec.
	// Applied via SysProcAttr.Chroot.
	Chroot string

	// NewSession creates a new session (setsid) for the child process.
	// When true, overrides the default Setpgid behavior.
	NewSession bool

	// LockFile is the path to a file to flock(LOCK_EX|LOCK_NB) before exec.
	// If the lock cannot be acquired, the process fails to start.
	LockFile string

	// CloseStdin closes fd 0 in the child process.
	CloseStdin bool
	// CloseStdout closes fd 1 in the child process.
	CloseStdout bool
	// CloseStderr closes fd 2 in the child process.
	CloseStderr bool
}

// Rlimit holds a resource limit (soft, hard) for a given resource.
type Rlimit struct {
	Resource int    // syscall.RLIMIT_* constant
	Soft     uint64
	Hard     uint64
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

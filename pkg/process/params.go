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

// ServiceDir is one auto-managed service directory (systemd's
// RuntimeDirectory= family). Path is absolute and already prefixed
// with its base (/run, /var/lib, /var/cache, /var/log, /etc). Volatile
// marks a RuntimeDirectory, removed when the service stops.
type ServiceDir struct {
	Path     string
	Mode     os.FileMode
	Volatile bool
}

// ExecParams holds the parameters for starting a child process.
type ExecParams struct {
	// Command is the program and arguments to execute.
	Command []string

	// Argv0, if non-empty, is the string presented to the exec'd target
	// as argv[0]. The actual binary loaded is still Command[0]; only the
	// name the child sees changes. Mirrors runit's chpst -b and Debian's
	// start-stop-daemon --startas. When the child needs to go through
	// slinit-runner (mlockall / sandbox / seccomp / etc.), the runner is
	// invoked with --argv0 so the override survives the intermediate exec.
	Argv0 string

	// WorkingDir is the working directory for the process.
	WorkingDir string

	// Env holds additional environment variables (key=value).
	Env []string

	// RunAsUID/RunAsGID specify credentials to run as (0 means no change).
	RunAsUID uint32
	RunAsGID uint32

	// SupplementaryGIDs is the child's supplementary group set. Loaded
	// from the service's supplementary-groups= directive after name
	// resolution. Nil means "leave supplementary groups untouched"
	// (the parent's set is inherited). An empty non-nil slice would
	// clear the set, which we don't currently expose — treat nil and
	// [] identically at the call site.
	SupplementaryGIDs []uint32

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

	// ErrorPipe, if non-nil, is the write end of a pipe used to capture
	// the child's stderr separately from stdout. When set, OutputPipe
	// captures only stdout and ErrorPipe captures stderr. Used by the
	// error-logger feature (OpenRC ERROR_LOGGER). The caller must close
	// it after StartProcess returns.
	ErrorPipe *os.File

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

	// CgroupSettings are key-value pairs written to the cgroup directory
	// before moving the child process into it. Each entry is {file, value},
	// e.g., {"memory.max", "536870912"} or {"pids.max", "100"}.
	// The cgroup directory is created if it does not exist.
	CgroupSettings []CgroupSetting

	// NoNewPrivs sets PR_SET_NO_NEW_PRIVS on the child process.
	NoNewPrivs bool

	// AmbientCaps is the list of ambient capabilities (CAP_* numbers)
	// to set on the child process via SysProcAttr.AmbientCaps.
	AmbientCaps []uintptr

	// BoundingCaps is the positive keep-list for the child's CapBnd.
	// Every cap NOT in this list is dropped via PR_CAPBSET_DROP in
	// slinit-runner before exec. Nil means "inherit parent's bounding
	// set" (no narrowing).
	BoundingCaps []uintptr

	// Securebits is a bitmask of securebits flags to apply post-fork
	// via prctl(PR_SET_SECUREBITS). Best-effort from parent.
	Securebits uint32

	// CPUAffinity is a set of CPU numbers to pin the child process to
	// via sched_setaffinity(). nil/empty means don't change.
	CPUAffinity []uint

	// SchedPolicy is the scheduling policy applied via sched_setattr.
	// 0 (SCHED_NORMAL/OTHER) means "do not change" — slinit only issues
	// the syscall when an explicit non-default policy was requested,
	// otherwise the child keeps whatever the parent had. Use the
	// unix.SCHED_* constants for the named policies.
	SchedPolicy uint32

	// SchedPriority is the static priority for SCHED_FIFO / SCHED_RR
	// (1..99). Ignored for the other policies.
	SchedPriority uint32

	// SchedRuntime / SchedDeadline / SchedPeriod (nanoseconds) describe
	// a SCHED_DEADLINE bandwidth reservation. Required when SchedPolicy
	// is SCHED_DEADLINE; ignored otherwise.
	SchedRuntime  uint64
	SchedDeadline uint64
	SchedPeriod   uint64

	// SchedResetOnFork sets SCHED_FLAG_RESET_ON_FORK on the policy: any
	// child fork()ed by the service drops back to SCHED_OTHER. Strongly
	// recommended for RT services so a runaway shell or build script
	// the service may exec doesn't inherit FIFO priority and starve the
	// scheduler. Defaults to true at the parser level.
	SchedResetOnFork bool

	// MlockallFlags is the bitmask passed to mlockall(2) (MCL_CURRENT |
	// MCL_FUTURE | MCL_ONFAULT). Zero means do not lock memory. The
	// syscall affects the *calling* process, so slinit applies it via
	// the slinit-runner exec helper rather than from the parent.
	// Requires CAP_IPC_LOCK or a sufficient RLIMIT_MEMLOCK.
	MlockallFlags int

	// NumaMempolicy is the NUMA memory policy applied via
	// set_mempolicy(2). Zero (MPOL_DEFAULT) means do not change. Like
	// mlockall, applied via slinit-runner.
	NumaMempolicy uint32

	// NumaMempolicySet distinguishes "explicit MPOL_DEFAULT" from
	// "field unset" — same shape as SchedPolicySet.
	NumaMempolicySet bool

	// NumaNodes is the node mask for BIND/INTERLEAVE/PREFERRED. Empty
	// for DEFAULT and LOCAL.
	NumaNodes []uint

	// MemoryTHP mirrors systemd v261 MemoryTHP= (never|madvise|always).
	// Only "never" has a per-process effect (PR_SET_THP_DISABLE);
	// the other values are accepted for parity but leave the system
	// default in place. Applied by slinit-runner. Empty = no change.
	MemoryTHP string

	// RunnerPath is the absolute path to slinit-runner. Empty disables
	// the wrapper even if Mlockall/NUMA fields are set (in which case
	// the syscalls are silently ignored — the operator gets a startup
	// warning). Set by the daemon at startup so service-side code does
	// not have to discover it.
	RunnerPath string

	// AppArmorLoadProfile, if non-empty, is an absolute path to an
	// AppArmor profile loaded with `apparmor_parser -r` in the parent
	// before the child is started. A load failure fails the start
	// (security must fail closed, never silently run unconfined).
	AppArmorLoadProfile string

	// SELinuxContext, if non-empty, is a SELinux security context the
	// runner writes to /proc/self/attr/exec before execve. Fails
	// closed if selinuxfs is absent (LSM not active).
	SELinuxContext string

	// TTYPath, when non-empty, opens the given TTY device (O_RDWR|
	// O_NOCTTY) and wires it as stdin/stdout/stderr for the child.
	// Setsid + Setctty are enabled so the child becomes the session
	// leader with a controlling terminal — matches getty semantics.
	// Overrides OnConsole (they're mutually exclusive; TTYPath is
	// more specific).
	TTYPath string

	// TTYColumns / TTYRows, when > 0, set the terminal winsize via
	// TIOCSWINSZ on the opened TTY. Both must be set together to
	// take effect (single-axis winsize is ill-defined).
	TTYColumns uint16
	TTYRows    uint16

	// TTYVHangup: call vhangup(2) on the TTY before setup to force
	// any prior session off. Matches systemd TTYVHangup=yes.
	TTYVHangup bool

	// TTYVTDisallocate: for /dev/ttyN (virtual terminals), call
	// ioctl(fd, VT_DISALLOCATE, N) so the kernel wipes VT state
	// (screen buffer, escape mode) before we hand it to the child.
	// No-op on non-VT paths (serial ports, ptys).
	TTYVTDisallocate bool

	// TTYReset: write the terminal reset sequence (ESC c = RIS,
	// full reset) to the TTY before setup so a prior client's
	// escape mode / color / cursor state doesn't leak in.
	TTYReset bool

	// SMACKProcessLabel, if non-empty, is a SMACK label the runner
	// writes to /proc/self/attr/current. SMACK changes the label
	// immediately (unlike SELinux which schedules on execve) but the
	// label survives the execve and applies to the service.
	SMACKProcessLabel string

	// AppArmorProfile, if non-empty, is an AppArmor profile name the
	// child transitions into on exec (aa_change_onexec). It is applied
	// by slinit-runner because the kernel ties the transition to the
	// task that performs the execve, which only the child can do.
	AppArmorProfile string

	// ServiceDirs are runtime/state/cache/logs/configuration directories
	// created (and chowned to RunAsUID/RunAsGID) in the parent before the
	// child starts, mirroring systemd's RuntimeDirectory= family. Volatile
	// entries (RuntimeDirectory) are removed when the service stops.
	ServiceDirs []ServiceDir

	// Credentials are files exposed to the service via a fresh tmpfs at
	// /run/credentials/<service>/, owned by RunAsUID/RunAsGID, files
	// mode 0400, tmpfs remounted ro after populate. ServiceName tells
	// StartProcess where to mount it. The path is written to the
	// $CREDENTIALS_DIRECTORY env var; the caller does not pre-set it.
	ServiceName string
	Credentials []CredentialSource

	// File-descriptor-store handoff (#14). StoredFDs are prepended to
	// the LISTEN_FDS sequence so a restart sees the previous run's
	// listening sockets first; their FDNAMEs become LISTEN_FDNAMES.
	// NotifySocketPath, if non-empty, is exported as $NOTIFY_SOCKET so
	// the child can send sd_notify FDSTORE=1 packets back to the
	// daemon.
	StoredFDs        []FDStoreEntry
	NotifySocketPath string

	// DebugStop, when true, makes slinit-runner raise SIGSTOP on itself
	// before exec so a developer can `gdb -p` the (pre-exec) process and
	// `kill -CONT` it to proceed. Requires the runner wrap.
	DebugStop bool

	// Umask, if non-nil, is the file-creation mask to apply to the child
	// process. slinit sets it in the parent immediately before fork (then
	// restores its own) so the child inherits it; this is safe because
	// StartProcess calls are serialized under ServiceSet.queueMu.
	Umask *uint32

	// Chroot is the directory to chroot into before exec.
	// Applied via SysProcAttr.Chroot.
	Chroot string

	// NewSession creates a new session (setsid) for the child process.
	// When true, overrides the default Setpgid behavior.
	NewSession bool

	// LockFile is the path to a file to flock(LOCK_EX|LOCK_NB) before exec.
	// If the lock cannot be acquired, the process fails to start.
	LockFile string

	// PTYSlave, if non-empty, is the path to a PTY slave device.
	// When set, the child's stdin/stdout/stderr are connected to this PTY
	// and a new session is created (setsid + TIOCSCTTY) so the PTY becomes
	// the controlling terminal. Used for virtual TTY (screen-like attach).
	PTYSlave string

	// CloseStdin closes fd 0 in the child process.
	CloseStdin bool

	// StdinBytes, when non-nil, is written to the child's stdin
	// before exec (systemd StandardInputText= / StandardInputData=).
	// Wins over CloseStdin — closing an already-fed pipe still lets
	// the child read the bytes, then see EOF, which is the expected
	// semantics for one-shot services.
	StdinBytes []byte

	// OpenFiles is the systemd OpenFile= list: each entry becomes an
	// inherited fd on top of the socket-listen range, with a matching
	// LISTEN_FDNAMES token so the child can identify it by name.
	OpenFiles []OpenFileEntry
	// CloseStdout closes fd 1 in the child process.
	CloseStdout bool
	// CloseStderr closes fd 2 in the child process.
	CloseStderr bool

	// Filesystem sandbox (systemd-style), applied by slinit-runner in
	// the service's private mount namespace. The loader auto-implies
	// CLONE_NEWNS into Cloneflags whenever any of these are set.
	//
	// PrivateTmp: per-service tmpfs at /tmp and /var/tmp.
	// ProtectSystem: "" (off), "yes" (ro /usr,/boot,/efi),
	//   "full" (yes + /etc), "strict" (whole / ro except writable
	//   carve-outs and the standard mountpoints kept writable).
	// ReadOnlyPaths/ReadWritePaths: explicit per-path overrides, applied
	//   after ProtectSystem (rw first, then ro).
	PrivateTmp     bool
	ProtectSystem  string
	ReadOnlyPaths  []string
	ReadWritePaths []string

	// Sandbox expansion (#3b). Same plumbing path through slinit-runner
	// as the MVP fields above; see SandboxConfig in pkg/service for
	// per-field semantics.
	ProtectHome         string
	InaccessiblePaths   []string
	ProtectProc         string
	ProcSubset          string
	BindPaths           []string // "src:dst" pairs, writable
	BindReadOnlyPaths   []string // "src:dst" pairs, read-only
	TemporaryFileSystem []string // "path[:options]" entries

	// systemd-style seccomp-bpf filter (#4). The runner expands
	// @group tokens, compiles the resolved list into BPF via
	// pkg/seccomp, and installs it just before exec. The parent
	// auto-sets NoNewPrivs whenever any of these are set.
	//
	// SeccompFilter: syscall names / @groups (leading '~' = deny mode).
	// SeccompArchitectures: canonical arch names (defaults to current).
	// SeccompErrorAction: "" | kill | log | trap | errno-name | errno-number.
	// SeccompLogFilter: syscalls always logged independent of mode.
	SeccompFilter        []string
	SeccompArchitectures []string
	SeccompErrorAction   string
	SeccompLogFilter     []string

	// systemd-style Restrict*/Protect* hardening cluster (#7). Each
	// active knob expands at runner-side to a small fixed deny syscall
	// list (installed as a second seccomp filter) and/or an extra
	// mount op. The parent auto-sets NoNewPrivs whenever any of these
	// are set, same as for SeccompFilter.
	ProtectKernelTunables bool
	ProtectKernelModules  bool
	ProtectKernelLogs     bool
	ProtectClock          bool
	ProtectControlGroups  bool
	ProtectHostname       bool
	LockPersonality       bool
	// Bucket A hardening extension: argument-checking BPF fragments +
	// prctl. Each maps to a --restrict-* flag on slinit-runner.
	RestrictRealtime        bool
	RestrictNamespaces      bool
	RestrictSUIDSGID        bool
	RestrictFileSystems     bool
	RestrictAddressFamilies []string // AF_* names or numeric strings
	RestrictAFEnabled       bool     // presence marker; distinguishes unset from empty allow-list
	MemoryDenyWriteExecute  bool

	// Bucket B — legacy-safe niches. All runner-side except RemoveIPC
	// (master-side stop-time cleanup). Zero-value on each is "leave
	// untouched"; the loader sets these only when the operator opted
	// in.
	CoredumpFilter    string
	TimerSlackNsec    int64
	MemoryKSM         bool
	IgnoreSIGPIPE     bool
	IgnoreSIGPIPESet  bool   // distinguishes explicit "no" from unset (default is yes)
	Personality       string

	// Cloneflags specifies Linux clone flags for namespace isolation.
	// OR'd into SysProcAttr.Cloneflags (e.g. syscall.CLONE_NEWPID).
	Cloneflags uintptr

	// UidMappings and GidMappings are used when CLONE_NEWUSER is set.
	// If empty and CLONE_NEWUSER is set, a default 1:1 mapping is created.
	UidMappings []syscall.SysProcIDMap
	GidMappings []syscall.SysProcIDMap
}

// CgroupSetting is a key-value pair for a cgroup v2 controller knob.
// File is the filename within the cgroup directory (e.g., "memory.max").
// Value is the string to write (e.g., "536870912", "max", "100").
type CgroupSetting struct {
	File  string
	Value string
}

// OpenFileEntry is one systemd OpenFile= directive resolved to the
// exec path. Options is the raw comma-separated flags string from
// the config; parsed at open time in pkg/process/openfile.go.
type OpenFileEntry struct {
	Path    string
	FDName  string
	Options string
}

// Rlimit holds a resource limit (soft, hard) for a given resource.
type Rlimit struct {
	Resource int // syscall.RLIMIT_* constant
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

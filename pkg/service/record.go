package service

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// atomicServiceState is a race-free wrapper around ServiceState. State fields
// are written under queueMu.Lock (single-writer) but read from many goroutines
// (control connections, listeners, tests). Atomic ops keep the race detector
// quiet and avoid the RWMutex reentrancy deadlock that occurs when a callback
// from notifyListeners re-enters State() while the scheduler holds queueMu.
type atomicServiceState struct{ v atomic.Uint32 }

func (a *atomicServiceState) Load() ServiceState   { return ServiceState(a.v.Load()) }
func (a *atomicServiceState) Store(s ServiceState) { a.v.Store(uint32(s)) }

// Service is the core interface that all service types implement.
// It replaces the C++ virtual method pattern from dinit's service_record hierarchy.
type Service interface {
	// Identity
	Name() string
	Type() ServiceType

	// State
	State() ServiceState
	TargetState() ServiceState
	StopReason() StoppedReason

	// Lifecycle - called by the state machine
	BringUp() bool // start the service; returns false on failure
	BringDown()    // stop the service
	CanInterruptStart() bool
	InterruptStart() bool
	BecomingInactive()
	CheckRestart() bool

	// Process info (for process-based services; defaults return -1/{})
	PID() int
	GetExitStatus() ExitStatus

	// Dependency management
	Dependencies() []*ServiceDep
	Dependents() []*ServiceDep
	RequiredBy() int

	// State machine operations
	Start()
	Stop(bringDown bool)
	Restart() bool
	ForcedStop()

	// Pinning
	PinStart()
	PinStop()
	Unpin()

	// Listeners
	AddListener(ServiceListener)
	RemoveListener(ServiceListener)

	// Log buffer access (for catlog command)
	GetLogBuffer() *LogBuffer
	GetLogType() LogType
	GetLogFile() string

	// Internal access to the record (for state machine operations)
	Record() *ServiceRecord
}

// ServiceListener is notified of service state changes.
type ServiceListener interface {
	ServiceEvent(svc Service, event ServiceEvent)
}

// ServiceRecord holds the shared state for all service types.
// Service implementations embed this struct.
type ServiceRecord struct {
	self        Service // pointer back to the implementing Service
	serviceName string
	serviceDir  string // directory where service description was found
	description string // human-readable description for status/list output
	author      string // upstart-style metadata (informational)
	version     string
	usage       string
	recordType  ServiceType

	// State (atomic: written under queueMu.Lock, read lockless)
	state   atomicServiceState
	desired atomicServiceState

	// Flags
	autoRestart    AutoRestartMode
	smoothRecovery bool
	manualStart    bool // upstart-style: refuse all auto-activation
	// systemd-style RefuseManualStart / RefuseManualStop. Reject the
	// direct control-socket path only — dependency-driven activation
	// or teardown is still allowed. See connection.handleStartService /
	// handleStopService for the gate.
	refuseManualStart bool
	refuseManualStop  bool
	// systemd StopWhenUnneeded=: when true, the service auto-stops once
	// no dependent is holding it up AND the operator hasn't explicitly
	// started it. Checked in dependentStopped after the usual bookkeeping.
	stopWhenUnneeded  bool

	// upstart-style "normal exit": exit codes / signals that count as
	// success, suppressing respawn even when autoRestart=RestartAlways.
	normalExitCodes   []int
	// systemd RestartForceExitStatus: exit codes/signals that FORCE a
	// restart independent of the `restart =` setting. Sibling of
	// normalExitCodes (which suppresses restart); this override wins
	// even when restart=no. Applied in doStop before the per-mode
	// auto-restart branch.
	restartForceExitCodes []int
	// systemd RestartMode=. `direct` skips the cascade to hard
	// dependents on a user-initiated or auto-restart; `normal`
	// (default) preserves the historical cascade.
	restartMode RestartMode
	normalExitSignals []syscall.Signal

	// Pins
	pinnedStopped     bool
	pinnedStarted     bool
	deptPinnedStarted bool
	markedDown        bool // 'down' file exists — don't auto-start

	// Waiting flags
	waitingForDeps      bool
	waitingForConsole   bool
	haveConsole         bool
	startExplicit       bool
	waitingForStartSlot bool // waiting for start limiter slot

	// Propagation flags
	propRequire bool
	propRelease bool
	propFailure bool
	propStart   bool
	propStop    bool
	propPinDpt  bool

	// Start status
	startFailed  bool
	startSkipped bool

	// Restart tracking
	inAutoRestart bool
	inUserRestart bool

	// Loading
	isLoading bool

	// Force stop flag
	forceStop bool

	// Reference counting
	requiredBy int

	// Dependencies
	dependsOn  []*ServiceDep // services this one depends on
	dependents []*ServiceDep // services depending on this one
	depDepth   int           // max depth of transitive dependency chain

	// The set this service belongs to
	services *ServiceSet

	// Listeners (protected by listenerMu)
	listenerMu sync.Mutex
	listeners  []ServiceListener

	// Process settings (shared across service types)
	termSignal   syscall.Signal
	reloadSignal syscall.Signal // 0 = unset; sent by `slinitctl reload-signal`
	socketPath   string         // primary socket path (for backwards compat)
	socketPaths  []string       // all socket-listen paths (for multiple sockets)
	socketPerms  int
	socketUID    int
	socketGID    int
	stopReason   StoppedReason
	chainTo      string // service to start when this one completes

	// Service alias (alternative name for lookup)
	provides string

	// Enable-via: default "from" service for enable/disable commands
	enableVia string

	// UTMP/WTMP fields
	inittabID   string
	inittabLine string

	// Output pipe for log-type=pipe / consumer-of
	outputPipeR *os.File // read end (consumer uses as stdin)
	outputPipeW *os.File // write end (producer uses as stdout/stderr)
	logConsumer Service  // which service consumes our output (set on producer)
	consumerFor Service  // which service we consume (set on consumer)

	// Shared logger: multiple producers → single logger service
	sharedLoggerName       string // name of the shared logger service (empty if not used)
	sharedLoggerLossy      bool   // (on the logger svc) drop instead of block on backpressure
	sharedLoggerQueueSize  int    // (on the logger svc) buffered channel depth (0 = default)

	// profiles is the set of profile tags this service belongs to
	// (runsvchdir analogue). Empty = global (always eligible).
	profiles []string

	// bundleMembers, when non-empty, marks this service as an s6-rc-
	// style bundle. Members are also present as depends-on entries;
	// this field is kept for status output ("Bundle members:" line
	// with each member's live state).
	bundleMembers []string

	// Extra commands (OpenRC-style custom actions)
	// extraCommands are available in any service state.
	// extraStartedCommands are only available when the service is STARTED.
	extraCommands        map[string][]string
	extraStartedCommands map[string][]string

	// Runtime environment variables (set via control protocol)
	extraEnv map[string]string

	// Process attributes (applied post-fork)
	nice           *int
	oomScoreAdj    *int
	noNewPrivs     bool
	ioPrioClass    int
	ioPrioLevel    int
	cgroupPath     string
	cgroupSettings []process.CgroupSetting // cgroup v2 resource limits
	// slice: systemd-style hierarchical grouping (e.g. "system.slice",
	// "system.slice/database.slice"). When set with no explicit
	// cgroupPath, the effective cgroup becomes
	// /sys/fs/cgroup/<slice>/<svc-name>, letting a fleet of services
	// share cumulative limits at the slice level. Explicit cgroupPath
	// still wins for backward compatibility.
	slice string
	rlimits        []process.Rlimit
	ambientCaps    []uintptr
	boundingCaps   []uintptr // positive keep-list for CapBnd; nil = inherit
	securebits     uint32
	cpuAffinity    []uint
	umask          *uint32 // file-creation mask for the service process (nil = inherit slinit's)

	// Path-based activation. startOnPath is the filesystem path (or
	// directory) whose state triggers start; startOnPathTrigger names
	// which condition (1=exists, 2=changed, 3=modified, 4=dir-not-empty).
	// Wiring lives outside pkg/service (main.go + pkg/pathwatch) to
	// avoid coupling the service core to inotify; this record only
	// carries the config-time values so an external watcher can read them.
	startOnPath        string
	startOnPathTrigger int

	// AppArmor confinement. appArmorLoad is an absolute path to a
	// profile parsed (apparmor_parser -r) before the service starts;
	// appArmorSwitch is a profile name the process transitions into on
	// exec (aa_change_onexec). The switch is applied by slinit-runner
	// because the kernel attaches the transition to the calling task's
	// next execve, which only the child can perform.
	appArmorLoad   string
	appArmorSwitch string

	// debug raises SIGSTOP in the child before exec so a developer can
	// attach a debugger; the operator resumes it with SIGCONT.
	debug bool

	// systemd-style auto-managed directories, already resolved to
	// absolute paths. Volatile entries (RuntimeDirectory) are removed
	// when the service stops, subject to runtimeDirPreserve
	// (0=no, 1=yes, 2=restart).
	serviceDirs        []process.ServiceDir
	runtimeDirPreserve int

	// Real-time scheduling (telco / 5G data plane). Zero values keep
	// the kernel default; only when schedPolicySet is true does the
	// post-fork attr step issue a sched_setattr.
	schedPolicy      uint32
	schedPolicySet   bool
	schedPriority    uint32
	schedRuntime     uint64 // ns
	schedDeadline    uint64 // ns
	schedPeriod      uint64 // ns
	schedResetOnFork bool

	// Memory locking + NUMA. Empty/zero = no change. Applied via the
	// slinit-runner exec helper (stored on ServiceSet.RunnerPath).
	mlockallFlags    int
	numaMempolicy    uint32
	numaMempolicySet bool
	numaNodes        []uint

	// memoryTHP is systemd v261 MemoryTHP=: per-service Transparent
	// Huge Pages policy. Empty = no change; "never" opts out via
	// PR_SET_THP_DISABLE at the runner; "madvise"/"always" are
	// accepted for parity but rely on the system default.
	memoryTHP string

	cloneflags  uintptr                // namespace clone flags (CLONE_NEWPID, CLONE_NEWNS, etc.)
	uidMappings []syscall.SysProcIDMap // user namespace UID mappings
	gidMappings []syscall.SysProcIDMap // user namespace GID mappings

	// systemd-style filesystem sandbox. Any non-empty/non-false field
	// here causes the loader to OR CLONE_NEWNS into cloneflags, and
	// slinit-runner sets up the requested isolation inside that fresh
	// mount namespace before exec'ing the service.
	sandbox SandboxConfig

	// systemd-style seccomp-bpf filter (#4). Compiled and installed
	// by slinit-runner before exec; the loader auto-implies
	// PR_SET_NO_NEW_PRIVS when this is in use.
	seccomp SeccompConfig

	// systemd-style Restrict*/Protect* hardening cluster (#7). Each
	// active knob translates at runner-side to a small seccomp deny
	// filter and/or a mount op. Like seccomp, the loader auto-implies
	// PR_SET_NO_NEW_PRIVS when any knob is set.
	hardening HardeningConfig

	// Bucket B — legacy-safe niches. Each is a small runner-side apply
	// (coredumpFilter, timerSlackNsec, memoryKSM, personality,
	// ignoreSIGPIPE) plus one master-side cleanup (removeIPC) and one
	// utmp knob (utmpMode). Kept as flat fields rather than a struct
	// because they don't share cluster semantics — an operator can
	// enable memory-ksm without any other Bucket B directive.
	coredumpFilter   string
	timerSlackNsec   int64
	memoryKSM        bool
	personality      string
	ignoreSIGPIPE    *bool // nil = default (yes)
	removeIPC        bool
	utmpMode         string // "" (init) | "init" | "login" | "user"

	// Bucket C — v261/262 catch-up.
	cpusetPartition           string
	cacheDirectoryQuota       int64
	logsDirectoryQuota        int64
	stateDirectoryQuota       int64
	cacheDirectoryAccounting  bool
	logsDirectoryAccounting   bool
	stateDirectoryAccounting  bool
	startupAllowedCPUs        string
	startupAllowedMemoryNodes string
	// steadyAllowedCPUs / steadyAllowedMemoryNodes hold the values
	// that the cgroup should carry AFTER Started() fires. Populated by
	// the loader only when the startup-* twin is set; retune fires in
	// applySteadyStateCgroup().
	steadyAllowedCPUs         string
	steadyAllowedMemoryNodes  string
	timeoutStopFailureMode    TimeoutFailureMode
	watchdogSignal            syscall.Signal // 0 = default SIGABRT (systemd), or SIGTERM if the operator hasn't opted in — see fireWatchdogStop.
	finalKillSignal           syscall.Signal // 0 = default SIGKILL
	surviveFinalKillSignal    bool
	restartKillSignal         syscall.Signal
	killMode                  KillMode

	// Bucket D — env + credential pipeline. Flat fields, none share
	// cluster semantics with each other.
	passEnvironment    []string
	passEnvSet         bool
	unsetEnvironment   []string
	execSearchPath     string
	standardInput      []byte
	standardInputSet   bool
	openFiles          []OpenFileRecord
	importCredentials  []string
	notifyAccess       NotifyAccess
	notifyAccessSet    bool
	guessMainPID       bool
	// Bucket E partial — LSM domain transition mirrors of AppArmor.
	selinuxContext     string
	smackProcessLabel  string
	// TTY cluster — no-ops unless ttyPath is set.
	ttyPath          string
	ttyColumns       uint16
	ttyRows          uint16
	ttyVHangup       bool
	ttyVTDisallocate bool
	ttyReset         bool

	// Queue membership flags
	InPropQueue bool
	InStopQueue bool

	// On-start flags
	Flags ServiceFlags

	// Description source directory
	serviceDscDir string

	// Modification time of the service description file at load time.
	// Used by protocol v6 to detect stale configurations.
	loadModTime time.Time

	// Boot timing timestamps
	startRequestTime time.Time // when doStart() was called
	startedTime      time.Time // when Started() was called (reached STARTED)
	stoppedTime      time.Time // when Stopped() was called (reached STOPPED)

	// Pre-start fail-fast path checks (OpenRC-inspired):
	// BringUp refuses to start the service if any of these paths is missing.
	requiredFiles []string
	requiredDirs  []string

	predicates []Predicate

	failureAction    SystemAction
	successAction    SystemAction
	// startLimitAction fires when restart-limit-count is exhausted,
	// independently of failure-action/success-action which are gated on
	// stopReason. This mirrors systemd's StartLimitAction=.
	startLimitAction SystemAction
	rebootArgument   string

	// restartLimitExhausted is set by doStop when CheckRestart denies a
	// wanted auto-restart. Stopped() reads it to override willRestart
	// and route the service into the FAILED stable state instead of
	// re-entering initiateStart.
	restartLimitExhausted bool

	// startedEmitted collapses redundant Started() calls within one
	// session so the boot console shows exactly one "[ OK ] name"
	// per successful start, even when multiple readiness paths race
	// (ready-notification arrives right before the fallback fires,
	// auto-restart flow re-enters, etc.). Cleared in initiateStart()
	// so the next session's Started() emits again.
	startedEmitted bool

	// runtimeMax is a hard cap on total time the service may spend in
	// STARTED. A zero value disables the cap (the default). When the
	// timer fires the service is asked to stop the same way an operator
	// stop would — including running stop-command and honouring
	// stop-timeout / SIGKILL escalation.
	runtimeMax      time.Duration
	runtimeMaxTimer *time.Timer
	// runtimeMaxExtra: RuntimeRandomizedExtraSec sibling. Jitter added
	// to runtimeMax on each armRuntimeMaxTimer call so a fleet of
	// identical services doesn't all hit the ceiling simultaneously.
	runtimeMaxExtra time.Duration

	// jobTimeout is systemd's JobTimeoutSec=: a hard cap on the whole
	// start job (waiting-for-deps + own start-timeout). It fires even
	// when start-timeout=0 or the service is stuck waiting on a slow
	// dependency graph. Zero disables. Cancelled when the service
	// reaches STARTED or STOPPED.
	jobTimeout      time.Duration
	jobTimeoutTimer *time.Timer

	// oomPolicy is the action to take when the service's cgroup v2
	// reports an OOM kill. OOMContinue (default) lets the kernel's
	// OOM kill handler complete without slinit intervention; OOMStop
	// asks the service to stop cleanly; OOMKill SIGKILLs everything
	// in the cgroup tree. The watcher is set up lazily in Started().
	oomPolicy OOMPolicy
	oomWatch  *oomWatcher

	// PSI pressure watches (cgroup v2, systemd-parity). Each threshold
	// is the stall time within a fixed 2s window that must be exceeded
	// before a SERVICEEVENT is emitted. A zero threshold when the
	// matching *Watch bool is true selects the default (200ms). Watcher
	// goroutines are armed in Started(), torn down in Stopped().
	psiMemWatch bool
	psiMemThr   time.Duration
	psiCPUWatch bool
	psiCPUThr   time.Duration
	psiIOWatch  bool
	psiIOThr    time.Duration
	psiWatch    *psiWatcher

	// credentials are file/literal secrets materialised at
	// /run/credentials/<service>/ via a fresh ro tmpfs and exposed
	// through $CREDENTIALS_DIRECTORY. Set up in StartProcess (parent
	// side) and torn down in Stopped().
	credentials []process.CredentialSource

	// dynamic-user (systemd #13): allocate a transient UID/GID from
	// the set's UIDPool at BringUp, release it in Stopped(). The UID
	// exists only in-process — no /etc/passwd entry. dynamicUID == 0
	// means either disabled or "not yet allocated" (use dynamicUser
	// to disambiguate).
	dynamicUser bool
	dynamicUID  uint32

	// file-descriptor-store (systemd #14). When fdStoreMax > 0 a
	// $NOTIFY_SOCKET is created at BringUp and the parent listens for
	// sd_notify FDSTORE=1 messages. Stored fds are kept across
	// restarts inside the daemon and prepended to the child's
	// LISTEN_FDS on the next BringUp.
	fdStoreMax      int
	fdStore         *process.FDStore
	fdStorePreserve string // "" | "no" | "yes" | "on-success" (systemd v261)
	notifySock *process.NotifySocketListener
}

// NewServiceRecord creates a new ServiceRecord with default values.
func NewServiceRecord(self Service, set *ServiceSet, name string, recordType ServiceType) *ServiceRecord {
	sr := &ServiceRecord{
		self:        self,
		serviceName: name,
		recordType:  recordType,
		autoRestart: RestartNever,
		termSignal:  syscall.SIGTERM,
		services:    set,
	}
	sr.state.Store(StateStopped)
	sr.desired.Store(StateStopped)
	return sr
}

// --- Interface implementation methods ---

func (sr *ServiceRecord) Name() string             { return sr.serviceName }
func (sr *ServiceRecord) ServiceDir() string       { return sr.serviceDir }
func (sr *ServiceRecord) SetServiceDir(dir string) { sr.serviceDir = dir }
func (sr *ServiceRecord) Description() string      { return sr.description }
func (sr *ServiceRecord) SetDescription(d string)  { sr.description = d }
func (sr *ServiceRecord) Author() string           { return sr.author }
func (sr *ServiceRecord) SetAuthor(s string)       { sr.author = s }
func (sr *ServiceRecord) Version() string          { return sr.version }
func (sr *ServiceRecord) SetVersion(s string)      { sr.version = s }
func (sr *ServiceRecord) Usage() string            { return sr.usage }
func (sr *ServiceRecord) SetUsage(s string)        { sr.usage = s }

// SetRequiredPaths records files and directories that must exist before
// the service can start. Copies the slices so the caller may reuse them.
func (sr *ServiceRecord) SetRequiredPaths(files, dirs []string) {
	if len(files) > 0 {
		sr.requiredFiles = append(sr.requiredFiles[:0], files...)
	} else {
		sr.requiredFiles = nil
	}
	if len(dirs) > 0 {
		sr.requiredDirs = append(sr.requiredDirs[:0], dirs...)
	} else {
		sr.requiredDirs = nil
	}
}

// RequiredFiles returns the configured list of required files (for tests/introspection).
func (sr *ServiceRecord) RequiredFiles() []string { return sr.requiredFiles }

// RequiredDirs returns the configured list of required directories.
func (sr *ServiceRecord) RequiredDirs() []string { return sr.requiredDirs }

// SetPredicates records the systemd-style start preconditions for this
// service. Copies the slice so the caller may reuse it.
func (sr *ServiceRecord) SetPredicates(preds []Predicate) {
	if len(preds) > 0 {
		sr.predicates = append(sr.predicates[:0], preds...)
	} else {
		sr.predicates = nil
	}
}

// Predicates returns the configured start preconditions for
// introspection/tests.
func (sr *ServiceRecord) Predicates() []Predicate { return sr.predicates }

// SetFailureAction records the system-level action to trigger when this
// service stops in a failure state (failed start, non-zero exit without
// auto-restart, or restart-limit exhausted).
func (sr *ServiceRecord) SetFailureAction(a SystemAction) { sr.failureAction = a }

// SetSuccessAction records the system-level action to trigger when this
// service finishes successfully (clean exit 0, no restart configured).
func (sr *ServiceRecord) SetSuccessAction(a SystemAction) { sr.successAction = a }

// SetStartLimitAction records the action fired when restart-limit-count
// is exhausted. Independent of failure-action which is gated on stopReason;
// this hooks the specific "we asked for a restart but rate-limit denied it"
// path.
func (sr *ServiceRecord) SetStartLimitAction(a SystemAction) { sr.startLimitAction = a }

// StartLimitAction returns the configured restart-limit exhaustion action.
func (sr *ServiceRecord) StartLimitAction() SystemAction { return sr.startLimitAction }

// SetRebootArgument records the optional kernel-command-line passed to
// reboot(2) when failure-action / success-action triggers a reboot.
func (sr *ServiceRecord) SetRebootArgument(s string) { sr.rebootArgument = s }

// FailureAction returns the configured failure action.
func (sr *ServiceRecord) FailureAction() SystemAction { return sr.failureAction }

// SuccessAction returns the configured success action.
func (sr *ServiceRecord) SuccessAction() SystemAction { return sr.successAction }

// RebootArgument returns the configured reboot argument.
func (sr *ServiceRecord) RebootArgument() string { return sr.rebootArgument }

// SetJobTimeout configures JobTimeoutSec=: the whole start job
// (dep-wait + own start-timeout) is aborted when this elapses.
// Zero disables. Cancelled at Started() / Stopped().
func (sr *ServiceRecord) SetJobTimeout(d time.Duration) { sr.jobTimeout = d }

// JobTimeout returns the configured job-timeout duration.
func (sr *ServiceRecord) JobTimeout() time.Duration { return sr.jobTimeout }

// SetRuntimeMax records the maximum total time the service may stay in
// STARTED. A zero or negative duration disables the cap.
func (sr *ServiceRecord) SetRuntimeMax(d time.Duration) { sr.runtimeMax = d }

// SetRuntimeMaxExtra configures the additive jitter drawn from [0, N)
// and added to runtimeMax at armRuntimeMaxTimer time. Zero disables.
func (sr *ServiceRecord) SetRuntimeMaxExtra(d time.Duration) { sr.runtimeMaxExtra = d }

// RuntimeMax returns the configured runtime cap.
func (sr *ServiceRecord) RuntimeMax() time.Duration { return sr.runtimeMax }

// SetOOMPolicy records the cgroup v2 OOM-kill response policy.
func (sr *ServiceRecord) SetOOMPolicy(p OOMPolicy) { sr.oomPolicy = p }

// SetPSIMemoryWatch enables cgroup v2 memory.pressure monitoring. A zero
// threshold selects the default (200ms within a 2s window).
func (sr *ServiceRecord) SetPSIMemoryWatch(enabled bool, threshold time.Duration) {
	sr.psiMemWatch = enabled
	sr.psiMemThr = threshold
}

// SetPSICPUWatch enables cgroup v2 cpu.pressure monitoring.
func (sr *ServiceRecord) SetPSICPUWatch(enabled bool, threshold time.Duration) {
	sr.psiCPUWatch = enabled
	sr.psiCPUThr = threshold
}

// SetPSIIOWatch enables cgroup v2 io.pressure monitoring.
func (sr *ServiceRecord) SetPSIIOWatch(enabled bool, threshold time.Duration) {
	sr.psiIOWatch = enabled
	sr.psiIOThr = threshold
}

// OOMPolicy returns the configured OOM policy.
func (sr *ServiceRecord) OOMPolicy() OOMPolicy { return sr.oomPolicy }

// SetCredentials records the per-service credential sources. Copies
// the slice so the caller may reuse it.
func (sr *ServiceRecord) SetCredentials(c []process.CredentialSource) {
	if len(c) > 0 {
		sr.credentials = append(sr.credentials[:0], c...)
	} else {
		sr.credentials = nil
	}
}

// Credentials returns the configured credential sources for
// introspection / tests.
func (sr *ServiceRecord) Credentials() []process.CredentialSource { return sr.credentials }

// SetDynamicUser enables/disables the dynamic-user feature for this
// service. When enabled, BringUp allocates a transient UID from the
// set's pool and Stopped() releases it.
func (sr *ServiceRecord) SetDynamicUser(v bool) { sr.dynamicUser = v }

// DynamicUser reports whether dynamic-user is enabled.
func (sr *ServiceRecord) DynamicUser() bool { return sr.dynamicUser }

// DynamicUID returns the currently allocated transient UID, or 0 if
// none. Concrete services check this in BringUp; the record exposes it
// for introspection and tests.
func (sr *ServiceRecord) DynamicUID() uint32 { return sr.dynamicUID }

// allocateDynamicUID reserves a UID from the set's pool and records it
// on the record. No-op when dynamic-user is disabled or a UID is
// already allocated (idempotent across restart attempts within the
// same start cycle).
func (sr *ServiceRecord) allocateDynamicUID() error {
	if !sr.dynamicUser || sr.dynamicUID != 0 {
		return nil
	}
	uid, err := sr.services.UIDPool().Allocate(sr.serviceName)
	if err != nil {
		return err
	}
	sr.dynamicUID = uid
	return nil
}

// releaseDynamicUID returns the transient UID to the pool (no-op when
// none was allocated). Called from Stopped().
func (sr *ServiceRecord) releaseDynamicUID() {
	if sr.dynamicUID == 0 {
		return
	}
	sr.services.UIDPool().Release(sr.dynamicUID)
	sr.dynamicUID = 0
}

// SetFDStoreMax configures the file-descriptor-store capacity for this
// service. A value > 0 enables a $NOTIFY_SOCKET listener at BringUp.
func (sr *ServiceRecord) SetFDStoreMax(n int) {
	sr.fdStoreMax = n
	if n > 0 && sr.fdStore == nil {
		sr.fdStore = process.NewFDStore(n)
	}
}

// FDStoreMax reports the configured capacity.
func (sr *ServiceRecord) FDStoreMax() int { return sr.fdStoreMax }

// SetFDStorePreserve configures whether stored fds survive a service
// stop. Values match systemd's FileDescriptorStorePreserve=: ""/"no"
// clear on stop; "yes" retain until daemon exit; "on-success" retain
// only across a clean exit. No-op when the store is disabled.
func (sr *ServiceRecord) SetFDStorePreserve(mode string) { sr.fdStorePreserve = mode }

// FDStore returns the underlying store (nil when the feature is off).
func (sr *ServiceRecord) FDStore() *process.FDStore { return sr.fdStore }

// OnNotify implements process.NotifySocketHandler — the listener
// goroutine calls it for every sd_notify packet. FDSTORE=1 messages
// stash their attached fds; other messages are currently logged-only
// (READY=1 is handled via the legacy pipe path in ProcessService).
func (sr *ServiceRecord) OnNotify(msg process.NotifyMessage, fds []*os.File) {
	if msg.FDStore && sr.fdStore != nil {
		for _, f := range fds {
			name := msg.FDName
			if name == "" {
				name = "stored"
			}
			if err := sr.fdStore.Add(process.FDStoreEntry{Name: name, File: f}); err != nil {
				sr.services.logger.Error(
					"Service '%s': fd-store rejected fd %s: %v",
					sr.serviceName, f.Name(), err)
			}
		}
		sr.services.logger.Info(
			"Service '%s': fd-store accepted %d fd(s); store=%d/%d",
			sr.serviceName, len(fds), sr.fdStore.Len(), sr.fdStoreMax)
		return
	}
	if msg.Status != "" {
		sr.services.logger.Info("Service '%s': status %q", sr.serviceName, msg.Status)
	}
}

// setupNotifySocket creates the per-service $NOTIFY_SOCKET and starts
// the listener. Called from BringUp when fdStoreMax > 0. The returned
// path goes into the child's environment.
func (sr *ServiceRecord) setupNotifySocket(uid, gid uint32) (string, error) {
	if sr.notifySock != nil {
		sr.notifySock.Stop()
	}
	l, err := process.NewNotifySocketListener(sr.serviceName, uid, gid)
	if err != nil {
		return "", err
	}
	sr.notifySock = l
	l.Start(sr)
	return l.Path(), nil
}

// teardownNotifySocket stops the listener if running.
func (sr *ServiceRecord) teardownNotifySocket() {
	if sr.notifySock != nil {
		sr.notifySock.Stop()
		sr.notifySock = nil
	}
}

// armRuntimeMaxTimer schedules a stop request runtimeMax after now.
// Called from Started(); the AfterFunc runs in its own goroutine so it
// acquires queueMu before touching service state. A previously-armed
// timer (from a prior restart) is cancelled first.
func (sr *ServiceRecord) armRuntimeMaxTimer() {
	sr.cancelRuntimeMaxTimer()
	if sr.runtimeMax <= 0 {
		return
	}
	// Apply RuntimeRandomizedExtraSec jitter per-session so consecutive
	// starts of the same service, and a fleet of identical instances,
	// don't converge on a single tick.
	d := sr.runtimeMax + jitter(sr.runtimeMaxExtra)
	svc := sr.self
	set := sr.services
	name := sr.serviceName
	sr.runtimeMaxTimer = time.AfterFunc(d, func() {
		set.queueMu.Lock()
		defer set.queueMu.Unlock()
		// Re-check: the service may have already stopped (process
		// exited on its own, operator stopped it) between the timer
		// firing and us acquiring the lock. Stopping a STOPPED service
		// is a no-op but logging is noisy.
		if svc.State() != StateStarted {
			return
		}
		set.logger.Info("Service '%s': runtime-max-sec (%s) reached, stopping",
			name, d)
		svc.Record().Stop(true)
		set.processQueuesLocked()
	})
}

// cancelRuntimeMaxTimer disarms the runtime cap timer if armed. Safe to
// call when no timer is active.
func (sr *ServiceRecord) cancelRuntimeMaxTimer() {
	if sr.runtimeMaxTimer != nil {
		sr.runtimeMaxTimer.Stop()
		sr.runtimeMaxTimer = nil
	}
}

// armJobTimeoutTimer starts a wall-clock timer that fires jobTimeout
// after initiateStart. If the service is still StateStarting when the
// timer fires (dep-wait, start-limiter, or the child's own start-
// timeout hasn't yanked it), the start is failed. Cancelled once the
// service reaches STARTED or STOPPED.
func (sr *ServiceRecord) armJobTimeoutTimer() {
	sr.cancelJobTimeoutTimer()
	if sr.jobTimeout <= 0 {
		return
	}
	d := sr.jobTimeout
	svc := sr.self
	set := sr.services
	name := sr.serviceName
	sr.jobTimeoutTimer = time.AfterFunc(d, func() {
		set.queueMu.Lock()
		defer set.queueMu.Unlock()
		if svc.State() != StateStarting {
			return
		}
		set.logger.Error("Service '%s': job-timeout-sec (%s) elapsed, failing start",
			name, d)
		rec := svc.Record()
		rec.stopReason = ReasonTimedOut
		rec.startFailed = true
		rec.UnrecoverableStop()
		set.processQueuesLocked()
	})
}

// cancelJobTimeoutTimer disarms the job-timeout timer if armed.
func (sr *ServiceRecord) cancelJobTimeoutTimer() {
	if sr.jobTimeoutTimer != nil {
		sr.jobTimeoutTimer.Stop()
		sr.jobTimeoutTimer = nil
	}
}

// chooseStoppedAction picks the system-level action to apply now that
// the service has reached STOPPED. Returns ActionNone when the daemon
// is already shutting down (we don't second-guess shutdown ordering),
// when the service is about to restart (the failure is recoverable),
// or when neither action is configured.
func (sr *ServiceRecord) chooseStoppedAction(willRestart bool) SystemAction {
	if willRestart || sr.services.IsShuttingDown() {
		return ActionNone
	}
	// Failure path: start never succeeded, or post-start failure
	// surfaced (signal/non-zero exit/timeout) — anything other than a
	// clean operator-issued stop or a clean post-run finish.
	exitStatus := sr.self.GetExitStatus()
	cleanFinish := sr.stopReason == ReasonTerminated &&
		exitStatus.Exited() && exitStatus.ExitCode() == 0
	if sr.startFailed ||
		sr.stopReason == ReasonFailed ||
		sr.stopReason == ReasonExecFailed ||
		sr.stopReason == ReasonTimedOut ||
		(sr.stopReason == ReasonTerminated && !cleanFinish) {
		return sr.failureAction
	}
	// Success path: only fire for a service that finished on its own
	// with a clean exit. Operator-issued stops (ReasonNormal without a
	// terminated process) do not count.
	if cleanFinish {
		return sr.successAction
	}
	return ActionNone
}

// markSkippedStart short-circuits the start path when a condition-*
// predicate fails: the service transitions straight to STARTED with
// no process so dependents proceed as if the start succeeded, and
// WasStartSkipped() returns true for introspection. Mirrors
// systemd's "ConditionXxx=" silent-skip semantics.
func (sr *ServiceRecord) markSkippedStart() {
	sr.startSkipped = true
	sr.Started()
}

// PredicateOutcome is the result of evaluating CheckPredicates.
type PredicateOutcome uint8

const (
	PredOK      PredicateOutcome = iota // all checks passed (or none configured)
	PredSkip                            // a condition-* failed → skip silently
	PredFailed                          // an assert-* failed → fail start
)

// CheckPredicates evaluates every configured predicate in order and
// returns the strictest outcome. Within a category the FIRST failing
// predicate wins (so the reason string points at it). assert-* outranks
// condition-*: an assertion failure is still surfaced even if a prior
// condition would have caused a skip.
//
// Returns ("", "") when every predicate passes or none are configured.
func (sr *ServiceRecord) CheckPredicates() (PredicateOutcome, string) {
	var (
		firstSkipReason string
		failReason      string
	)
	for _, p := range sr.predicates {
		ok, why := p.Evaluate()
		if ok {
			continue
		}
		if p.IsAssert {
			if failReason == "" {
				failReason = fmt.Sprintf("%s failed: %s", p.String(), why)
			}
			continue
		}
		if firstSkipReason == "" {
			firstSkipReason = fmt.Sprintf("%s failed: %s", p.String(), why)
		}
	}
	if failReason != "" {
		return PredFailed, failReason
	}
	if firstSkipReason != "" {
		return PredSkip, firstSkipReason
	}
	return PredOK, ""
}

// CheckRequiredPaths verifies all configured required paths exist. Returns
// the first error encountered with a clear message suitable for logging.
// The check is deliberately read-only: it does not create or modify anything.
// Mirrors OpenRC semantics: required_dirs → stat+is-dir, required_files →
// stat+readable. Symlinks are followed (os.Stat, not os.Lstat) to match the
// shell test operators [ -d ... ] and [ -r ... ].
func (sr *ServiceRecord) CheckRequiredPaths() error {
	for _, d := range sr.requiredDirs {
		st, err := os.Stat(d)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("required directory %q does not exist", d)
			}
			return fmt.Errorf("required directory %q: %w", d, err)
		}
		if !st.IsDir() {
			return fmt.Errorf("required directory %q is not a directory", d)
		}
	}
	for _, f := range sr.requiredFiles {
		st, err := os.Stat(f)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("required file %q does not exist", f)
			}
			return fmt.Errorf("required file %q: %w", f, err)
		}
		if st.IsDir() {
			return fmt.Errorf("required file %q is a directory", f)
		}
		// Readability check: open O_RDONLY and immediately close.
		rf, err := os.Open(f)
		if err != nil {
			return fmt.Errorf("required file %q is not readable: %w", f, err)
		}
		rf.Close()
	}
	return nil
}
func (sr *ServiceRecord) LoadModTime() time.Time      { return sr.loadModTime }
func (sr *ServiceRecord) SetLoadModTime(t time.Time)  { sr.loadModTime = t }
func (sr *ServiceRecord) Type() ServiceType           { return sr.recordType }
func (sr *ServiceRecord) State() ServiceState         { return sr.state.Load() }
func (sr *ServiceRecord) TargetState() ServiceState   { return sr.desired.Load() }
func (sr *ServiceRecord) StopReason() StoppedReason   { return sr.stopReason }
func (sr *ServiceRecord) RequiredBy() int             { return sr.requiredBy }
func (sr *ServiceRecord) Dependencies() []*ServiceDep { return sr.dependsOn }
func (sr *ServiceRecord) Dependents() []*ServiceDep   { return sr.dependents }
func (sr *ServiceRecord) DepDepth() int               { return sr.depDepth }
func (sr *ServiceRecord) SetDepDepth(d int)           { sr.depDepth = d }
func (sr *ServiceRecord) Record() *ServiceRecord      { return sr }
func (sr *ServiceRecord) PID() int                    { return -1 }
func (sr *ServiceRecord) GetExitStatus() ExitStatus   { return ExitStatus{} }
func (sr *ServiceRecord) BecomingInactive()           {}
func (sr *ServiceRecord) CheckRestart() bool          { return true }
func (sr *ServiceRecord) GetSmoothRecovery() bool     { return sr.smoothRecovery }
func (sr *ServiceRecord) IsManualStart() bool         { return sr.manualStart }
func (sr *ServiceRecord) RefusesManualStart() bool    { return sr.refuseManualStart }
func (sr *ServiceRecord) RefusesManualStop() bool     { return sr.refuseManualStop }
func (sr *ServiceRecord) StopsWhenUnneeded() bool     { return sr.stopWhenUnneeded }

// UnrecoverableStop forces the service to stop without possibility of restart.
func (sr *ServiceRecord) UnrecoverableStop() {
	sr.desired.Store(StateStopped)
	sr.ForcedStop()
}

func (sr *ServiceRecord) AddListener(l ServiceListener) {
	sr.listenerMu.Lock()
	defer sr.listenerMu.Unlock()
	sr.listeners = append(sr.listeners, l)
}

func (sr *ServiceRecord) RemoveListener(l ServiceListener) {
	sr.listenerMu.Lock()
	defer sr.listenerMu.Unlock()
	for i, existing := range sr.listeners {
		if existing == l {
			// Swap with last element to avoid splice copy
			last := len(sr.listeners) - 1
			sr.listeners[i] = sr.listeners[last]
			sr.listeners[last] = nil // GC hint
			sr.listeners = sr.listeners[:last]
			return
		}
	}
}

// --- Setters ---

func (sr *ServiceRecord) SetAutoRestart(mode AutoRestartMode) { sr.autoRestart = mode }
func (sr *ServiceRecord) SetSmoothRecovery(v bool)            { sr.smoothRecovery = v }
func (sr *ServiceRecord) SetManualStart(v bool)               { sr.manualStart = v }
func (sr *ServiceRecord) SetRefuseManualStart(v bool)         { sr.refuseManualStart = v }
func (sr *ServiceRecord) SetRefuseManualStop(v bool)          { sr.refuseManualStop = v }
func (sr *ServiceRecord) SetStopWhenUnneeded(v bool)          { sr.stopWhenUnneeded = v }

func (sr *ServiceRecord) SetNormalExitCodes(c []int) { sr.normalExitCodes = c }

// SetRestartForceExitCodes records exit codes that FORCE a restart
// regardless of the `restart =` setting. Sibling of normal-exit codes.
func (sr *ServiceRecord) SetRestartForceExitCodes(c []int) { sr.restartForceExitCodes = c }

// SetRestartMode chooses the cascade behaviour on restart:
// RestartModeDirect skips the dependent-restart cascade so the
// dependents keep running through the parent's flip.
func (sr *ServiceRecord) SetRestartMode(m RestartMode) { sr.restartMode = m }

// IsForceRestartExit reports whether the given exit status matches one
// of the configured RestartForceExitStatus codes. Signals aren't part
// of this list — the operator writes signal-based force-restart via
// autoRestart RestartAlways instead.
func (sr *ServiceRecord) IsForceRestartExit(es ExitStatus) bool {
	if !es.Exited() || len(sr.restartForceExitCodes) == 0 {
		return false
	}
	code := es.ExitCode()
	for _, c := range sr.restartForceExitCodes {
		if c == code {
			return true
		}
	}
	return false
}
func (sr *ServiceRecord) SetNormalExitSignals(s []syscall.Signal) {
	sr.normalExitSignals = s
}

// IsNormalExit returns true if `es` matches one of the codes or
// signals declared via the `normal-exit` stanza. The state machine
// uses this to suppress respawn for exits the operator has marked
// as success.
func (sr *ServiceRecord) IsNormalExit(es ExitStatus) bool {
	if es.Exited() {
		code := es.ExitCode()
		for _, c := range sr.normalExitCodes {
			if c == code {
				return true
			}
		}
		return false
	}
	if es.Signaled() {
		sig := es.Signal()
		for _, s := range sr.normalExitSignals {
			if s == sig {
				return true
			}
		}
	}
	return false
}
func (sr *ServiceRecord) SetChainTo(name string)             { sr.chainTo = name }
func (sr *ServiceRecord) SetServiceDscDir(dir string)        { sr.serviceDscDir = dir }
func (sr *ServiceRecord) SetTermSignal(sig syscall.Signal)   { sr.termSignal = sig }
func (sr *ServiceRecord) SetReloadSignal(sig syscall.Signal) { sr.reloadSignal = sig }
func (sr *ServiceRecord) ReloadSignal() syscall.Signal       { return sr.reloadSignal }

func (sr *ServiceRecord) SetFlags(flags ServiceFlags) { sr.Flags = flags }
func (sr *ServiceRecord) SetProvides(name string)     { sr.provides = name }
func (sr *ServiceRecord) Provides() string            { return sr.provides }
func (sr *ServiceRecord) SetEnableVia(name string)    { sr.enableVia = name }
func (sr *ServiceRecord) EnableVia() string           { return sr.enableVia }

func (sr *ServiceRecord) SetLogConsumer(svc Service)      { sr.logConsumer = svc }
func (sr *ServiceRecord) LogConsumer() Service            { return sr.logConsumer }
func (sr *ServiceRecord) SetConsumerFor(svc Service)      { sr.consumerFor = svc }
func (sr *ServiceRecord) ConsumerFor() Service            { return sr.consumerFor }
func (sr *ServiceRecord) SetSharedLoggerName(name string) { sr.sharedLoggerName = name }
func (sr *ServiceRecord) SharedLoggerName() string        { return sr.sharedLoggerName }

// SharedLoggerLossy / QueueSize are read from the *logger* service
// (the sink), not from producers. Set at load time; passed to
// GetOrCreateSharedLogMux when a producer registers with the mux.
func (sr *ServiceRecord) SetSharedLoggerLossy(b bool)      { sr.sharedLoggerLossy = b }
func (sr *ServiceRecord) SharedLoggerLossy() bool          { return sr.sharedLoggerLossy }
func (sr *ServiceRecord) SetSharedLoggerQueueSize(n int)   { sr.sharedLoggerQueueSize = n }
func (sr *ServiceRecord) SharedLoggerQueueSize() int       { return sr.sharedLoggerQueueSize }

// SetBundleMembers records the s6-rc-style member list for this
// bundle. The loader has already added each member as a depends-on
// dep — this list is only kept so `slinitctl status` can render a
// "Bundle members:" section.
func (sr *ServiceRecord) SetBundleMembers(m []string) {
	sr.bundleMembers = append(sr.bundleMembers[:0], m...)
}

// BundleMembers returns the recorded member list (empty when the
// service isn't a bundle).
func (sr *ServiceRecord) BundleMembers() []string { return sr.bundleMembers }

// SetProfiles assigns the profile tags this service belongs to.
// Empty (nil / len==0) = global service.
func (sr *ServiceRecord) SetProfiles(p []string) { sr.profiles = append(sr.profiles[:0], p...) }

// Profiles returns the profile tags this service is a member of.
func (sr *ServiceRecord) Profiles() []string { return sr.profiles }

// InProfile reports whether the service is a member of profile `name`.
// A service with no profile tags is always "in" every profile — it's
// global infrastructure that must not be swapped out during profile
// activation.
func (sr *ServiceRecord) InProfile(name string) bool {
	if len(sr.profiles) == 0 {
		return true
	}
	for _, p := range sr.profiles {
		if p == name {
			return true
		}
	}
	return false
}

// SetExtraCommands sets custom actions available in any service state.
func (sr *ServiceRecord) SetExtraCommands(cmds map[string][]string) { sr.extraCommands = cmds }

// SetExtraStartedCommands sets custom actions only available when STARTED.
func (sr *ServiceRecord) SetExtraStartedCommands(cmds map[string][]string) {
	sr.extraStartedCommands = cmds
}

// ExtraCommands returns all extra commands (both always and started-only).
func (sr *ServiceRecord) ExtraCommands() map[string][]string        { return sr.extraCommands }
func (sr *ServiceRecord) ExtraStartedCommands() map[string][]string { return sr.extraStartedCommands }

// LookupExtraCommand finds an extra command by action name. Returns the command
// and whether the action was found. If the action is a started-only command and
// the service is not in STARTED state, returns nil, false.
func (sr *ServiceRecord) LookupExtraCommand(action string) ([]string, bool) {
	if cmd, ok := sr.extraCommands[action]; ok {
		return cmd, true
	}
	if cmd, ok := sr.extraStartedCommands[action]; ok {
		if sr.state.Load() == StateStarted {
			return cmd, true
		}
		return nil, false // exists but service not started
	}
	return nil, false
}

// ListExtraActions returns the names of all registered extra commands.
func (sr *ServiceRecord) ListExtraActions() []string {
	seen := make(map[string]bool)
	var actions []string
	for name := range sr.extraCommands {
		actions = append(actions, name)
		seen[name] = true
	}
	for name := range sr.extraStartedCommands {
		if !seen[name] {
			actions = append(actions, name+"*") // * suffix = started-only
		}
	}
	return actions
}
func (sr *ServiceRecord) OutputPipeW() *os.File { return sr.outputPipeW }
func (sr *ServiceRecord) OutputPipeR() *os.File { return sr.outputPipeR }

// EnsureOutputPipe lazily creates the output pipe for log-type=pipe.
func (sr *ServiceRecord) EnsureOutputPipe() error {
	if sr.outputPipeW != nil {
		return nil
	}
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	sr.outputPipeR = r
	sr.outputPipeW = w
	return nil
}

// CloseOutputPipe closes both ends of the output pipe.
func (sr *ServiceRecord) CloseOutputPipe() {
	if sr.outputPipeR != nil {
		sr.outputPipeR.Close()
		sr.outputPipeR = nil
	}
	if sr.outputPipeW != nil {
		sr.outputPipeW.Close()
		sr.outputPipeW = nil
	}
}

// TransferOutputPipe returns both pipe fds and clears them from this record.
func (sr *ServiceRecord) TransferOutputPipe() (r *os.File, w *os.File) {
	r, w = sr.outputPipeR, sr.outputPipeW
	sr.outputPipeR = nil
	sr.outputPipeW = nil
	return
}

// SetOutputPipeFDs sets pre-existing pipe fds (from reload transfer).
func (sr *ServiceRecord) SetOutputPipeFDs(r, w *os.File) {
	sr.outputPipeR = r
	sr.outputPipeW = w
}

func (sr *ServiceRecord) SetSocketDetails(path string, perms int, uid, gid int) {
	sr.socketPath = path
	sr.socketPerms = perms
	sr.socketUID = uid
	sr.socketGID = gid
}

// SetSocketPaths sets multiple socket-listen paths.
func (sr *ServiceRecord) SetSocketPaths(paths []string) {
	sr.socketPaths = paths
}

// SetUtmpDetails sets the inittab-id and inittab-line for UTMPX logging.
func (sr *ServiceRecord) SetUtmpDetails(id, line string) {
	sr.inittabID = id
	sr.inittabLine = line
}

// HasUtmp returns true if either inittab-id or inittab-line is set.
func (sr *ServiceRecord) HasUtmp() bool {
	return sr.inittabID != "" || sr.inittabLine != ""
}

// InittabID returns the inittab-id.
func (sr *ServiceRecord) InittabID() string { return sr.inittabID }

// InittabLine returns the inittab-line.
func (sr *ServiceRecord) InittabLine() string { return sr.inittabLine }

func (sr *ServiceRecord) IsMarkedActive() bool    { return sr.startExplicit }
func (sr *ServiceRecord) IsStartPinned() bool     { return sr.pinnedStarted || sr.deptPinnedStarted }
func (sr *ServiceRecord) IsStopPinned() bool      { return sr.pinnedStopped }
func (sr *ServiceRecord) DidStartFail() bool      { return sr.startFailed }

// ResetFailed clears the startFailed flag so subsequent status queries
// no longer report the service as failed. Mirrors systemd's
// `systemctl reset-failed`. No-op on a service that isn't marked failed.
func (sr *ServiceRecord) ResetFailed() {
	sr.startFailed = false
}
func (sr *ServiceRecord) WasStartSkipped() bool   { return sr.startSkipped }
func (sr *ServiceRecord) IsLoading() bool         { return sr.isLoading }
func (sr *ServiceRecord) HasConsole() bool        { return sr.haveConsole }
func (sr *ServiceRecord) WaitingForConsole() bool { return sr.waitingForConsole }

// --- Environment variable management ---

func (sr *ServiceRecord) SetEnvVar(key, value string) {
	if sr.extraEnv == nil {
		sr.extraEnv = make(map[string]string)
	}
	sr.extraEnv[key] = value
}

func (sr *ServiceRecord) UnsetEnvVar(key string) {
	delete(sr.extraEnv, key)
}

// ResetEnv clears all runtime-set environment variables on this service.
// After reset, the next start sees only the daemon's global env plus
// the service's env-file (the "defaults"). Mirrors upstart's
// `initctl reset-env JOB`.
func (sr *ServiceRecord) ResetEnv() {
	sr.extraEnv = nil
}

func (sr *ServiceRecord) GetAllEnv() map[string]string {
	if sr.extraEnv == nil {
		return nil
	}
	result := make(map[string]string, len(sr.extraEnv))
	for k, v := range sr.extraEnv {
		result[k] = v
	}
	return result
}

// BuildEnvSlice converts extraEnv to []string for ExecParams.Env.
func (sr *ServiceRecord) BuildEnvSlice() []string {
	if len(sr.extraEnv) == 0 {
		return nil
	}
	result := make([]string, 0, len(sr.extraEnv))
	for k, v := range sr.extraEnv {
		result = append(result, k+"="+v)
	}
	return result
}

// BuildFullEnv returns global daemon env + per-service extraEnv combined.
// Used by service types that don't have their own env-file (e.g., scripted).
func (sr *ServiceRecord) BuildFullEnv() []string {
	globalEnv := sr.services.GlobalEnv()
	extra := sr.BuildEnvSlice()
	if len(globalEnv) == 0 && len(extra) == 0 {
		return nil
	}
	result := make([]string, 0, len(globalEnv)+len(extra))
	result = append(result, globalEnv...)
	result = append(result, extra...)
	return sr.applyBucketDEnvFilters(result)
}

// BuildEnvWithFile returns global env + env-file vars + per-service extraEnv
// with a single pre-allocated slice. Used by ProcessService and BGProcessService.
func (sr *ServiceRecord) BuildEnvWithFile(envFile string) []string {
	globalEnv := sr.services.GlobalEnv()
	extra := sr.BuildEnvSlice()

	var fileEnv map[string]string
	if envFile != "" {
		var err error
		fileEnv, err = process.ReadEnvFile(envFile)
		if err != nil {
			sr.services.logger.Error("Service '%s': failed to read env-file '%s': %v",
				sr.serviceName, envFile, err)
		}
	}

	totalCap := len(globalEnv) + len(fileEnv) + len(extra)
	if totalCap == 0 {
		return nil
	}
	env := make([]string, 0, totalCap)
	env = append(env, globalEnv...)
	for k, v := range fileEnv {
		env = append(env, k+"="+v)
	}
	env = append(env, extra...)
	return sr.applyBucketDEnvFilters(env)
}

// applyBucketDEnvFilters is the tail of every env-build path. Applies
// systemd's PassEnvironment= / UnsetEnvironment= / ExecSearchPath=
// semantics in that order:
//
//   1. If PassEnvironment was set explicitly, drop every var whose
//      NAME is not in the allow-list. Empty list = drop everything.
//   2. Remove every var whose NAME appears in UnsetEnvironment.
//   3. If ExecSearchPath is set, override PATH= (or append if not
//      present).
//
// The dinit-compatible default (nothing set) is a no-op so existing
// services see no behaviour change.
func (sr *ServiceRecord) applyBucketDEnvFilters(env []string) []string {
	pass, passSet := sr.PassEnvironment()
	unset := sr.UnsetEnvironment()
	path := sr.ExecSearchPath()
	if !passSet && len(unset) == 0 && path == "" {
		return env
	}
	var passSetLookup map[string]struct{}
	if passSet {
		passSetLookup = make(map[string]struct{}, len(pass))
		for _, name := range pass {
			passSetLookup[name] = struct{}{}
		}
		// SLINIT_* names are always forwarded — they're set BY slinit
		// after this filter and would be dropped by a strict filter
		// otherwise, breaking dinit-compat query env vars.
		passSetLookup["SLINIT_SERVICENAME"] = struct{}{}
		passSetLookup["SLINIT_SERVICEDSCDIR"] = struct{}{}
	}
	unsetLookup := make(map[string]struct{}, len(unset))
	for _, name := range unset {
		unsetLookup[name] = struct{}{}
	}
	out := env[:0]
	for _, kv := range env {
		eq := indexByteFast(kv, '=')
		if eq <= 0 {
			continue
		}
		name := kv[:eq]
		if passSet {
			if _, ok := passSetLookup[name]; !ok {
				continue
			}
		}
		if _, drop := unsetLookup[name]; drop {
			continue
		}
		out = append(out, kv)
	}
	if path != "" {
		out = replaceOrAppendEnv(out, "PATH", path)
	}
	return out
}

// indexByteFast is a strings.IndexByte alias kept local so record.go
// doesn't grow a strings import just for one call site.
func indexByteFast(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// replaceOrAppendEnv sets NAME=value in env, replacing an existing
// entry or appending a new one.
func replaceOrAppendEnv(env []string, name, value string) []string {
	prefix := name + "="
	for i, kv := range env {
		if len(kv) >= len(prefix) && kv[:len(prefix)] == prefix {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

// --- Process attribute setters ---

func (sr *ServiceRecord) SetNice(n *int)                              { sr.nice = n }
func (sr *ServiceRecord) SetOOMScoreAdj(n *int)                       { sr.oomScoreAdj = n }
func (sr *ServiceRecord) SetNoNewPrivs(v bool)                        { sr.noNewPrivs = v }
func (sr *ServiceRecord) SetIOPrio(class, level int)                  { sr.ioPrioClass = class; sr.ioPrioLevel = level }
func (sr *ServiceRecord) SetCgroupPath(p string)                      { sr.cgroupPath = p }
func (sr *ServiceRecord) SetSlice(s string)                           { sr.slice = s }
func (sr *ServiceRecord) Slice() string                               { return sr.slice }
func (sr *ServiceRecord) SetCgroupSettings(s []process.CgroupSetting) { sr.cgroupSettings = s }
func (sr *ServiceRecord) SetRlimits(rl []process.Rlimit)              { sr.rlimits = rl }
func (sr *ServiceRecord) AddRlimit(rl process.Rlimit)                 { sr.rlimits = append(sr.rlimits, rl) }
func (sr *ServiceRecord) SetAmbientCaps(caps []uintptr)               { sr.ambientCaps = caps }
func (sr *ServiceRecord) SetBoundingCaps(caps []uintptr)              { sr.boundingCaps = caps }
func (sr *ServiceRecord) SetSecurebits(bits uint32)                   { sr.securebits = bits }
func (sr *ServiceRecord) SetCPUAffinity(cpus []uint)                  { sr.cpuAffinity = cpus }
func (sr *ServiceRecord) SetUmask(m *uint32)                          { sr.umask = m }

// SetAppArmor records AppArmor confinement: load is an absolute path to
// a profile parsed before start (empty = none); profile is the name the
// service transitions into on exec (empty = no switch).
func (sr *ServiceRecord) SetAppArmor(load, profile string) {
	sr.appArmorLoad = load
	sr.appArmorSwitch = profile
}

// AppArmor returns the configured load-profile path and switch profile.
func (sr *ServiceRecord) AppArmor() (load, profile string) {
	return sr.appArmorLoad, sr.appArmorSwitch
}

// SandboxConfig captures the full systemd-style filesystem-sandbox
// configuration. ServiceRecord stores one of these by value; the loader
// populates it from ServiceDescription and ApplyProcessAttrs forwards
// every field onto ExecParams. Any non-zero field causes the loader to
// auto-imply CLONE_NEWNS into the clone flags.
//
// Field semantics mirror their systemd counterparts; see slinit-service(5)
// for the user-facing description.
type SandboxConfig struct {
	PrivateTmp          bool
	ProtectSystem       string // "" | "yes" | "full" | "strict"
	ReadOnlyPaths       []string
	ReadWritePaths      []string
	ProtectHome         string // "" | "yes" | "read-only" | "tmpfs"
	InaccessiblePaths   []string
	ProtectProc         string // "" | "noaccess" | "invisible" | "ptraceable"
	ProcSubset          string // "" | "pid"
	BindPaths           []string // "src:dst" pairs (writable)
	BindReadOnlyPaths   []string // "src:dst" pairs (read-only)
	TemporaryFileSystem []string // "path[:options]" tmpfs mounts
}

// Active reports whether any sandbox knob is set. Used by the loader to
// decide whether to OR CLONE_NEWNS into the clone flags and by the
// process layer to gate the runner wrap.
func (c SandboxConfig) Active() bool {
	return c.PrivateTmp ||
		c.ProtectSystem != "" || len(c.ReadOnlyPaths) > 0 || len(c.ReadWritePaths) > 0 ||
		c.ProtectHome != "" || len(c.InaccessiblePaths) > 0 ||
		c.ProtectProc != "" || c.ProcSubset != "" ||
		len(c.BindPaths) > 0 || len(c.BindReadOnlyPaths) > 0 ||
		len(c.TemporaryFileSystem) > 0
}

// SetSandbox records the filesystem-sandbox configuration. The mount
// namespace is set up by slinit-runner; the loader is responsible for
// OR'ing CLONE_NEWNS into cloneflags when sandbox is in use.
func (sr *ServiceRecord) SetSandbox(c SandboxConfig) { sr.sandbox = c }

// SeccompConfig captures the systemd-style seccomp-bpf filter the
// service should run under. Items in Filter may include @group tokens
// and an optional leading '~' that flips into deny mode; the runner
// expands and compiles them via pkg/seccomp. Architectures defaults to
// the current arch when empty.
type SeccompConfig struct {
	Filter        []string
	Architectures []string
	ErrorAction   string // "" | "kill" | "log" | "trap" | errno name | errno number
	LogFilter     []string
}

// Active reports whether any seccomp knob is set.
func (c SeccompConfig) Active() bool {
	return len(c.Filter) > 0 || len(c.LogFilter) > 0 ||
		c.ErrorAction != "" || len(c.Architectures) > 0
}

// SetSeccomp records the seccomp filter spec. The loader auto-implies
// PR_SET_NO_NEW_PRIVS when this is in use, since the seccomp install
// requires it for non-root callers.
func (sr *ServiceRecord) SetSeccomp(c SeccompConfig) { sr.seccomp = c }

// Seccomp returns the recorded seccomp configuration.
func (sr *ServiceRecord) Seccomp() SeccompConfig { return sr.seccomp }

// SeccompActive reports whether seccomp filtering was requested.
func (sr *ServiceRecord) SeccompActive() bool { return sr.seccomp.Active() }

// HardeningConfig captures the systemd-style Restrict*/Protect* knobs
// that ship in slinit's hardening cluster. Each is a yes/no toggle
// except RestrictAddressFamilies, which carries an allow-list. The
// runner expands actives to seccomp filters (one union deny-mode
// filter for the block-outright knobs + per-directive arg-checking
// programs for the Restrict* set) and, where needed, a mount op.
type HardeningConfig struct {
	ProtectKernelTunables bool
	ProtectKernelModules  bool
	ProtectKernelLogs     bool
	ProtectClock          bool
	ProtectControlGroups  bool
	ProtectHostname       bool
	LockPersonality       bool
	// Bucket A extension — argument-checking BPF fragments + one
	// prctl. Landed as a follow-on to Tier B once pkg/seccomp grew
	// arg-inspection support.
	RestrictRealtime         bool
	RestrictNamespaces       bool
	RestrictSUIDSGID         bool
	RestrictFileSystems      bool
	RestrictAddressFamilies  []string
	RestrictAFEnabled        bool
	MemoryDenyWriteExecute   bool
}

// Active reports whether any hardening knob is set.
func (c HardeningConfig) Active() bool {
	return c.ProtectKernelTunables || c.ProtectKernelModules ||
		c.ProtectKernelLogs || c.ProtectClock ||
		c.ProtectControlGroups || c.ProtectHostname ||
		c.LockPersonality ||
		c.RestrictRealtime || c.RestrictNamespaces ||
		c.RestrictSUIDSGID || c.RestrictFileSystems ||
		c.RestrictAFEnabled || c.MemoryDenyWriteExecute
}

// SetHardening records the Restrict*/Protect* knob set. The loader
// auto-implies PR_SET_NO_NEW_PRIVS when any knob is active.
func (sr *ServiceRecord) SetHardening(c HardeningConfig) { sr.hardening = c }

// Hardening returns the recorded hardening config.
func (sr *ServiceRecord) Hardening() HardeningConfig { return sr.hardening }

// HardeningActive reports whether any Restrict*/Protect* knob is set.
func (sr *ServiceRecord) HardeningActive() bool { return sr.hardening.Active() }

// Bucket B setters + accessors.
func (sr *ServiceRecord) SetCoredumpFilter(s string)      { sr.coredumpFilter = s }
func (sr *ServiceRecord) SetTimerSlackNsec(n int64)       { sr.timerSlackNsec = n }
func (sr *ServiceRecord) SetMemoryKSM(b bool)             { sr.memoryKSM = b }
func (sr *ServiceRecord) SetPersonality(s string)         { sr.personality = s }
func (sr *ServiceRecord) SetIgnoreSIGPIPE(b *bool)        { sr.ignoreSIGPIPE = b }
func (sr *ServiceRecord) SetRemoveIPC(b bool)             { sr.removeIPC = b }
func (sr *ServiceRecord) SetUtmpMode(s string)            { sr.utmpMode = s }

// RemoveIPCEnabled reports whether the operator asked slinit to clean
// up SysV IPC + POSIX shm for the service's UID after each stop.
func (sr *ServiceRecord) RemoveIPCEnabled() bool { return sr.removeIPC }

// UtmpMode returns the operator-requested utmp record type: "init"
// (default), "login", or "user". Empty string is treated as "init"
// by the utmp write path.
func (sr *ServiceRecord) UtmpMode() string { return sr.utmpMode }

// Bucket C setters + accessors.
func (sr *ServiceRecord) SetCpusetPartition(s string)              { sr.cpusetPartition = s }
func (sr *ServiceRecord) SetCacheDirectoryQuota(n int64)           { sr.cacheDirectoryQuota = n }
func (sr *ServiceRecord) SetLogsDirectoryQuota(n int64)            { sr.logsDirectoryQuota = n }
func (sr *ServiceRecord) SetStateDirectoryQuota(n int64)           { sr.stateDirectoryQuota = n }
func (sr *ServiceRecord) SetCacheDirectoryAccounting(b bool)       { sr.cacheDirectoryAccounting = b }
func (sr *ServiceRecord) SetLogsDirectoryAccounting(b bool)        { sr.logsDirectoryAccounting = b }
func (sr *ServiceRecord) SetStateDirectoryAccounting(b bool)       { sr.stateDirectoryAccounting = b }
func (sr *ServiceRecord) SetStartupAllowedCPUs(s string)           { sr.startupAllowedCPUs = s }
func (sr *ServiceRecord) SetStartupAllowedMemoryNodes(s string)    { sr.startupAllowedMemoryNodes = s }
func (sr *ServiceRecord) SetSteadyAllowedCPUs(s string)            { sr.steadyAllowedCPUs = s }
func (sr *ServiceRecord) SetSteadyAllowedMemoryNodes(s string)     { sr.steadyAllowedMemoryNodes = s }

// applySteadyStateCgroup rewrites cpuset.cpus / cpuset.mems to the
// operator's steady-state values after the service reaches Started.
// A no-op unless startup-allowed-cpus / startup-allowed-memory-nodes
// were set — otherwise the initial cgroup write already carries the
// desired state. Best-effort: errors are logged, not surfaced (the
// service is already Started; failing here would be worse than
// leaving the wrong cpuset for a beat).
func (sr *ServiceRecord) applySteadyStateCgroup() {
	if sr.startupAllowedCPUs == "" && sr.startupAllowedMemoryNodes == "" {
		return
	}
	cgPath := sr.EffectiveCgroupPath()
	if cgPath == "" {
		return
	}
	if sr.startupAllowedCPUs != "" && sr.steadyAllowedCPUs != "" {
		writeCgroupFile(cgPath, "cpuset.cpus", sr.steadyAllowedCPUs, sr)
	}
	if sr.startupAllowedMemoryNodes != "" && sr.steadyAllowedMemoryNodes != "" {
		writeCgroupFile(cgPath, "cpuset.mems", sr.steadyAllowedMemoryNodes, sr)
	}
}

// writeCgroupFile writes value to <cgPath>/name. Best-effort: an error
// (missing file, no controller, unwritable delegation) is logged with
// the service name and swallowed.
func writeCgroupFile(cgPath, name, value string, sr *ServiceRecord) {
	full := cgPath + "/" + name
	if err := os.WriteFile(full, []byte(value), 0); err != nil {
		sr.services.logger.Error("Service '%s': cgroup retune write %s = %q: %v",
			sr.serviceName, name, value, err)
	}
}
func (sr *ServiceRecord) SetTimeoutStopFailureMode(m TimeoutFailureMode) {
	sr.timeoutStopFailureMode = m
}
func (sr *ServiceRecord) SetWatchdogSignal(s syscall.Signal)       { sr.watchdogSignal = s }
func (sr *ServiceRecord) SetFinalKillSignal(s syscall.Signal)      { sr.finalKillSignal = s }
func (sr *ServiceRecord) SetSurviveFinalKillSignal(b bool)         { sr.surviveFinalKillSignal = b }
func (sr *ServiceRecord) SetRestartKillSignal(s syscall.Signal)    { sr.restartKillSignal = s }
func (sr *ServiceRecord) SetKillMode(m KillMode)                   { sr.killMode = m }

// TimeoutStopFailureMode returns the signal picker used when the stop
// timeout expires.
func (sr *ServiceRecord) TimeoutStopFailureMode() TimeoutFailureMode {
	return sr.timeoutStopFailureMode
}

// WatchdogSignal returns the signal sent on watchdog miss (0 = use
// termSignal path).
func (sr *ServiceRecord) WatchdogSignal() syscall.Signal { return sr.watchdogSignal }

// FinalKillSignal returns the escalation-final signal (0 = SIGKILL).
func (sr *ServiceRecord) FinalKillSignal() syscall.Signal {
	if sr.finalKillSignal == 0 {
		return syscall.SIGKILL
	}
	return sr.finalKillSignal
}

// SurviveFinalKillSignal reports whether the operator opted out of the
// final SIGKILL escalation. Rare — used by services that manage their
// own hard-stop path.
func (sr *ServiceRecord) SurviveFinalKillSignal() bool { return sr.surviveFinalKillSignal }

// RestartKillSignal returns the signal to use when the current stop is
// part of a restart cycle (0 = fall back to termSignal).
func (sr *ServiceRecord) RestartKillSignal() syscall.Signal { return sr.restartKillSignal }

// KillMode returns the configured KillMode.
func (sr *ServiceRecord) KillMode() KillMode { return sr.killMode }

// InAutoRestart exposes the record's auto-restart marker for callers
// that need to pick behaviour based on whether the current stop is
// part of a restart cycle (e.g. restart-kill-signal).
func (sr *ServiceRecord) InAutoRestart() bool { return sr.inAutoRestart }

// CpusetPartition returns the cgroup v2 cpuset partition mode ("" =
// don't touch).
func (sr *ServiceRecord) CpusetPartition() string { return sr.cpusetPartition }

// StartupAllowedCPUs returns the cpuset spec used during Startup (empty
// = use the steady-state allowed-cpus / cgroup-cpuset-cpus).
func (sr *ServiceRecord) StartupAllowedCPUs() string { return sr.startupAllowedCPUs }

// StartupAllowedMemoryNodes returns the memory-node spec used during
// Startup.
func (sr *ServiceRecord) StartupAllowedMemoryNodes() string {
	return sr.startupAllowedMemoryNodes
}

// OpenFileRecord is the service-package copy of pkg/config's
// OpenFileSpec — kept here so the service package doesn't import
// config (would form a cycle). The loader converts one to the other
// at wire-up time.
type OpenFileRecord struct {
	Path    string
	FDName  string
	Options string
}

// Bucket D setters + accessors.
func (sr *ServiceRecord) SetPassEnvironment(names []string, set bool) {
	sr.passEnvironment = names
	sr.passEnvSet = set
}
func (sr *ServiceRecord) SetUnsetEnvironment(names []string) { sr.unsetEnvironment = names }
func (sr *ServiceRecord) SetExecSearchPath(p string)         { sr.execSearchPath = p }
func (sr *ServiceRecord) SetStandardInput(data []byte, set bool) {
	sr.standardInput = data
	sr.standardInputSet = set
}
func (sr *ServiceRecord) SetOpenFiles(files []OpenFileRecord)  { sr.openFiles = files }
func (sr *ServiceRecord) SetImportCredentials(pats []string)   { sr.importCredentials = pats }
func (sr *ServiceRecord) SetNotifyAccess(n NotifyAccess, set bool) {
	sr.notifyAccess = n
	sr.notifyAccessSet = set
}
func (sr *ServiceRecord) SetGuessMainPID(b bool) { sr.guessMainPID = b }
func (sr *ServiceRecord) SetSELinuxContext(s string)     { sr.selinuxContext = s }
func (sr *ServiceRecord) SetSMACKProcessLabel(s string)  { sr.smackProcessLabel = s }
func (sr *ServiceRecord) SetTTYPath(s string)            { sr.ttyPath = s }
func (sr *ServiceRecord) SetTTYColumns(n uint16)         { sr.ttyColumns = n }
func (sr *ServiceRecord) SetTTYRows(n uint16)            { sr.ttyRows = n }
func (sr *ServiceRecord) SetTTYVHangup(b bool)           { sr.ttyVHangup = b }
func (sr *ServiceRecord) SetTTYVTDisallocate(b bool)     { sr.ttyVTDisallocate = b }
func (sr *ServiceRecord) SetTTYReset(b bool)             { sr.ttyReset = b }

// TTYPath returns the tty device path (empty = no TTY handling).
func (sr *ServiceRecord) TTYPath() string { return sr.ttyPath }

// SELinuxContext returns the operator-requested SELinux domain (empty
// = don't touch).
func (sr *ServiceRecord) SELinuxContext() string { return sr.selinuxContext }

// SMACKProcessLabel returns the operator-requested SMACK label (empty
// = don't touch).
func (sr *ServiceRecord) SMACKProcessLabel() string { return sr.smackProcessLabel }

// PassEnvironment returns the operator-declared pass-list + whether
// the directive was set. When set is false the caller inherits every
// PID-1 env var; when true the caller keeps only names in the list.
func (sr *ServiceRecord) PassEnvironment() ([]string, bool) {
	return sr.passEnvironment, sr.passEnvSet
}

// UnsetEnvironment returns the operator-declared unset list.
func (sr *ServiceRecord) UnsetEnvironment() []string { return sr.unsetEnvironment }

// ExecSearchPath returns the override for $PATH (empty = inherit).
func (sr *ServiceRecord) ExecSearchPath() string { return sr.execSearchPath }

// StandardInput returns the baked stdin bytes + whether the directive
// was set. Empty slice with set=true is legal (means "provide EOF
// immediately"); nil with set=false means "inherit whatever the
// runner would attach."
func (sr *ServiceRecord) StandardInput() ([]byte, bool) {
	return sr.standardInput, sr.standardInputSet
}

// OpenFiles returns the pre-open specs to pass to the child.
func (sr *ServiceRecord) OpenFiles() []OpenFileRecord { return sr.openFiles }

// ImportCredentials returns the credential glob patterns.
func (sr *ServiceRecord) ImportCredentials() []string { return sr.importCredentials }

// NotifyAccess returns the picker + whether the directive was
// explicitly set (used by the ready-notification path to decide
// whether to wire the pipe at all).
func (sr *ServiceRecord) NotifyAccess() (NotifyAccess, bool) {
	return sr.notifyAccess, sr.notifyAccessSet
}

// GuessMainPID reports whether the cgroup-scan fallback is enabled
// for a bgprocess service that lacks a pid-file.
func (sr *ServiceRecord) GuessMainPID() bool { return sr.guessMainPID }

// DirectoryQuotas returns the (state, cache, logs) byte quotas
// requested by the operator (0 = not set).
func (sr *ServiceRecord) DirectoryQuotas() (state, cache, logs int64) {
	return sr.stateDirectoryQuota, sr.cacheDirectoryQuota, sr.logsDirectoryQuota
}

// SandboxActive reports whether any sandbox knob is set; the loader uses
// this to decide if it must auto-imply CLONE_NEWNS.
func (sr *ServiceRecord) SandboxActive() bool { return sr.sandbox.Active() }

// Sandbox returns the recorded sandbox configuration. Used by tests and
// the status reporter; the production read path is ApplyProcessAttrs.
func (sr *ServiceRecord) Sandbox() SandboxConfig { return sr.sandbox }

// SetDebug enables the pre-exec SIGSTOP debug stop for this service.
func (sr *ServiceRecord) SetDebug(b bool) { sr.debug = b }

// Debug reports whether the pre-exec debug stop is enabled.
func (sr *ServiceRecord) Debug() bool { return sr.debug }

// SetServiceDirs records the resolved auto-managed directories and the
// runtime-directory preservation policy (0=no, 1=yes, 2=restart).
func (sr *ServiceRecord) SetServiceDirs(dirs []process.ServiceDir, runtimePreserve int) {
	sr.serviceDirs = dirs
	sr.runtimeDirPreserve = runtimePreserve
}

// cleanupServiceDirs removes volatile (RuntimeDirectory) directories
// when the service stops, honouring runtimeDirPreserve. willRestart is
// true when the stop is part of a restart cycle.
func (sr *ServiceRecord) cleanupServiceDirs(willRestart bool) {
	for _, d := range sr.serviceDirs {
		if !d.Volatile {
			continue
		}
		if sr.runtimeDirPreserve == 1 {
			continue // yes: never remove
		}
		if sr.runtimeDirPreserve == 2 && willRestart {
			continue // restart: keep across a restart
		}
		if err := os.RemoveAll(d.Path); err != nil {
			sr.services.logger.Error("Service '%s': runtime directory %s cleanup failed: %v",
				sr.serviceName, d.Path, err)
		}
	}
}

// SetStartOnPath records the path-activation configuration. trigger must
// match the pathwatch.Trigger constants (1=exists, 2=changed,
// 3=modified, 4=dir-not-empty). Calling with trigger=0 clears the
// configuration.
func (sr *ServiceRecord) SetStartOnPath(path string, trigger int) {
	sr.startOnPath = path
	sr.startOnPathTrigger = trigger
}

// StartOnPath returns the path-activation configuration. Empty path and
// trigger==0 mean "not configured".
func (sr *ServiceRecord) StartOnPath() (path string, trigger int) {
	return sr.startOnPath, sr.startOnPathTrigger
}

// SetSchedPolicy programs the kernel scheduling policy (unix.SCHED_*).
// Pass policySet=false to keep the inherited policy; the rest of the
// sched-* setters are then ignored at apply time.
func (sr *ServiceRecord) SetSchedPolicy(policy uint32, policySet bool) {
	sr.schedPolicy = policy
	sr.schedPolicySet = policySet
}
func (sr *ServiceRecord) SetSchedPriority(p uint32) { sr.schedPriority = p }
func (sr *ServiceRecord) SetSchedDeadlineParams(runtime, deadline, period uint64) {
	sr.schedRuntime = runtime
	sr.schedDeadline = deadline
	sr.schedPeriod = period
}
func (sr *ServiceRecord) SetSchedResetOnFork(b bool) { sr.schedResetOnFork = b }

func (sr *ServiceRecord) SetMlockallFlags(flags int) { sr.mlockallFlags = flags }

// SetMemoryTHP records the per-service THP policy (never|madvise|always).
// Empty leaves the system default. Passed through to slinit-runner.
func (sr *ServiceRecord) SetMemoryTHP(mode string) { sr.memoryTHP = mode }
func (sr *ServiceRecord) SetNumaMempolicy(mode uint32, set bool) {
	sr.numaMempolicy = mode
	sr.numaMempolicySet = set
}
func (sr *ServiceRecord) SetNumaNodes(nodes []uint) { sr.numaNodes = nodes }

func (sr *ServiceRecord) SetCloneflags(flags uintptr)             { sr.cloneflags = flags }
func (sr *ServiceRecord) Cloneflags() uintptr                     { return sr.cloneflags }
func (sr *ServiceRecord) SetUidMappings(m []syscall.SysProcIDMap) { sr.uidMappings = m }
func (sr *ServiceRecord) SetGidMappings(m []syscall.SysProcIDMap) { sr.gidMappings = m }

// EffectiveCgroupPath returns the cgroup path for this service.
// Resolution order:
//  1. Explicit cgroup = path (backward compat, wins outright).
//  2. slice = name → /sys/fs/cgroup/<slice>/<svc-name>.
//  3. Daemon default cgroup path.
// Empty if none apply.
func (sr *ServiceRecord) EffectiveCgroupPath() string {
	if sr.cgroupPath != "" {
		return sr.cgroupPath
	}
	if sr.slice != "" {
		return "/sys/fs/cgroup/" + sr.slice + "/" + sr.serviceName
	}
	return sr.services.DefaultCgroupPath()
}

// ApplyProcessAttrs fills ExecParams with process attributes from this record.
func (sr *ServiceRecord) ApplyProcessAttrs(params *process.ExecParams) {
	params.Nice = sr.nice
	params.OOMScoreAdj = sr.oomScoreAdj
	params.NoNewPrivs = sr.noNewPrivs
	params.IOPrioClass = sr.ioPrioClass
	params.IOPrioLevel = sr.ioPrioLevel
	params.CgroupPath = sr.EffectiveCgroupPath()
	params.CgroupSettings = sr.cgroupSettings
	params.Rlimits = sr.rlimits
	params.AmbientCaps = sr.ambientCaps
	params.BoundingCaps = sr.boundingCaps
	params.Securebits = sr.securebits
	params.Umask = sr.umask
	params.CPUAffinity = sr.cpuAffinity
	if len(params.CPUAffinity) == 0 {
		params.CPUAffinity = sr.services.DefaultCPUAffinity()
	}
	if sr.schedPolicySet {
		params.SchedPolicy = sr.schedPolicy
		params.SchedPriority = sr.schedPriority
		params.SchedRuntime = sr.schedRuntime
		params.SchedDeadline = sr.schedDeadline
		params.SchedPeriod = sr.schedPeriod
		params.SchedResetOnFork = sr.schedResetOnFork
	}
	params.ServiceDirs = sr.serviceDirs
	params.ServiceName = sr.serviceName
	params.Credentials = sr.credentials
	params.AppArmorLoadProfile = sr.appArmorLoad
	params.AppArmorProfile = sr.appArmorSwitch
	params.SELinuxContext = sr.selinuxContext
	params.SMACKProcessLabel = sr.smackProcessLabel
	params.TTYPath = sr.ttyPath
	params.TTYColumns = sr.ttyColumns
	params.TTYRows = sr.ttyRows
	params.TTYVHangup = sr.ttyVHangup
	params.TTYVTDisallocate = sr.ttyVTDisallocate
	params.TTYReset = sr.ttyReset
	params.DebugStop = sr.debug
	params.MlockallFlags = sr.mlockallFlags
	params.NumaMempolicy = sr.numaMempolicy
	params.NumaMempolicySet = sr.numaMempolicySet
	params.NumaNodes = sr.numaNodes
	params.MemoryTHP = sr.memoryTHP
	params.RunnerPath = sr.services.RunnerPath()
	params.Cloneflags = sr.cloneflags
	params.UidMappings = sr.uidMappings
	params.GidMappings = sr.gidMappings
	params.PrivateTmp = sr.sandbox.PrivateTmp
	params.ProtectSystem = sr.sandbox.ProtectSystem
	params.ReadOnlyPaths = sr.sandbox.ReadOnlyPaths
	params.ReadWritePaths = sr.sandbox.ReadWritePaths
	params.ProtectHome = sr.sandbox.ProtectHome
	params.InaccessiblePaths = sr.sandbox.InaccessiblePaths
	params.ProtectProc = sr.sandbox.ProtectProc
	params.ProcSubset = sr.sandbox.ProcSubset
	params.BindPaths = sr.sandbox.BindPaths
	params.BindReadOnlyPaths = sr.sandbox.BindReadOnlyPaths
	params.TemporaryFileSystem = sr.sandbox.TemporaryFileSystem

	params.SeccompFilter = sr.seccomp.Filter
	params.SeccompArchitectures = sr.seccomp.Architectures
	params.SeccompErrorAction = sr.seccomp.ErrorAction
	params.SeccompLogFilter = sr.seccomp.LogFilter
	// seccomp(2) requires PR_SET_NO_NEW_PRIVS for non-root callers.
	// Auto-implying it is safer than failing the install with a
	// confusing EACCES; the operator can still set
	// `options=no-new-privs` explicitly when they want it without a
	// filter.
	if sr.seccomp.Active() {
		params.NoNewPrivs = true
	}

	params.ProtectKernelTunables = sr.hardening.ProtectKernelTunables
	params.ProtectKernelModules = sr.hardening.ProtectKernelModules
	params.ProtectKernelLogs = sr.hardening.ProtectKernelLogs
	params.ProtectClock = sr.hardening.ProtectClock
	params.ProtectControlGroups = sr.hardening.ProtectControlGroups
	params.ProtectHostname = sr.hardening.ProtectHostname
	params.LockPersonality = sr.hardening.LockPersonality
	params.RestrictRealtime = sr.hardening.RestrictRealtime
	params.RestrictNamespaces = sr.hardening.RestrictNamespaces
	params.RestrictSUIDSGID = sr.hardening.RestrictSUIDSGID
	params.RestrictFileSystems = sr.hardening.RestrictFileSystems
	params.RestrictAddressFamilies = sr.hardening.RestrictAddressFamilies
	params.RestrictAFEnabled = sr.hardening.RestrictAFEnabled
	params.MemoryDenyWriteExecute = sr.hardening.MemoryDenyWriteExecute
	params.CoredumpFilter = sr.coredumpFilter
	params.TimerSlackNsec = sr.timerSlackNsec
	params.MemoryKSM = sr.memoryKSM
	params.Personality = sr.personality
	if sr.ignoreSIGPIPE != nil {
		params.IgnoreSIGPIPE = *sr.ignoreSIGPIPE
		params.IgnoreSIGPIPESet = true
	}
	if sr.standardInputSet {
		// Copy defensively: the record may live longer than the exec,
		// and a caller stashing this slice for retry would otherwise
		// see it mutated by the child's stdin pipe writer goroutine.
		params.StdinBytes = append([]byte(nil), sr.standardInput...)
	}
	if len(sr.openFiles) > 0 {
		ofs := make([]process.OpenFileEntry, len(sr.openFiles))
		for i, f := range sr.openFiles {
			ofs[i] = process.OpenFileEntry{Path: f.Path, FDName: f.FDName, Options: f.Options}
		}
		params.OpenFiles = ofs
	}
	if sr.hardening.Active() {
		params.NoNewPrivs = true
	}

	// Inject dinit-compatible query env vars
	params.Env = append(params.Env, "SLINIT_SERVICENAME="+sr.serviceName)
	if sr.serviceDir != "" {
		params.Env = append(params.Env, "SLINIT_SERVICEDSCDIR="+sr.serviceDir)
	}
}

// Default log buffer implementations (overridden by process-based services)
func (sr *ServiceRecord) GetLogBuffer() *LogBuffer { return nil }
func (sr *ServiceRecord) GetLogType() LogType      { return LogNone }
func (sr *ServiceRecord) GetLogFile() string       { return "" }

// Boot timing getters
func (sr *ServiceRecord) StartRequestTime() time.Time { return sr.startRequestTime }
func (sr *ServiceRecord) StartedTime() time.Time      { return sr.startedTime }
func (sr *ServiceRecord) StoppedTime() time.Time      { return sr.stoppedTime }

// StartupDuration returns the time from start request to STARTED state.
// Returns 0 if the service hasn't reached STARTED yet.
func (sr *ServiceRecord) StartupDuration() time.Duration {
	if sr.startedTime.IsZero() || sr.startRequestTime.IsZero() {
		return 0
	}
	return sr.startedTime.Sub(sr.startRequestTime)
}

// IsFundamentallyStopped returns true if the service is effectively stopped:
// either in STOPPED state, or STARTING but still waiting for deps.
func (sr *ServiceRecord) IsFundamentallyStopped() bool {
	return sr.state.Load() == StateStopped ||
		(sr.state.Load() == StateStarting && sr.waitingForDeps)
}

// CanInterruptStop returns true if a STOPPING service can immediately go back to STARTED.
func (sr *ServiceRecord) CanInterruptStop() bool {
	return sr.waitingForDeps && !sr.forceStop
}

// --- State machine methods ---

// Start marks the service as explicitly started and initiates the start sequence.
func (sr *ServiceRecord) Start() {
	if sr.pinnedStopped {
		return
	}

	// Explicit start clears the 'down' marker
	if sr.markedDown {
		sr.markedDown = false
	}

	if !sr.startExplicit {
		sr.requiredBy++
		sr.startExplicit = true
	}

	sr.doStart()
}

// Wake re-attaches a service to its active dependents without marking it as
// explicitly started. If no active dependents hold a non-ordering dependency,
// returns false (nothing to wake for). Otherwise increments requiredBy via
// the dependency acquisition and starts the service.
func (sr *ServiceRecord) Wake() bool {
	if sr.pinnedStopped {
		return false
	}
	// `manual = yes` blocks wake the same way it blocks Require:
	// auto-attaching to active dependents is exactly the kind of
	// auto-activation manual is meant to refuse.
	if sr.manualStart && !sr.startExplicit {
		return false
	}

	found := false
	for _, dept := range sr.dependents {
		if dept.IsOnlyOrdering() {
			continue
		}
		from := dept.From
		fromState := from.Record().state.Load()
		if fromState == StateStarted || fromState == StateStarting {
			found = true
			if !dept.HoldingAcq {
				dept.HoldingAcq = true
				sr.requiredBy++
			}
		}
	}

	if !found {
		return false
	}

	sr.doStart()
	return true
}

// Stop removes explicit activation and optionally stops the service.
func (sr *ServiceRecord) Stop(bringDown bool) {
	if sr.startExplicit {
		sr.startExplicit = false
		sr.requiredBy--
	}

	if bringDown || sr.requiredBy == 0 {
		sr.desired.Store(StateStopped)
	}

	if sr.IsStartPinned() {
		return
	}

	if sr.requiredBy == 0 {
		bringDown = true
		sr.propRelease = !sr.propRequire
		if sr.propRelease {
			sr.services.AddPropQueue(sr.self)
		}
	}

	if bringDown && sr.state.Load() != StateStopped {
		sr.stopReason = ReasonNormal
		sr.doStop(false)
	}
}

// Restart restarts the service. Returns true if restart was issued.
func (sr *ServiceRecord) Restart() bool {
	if sr.state.Load() == StateStarted {
		sr.stopReason = ReasonNormal
		sr.forceStop = true
		sr.doStop(true)
		return true
	}
	return false
}

// ForcedStop marks this service and all dependents for forced stop.
func (sr *ServiceRecord) ForcedStop() {
	if sr.state.Load() != StateStopped {
		sr.forceStop = true
		if !sr.IsStartPinned() {
			sr.propStop = true
			sr.services.AddPropQueue(sr.self)
		}
	}
}

// PinStart pins the service in started state.
// SetMarkedDown sets the down-file marker.
func (sr *ServiceRecord) SetMarkedDown(v bool) { sr.markedDown = v }

// IsMarkedDown returns true if a 'down' file was found for this service.
func (sr *ServiceRecord) IsMarkedDown() bool { return sr.markedDown }

func (sr *ServiceRecord) PinStart() {
	if !sr.pinnedStarted {
		if !sr.deptPinnedStarted {
			for _, dep := range sr.dependsOn {
				if dep.IsHard() {
					toRec := dep.To.Record()
					if !toRec.deptPinnedStarted {
						toRec.propPinDpt = true
						sr.services.AddPropQueue(dep.To)
					}
				}
			}
		}
		sr.pinnedStarted = true
	}
}

// PinStop pins the service in stopped state.
func (sr *ServiceRecord) PinStop() {
	sr.pinnedStopped = true
}

// Unpin removes both start and stop pins.
func (sr *ServiceRecord) Unpin() {
	if sr.pinnedStarted {
		sr.pinnedStarted = false

		if sr.deptPinnedStarted {
			return
		}

		for _, dep := range sr.dependsOn {
			if dep.IsHard() {
				toRec := dep.To.Record()
				if toRec.deptPinnedStarted {
					toRec.propPinDpt = true
					sr.services.AddPropQueue(dep.To)
				}
			}
		}

		if sr.state.Load() == StateStarted {
			if sr.requiredBy == 0 {
				sr.propRelease = true
				sr.services.AddPropQueue(sr.self)
			}
			if sr.desired.Load() == StateStopped || sr.forceStop {
				sr.doStop(false)
				// Note: caller is responsible for draining queues
				// (e.g. StopAllServices calls processQueuesLocked,
				// handleUnpinService calls ProcessQueues).
			}
		}
	}
	if sr.pinnedStopped {
		sr.pinnedStopped = false
	}
}

// Require increments the required_by count and triggers start if needed.
//
// `manual = yes` blocks the start trigger but still bumps requiredBy:
// the bookkeeping has to stay consistent with the caller's HoldingAcq
// flag (so a later Release() doesn't underflow). The service stays
// STOPPED; hard-depending callers see their dep unsatisfied and block
// or fail. That is the intended trade-off — `manual` declares "I am
// opt-in", and the operator owns the order via explicit
// `slinitctl start`.
func (sr *ServiceRecord) Require() {
	sr.requiredBy++
	if sr.manualStart && !sr.startExplicit {
		return
	}
	if sr.requiredBy == 1 {
		if sr.state.Load() != StateStarting && sr.state.Load() != StateStarted {
			sr.propStart = true
			sr.services.AddPropQueue(sr.self)
		}
	}
}

// Release decrements the required_by count and triggers stop if appropriate.
func (sr *ServiceRecord) Release(issueStop bool) {
	sr.requiredBy--
	if sr.requiredBy == 0 {
		if sr.state.Load() == StateStopping {
			if sr.desired.Load() == StateStarted && !sr.IsStartPinned() {
				sr.notifyListeners(EventStartCancelled)
			}
		}
		sr.desired.Store(StateStopped)

		if sr.IsStartPinned() {
			return
		}

		sr.propRelease = !sr.propRequire
		sr.propRequire = false
		if sr.propRelease {
			sr.services.AddPropQueue(sr.self)
		}

		if sr.state.Load() != StateStopped && sr.state.Load() != StateStopping && issueStop {
			sr.stopReason = ReasonNormal
			sr.doStop(false)
		}
	}
}

// ReleaseDependencies releases all held dependency acquisitions.
func (sr *ServiceRecord) ReleaseDependencies() {
	for _, dep := range sr.dependsOn {
		if dep.HoldingAcq {
			dep.HoldingAcq = false
			dep.To.Record().Release(true)
		}
	}
}

// DoPropagation processes pending propagation flags.
func (sr *ServiceRecord) DoPropagation() {
	if sr.propRequire {
		for _, dep := range sr.dependsOn {
			if !dep.IsOnlyOrdering() {
				dep.To.Record().Require()
				dep.HoldingAcq = true
			}
		}
		sr.propRequire = false
	}

	if sr.propRelease {
		sr.ReleaseDependencies()
		sr.propRelease = false
	}

	if sr.propFailure {
		sr.propFailure = false
		sr.stopReason = ReasonDepFailed
		sr.state.Store(StateStopped)
		sr.failedToStart(true, true)
	}

	if sr.propStart {
		sr.propStart = false
		sr.doStart()
	}

	if sr.propStop {
		sr.propStop = false
		sr.doStop(sr.inUserRestart)
	}

	if sr.propPinDpt {
		sr.propPinDpt = false
		deptPin := false
		for _, dept := range sr.dependents {
			if dept.IsHard() && dept.From.Record().IsStartPinned() {
				deptPin = true
				break
			}
		}
		if deptPin != sr.deptPinnedStarted {
			sr.deptPinnedStarted = deptPin
			for _, dep := range sr.dependsOn {
				if dep.IsHard() {
					toRec := dep.To.Record()
					if toRec.deptPinnedStarted != deptPin {
						toRec.propPinDpt = true
						sr.services.AddPropQueue(dep.To)
					}
				}
			}

			if !sr.deptPinnedStarted && !sr.pinnedStarted {
				if (sr.desired.Load() == StateStopped || sr.forceStop) && sr.state.Load() == StateStarted {
					sr.doStop(false)
				}
			}
		}
	}
}

// ExecuteTransition performs a state transition based on the current and desired states.
func (sr *ServiceRecord) ExecuteTransition() {
	if sr.state.Load() == StateStarting {
		if sr.checkDepsStarted() {
			sr.waitingForDeps = false
			sr.allDepsStarted()
		}
	} else if sr.state.Load() == StateStopping {
		if sr.stopCheckDependents() {
			sr.waitingForDeps = false
			sr.self.BringDown()
		}
	}
}

// --- Internal state machine helpers ---

func (sr *ServiceRecord) notifyListeners(event ServiceEvent) {
	sr.listenerMu.Lock()
	n := len(sr.listeners)
	if n == 0 {
		sr.listenerMu.Unlock()
		return
	}
	// Fast path: single listener (most common case — one control connection)
	if n == 1 {
		l := sr.listeners[0]
		sr.listenerMu.Unlock()
		l.ServiceEvent(sr.self, event)
		return
	}
	snapshot := make([]ServiceListener, n)
	copy(snapshot, sr.listeners)
	sr.listenerMu.Unlock()
	for _, l := range snapshot {
		l.ServiceEvent(sr.self, event)
	}
}

func (sr *ServiceRecord) doStart() {
	wasActive := sr.state.Load() != StateStopped

	sr.desired.Store(StateStarted)

	if sr.pinnedStopped {
		if !wasActive {
			sr.failedToStart(false, false)
		}
		return
	}

	// 'down' marker prevents auto-start (e.g., as dependency)
	// Explicit Start() clears markedDown before calling doStart()
	if sr.markedDown {
		sr.desired.Store(StateStopped)
		return
	}

	// Re-attach soft dependents when starting again
	if !wasActive {
		for _, dept := range sr.dependents {
			if !dept.IsHard() {
				deptState := dept.From.Record().state.Load()
				if !dept.HoldingAcq &&
					(deptState == StateStarted || deptState == StateStarting) {
					dept.HoldingAcq = true
					sr.requiredBy++
				}
			}
		}
	}

	if wasActive {
		if sr.state.Load() != StateStopping {
			return
		}
		if !sr.CanInterruptStop() {
			return
		}
		sr.notifyListeners(EventStopCancelled)
	} else {
		sr.services.ServiceActive(sr.self)
		sr.propRequire = !sr.propRelease
		sr.propRelease = false
		if sr.propRequire {
			sr.services.AddPropQueue(sr.self)
		}
	}

	sr.initiateStart()
}

func (sr *ServiceRecord) initiateStart() {
	sr.startFailed = false
	// Clear the per-session Started()-emitted flag so the next
	// successful start emits its own boot-console line.
	sr.startedEmitted = false
	sr.startSkipped = false
	sr.startRequestTime = time.Now()
	sr.state.Store(StateStarting)
	sr.waitingForDeps = true

	// Arm the whole-job timer (JobTimeoutSec=): dep-wait + start-timeout
	// combined. Disarmed when the service reaches STARTED / STOPPED so
	// it never fires against a healthy service.
	sr.armJobTimeoutTimer()

	if sr.startCheckDependencies() {
		sr.services.AddTransitionQueue(sr.self)
	}
}

func (sr *ServiceRecord) startCheckDependencies() bool {
	allStarted := true

	for _, dep := range sr.dependsOn {
		to := dep.To
		if dep.IsOnlyOrdering() && to.Record().state.Load() != StateStarting {
			continue
		}
		if to.Record().state.Load() != StateStarted {
			dep.WaitingOn = true
			allStarted = false
		}
	}

	for _, dept := range sr.dependents {
		if !dept.WaitingOn && dept.IsOnlyOrdering() {
			if dept.From.Record().state.Load() == StateStarting {
				dept.WaitingOn = true
			}
		}
	}

	return allStarted
}

func (sr *ServiceRecord) checkDepsStarted() bool {
	for _, dep := range sr.dependsOn {
		if dep.WaitingOn {
			return false
		}
	}
	return true
}

func (sr *ServiceRecord) allDepsStarted() {
	if sr.Flags.StartsOnConsole && !sr.haveConsole {
		sr.queueForConsole()
		return
	}

	sr.waitingForDeps = false

	// Check start limiter (skip during shutdown — don't queue services)
	if limiter := sr.services.GetStartLimiter(); limiter != nil && !sr.services.IsShuttingDown() {
		ok, waitCh := limiter.Acquire(sr.self)
		if !ok {
			sr.waitingForStartSlot = true
			// Wait for slot in a goroutine to avoid blocking the queue
			go func() {
				<-waitCh
				sr.services.queueMu.Lock()
				sr.waitingForStartSlot = false
				if !sr.self.BringUp() {
					sr.state.Store(StateStopping)
					sr.failedToStart(false, true)
				}
				sr.services.processQueuesLocked()
				sr.services.queueMu.Unlock()
			}()
			return
		}
	}

	if !sr.self.BringUp() {
		sr.state.Store(StateStopping)
		sr.failedToStart(false, true)
	}
}

// Started is called when the service has successfully started.
func (sr *ServiceRecord) Started() {
	// Idempotent: multiple Started() calls per session (races between
	// ready-notification / ready-check / immediate-fallback paths, or
	// a re-entrant auto-restart flow) collapse to one observable
	// event. Prevents the duplicate "[ OK ] name" boot-console line
	// operators observed on ceres v1.10.46. Cleared in initiateStart()
	// for the next session.
	if sr.startedEmitted {
		return
	}
	sr.startedEmitted = true

	// Job completed in time — disarm the whole-job timer so a late
	// AfterFunc firing can't fail an already-STARTED service.
	sr.cancelJobTimeoutTimer()

	// Release start limiter slot
	if limiter := sr.services.GetStartLimiter(); limiter != nil {
		limiter.Release(sr.self)
	}

	if sr.haveConsole && !sr.Flags.RunsOnConsole {
		sr.releaseConsole()
	}

	sr.startedTime = time.Now()

	// systemd StartupAllowedCPUs= / StartupAllowedMemoryNodes= — after
	// the service reaches Started, retune the cgroup cpuset to the
	// steady-state values. No-op unless a startup-* override was set
	// AND a steady-state cgroup-cpuset-* value was configured for the
	// retune target.
	sr.applySteadyStateCgroup()

	// Auto-detect boot service reaching STARTED
	if sr.services.bootServiceName != "" && sr.serviceName == sr.services.bootServiceName && sr.services.bootReadyTime.IsZero() {
		sr.services.bootReadyTime = time.Now()
		if sr.services.OnBootReady != nil {
			sr.services.OnBootReady()
		}
	}

	// Signal filesystem/logging readiness
	if sr.Flags.RWReady && !sr.services.RWReady() {
		sr.services.SetRWReady()
		sr.services.logger.Info("Filesystem is now read-write (service '%s')", sr.serviceName)
		if sr.services.OnRWReady != nil {
			sr.services.OnRWReady()
		}
	}
	if sr.Flags.LogReady && !sr.services.LogReady() {
		sr.services.SetLogReady()
		sr.services.logger.Info("Logging system is now ready (service '%s')", sr.serviceName)
	}

	sr.services.logger.ServiceStarted(sr.serviceName)
	sr.state.Store(StateStarted)
	sr.notifyListeners(EventStarted)

	// Arm the runtime-max-sec cap (if any) now that we're STARTED.
	// Skipped path leaves runtimeMaxTimer nil; an existing timer from a
	// previous start has already been cancelled in Stopped().
	sr.armRuntimeMaxTimer()

	// Start the cgroup OOM watcher (if oom-policy demands it).
	sr.armOOMWatcher()

	// Arm the cgroup v2 PSI pressure watches (if any pressure-watch is
	// enabled). No-op when no cgroup path is set — pressure files live
	// under the cgroup tree.
	sr.armPSIWatcher()

	if sr.forceStop || sr.desired.Load() == StateStopped {
		sr.doStop(false)
		return
	}

	// Notify dependents
	for _, dept := range sr.dependents {
		if dept.WaitingOn {
			dept.From.Record().dependencyStarted()
			dept.WaitingOn = false
		}
	}
}

// Stopped is called when the service has actually stopped.
func (sr *ServiceRecord) Stopped() {
	sr.stoppedTime = time.Now()

	// Disarm the runtime-max-sec timer (if any). Safe to call when no
	// timer was armed (service never reached STARTED, or runtime-max
	// wasn't configured).
	sr.cancelRuntimeMaxTimer()

	// Also disarm the job-timeout timer for the same reason — the
	// job has ended (either successfully or by failure); no need for
	// the timer to fire and race a re-start.
	sr.cancelJobTimeoutTimer()

	// Cancel the OOM watcher (if armed). Idempotent — nil-safe.
	sr.cancelOOMWatcher()

	// Cancel any armed PSI pressure watchers. Idempotent — nil-safe.
	sr.cancelPSIWatcher()

	// Tear down the per-service /run/credentials/<svc>/ tmpfs (no-op
	// when no credentials were configured). Cleanup errors are logged
	// but don't block the rest of Stopped — the daemon will hold a
	// pointer to the umount target until next boot at worst.
	if len(sr.credentials) > 0 {
		if err := process.CleanupCredentials(sr.serviceName); err != nil {
			sr.services.logger.Error(
				"Service '%s': credentials cleanup: %v", sr.serviceName, err)
		}
	}

	// Release the dynamic-user transient UID (no-op when disabled).
	sr.releaseDynamicUID()

	// Tear down the $NOTIFY_SOCKET listener (no-op when disabled). The
	// stored fds outlive the socket — a fresh listener is created on the
	// next BringUp; see below for their lifecycle.
	sr.teardownNotifySocket()

	if sr.haveConsole {
		sr.releaseConsole()
	}

	sr.forceStop = false

	willRestart := sr.desired.Load() == StateStarted && !sr.pinnedStopped
	// restart-limit-count exhausted overrides willRestart: settle into
	// the FAILED stable state instead of trying initiateStart again.
	if sr.restartLimitExhausted {
		willRestart = false
		sr.startFailed = true
		sr.desired.Store(StateStopped)
		sr.restartLimitExhausted = false
		sr.services.logger.Error(
			"Service '%s': restart-limit-count exhausted, marking failed",
			sr.serviceName)
		// systemd StartLimitAction=: escalate a rate-limit failure into a
		// system-level action (reboot/kexec/etc). Fires here — not from
		// chooseStoppedAction — so it's independent of the failure-action
		// path and always triggers when the operator asked for it.
		if act := sr.startLimitAction; act != ActionNone {
			if cb := sr.services.OnSystemAction; cb != nil {
				sr.services.logger.Info(
					"Service '%s': start-limit-action=%s — initiating system action",
					sr.serviceName, act)
				cb(act, sr.rebootArgument)
			}
		}
	}

	// fdStore lifetime — decided after willRestart so a stop-then-start
	// (Restart(), auto-restart) preserves stashed fds regardless of the
	// file-descriptor-store-preserve setting. That directive only
	// governs final deactivation, per systemd v261 semantics:
	//   ""|"no"      → close on final deactivation (systemd default)
	//   "yes"        → keep across deactivation until daemon exits
	//   "on-success" → keep only if this stop is a clean exit
	if sr.fdStore != nil {
		keep := willRestart
		if !keep {
			switch sr.fdStorePreserve {
			case "yes":
				keep = true
			case "on-success":
				es := sr.self.GetExitStatus()
				keep = sr.stopReason == ReasonNormal ||
					(sr.stopReason == ReasonTerminated && es.Exited() && es.ExitCode() == 0)
			}
		}
		if !keep {
			sr.fdStore.Close()
		}
	}

	sr.cleanupServiceDirs(willRestart)

	// If we won't restart, break soft dependencies
	if !willRestart {
		for _, dept := range sr.dependents {
			if !dept.IsHard() {
				if dept.WaitingOn {
					dept.WaitingOn = false
					dept.From.Record().dependencyStarted()
				}
				if dept.HoldingAcq {
					dept.HoldingAcq = false
					sr.Release(false)
				}
			}
		}
	}

	// Signal dependencies that we've stopped
	for _, dep := range sr.dependsOn {
		dep.To.Record().dependentStopped()
	}

	sr.state.Store(StateStopped)

	if willRestart {
		// Record this as a supervisor-driven restart for heartbeat /
		// health-signal accounting. First-boot BringUp does not count.
		sr.services.NoteRestart()

		// Restart any PREPARED_BY dependencies first. They are hard deps
		// (IsHard returns true), so startCheckDependencies in initiateStart
		// below will see them in non-STARTED state and make us wait until
		// they reach STARTED again.
		for _, dep := range sr.dependsOn {
			if dep.DepType == DepPreparedBy {
				dep.To.Record().Restart()
			}
		}
		sr.initiateStart()
	} else {
		sr.self.BecomingInactive()

		if sr.startExplicit {
			sr.startExplicit = false
			sr.Release(false)
		} else if sr.requiredBy == 0 {
			sr.services.ServiceInactive(sr.self)
		}
	}

	// systemd-style failure-action / success-action: pick whichever
	// applies and let main's shutdown initiator handle it. The hook is
	// invoked AFTER state is STOPPED so a reboot-action service is
	// reported as STOPPED to listeners that fire before the reboot.
	if action := sr.chooseStoppedAction(willRestart); action != ActionNone {
		if cb := sr.services.OnSystemAction; cb != nil {
			kind := "success"
			if action == sr.failureAction {
				kind = "failure"
			}
			sr.services.logger.Info(
				"Service '%s': %s-action=%s — initiating system action",
				sr.serviceName, kind, action)
			cb(action, sr.rebootArgument)
		}
	}

	if !sr.startFailed {
		sr.services.logger.ServiceStopped(sr.serviceName)

		// Chain to next service if applicable
		if sr.chainTo != "" && !sr.services.IsShuttingDown() {
			shouldChain := sr.Flags.AlwaysChain ||
				(sr.stopReason.DidFinish() && sr.self.GetExitStatus().Exited() &&
					sr.self.GetExitStatus().ExitCode() == 0 && !willRestart)
			if shouldChain {
				chainSvc, err := sr.services.LoadService(sr.chainTo)
				if err != nil {
					sr.services.logger.Error("Couldn't chain to service %s: %v", sr.chainTo, err)
				} else {
					chainSvc.Start()
				}
			}
		}
	}
	sr.notifyListeners(EventStopped)
}

// failedToStart handles start failure.
func (sr *ServiceRecord) failedToStart(depFailed bool, immediateStop bool) {
	// Release start limiter slot or cancel waiting
	if limiter := sr.services.GetStartLimiter(); limiter != nil {
		if sr.waitingForStartSlot {
			limiter.CancelWait(sr.self)
			sr.waitingForStartSlot = false
		} else {
			limiter.Release(sr.self)
		}
	}

	sr.desired.Store(StateStopped)

	if sr.waitingForConsole {
		sr.services.UnqueueConsole(sr.self)
		sr.waitingForConsole = false
	}

	if sr.startExplicit {
		sr.startExplicit = false
		sr.Release(false)
	}

	// Cancel start of dependents
	for _, dept := range sr.dependents {
		switch dept.DepType {
		case DepRegular, DepPreparedBy, DepMilestone:
			if dept.From.Record().state.Load() == StateStarting {
				dept.From.Record().propFailure = true
				sr.services.AddPropQueue(dept.From)
			}
		case DepWaitsFor, DepSoft, DepBefore, DepAfter:
			if dept.WaitingOn {
				dept.WaitingOn = false
				dept.From.Record().dependencyStarted()
			}
		}

		if dept.HoldingAcq {
			dept.HoldingAcq = false
			sr.Release(false)
		}
	}

	sr.startFailed = true
	sr.services.logger.ServiceFailed(sr.serviceName, depFailed)
	sr.notifyListeners(EventFailedStart)
	sr.pinnedStarted = false

	if immediateStop {
		sr.Stopped()
	}
}

func (sr *ServiceRecord) doStop(withRestart bool) {
	if sr.IsStartPinned() {
		return
	}

	sr.inAutoRestart = false
	sr.inUserRestart = false

	forRestart := withRestart
	// systemd RestartMode=direct: keep dependents up through this
	// service's restart cycle. `withRestart` still gates the parent
	// itself, only the cascade to dependents is suppressed.
	restartDeps := withRestart && sr.restartMode != RestartModeDirect

	if !withRestart {
		// upstart-style `normal exit`: codes / signals the operator
		// declared as success suppress respawn even with restart=yes.
		// Apply this *before* the per-mode logic so it shadows both
		// RestartAlways and RestartOnFailure uniformly.
		exitStatus := sr.self.GetExitStatus()
		normal := sr.IsNormalExit(exitStatus)

		// Track whether auto-restart was asked for; if CheckRestart
		// denies it (rate-limit exhausted) we land in the
		// restart-limit-exhausted branch below and treat the service
		// as failed instead of looping.
		wantedRestart := false

		// systemd RestartForceExitStatus: codes that force a restart
		// regardless of the `restart =` setting. Applied FIRST so
		// even a normal-exit code entry gets overridden — the
		// operator's declared "always come back on THIS code" wins
		// over both `restart = no` and `normal-exit`.
		if sr.desired.Load() == StateStarted && sr.IsForceRestartExit(exitStatus) {
			wantedRestart = true
			forRestart = sr.self.CheckRestart()
			sr.inAutoRestart = forRestart
			// Skip the per-mode auto-restart block below — the force
			// path already decided.
			sr.restartLimitExhausted = wantedRestart && !forRestart
			goto forceHandled
		}

		// Check for auto-restart
		if sr.autoRestart == RestartAlways && sr.desired.Load() == StateStarted {
			if !normal {
				wantedRestart = true
				forRestart = sr.self.CheckRestart()
				sr.inAutoRestart = forRestart
			}
		} else if sr.autoRestart == RestartOnFailure && sr.desired.Load() == StateStarted {
			if !normal {
				if exitStatus.Signaled() {
					// Don't auto-restart for administrative signals (matching dinit)
					sig := exitStatus.Signal()
					if sig != syscall.SIGHUP && sig != syscall.SIGINT &&
						sig != syscall.SIGUSR1 && sig != syscall.SIGUSR2 &&
						sig != syscall.SIGTERM {
						wantedRestart = true
						forRestart = sr.self.CheckRestart()
						sr.inAutoRestart = forRestart
					}
				} else if exitStatus.Exited() && exitStatus.ExitCode() != 0 {
					wantedRestart = true
					forRestart = sr.self.CheckRestart()
					sr.inAutoRestart = forRestart
				}
			}
		}

		// Remember whether auto-restart was wanted but denied so the
		// post-stop logic below can route the service into the FAILED
		// state instead of letting it loop. Without this, Stopped() sees
		// desired=Started and re-enters initiateStart, which resets
		// startFailed=false and BringUp clears exitStatus={} — leaving
		// slinitctl is-failed racy because it can sample mid-iteration.
		sr.restartLimitExhausted = wantedRestart && !forRestart
	forceHandled:
	}

	// If we won't restart, release explicit activation
	if !forRestart {
		if sr.startExplicit {
			sr.startExplicit = false
			sr.Release(false)
		}
	}

	allDepsStopped := sr.stopDependents(forRestart, restartDeps)

	if sr.state.Load() != StateStarted {
		if sr.state.Load() == StateStarting {
			if !sr.waitingForDeps && !sr.waitingForConsole {
				if !sr.self.CanInterruptStart() {
					return
				}
				if !sr.self.InterruptStart() {
					sr.notifyListeners(EventStartCancelled)
					return
				}
			} else if sr.waitingForConsole {
				sr.services.UnqueueConsole(sr.self)
				sr.waitingForConsole = false
			}

			sr.notifyListeners(EventStartCancelled)
		} else {
			return
		}
	}

	sr.state.Store(StateStopping)
	sr.waitingForDeps = !allDepsStopped
	if allDepsStopped {
		sr.services.AddTransitionQueue(sr.self)
	}
}

func (sr *ServiceRecord) dependencyStarted() {
	if (sr.state.Load() == StateStarting || sr.state.Load() == StateStarted) && sr.waitingForDeps {
		sr.services.AddTransitionQueue(sr.self)
	}
}

func (sr *ServiceRecord) dependentStopped() {
	if sr.state.Load() == StateStopping && sr.waitingForDeps {
		sr.services.AddTransitionQueue(sr.self)
	}
	// systemd StopWhenUnneeded=: once the last dependent falls off and
	// nobody explicitly asked us to be up, spontaneously stop. Guarded
	// on stopCheckDependents so we don't rip out a service that still
	// has another live dependent, and on !startExplicit so an operator
	// hold beats the auto-stop.
	if sr.stopWhenUnneeded &&
		sr.state.Load() == StateStarted &&
		!sr.startExplicit &&
		!sr.pinnedStarted &&
		sr.stopCheckDependents() {
		sr.desired.Store(StateStopped)
		sr.propStop = true
		sr.services.AddPropQueue(sr.self)
	}
}

func (sr *ServiceRecord) stopCheckDependents() bool {
	for _, dept := range sr.dependents {
		if dept.IsHard() && dept.HoldingAcq && !dept.WaitingOn {
			return false
		}
	}
	return true
}

func (sr *ServiceRecord) stopDependents(forRestart bool, restartDeps bool) bool {
	allStopped := true

	for _, dept := range sr.dependents {
		if dept.IsHard() {
			depFrom := dept.From.Record()

			if !depFrom.IsFundamentallyStopped() {
				allStopped = false
			}

			if sr.forceStop {
				if sr.desired.Load() == StateStopped {
					depFrom.stopReason = ReasonDepFailed
					depFrom.UnrecoverableStop()
				} else {
					depFrom.ForcedStop()
				}
			}

			if dept.From.Record().state.Load() != StateStopped {
				if sr.desired.Load() == StateStopped {
					if depFrom.desired.Load() != StateStopped {
						depFrom.desired.Store(StateStopped)
						if depFrom.startExplicit {
							depFrom.startExplicit = false
							depFrom.Release(true)
						}
						depFrom.propStop = true
						sr.services.AddPropQueue(dept.From)
					}
				} else if restartDeps && dept.From.Record().state.Load() != StateStopping {
					depFrom.stopReason = ReasonDepRestart
					depFrom.inUserRestart = true
					depFrom.propStop = true
					sr.services.AddPropQueue(dept.From)
				}
			}
		} else if !forRestart {
			// Soft dependency: break the link
			if dept.WaitingOn {
				dept.WaitingOn = false
				dept.From.Record().dependencyStarted()
			}
			if dept.HoldingAcq {
				dept.HoldingAcq = false
				sr.Release(false)
			}
		}
	}

	return allStopped
}

func (sr *ServiceRecord) queueForConsole() {
	sr.waitingForConsole = true
	sr.services.AppendConsoleQueue(sr.self)
}

func (sr *ServiceRecord) releaseConsole() {
	sr.haveConsole = false
	sr.services.PullConsoleQueue()
}

// AcquiredConsole is called when the console becomes available.
func (sr *ServiceRecord) AcquiredConsole() {
	sr.waitingForConsole = false
	sr.haveConsole = true

	if sr.state.Load() != StateStarting {
		sr.releaseConsole()
	} else if sr.checkDepsStarted() {
		sr.allDepsStarted()
	} else {
		sr.releaseConsole()
	}
}

// AddDep adds a dependency to the service.
func (sr *ServiceRecord) AddDep(to Service, depType DependencyType) *ServiceDep {
	dep := NewServiceDep(sr.self, to, depType)
	if sr.dependsOn == nil {
		sr.dependsOn = make([]*ServiceDep, 0, 4)
	}
	sr.dependsOn = append(sr.dependsOn, dep)
	toRec := to.Record()
	if toRec.dependents == nil {
		toRec.dependents = make([]*ServiceDep, 0, 4)
	}
	toRec.dependents = append(toRec.dependents, dep)

	if depType != DepBefore && depType != DepAfter {
		if depType == DepRegular || depType == DepPreparedBy ||
			to.Record().state.Load() == StateStarted ||
			to.Record().state.Load() == StateStarting {
			if sr.state.Load() == StateStarting || sr.state.Load() == StateStarted {
				toRec.Require()
				dep.HoldingAcq = true
			}
		}
	}

	return dep
}

// RmDep removes a dependency of the given type to the given service.
func (sr *ServiceRecord) RmDep(to Service, depType DependencyType) bool {
	for i, dep := range sr.dependsOn {
		if dep.To == to && dep.DepType == depType {
			sr.rmDepByIndex(i)
			return true
		}
	}
	return false
}

func (sr *ServiceRecord) rmDepByIndex(i int) {
	dep := sr.dependsOn[i]
	toRec := dep.To.Record()

	// Remove from target's dependents
	for j, d := range toRec.dependents {
		if d == dep {
			toRec.dependents = append(toRec.dependents[:j], toRec.dependents[j+1:]...)
			break
		}
	}

	if dep.HoldingAcq {
		toRec.Release(true)
	}

	sr.dependsOn = append(sr.dependsOn[:i], sr.dependsOn[i+1:]...)
}

// SetDependents replaces the dependents slice (used during reload to transfer dependents).
func (sr *ServiceRecord) SetDependents(deps []*ServiceDep) {
	sr.dependents = deps
}

// ClearDependencies removes all dependencies without updating the target's dependents.
func (sr *ServiceRecord) ClearDependencies() {
	sr.dependsOn = nil
}

// HasLoneRef returns true if this service has no significant references beyond
// the given handleCount (control connection handles). A service can be unloaded
// only if all its dependents are ordering-only (BEFORE/AFTER).
func (sr *ServiceRecord) HasLoneRef(handleCount int) bool {
	if sr.logConsumer != nil {
		return false
	}
	for _, dept := range sr.dependents {
		if !dept.IsOnlyOrdering() {
			return false
		}
	}
	return true
}

// PrepareForUnload removes all dependency links bidirectionally before the
// service is removed from the ServiceSet.
func (sr *ServiceRecord) PrepareForUnload() {
	// Remove ourselves from each dependency's dependents list
	for _, dep := range sr.dependsOn {
		toRec := dep.To.Record()
		for j, d := range toRec.dependents {
			if d == dep {
				toRec.dependents = append(toRec.dependents[:j], toRec.dependents[j+1:]...)
				break
			}
		}
		if dep.HoldingAcq {
			dep.To.Record().Release(false)
		}
	}
	sr.dependsOn = nil

	// Remove ourselves from each dependent's dependsOn list
	for _, dept := range sr.dependents {
		fromRec := dept.From.Record()
		for j, d := range fromRec.dependsOn {
			if d == dept {
				fromRec.dependsOn = append(fromRec.dependsOn[:j], fromRec.dependsOn[j+1:]...)
				break
			}
		}
	}
	sr.dependents = nil

	// Clear consumer-of cross-links
	if sr.logConsumer != nil {
		sr.logConsumer.Record().consumerFor = nil
		sr.logConsumer = nil
	}
	if sr.consumerFor != nil {
		sr.consumerFor.Record().logConsumer = nil
		sr.consumerFor = nil
	}
}

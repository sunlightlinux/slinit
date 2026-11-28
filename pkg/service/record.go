package service

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
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

	// ScheduleRestartWithBackoff is called by Stopped() before it would
	// otherwise fire initiateStart() on the willRestart path. Services
	// with a configured restart-delay / restart-delay-step should
	// compute the next effective delay, and if lastStartTime is closer
	// than that delay, arm a private timer and return true. Stopped()
	// then skips initiateStart(); when the timer fires, the service
	// calls initiateStart() itself. Returning false means "no delay
	// needed, start now" — Stopped() proceeds with initiateStart().
	// Default (ServiceRecord) always returns false so services without
	// restart-delay semantics restart immediately, as they do today.
	ScheduleRestartWithBackoff() bool

	// CancelPendingRestart disarms a restart-delay timer armed by
	// ScheduleRestartWithBackoff and reports whether there was one to
	// disarm. It is the counterpart that lets the state machine call a
	// deferred restart off, since a service parked on such a timer sits
	// in STOPPED while still counted active. Default (ServiceRecord)
	// returns false — nothing to cancel.
	CancelPendingRestart() bool

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
	// waitsForDirs are the `waits-for.d:` paths declared in the
	// description (possibly zero). Kept on the record so
	// persistEnable can write a symlink into the SAME directory
	// the loader will scan on next boot, instead of guessing
	// waits-for.d/ and dropping the enable to a place the loader
	// ignores.
	waitsForDirs []string
	description  string // human-readable description for status/list output
	author       string // upstart-style metadata (informational)
	version      string
	usage        string
	recordType   ServiceType

	// State (atomic: written under queueMu.Lock, read lockless)
	state   atomicServiceState
	desired atomicServiceState

	// Flags
	autoRestart AutoRestartMode
	// onceRun: this activation was started by `once` and is not to be
	// restarted, whatever autoRestart says (runit's `sv once`). Cleared
	// by the next ordinary Start; see autoRestartThisRun.
	onceRun        bool
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
	stopWhenUnneeded bool

	// upstart-style "normal exit": exit codes / signals that count as
	// success, suppressing respawn even when autoRestart=RestartAlways.
	normalExitCodes []int
	// systemd RestartForceExitStatus: exit codes/signals that FORCE a
	// restart independent of the `restart =` setting. Sibling of
	// normalExitCodes (which suppresses restart); this override wins
	// even when restart=no. Applied in doStop before the per-mode
	// auto-restart branch.
	restartForceExitCodes []int
	// systemd RestartMode=. `direct` skips the cascade to hard
	// dependents on a user-initiated or auto-restart; `normal`
	// (default) preserves the historical cascade.
	restartMode       RestartMode
	normalExitSignals []syscall.Signal

	// failure-conditions: when a stop after starting counts as failed.
	failureConditions FailureConditions

	// invocationID is the 128-bit-hex identifier minted at each
	// initiateStart. Rides on every journal event emitted during the
	// invocation lifecycle (Starting → Started → Stopping → Stopped
	// and any Failed variants), so client-side dedupe by
	// SLINIT_INVOCATION_ID gives exactly one row per start attempt.
	invocationID string

	// Pins
	pinnedStopped     bool
	pinnedStarted     bool
	deptPinnedStarted bool
	markedDown        bool // 'down' file exists — don't auto-start

	// countedActive is whether this service is in the set's active
	// count, so that it is counted once and taken off once. A start
	// pin can let a service stop with a hard dependent still holding
	// it (see Release): counted when it started, it was counted again
	// when it started anew, and the release that came once it was
	// STOPPED did not take it off at all.
	countedActive bool

	// depsStartedFrom is where checkDepsStarted resumes: every edge in
	// dependsOn[:depsStartedFrom] has WaitingOn false. Anything that
	// sets WaitingOn on one of our edges, or removes an edge, resets it
	// to 0 (appending keeps it valid). Under queueMu.
	depsStartedFrom int

	// Waiting flags
	waitingForDeps      bool
	waitingForConsole   bool
	haveConsole         bool
	startExplicit       bool
	waitingForStartSlot bool // waiting for start limiter slot

	// The wait above: a woken waiter checks it is still this one.
	startSlotCh <-chan struct{}

	// Propagation flags
	propRequire bool
	propRelease bool
	propFailure bool
	propStart   bool
	propStop    bool
	propPinDpt  bool

	// restarts counts supervisor-driven restarts of this service over
	// the daemon's lifetime. Monotonic on purpose: the set's restart
	// log is a one-hour window for the heartbeat's rate signal, which
	// is the wrong shape for a metric a fleet scrapes and rates itself.
	restarts atomic.Uint64

	// Start status
	startFailed  bool
	startSkipped bool
	// startSkipReason is why the start was skipped, kept so `status` and
	// `show` can say so. Without it a skipped service is reported as
	// STARTED and nothing anywhere names the condition that skipped it.
	startSkipReason string

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
	termSignal      syscall.Signal
	sendSIGHUP      bool           // send-sighup: SIGHUP right after the stop signal
	reloadSignal    syscall.Signal // 0 = unset; sent by `slinitctl reload-signal`
	socketPath      string         // primary socket path (for backwards compat)
	socketPaths     []string       // all socket-listen paths (for multiple sockets)
	socketReusePort bool           // SO_REUSEPORT on inet listeners (see SetSocketReusePort)
	socketMcastGrp  string         // multicast group a datagram listener joins after bind
	socketAccept    bool           // inetd nowait: one instance per accepted connection
	startOnMount    string         // mount point whose appearance starts this service ("*" = any)
	socketPerms     int
	socketUID       int
	socketGID       int
	stopReason      StoppedReason
	chainTo         string // service to start when this one completes
	// partOf names the services whose stop or restart reaches this one
	// (part-of, systemd's PartOf=); see propagatePartOf in partof.go.
	partOf []string
	// partOfGen is the restart wave this service last took part in, so
	// a part-of loop restarts each service once; partOfRestartOnStart
	// says the coming start is a restart whose wave reaches the
	// services part of this one.
	partOfGen            uint64
	partOfRestartOnStart bool
	// conflicts and onFailure name services by name, as partOf does:
	// see conflicts.go and onfailure.go.
	conflicts []string
	onFailure []string
	upholds   []string // upholds.go
	// inlineEnv: the environment setting's KEY=VALUE words, between the
	// global environment and the env-file (BuildFullEnv).
	inlineEnv []string
	// startOnCalendar: when CalendarStarter starts the service
	// (calendarstart.go); nil for none.
	startOnCalendar     *CalendarSpec
	startOnCalendarExpr string
	// startOnCalendarPersistent: catch up a time missed while slinit
	// was down (calendarstart.go).
	startOnCalendarPersistent bool
	// startOnCalendarDelay: start-on-calendar-randomized-delay.
	startOnCalendarDelay time.Duration
	// rerunJob marks the stop of a scripted job the calendar runs
	// again: not a restart, so its dependents, the services part of it
	// and its prepared-by dependencies are left alone, and it is not
	// counted as one (calendarFire).
	rerunJob bool
	// devicePolicy / deviceAllow: the cgroup device filter, attached at
	// each start (process.applyDeviceFilter).
	devicePolicy string
	deviceAllow  []string
	// ipAddressAllow / ipAddressDeny: the cgroup IP filter, attached at
	// each start (process.applyIPFilter).
	ipAddressAllow []string
	ipAddressDeny  []string
	// hadDeviceFilter / hadIPFilter: a filter was configured at some
	// start, so one may be attached to the cgroup still.
	hadDeviceFilter, hadIPFilter bool
	// onFailureStarts: when on-failure last started this service, for
	// its start limit (onfailure.go).
	onFailureStarts []time.Time
	// backoffRestarts counts the automatic restarts restart-steps has
	// walked (restartsteps.go). As systemd's n_restarts it starts over
	// on an explicit start or reset-failed only: the geometric delays
	// soon outgrow restart-limit-interval, and resetting with the
	// linear backoff's stable period kept the curve from its end.
	backoffRestarts int
	inPartOfWave    bool // this restart came from a part-of wave (partOfGen)

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
	sharedLoggerName      string // name of the shared logger service (empty if not used)
	sharedLoggerLossy     bool   // (on the logger svc) drop instead of block on backpressure
	sharedLoggerQueueSize int    // (on the logger svc) buffered channel depth (0 = default)

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

	// Runtime environment variables (set via control protocol), guarded
	// by envMu. Every control connection is served on its own goroutine
	// and CmdSetEnv reaches this map directly, without taking the
	// ServiceSet lock — so concurrent `slinitctl setenv` on one service
	// really does write it in parallel. Leaving it bare was a Go runtime
	// "concurrent map writes" fatal, which in PID 1 is a kernel panic.
	envMu    sync.Mutex
	extraEnv map[string]string
	// loadEnv is what the loader last put in extraEnv from load-options,
	// so a reload can drop what it no longer sets without touching a
	// value `slinitctl setenv` has changed since.
	loadEnv map[string]string

	// Process attributes (applied post-fork)
	nice           *int
	oomScoreAdj    *int
	noNewPrivs     bool
	ioPrioClass    int
	ioPrioLevel    int
	cgroupPath     string
	cgroupSettings []process.CgroupSetting // cgroup v2 resource limits
	// delegate: hand the service's own cgroup subtree to the service, for
	// payloads that manage cgroups themselves. delegateControllers names
	// which controllers; empty means whatever the parent can offer.
	delegate            bool
	delegateControllers []string

	// slice: systemd-style hierarchical grouping (e.g. "system.slice",
	// "system.slice/database.slice"). When set with no explicit
	// cgroupPath, the effective cgroup becomes
	// /sys/fs/cgroup/<slice>/<svc-name>, letting a fleet of services
	// share cumulative limits at the slice level. Explicit cgroupPath
	// still wins for backward compatibility.
	slice        string
	rlimits      []process.Rlimit
	ambientCaps  []uintptr
	boundingCaps []uintptr // positive keep-list for CapBnd; nil = inherit
	securebits   uint32
	cpuAffinity  []uint
	umask        *uint32 // file-creation mask for the service process (nil = inherit slinit's)

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
	coredumpFilter string
	timerSlackNsec int64
	memoryKSM      bool
	personality    string
	ignoreSIGPIPE  *bool // nil = default (yes)
	removeIPC      bool
	utmpMode       string // "" (init) | "init" | "login" | "user"

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
	steadyAllowedCPUs        string
	steadyAllowedMemoryNodes string
	timeoutStopFailureMode   TimeoutFailureMode
	watchdogSignal           syscall.Signal // 0 = default SIGABRT (systemd), or SIGTERM if the operator hasn't opted in — see fireWatchdogStop.
	finalKillSignal          syscall.Signal // 0 = default SIGKILL
	surviveFinalKillSignal   bool
	restartKillSignal        syscall.Signal
	killMode                 KillMode

	// Bucket D — env + credential pipeline. Flat fields, none share
	// cluster semantics with each other.
	passEnvironment   []string
	passEnvSet        bool
	unsetEnvironment  []string
	execSearchPath    string
	standardInput     []byte
	standardInputSet  bool
	standardInputFile string
	openFiles         []OpenFileRecord
	importCredentials []string
	notifyAccess      NotifyAccess
	notifyAccessSet   bool
	guessMainPID      bool
	// Bucket E partial — LSM domain transition mirrors of AppArmor.
	selinuxContext    string
	smackProcessLabel string
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

	// startGen counts initiateStart calls: a PREPARED_BY dependent
	// compares it across its stop (see ServiceDep.toStartGen).
	// Atomic: AddDep reads it for the dependent's note, and add-dep
	// runs under the graph lock rather than queueMu.
	startGen atomic.Uint64

	// Pre-start fail-fast path checks (OpenRC-inspired):
	// BringUp refuses to start the service if any of these paths is missing.
	requiredFiles []string
	requiredDirs  []string

	predicates []Predicate
	// privHooks: the commands with systemd's "+" prefix (PrivilegedHooks).
	privHooks PrivilegedHooks

	failureAction SystemAction
	successAction SystemAction
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

	// depRestartDenied says the dependency whose failure is stopping this
	// service has used up its restart limit, so this restart is refused too.
	depRestartDenied bool

	// stopRestartAsked and stopRestartOK hold the restart limit's answer
	// for the stop under way (checkRestartOnce): a stop is one failure,
	// however many places want to know whether it may restart.
	stopRestartAsked bool
	stopRestartOK    bool

	// restarting says this stop is the first half of a client-side restart.
	restarting bool

	// restartPending is set while Stopped() has handed a wanted restart
	// to a restart-delay timer instead of calling initiateStart itself.
	// The service then sits in STOPPED while still counted among the
	// active services, because the restart is part of the same
	// activation — so STOPPED alone no longer means "inactive", and
	// every place that keys off it has to consult this:
	//
	//   - doStart's wasActive test would otherwise take the active count
	//     a second time (one `slinitctl restart` per extra count);
	//   - Stop, Release and doStop are all guarded by `state != STOPPED`
	//     and would find nothing to do, leaving the count up for good.
	//
	// A count that never returns to zero makes the event loop wait out
	// the entire emergency timeout at shutdown with no service to blame,
	// since every record really is STOPPED.
	restartPending bool

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

	// run-as value that did not resolve at load; resolved at start.
	runAsSpec string

	// file-descriptor-store (systemd #14). When fdStoreMax > 0 a
	// $NOTIFY_SOCKET is created at BringUp and the parent listens for
	// sd_notify FDSTORE=1 messages. Stored fds are kept across
	// restarts inside the daemon and prepended to the child's
	// LISTEN_FDS on the next BringUp.
	fdStoreMax      int
	fdStore         *process.FDStore
	fdStorePreserve string // "" | "no" | "yes" | "on-success" (systemd v261)
	notifySock      *process.NotifySocketListener
	// lastNotifyStatus is the last STATUS= logged (OnNotify).
	lastNotifyStatus string
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

// WaitsForDirs returns the resolved `waits-for.d:` paths from the
// service description (relative or absolute, in declaration order).
// Consumers: persistEnable / persistDisable use the first entry to
// write / remove the enable-symlink into the directory the loader
// will actually scan.
func (sr *ServiceRecord) WaitsForDirs() []string { return sr.waitsForDirs }

// SetWaitsForDirs is called by the config loader with the parsed
// list. Mirrors SetServiceDir's shape.
func (sr *ServiceRecord) SetWaitsForDirs(dirs []string) { sr.waitsForDirs = dirs }
func (sr *ServiceRecord) Description() string           { return sr.description }
func (sr *ServiceRecord) SetDescription(d string)       { sr.description = d }
func (sr *ServiceRecord) Author() string                { return sr.author }
func (sr *ServiceRecord) SetAuthor(s string)            { sr.author = s }
func (sr *ServiceRecord) Version() string               { return sr.version }
func (sr *ServiceRecord) SetVersion(s string)           { sr.version = s }
func (sr *ServiceRecord) Usage() string                 { return sr.usage }
func (sr *ServiceRecord) SetUsage(s string)             { sr.usage = s }

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

// SetRunAsSpec records a run-as value that did not resolve when the
// service was loaded; "" clears it. See resolveDeferredRunAs.
func (sr *ServiceRecord) SetRunAsSpec(spec string) { sr.runAsSpec = spec }

// resolveDeferredRunAs looks up a run-as value the loader could not
// resolve, at start time — when a user created during boot (by
// slinit-sysusers, say) exists. Failing here refuses the start: the
// alternative is the process running as slinit itself, root for PID 1.
// Called from each concrete BringUp before any UID-dependent setup.
func (sr *ServiceRecord) resolveDeferredRunAs() error {
	if sr.runAsSpec == "" {
		return nil
	}
	uid, gid, err := process.ResolveRunAs(sr.runAsSpec)
	if err != nil {
		return fmt.Errorf("%w; refusing to start rather than run as slinit's own user", err)
	}
	if setter, ok := sr.self.(interface{ SetRunAs(uid, gid uint32) }); ok {
		setter.SetRunAs(uid, gid)
	}
	sr.runAsSpec = ""
	return nil
}

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
// goroutine calls it for every sd_notify packet. READY=1 and WATCHDOG=1
// go to the service's readiness pipe (ProcessService.sdNotify,
// ready-notification = sd-notify); FDSTORE=1 messages stash their
// attached fds; STATUS= is logged.
//
// The fd-store commands are taken in systemd's order of precedence:
// FDSTOREWIPE=<glob> drops every stored fd whose name matches, and
// makes the message's other fd-store commands ignored; else
// FDSTOREREMOVE=1 drops the fds named by FDNAME=, which it requires;
// else FDSTORE=1 adds. Descriptors attached to a removal are not
// stored, and are closed below with the rest.
func (sr *ServiceRecord) OnNotify(msg process.NotifyMessage, fds []*os.File) {
	// The socket is writable by anyone (NewNotifySocketListener): a
	// message counts only from the service's own processes, as
	// NotifyAccess= decides in systemd. Without the check any process of
	// any user could declare the service ready or keep its watchdog fed,
	// and a READY=1 the previous process queued before it died could
	// mark a smooth-recovery relaunch ready at once.
	if !sr.notifySenderAllowed(msg.SenderPID) {
		for _, f := range fds {
			_ = f.Close()
		}
		return
	}
	sr.handleNotify(msg, fds)
}

// handleNotify acts on a notify message from an allowed sender.
func (sr *ServiceRecord) handleNotify(msg process.NotifyMessage, fds []*os.File) {
	if msg.Ready || msg.Watchdog {
		if n, ok := sr.self.(interface{ sdNotify(ready, watchdog bool) }); ok {
			n.sdNotify(msg.Ready, msg.Watchdog)
		}
	}
	switch {
	case msg.HasFDStoreWipe:
		if !process.ValidFDName(msg.FDStoreWipe) {
			sr.services.logger.Error("Service '%s': FDSTOREWIPE=%q is not a valid pattern, ignoring",
				sr.serviceName, msg.FDStoreWipe)
		} else if sr.fdStore != nil {
			n := sr.fdStore.Remove(func(name string) bool { return process.MatchFDName(msg.FDStoreWipe, name) })
			sr.services.logger.Info("Service '%s': fd-store wiped %d fd(s) matching %q; store=%d/%d",
				sr.serviceName, n, msg.FDStoreWipe, sr.fdStore.Len(), sr.fdStoreMax)
		}
		msg.FDStore = false
	case msg.FDStoreRemove:
		if !process.ValidFDName(msg.FDName) {
			sr.services.logger.Error("Service '%s': FDSTOREREMOVE=1 without a valid FDNAME=, ignoring",
				sr.serviceName)
		} else if sr.fdStore != nil {
			n := sr.fdStore.Remove(func(name string) bool { return name == msg.FDName })
			sr.services.logger.Info("Service '%s': fd-store removed %d fd(s) named %q; store=%d/%d",
				sr.serviceName, n, msg.FDName, sr.fdStore.Len(), sr.fdStoreMax)
		}
		msg.FDStore = false
	}
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
	// Anything we were handed and did not take ownership of has to be
	// closed here. The store's Add() closes on both of its rejection
	// paths, so the leak was only this one: a datagram carrying
	// SCM_RIGHTS *without* FDSTORE=1 — a plain STATUS= line with
	// descriptors attached, say — fell through and dropped every
	// *os.File on the floor. Their only release was then the GC
	// finalizer, and PID 1 allocates little, so a service looping
	// sd_notify with descriptors attached can accumulate them faster
	// than they are reclaimed. A PID 1 at RLIMIT_NOFILE accepts no
	// control connections, opens no log files and forks nothing.
	//
	// Found by turning the CVE classes on systemdfree.com into checks
	// against slinit: this is the CVE-2021-33910 shape — unbounded
	// resource use in PID 1 driven by untrusted input — reached from a
	// service rather than from a mount path.
	for _, f := range fds {
		_ = f.Close()
	}

	// Logged when it changes: a daemon may resend the same STATUS= every
	// few seconds (php-fpm does), and each would be a line in PID 1's log.
	if msg.Status != "" && msg.Status != sr.lastNotifyStatus {
		sr.lastNotifyStatus = msg.Status
		sr.services.logger.Info("Service '%s': status %q", sr.serviceName, msg.Status)
	}
}

// notifySenderAllowed reports whether a notify message from pid counts:
// it must come from the service's main process, or, unless
// notify-access = main is set, from a process descended from it (a
// shell script's slinit-notify, a daemon's worker). The descent is read
// from /proc, at most 64 generations up; a process re-parented away from
// the main process (a double fork) is no longer its descendant.
func (sr *ServiceRecord) notifySenderAllowed(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, ok := sr.self.(interface{ PID() int })
	if !ok {
		return false
	}
	main := p.PID()
	if main <= 0 {
		return false
	}
	if pid == main {
		return true
	}
	if sr.notifyAccessSet && sr.notifyAccess == NotifyAccessMain {
		return false
	}
	for i := 0; i < 64 && pid > 1; i++ {
		pid = parentPID(pid)
		if pid == main {
			return true
		}
	}
	return false
}

// parentPID is pid's parent from /proc/PID/stat, 0 when it cannot be
// read. The fields after the command name, which may hold spaces and
// parentheses, are read from the last ')'.
func parentPID(pid int) int {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0
	}
	f := bytes.Fields(b[i+1:])
	if len(f) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(string(f[1]))
	return ppid
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
	var t *time.Timer
	t = time.AfterFunc(d, func() {
		set.queueMu.Lock()
		defer set.queueMu.Unlock()
		// Only the timer still armed is current: a callback that fired
		// while queueMu was held across a stop and a new start (Stop
		// returns false once the callback is running) belongs to the
		// previous run, and would stop the new one.
		if sr.runtimeMaxTimer != t {
			return
		}
		// Re-check: the service may have already stopped (process
		// exited on its own, operator stopped it) between the timer
		// firing and us acquiring the lock. Stopping a STOPPED service
		// is a no-op but logging is noisy.
		if svc.State() != StateStarted {
			return
		}
		// The run ends as a timeout, a failure, as systemd's
		// RuntimeMaxSec= ends it (result "timeout"): restart = yes or
		// on-failure brings the service back, as a watchdog expiry does,
		// and otherwise it is left stopped and failed. It used to be an
		// operator stop, so RuntimeMaxSec=1d with Restart=always, the
		// usual way to restart a leaky daemon daily, stopped it for good.
		set.logger.Error("Service '%s': runtime-max-sec (%s) reached, stopping",
			name, d)
		rec := svc.Record()
		withRestart := false
		switch rec.autoRestartThisRun() {
		case RestartAlways, RestartOnFailure:
			withRestart = rec.checkRestartOnce()
		}
		rec.stopReason = ReasonTimedOut
		rec.forceStop = true
		rec.doStop(withRestart)
		set.processQueuesLocked()
	})
	sr.runtimeMaxTimer = t
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
	var t *time.Timer
	t = time.AfterFunc(d, func() {
		set.queueMu.Lock()
		defer set.queueMu.Unlock()
		// A callback that fired while queueMu was held across a stop and
		// a restart belongs to the previous attempt: Stop cannot recall
		// it once it runs, and the new attempt is STARTING too, so the
		// state alone would fail it at once.
		if sr.jobTimeoutTimer != t || svc.State() != StateStarting {
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
	sr.jobTimeoutTimer = t
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
	switch sr.stoppedAs() {
	case endFailed:
		return sr.failureAction
	case endClean:
		return sr.successAction
	}
	return ActionNone
}

// How a stop that does not restart ended, for failure-action /
// success-action and on-failure.
const (
	endOther = iota
	endFailed
	endClean
)

func (sr *ServiceRecord) stoppedAs() int {
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
		(sr.stopReason == ReasonTerminated && sr.processEndIsFailure(exitStatus, !cleanFinish)) ||
		(sr.stopReason == ReasonDepFailed && sr.failureConditions.Dependency) {
		return endFailed
	}
	// failure-conditions = none can leave a non-clean end that is
	// neither: it is not a failure, and it is not a success either.
	// Success path: only fire for a service that finished on its own
	// with a clean exit. Operator-issued stops (ReasonNormal without a
	// terminated process) do not count.
	if cleanFinish {
		return endClean
	}
	return endOther
}

// processEndIsFailure applies failure-conditions to the end of a process
// that had started. legacy is the caller's own long-standing answer, which
// "process-fails" (the default) keeps: on-failure and failure-action have
// always disagreed about the admin signals, and the directive is not the
// place to settle that.
func (sr *ServiceRecord) processEndIsFailure(es ExitStatus, legacy bool) bool {
	switch sr.failureConditions.Primary {
	case FailOnProcessStop:
		return es.Exited() || es.Signaled() || es.Vanished
	case FailNever:
		return false
	}
	return legacy
}

// restartableFailure is the process-fails reading that restart =
// on-failure has always used: a non-zero exit, a signal other than the
// administrative ones (matching dinit), or a vanished bgprocess daemon.
func restartableFailure(es ExitStatus) bool {
	if es.Signaled() {
		switch es.Signal() {
		case syscall.SIGHUP, syscall.SIGINT, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGTERM:
			return false
		}
		return true
	}
	if es.Exited() {
		return es.ExitCode() != 0
	}
	// A watched process that disappeared without a status we could
	// collect — a bgprocess daemon, which is not our child. It was
	// running and now it is not, which is a failure however it happened;
	// the alternative is to decline the restart on the strength of a
	// status that belongs to a different process.
	return es.Vanished
}

// stopIsFailure reports whether the stop in progress counts as a failure,
// for the dependents that opted into dependency-fails. The rule for a
// process end is on-failure's, as dinit's is: its FAILED reason comes from
// the same test.
func (sr *ServiceRecord) stopIsFailure() bool {
	switch sr.stopReason {
	case ReasonFailed, ReasonExecFailed, ReasonTimedOut, ReasonDepFailed:
		return true
	case ReasonTerminated:
		es := sr.self.GetExitStatus()
		return sr.processEndIsFailure(es, restartableFailure(es))
	}
	return sr.startFailed
}

// IsFailed is the daemon's verdict for `slinitctl is-failed`: a stopped
// service whose start failed, or whose stop counts as a failure under
// its failure-conditions. A service stopped on request has not failed.
func (sr *ServiceRecord) IsFailed() bool {
	if sr.startFailed {
		return true
	}
	if sr.state.Load() != StateStopped {
		return false
	}
	switch sr.stopReason {
	case ReasonFailed, ReasonExecFailed, ReasonTimedOut, ReasonDepFailed:
		return true
	case ReasonTerminated:
		es := sr.self.GetExitStatus()
		clean := es.Exited() && es.ExitCode() == 0
		return sr.processEndIsFailure(es, !clean)
	}
	return false
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

// markSkippedStartReason is markSkippedStart with the predicate's own
// words kept for reporting.
func (sr *ServiceRecord) markSkippedStartReason(reason string) {
	sr.startSkipReason = reason
	sr.markSkippedStart()
}

// StartSkipReason returns why the last start was skipped, or "".
func (sr *ServiceRecord) StartSkipReason() string { return sr.startSkipReason }

// PredicateOutcome is the result of evaluating CheckPredicates.
type PredicateOutcome uint8

const (
	PredOK     PredicateOutcome = iota // all checks passed (or none configured)
	PredSkip                           // a condition-* failed → skip silently
	PredFailed                         // an assert-* failed → fail start
)

// CheckPredicates evaluates every configured predicate in order and
// returns the strictest outcome. Within a category the FIRST failing
// predicate wins (so the reason string points at it). assert-* outranks
// condition-*: an assertion failure is still surfaced even if a prior
// condition would have caused a skip.
//
// Returns ("", "") when every predicate passes or none are configured.
func (sr *ServiceRecord) CheckPredicates() (PredicateOutcome, string) {
	// The caller (BringUp) holds queueMu, as prepareHookCmdLocked wants.
	hp, _ := sr.self.(hookCmdPreparer)
	prep := prepareExecCondition(hp)
	res := make([]predResult, len(sr.predicates))
	for i, p := range sr.predicates {
		if p.Kind == PredServiceStarted {
			res[i].ok, res[i].why = sr.checkServiceStarted(p)
		} else {
			res[i].ok, res[i].why = p.evaluateWith(prep)
		}
	}
	return combinePredicates(sr.predicates, res)
}

// predResult is one predicate's answer.
type predResult struct {
	ok  bool
	why string
}

// combinePredicates is CheckPredicates' verdict from the answers res to
// preds, one for one.
//
// As systemd's condition_test_list, each list (conditions, assertions)
// needs every plain check to hold and, when it has triggering ones
// ("|"), at least one of those.
func combinePredicates(preds []Predicate, res []predResult) (PredicateOutcome, string) {
	var (
		firstSkipReason string
		failReason      string
	)
	// Per list: whether it has triggers, whether one held, and the
	// triggers that failed, for the reason.
	var trig [2]struct {
		has, held bool
		failed    []string
	}
	for i, p := range preds {
		ok, why := res[i].ok, res[i].why
		if p.Trigger {
			l := &trig[0]
			if p.IsAssert {
				l = &trig[1]
			}
			l.has = true
			if ok {
				l.held = true
			} else {
				l.failed = append(l.failed, fmt.Sprintf("%s (%s)", p.String(), why))
			}
			continue
		}
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
	if l := trig[1]; l.has && !l.held && failReason == "" {
		failReason = "no triggering assertion held: " + strings.Join(l.failed, "; ")
	}
	if l := trig[0]; l.has && !l.held && firstSkipReason == "" {
		firstSkipReason = "no triggering condition held: " + strings.Join(l.failed, "; ")
	}
	if failReason != "" {
		return PredFailed, failReason
	}
	if firstSkipReason != "" {
		return PredSkip, firstSkipReason
	}
	return PredOK, ""
}

// checkServiceStarted answers the one predicate that asks about slinit
// rather than the machine: is the named service already STARTED?
//
// This is `Requisite=`'s semantic, and it is a precondition rather than
// a dependency on purpose. A dependency type would have to either start
// the target (depends-on, depends-ms, prepared-by) or merely order
// against it (before, after) — and the point of Requisite= is that it
// does neither: the service must already be up, this one does not bring
// it up, and if it is down the start fails.
//
// Ordering is the caller's business, exactly as in systemd: pair it with
// `after:` when the two could be starting at the same moment, or the
// answer depends on who got there first.
//
// Lives here and not in Predicate.Evaluate because it needs the service
// set, and keeping Evaluate a pure function of the machine is worth more
// than putting every predicate behind one call.
func (sr *ServiceRecord) checkServiceStarted(p Predicate) (bool, string) {
	name := p.Param
	if name == "" {
		return p.Negate, "no service named"
	}
	// findPlaceholders=false: a placeholder stands for a service that
	// was referenced but never loaded, and "referenced" is not
	// "running".
	svc := sr.services.FindService(name, false)
	found := svc != nil
	started := found && svc.State() == StateStarted
	if p.Negate {
		if started {
			return false, fmt.Sprintf("service %q is started", name)
		}
		return true, ""
	}
	if started {
		return true, ""
	}
	if !found {
		return false, fmt.Sprintf("service %q is not loaded", name)
	}
	return false, fmt.Sprintf("service %q is %s, not STARTED", name, svc.State())
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
		// O_NONBLOCK so a FIFO does not block PID 1 in open(2)
		// waiting for a writer.
		rf, err := os.OpenFile(f, os.O_RDONLY|syscall.O_NONBLOCK, 0)
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

// CancelPendingRestart default: no restart-delay timer exists for this
// service kind, so there is never one to disarm.
func (sr *ServiceRecord) CancelPendingRestart() bool { return false }

// ScheduleRestartWithBackoff default: no delay semantics for this
// service kind. Types with a restart-delay knob (ProcessService,
// BGProcessService) override this.
func (sr *ServiceRecord) ScheduleRestartWithBackoff() bool { return false }
func (sr *ServiceRecord) GetSmoothRecovery() bool          { return sr.smoothRecovery }
func (sr *ServiceRecord) IsManualStart() bool              { return sr.manualStart }
func (sr *ServiceRecord) RefusesManualStart() bool         { return sr.refuseManualStart }
func (sr *ServiceRecord) RefusesManualStop() bool          { return sr.refuseManualStop }
func (sr *ServiceRecord) StopsWhenUnneeded() bool          { return sr.stopWhenUnneeded }

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

// autoRestartThisRun is the auto-restart mode in force for the current
// activation: RestartNever for one started by `once`, else autoRestart.
func (sr *ServiceRecord) autoRestartThisRun() AutoRestartMode {
	if sr.onceRun {
		return RestartNever
	}
	return sr.autoRestart
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
func (sr *ServiceRecord) SetChainTo(name string)   { sr.chainTo = name }
func (sr *ServiceRecord) SetPartOf(names []string) { sr.partOf = names }
func (sr *ServiceRecord) PartOf() []string         { return sr.partOf }
func (sr *ServiceRecord) SetConflicts(names []string) {
	sr.conflicts = names
	if len(names) > 0 {
		sr.services.anyConflicts.Store(true)
	}
}
func (sr *ServiceRecord) SetOnFailure(names []string) { sr.onFailure = names }

// SetStartOnCalendar sets the start-on-calendar expression (see
// calendarstart.go); "" clears it.
func (sr *ServiceRecord) SetStartOnCalendar(expr string) error {
	if expr == "" {
		sr.startOnCalendar, sr.startOnCalendarExpr = nil, ""
		return nil
	}
	c, err := ParseCalendar(expr)
	if err != nil {
		return err
	}
	sr.startOnCalendar, sr.startOnCalendarExpr = c, expr
	return nil
}

// StartOnCalendar returns the parsed expression, nil for none.
func (sr *ServiceRecord) StartOnCalendar() *CalendarSpec { return sr.startOnCalendar }

// SetStartOnCalendarPersistent sets start-on-calendar-persistent.
func (sr *ServiceRecord) SetStartOnCalendarPersistent(on bool) { sr.startOnCalendarPersistent = on }

// SetStartOnCalendarRandomizedDelay sets
// start-on-calendar-randomized-delay.
func (sr *ServiceRecord) SetStartOnCalendarRandomizedDelay(d time.Duration) {
	sr.startOnCalendarDelay = d
}

// StartOnCalendarRandomizedDelay reports
// start-on-calendar-randomized-delay.
func (sr *ServiceRecord) StartOnCalendarRandomizedDelay() time.Duration {
	return sr.startOnCalendarDelay
}

// StartOnCalendarPersistent reports start-on-calendar-persistent.
func (sr *ServiceRecord) StartOnCalendarPersistent() bool { return sr.startOnCalendarPersistent }

// StartOnCalendarExpr returns the expression as written.
func (sr *ServiceRecord) StartOnCalendarExpr() string { return sr.startOnCalendarExpr }
func (sr *ServiceRecord) SetUpholds(names []string) {
	sr.upholds = names
	if len(names) > 0 {
		sr.services.anyUpholds.Store(true)
	}
}
func (sr *ServiceRecord) SetServiceDscDir(dir string)        { sr.serviceDscDir = dir }
func (sr *ServiceRecord) SetTermSignal(sig syscall.Signal)   { sr.termSignal = sig }
func (sr *ServiceRecord) SetSendSIGHUP(v bool)               { sr.sendSIGHUP = v }
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
func (sr *ServiceRecord) SetSharedLoggerLossy(b bool)    { sr.sharedLoggerLossy = b }
func (sr *ServiceRecord) SharedLoggerLossy() bool        { return sr.sharedLoggerLossy }
func (sr *ServiceRecord) SetSharedLoggerQueueSize(n int) { sr.sharedLoggerQueueSize = n }
func (sr *ServiceRecord) SharedLoggerQueueSize() int     { return sr.sharedLoggerQueueSize }

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

func (sr *ServiceRecord) IsMarkedActive() bool { return sr.startExplicit }

// MarkRestarting says the coming stop is the first half of a restart.
func (sr *ServiceRecord) MarkRestarting() { sr.restarting = true }

// IsRestarting reports whether the current stop is the first half of a
// restart a client asked for (MarkRestarting).
func (sr *ServiceRecord) IsRestarting() bool { return sr.restarting }

func (sr *ServiceRecord) IsStartPinned() bool { return sr.pinnedStarted || sr.deptPinnedStarted }
func (sr *ServiceRecord) IsStopPinned() bool  { return sr.pinnedStopped }
func (sr *ServiceRecord) DidStartFail() bool  { return sr.startFailed }

// IsExplicitlyStartPinned reports a start pin set on this service itself,
// leaving out one inherited from a pinned dependent (deptPinnedStarted),
// which lasts only as long as that dependent's pin.
func (sr *ServiceRecord) IsExplicitlyStartPinned() bool { return sr.pinnedStarted }

// RestartCount reports how many times the supervisor has restarted this
// service since slinit started. Never decreases.
func (sr *ServiceRecord) RestartCount() uint64 { return sr.restarts.Load() }

// SetFailureConditions sets failure-conditions.
func (sr *ServiceRecord) SetFailureConditions(fc FailureConditions) { sr.failureConditions = fc }

// GetFailureConditions returns failure-conditions.
func (sr *ServiceRecord) GetFailureConditions() FailureConditions { return sr.failureConditions }

// ResetFailed clears the startFailed flag so subsequent status queries
// no longer report the service as failed. Mirrors systemd's
// `systemctl reset-failed`. No-op on a service that isn't marked failed.
func (sr *ServiceRecord) ResetFailed() {
	sr.startFailed = false
	sr.backoffRestarts = 0
	// IsFailed also reads the stop reason, which a failed start or a
	// failing exit leaves at Failed/ExecFailed/TimedOut/Terminated.
	// Clearing only the flag left is-failed answering "failed" after a
	// reset, for every kind of failure. A stopped service's reason goes
	// back to a normal stop; its exit status is kept for status.
	if sr.state.Load() == StateStopped && sr.stopIsFailure() {
		sr.stopReason = ReasonNormal
	}
}
func (sr *ServiceRecord) WasStartSkipped() bool   { return sr.startSkipped }
func (sr *ServiceRecord) IsLoading() bool         { return sr.isLoading }
func (sr *ServiceRecord) HasConsole() bool        { return sr.haveConsole }
func (sr *ServiceRecord) WaitingForConsole() bool { return sr.waitingForConsole }

// --- Environment variable management ---

func (sr *ServiceRecord) SetEnvVar(key, value string) {
	sr.envMu.Lock()
	defer sr.envMu.Unlock()
	if sr.extraEnv == nil {
		sr.extraEnv = make(map[string]string)
	}
	sr.extraEnv[key] = value
}

// SetLoadEnv replaces the variables derived from load-options (USER,
// HOME, DINIT_SERVICE and the rest): the ones the previous call set are
// removed first, so a reload that drops an option or changes run-as does
// not leave the old values behind. A variable whose value is no longer
// the one the loader set was changed at runtime, and is left as it is.
func (sr *ServiceRecord) SetLoadEnv(vars map[string]string) {
	sr.envMu.Lock()
	defer sr.envMu.Unlock()
	changed := make(map[string]bool)
	for k, v := range sr.loadEnv {
		if cur, ok := sr.extraEnv[k]; !ok || cur != v {
			changed[k] = true
			continue
		}
		delete(sr.extraEnv, k)
	}
	sr.loadEnv = nil
	for k, v := range vars {
		if changed[k] {
			continue
		}
		if sr.extraEnv == nil {
			sr.extraEnv = make(map[string]string)
		}
		if sr.loadEnv == nil {
			sr.loadEnv = make(map[string]string)
		}
		sr.extraEnv[k] = v
		sr.loadEnv[k] = v
	}
}

func (sr *ServiceRecord) UnsetEnvVar(key string) {
	sr.envMu.Lock()
	defer sr.envMu.Unlock()
	delete(sr.extraEnv, key)
}

// ResetEnv clears all runtime-set environment variables on this service.
// After reset, the next start sees only the daemon's global env plus
// the service's env-file (the "defaults"). Mirrors upstart's
// `initctl reset-env JOB`.
func (sr *ServiceRecord) ResetEnv() {
	sr.envMu.Lock()
	defer sr.envMu.Unlock()
	sr.extraEnv = nil
	sr.loadEnv = nil
}

func (sr *ServiceRecord) GetAllEnv() map[string]string {
	sr.envMu.Lock()
	defer sr.envMu.Unlock()
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
// Callers that already hold envMu must not use this; BuildFullEnv and
// BuildEnvWithFile go through it rather than touching extraEnv themselves,
// which keeps the lock strictly leaf-level and deadlock-free.
func (sr *ServiceRecord) BuildEnvSlice() []string {
	sr.envMu.Lock()
	defer sr.envMu.Unlock()
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
	// exec-search-path has to reach the child even when nothing else
	// does: the early nil used to drop it, leaving PID 1's PATH.
	if len(globalEnv) == 0 && len(sr.inlineEnv) == 0 && len(extra) == 0 && sr.ExecSearchPath() == "" {
		return nil
	}
	// Later entries win (os/exec keeps the last of a name): global, then
	// the environment setting, then what slinitctl setenv added.
	result := make([]string, 0, len(globalEnv)+len(sr.inlineEnv)+len(extra))
	result = append(result, globalEnv...)
	result = append(result, sr.inlineEnv...)
	result = append(result, extra...)
	return sr.applyBucketDEnvFilters(result)
}

// SetInlineEnv sets the environment setting's KEY=VALUE words.
func (sr *ServiceRecord) SetInlineEnv(env []string) { sr.inlineEnv = env }

// InlineEnv returns them.
func (sr *ServiceRecord) InlineEnv() []string { return sr.inlineEnv }

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

	totalCap := len(globalEnv) + len(sr.inlineEnv) + len(fileEnv) + len(extra)
	if totalCap == 0 && sr.ExecSearchPath() == "" {
		return nil
	}
	// Later entries win: global, the environment setting, the env-file
	// (as systemd's EnvironmentFile= overrides Environment=), setenv.
	env := make([]string, 0, totalCap)
	env = append(env, globalEnv...)
	env = append(env, sr.inlineEnv...)
	for k, v := range fileEnv {
		env = append(env, k+"="+v)
	}
	env = append(env, extra...)
	return sr.applyBucketDEnvFilters(env)
}

// inheritedEnvFilter applies pass-environment and unset-environment to
// the environment the child inherits from slinit itself, which
// slinit-service(5) says they filter too: the variables the kernel or a
// login session gave slinit reached every service whatever its
// allow-list said. Nil when neither is set, so the child inherits as
// before.
func (sr *ServiceRecord) inheritedEnvFilter() func(string) bool {
	pass, passSet := sr.PassEnvironment()
	unset := sr.UnsetEnvironment()
	if !passSet && len(unset) == 0 {
		return nil
	}
	allow := make(map[string]struct{}, len(pass))
	for _, n := range pass {
		allow[n] = struct{}{}
	}
	drop := make(map[string]struct{}, len(unset))
	for _, n := range unset {
		drop[n] = struct{}{}
	}
	return func(name string) bool {
		_, dropped := drop[name]
		if dropped {
			return false
		}
		// PATH stays unless unset by name: services relied on the
		// manager's PATH with a narrow allow-list, and without it a
		// shell falls back to a built-in default that may include ".".
		// systemd's PassEnvironment= leaves the manager's PATH alone too.
		if passSet && name != "PATH" {
			if _, ok := allow[name]; !ok {
				return false
			}
		}
		return true
	}
}

// applyBucketDEnvFilters is the tail of every env-build path. Applies
// systemd's PassEnvironment= / UnsetEnvironment= / ExecSearchPath=
// semantics in that order:
//
//  1. If PassEnvironment was set explicitly, drop every var whose
//     NAME is not in the allow-list. Empty list = drop everything.
//  2. Remove every var whose NAME appears in UnsetEnvironment.
//  3. If ExecSearchPath is set, override PATH= (or append if not
//     present).
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

func (sr *ServiceRecord) SetNice(n *int)             { sr.nice = n }
func (sr *ServiceRecord) SetOOMScoreAdj(n *int)      { sr.oomScoreAdj = n }
func (sr *ServiceRecord) SetNoNewPrivs(v bool)       { sr.noNewPrivs = v }
func (sr *ServiceRecord) SetIOPrio(class, level int) { sr.ioPrioClass = class; sr.ioPrioLevel = level }
func (sr *ServiceRecord) SetCgroupPath(p string)     { sr.cgroupPath = p }

// SetIPFilter sets ip-address-allow and ip-address-deny.
func (sr *ServiceRecord) SetIPFilter(allow, deny []string) {
	sr.ipAddressAllow, sr.ipAddressDeny = allow, deny
}

// SetDeviceFilter sets device-policy and device-allow.
func (sr *ServiceRecord) SetDeviceFilter(policy string, allow []string) {
	sr.devicePolicy, sr.deviceAllow = policy, allow
}
func (sr *ServiceRecord) SetSlice(s string) { sr.slice = s }
func (sr *ServiceRecord) Slice() string     { return sr.slice }

func (sr *ServiceRecord) SetDelegate(on bool, controllers []string) {
	sr.delegate = on
	sr.delegateControllers = controllers
}
func (sr *ServiceRecord) Delegate() bool                              { return sr.delegate }
func (sr *ServiceRecord) DelegateControllers() []string               { return sr.delegateControllers }
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
	PrivateDevices      bool
	ProtectSystem       string // "" | "yes" | "full" | "strict"
	ReadOnlyPaths       []string
	ReadWritePaths      []string
	ProtectHome         string // "" | "yes" | "read-only" | "tmpfs"
	InaccessiblePaths   []string
	ProtectProc         string   // "" | "noaccess" | "invisible" | "ptraceable"
	ProcSubset          string   // "" | "pid"
	BindPaths           []string // "src:dst" pairs (writable)
	BindReadOnlyPaths   []string // "src:dst" pairs (read-only)
	TemporaryFileSystem []string // "path[:options]" tmpfs mounts
}

// Active reports whether any sandbox knob is set. Used by the loader to
// decide whether to OR CLONE_NEWNS into the clone flags and by the
// process layer to gate the runner wrap.
func (c SandboxConfig) Active() bool {
	return c.PrivateTmp || c.PrivateDevices ||
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
	IOUringFilter []string // io-uring-filter assignments, as written
}

// Active reports whether any seccomp knob is set.
func (c SeccompConfig) Active() bool {
	return len(c.Filter) > 0 || len(c.LogFilter) > 0 ||
		c.ErrorAction != "" || len(c.Architectures) > 0 ||
		len(c.IOUringFilter) > 0
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
	RestrictRealtime        bool
	RestrictNamespaces      bool
	RestrictSUIDSGID        bool
	RestrictFileSystems     bool
	RestrictAddressFamilies []string
	RestrictAFEnabled       bool
	MemoryDenyWriteExecute  bool
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
func (sr *ServiceRecord) SetCoredumpFilter(s string) { sr.coredumpFilter = s }
func (sr *ServiceRecord) SetTimerSlackNsec(n int64)  { sr.timerSlackNsec = n }
func (sr *ServiceRecord) SetMemoryKSM(b bool)        { sr.memoryKSM = b }
func (sr *ServiceRecord) SetPersonality(s string)    { sr.personality = s }
func (sr *ServiceRecord) SetIgnoreSIGPIPE(b *bool)   { sr.ignoreSIGPIPE = b }
func (sr *ServiceRecord) SetRemoveIPC(b bool)        { sr.removeIPC = b }
func (sr *ServiceRecord) SetUtmpMode(s string)       { sr.utmpMode = s }

// RemoveIPCEnabled reports whether the operator asked slinit to clean
// up SysV IPC + POSIX shm for the service's UID after each stop.
func (sr *ServiceRecord) RemoveIPCEnabled() bool { return sr.removeIPC }

// UtmpMode returns the operator-requested utmp record type: "init"
// (default), "login", or "user". Empty string is treated as "init"
// by the utmp write path.
func (sr *ServiceRecord) UtmpMode() string { return sr.utmpMode }

// Bucket C setters + accessors.
func (sr *ServiceRecord) SetCpusetPartition(s string)           { sr.cpusetPartition = s }
func (sr *ServiceRecord) SetCacheDirectoryQuota(n int64)        { sr.cacheDirectoryQuota = n }
func (sr *ServiceRecord) SetLogsDirectoryQuota(n int64)         { sr.logsDirectoryQuota = n }
func (sr *ServiceRecord) SetStateDirectoryQuota(n int64)        { sr.stateDirectoryQuota = n }
func (sr *ServiceRecord) SetCacheDirectoryAccounting(b bool)    { sr.cacheDirectoryAccounting = b }
func (sr *ServiceRecord) SetLogsDirectoryAccounting(b bool)     { sr.logsDirectoryAccounting = b }
func (sr *ServiceRecord) SetStateDirectoryAccounting(b bool)    { sr.stateDirectoryAccounting = b }
func (sr *ServiceRecord) SetStartupAllowedCPUs(s string)        { sr.startupAllowedCPUs = s }
func (sr *ServiceRecord) SetStartupAllowedMemoryNodes(s string) { sr.startupAllowedMemoryNodes = s }
func (sr *ServiceRecord) SetSteadyAllowedCPUs(s string)         { sr.steadyAllowedCPUs = s }
func (sr *ServiceRecord) SetSteadyAllowedMemoryNodes(s string)  { sr.steadyAllowedMemoryNodes = s }

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
func (sr *ServiceRecord) SetWatchdogSignal(s syscall.Signal)    { sr.watchdogSignal = s }
func (sr *ServiceRecord) SetFinalKillSignal(s syscall.Signal)   { sr.finalKillSignal = s }
func (sr *ServiceRecord) SetSurviveFinalKillSignal(b bool)      { sr.surviveFinalKillSignal = b }
func (sr *ServiceRecord) SetRestartKillSignal(s syscall.Signal) { sr.restartKillSignal = s }
func (sr *ServiceRecord) SetKillMode(m KillMode)                { sr.killMode = m }

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

// SetStandardInputFile routes fd 0 from a file (launchd StandardInPath).
func (sr *ServiceRecord) SetStandardInputFile(path string) {
	sr.standardInputFile = path
}

func (sr *ServiceRecord) SetStandardInput(data []byte, set bool) {
	sr.standardInput = data
	sr.standardInputSet = set
}
func (sr *ServiceRecord) SetOpenFiles(files []OpenFileRecord) { sr.openFiles = files }
func (sr *ServiceRecord) SetImportCredentials(pats []string)  { sr.importCredentials = pats }
func (sr *ServiceRecord) SetNotifyAccess(n NotifyAccess, set bool) {
	sr.notifyAccess = n
	sr.notifyAccessSet = set
}

// SetSocketReusePort enables SO_REUSEPORT on this service's inet
// listeners. Set before bind, it lets several services hold the same
// host:port; the kernel then hashes each incoming connection to one of
// them. Meaningless for Unix sockets, which is why openOneSocket only
// applies it to the tcp/udp forms.
func (sr *ServiceRecord) SetSocketReusePort(b bool) { sr.socketReusePort = b }

// SocketReusePort reports whether SO_REUSEPORT is requested.
func (sr *ServiceRecord) SocketReusePort() bool { return sr.socketReusePort }

// SetSocketMulticastGroup sets the multicast group that datagram listeners
// join after bind. Empty = no membership.
func (sr *ServiceRecord) SetSocketMulticastGroup(g string) { sr.socketMcastGrp = g }

// SocketMulticastGroup returns the configured multicast group, or "".
func (sr *ServiceRecord) SocketMulticastGroup() string { return sr.socketMcastGrp }

// SetSocketAccept turns on inetd `nowait` semantics for this service.
func (sr *ServiceRecord) SetSocketAccept(b bool) { sr.socketAccept = b }

// SocketAccept reports whether the service runs one instance per
// accepted connection.
func (sr *ServiceRecord) SocketAccept() bool { return sr.socketAccept }

// SetStartOnMount sets the mount point whose appearance starts this
// service. "*" means any new mount.
func (sr *ServiceRecord) SetStartOnMount(p string) { sr.startOnMount = p }

// StartOnMount returns the configured mount trigger, or "".
func (sr *ServiceRecord) StartOnMount() string { return sr.startOnMount }

func (sr *ServiceRecord) SetGuessMainPID(b bool)        { sr.guessMainPID = b }
func (sr *ServiceRecord) SetSELinuxContext(s string)    { sr.selinuxContext = s }
func (sr *ServiceRecord) SetSMACKProcessLabel(s string) { sr.smackProcessLabel = s }
func (sr *ServiceRecord) SetTTYPath(s string)           { sr.ttyPath = s }
func (sr *ServiceRecord) SetTTYColumns(n uint16)        { sr.ttyColumns = n }
func (sr *ServiceRecord) SetTTYRows(n uint16)           { sr.ttyRows = n }
func (sr *ServiceRecord) SetTTYVHangup(b bool)          { sr.ttyVHangup = b }
func (sr *ServiceRecord) SetTTYVTDisallocate(b bool)    { sr.ttyVTDisallocate = b }
func (sr *ServiceRecord) SetTTYReset(b bool)            { sr.ttyReset = b }

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
//
// Empty if none apply.
// removeCgroupFunc is process.RemoveCgroup, indirected so tests can
// observe the call without a writable /sys/fs/cgroup.
var removeCgroupFunc = process.RemoveCgroup

// removeCgroupTreeFunc is the delegated-cgroup variant; separate var so a
// test can tell which of the two a service chose.
var removeCgroupTreeFunc = process.RemoveCgroupTree

// removeOwnCgroup reclaims the cgroup directory slinit created for this
// service, if the service has one of its own. A service with neither
// `cgroup` nor `slice` shares the daemon-wide default cgroup with every
// other unconfigured service, so there is nothing here that belongs to
// it alone and nothing to remove.
//
// The removal is an rmdir, so it does nothing while processes or child
// cgroups remain. That makes it safe to call unconditionally from
// Stopped(), including on a service that failed to start and never had
// a cgroup at all.
// OwnCgroupPath is EffectiveCgroupPath when the service has a cgroup of
// its own (cgroup or slice set), and "" when it only shares the daemon's
// default one with every other such service. Anything that acts on the
// cgroup as the service's - killing it, freezing it, watching it for OOM
// or pressure, waiting for it to drain, guessing the main pid from it,
// delegating it - must use this: on the shared cgroup it reached every
// other service in it (kill-mode = control-group SIGKILLed sshd and the
// gettys; delegate = yes chowned the common ancestor to the service user).
func (sr *ServiceRecord) OwnCgroupPath() string {
	if sr.cgroupPath == "" && sr.slice == "" {
		return ""
	}
	return sr.EffectiveCgroupPath()
}

func (sr *ServiceRecord) removeOwnCgroup() {
	path := sr.OwnCgroupPath()
	if path == "" {
		return
	}
	remove := removeCgroupFunc
	if sr.delegate {
		// The payload owned this subtree and may have left children in
		// it. A plain rmdir of the parent fails with ENOTEMPTY forever
		// after, so the directories accumulate one set per restart.
		// Still rmdir per directory, just bottom-up — anything still
		// holding processes survives untouched.
		remove = removeCgroupTreeFunc
	}
	if err := remove(path); err != nil {
		sr.services.logger.Info("Service '%s': cgroup %s not reclaimed: %v",
			sr.serviceName, path, err)
	}
}

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
	// Only on a cgroup of the service's own; without one (the loader
	// refuses that) the start fails rather than run unfiltered.
	params.DevicePolicy, params.DeviceAllow = sr.devicePolicy, sr.deviceAllow
	params.IPAddressAllow, params.IPAddressDeny = sr.ipAddressAllow, sr.ipAddressDeny
	params.DeviceFilterOwnCgroup = sr.OwnCgroupPath() != ""
	// A filter a reload took away is detached at the next start, from
	// a cgroup that outlived the stop (leftover processes, a delegated
	// payload's cgroups); a service that never had one is not asked.
	devOn := sr.devicePolicy != "" || len(sr.deviceAllow) > 0
	ipOn := len(sr.ipAddressAllow) > 0 || len(sr.ipAddressDeny) > 0
	sr.hadDeviceFilter = sr.hadDeviceFilter || devOn
	sr.hadIPFilter = sr.hadIPFilter || ipOn
	params.DetachDeviceFilter = !devOn && sr.hadDeviceFilter && params.DeviceFilterOwnCgroup
	params.DetachIPFilter = !ipOn && sr.hadIPFilter && params.DeviceFilterOwnCgroup
	params.CgroupSettings = sr.cgroupSettings
	// Only a cgroup of the service's own may be handed over.
	params.Delegate = sr.delegate && sr.OwnCgroupPath() != ""
	params.DelegateControllers = sr.delegateControllers
	params.Rlimits = sr.rlimits
	params.AmbientCaps = sr.ambientCaps
	params.BoundingCaps = sr.boundingCaps
	// The manager's bits are a floor: a service adds to them and cannot
	// clear them, as with systemd's manager SecureBits=.
	params.Securebits = sr.securebits | sr.services.ManagerSecurebits()
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
	params.InetdPath = sr.services.InetdPath()
	params.SocketAccept = sr.socketAccept
	params.Cloneflags = sr.cloneflags
	params.UidMappings = sr.uidMappings
	params.GidMappings = sr.gidMappings
	params.PrivateTmp = sr.sandbox.PrivateTmp
	params.PrivateDevices = sr.sandbox.PrivateDevices
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
	params.IOUringFilter = sr.seccomp.IOUringFilter
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
	if sr.standardInputFile != "" {
		params.StdinFile = sr.standardInputFile
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

	params.InheritEnv = sr.inheritedEnvFilter()

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
//
// Not while a state hook runs (statehook.go): the start would begin a
// new run under a finish-command or env-generator whose completion
// still has the old run's exit, or end of recovery, to deliver. The
// start is not lost: desired stays STARTED, and the stop, once the hook
// is done, restarts the service.
func (sr *ServiceRecord) CanInterruptStop() bool {
	return sr.waitingForDeps && !sr.forceStop && !stateHookBusy(sr.self)
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
	// ... and the restart-steps count, as systemd flushes n_restarts:
	// not for one only waiting out its restart-delay, which is still in
	// its run of restarts.
	if sr.state.Load() == StateStopped && !sr.restartPending {
		sr.backoffRestarts = 0
	}

	if !sr.startExplicit {
		sr.requiredBy++
		sr.startExplicit = true
	}
	// An ordinary start ends a `once` (StartOnce sets it again after).
	sr.onceRun = false

	sr.doStart()
}

// StartOnce starts the service, as Start does, for one run: when it
// stops it is not restarted, whatever its restart setting (runit's
// `sv once`). The next ordinary Start restores the restart policy.
func (sr *ServiceRecord) StartOnce() {
	sr.Start()
	sr.onceRun = true
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
	} else if bringDown {
		// Already STOPPED — but possibly only because a restart was
		// deferred to a restart-delay timer, which leaves the service
		// counted active. This is the stop that calls it off.
		sr.abandonPendingRestart()
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

		sr.unpinDependencies()

		// STARTING as well as STARTED (dinit acts on STARTED only): a
		// stop or release put off by the pin while the service was
		// still starting is owed just the same. Left to Started, it
		// never came for a service that does not start on its own (a
		// triggered service waiting for its trigger), and the
		// shutdown, which unpins everything, waited out its timeout.
		if st := sr.state.Load(); st == StateStarted || st == StateStarting {
			if sr.requiredBy == 0 {
				sr.propRelease = !sr.propRequire
				sr.propRequire = false
				if sr.propRelease {
					sr.services.AddPropQueue(sr.self)
				}
			}
			if sr.desired.Load() == StateStopped || sr.forceStop {
				sr.doStop(false)
				// Note: caller is responsible for draining queues
				// (e.g. StopAllServices calls processQueuesLocked,
				// handleUnpinService calls ProcessQueues).
			}
		} else if st == StateStopped && sr.requiredBy == 0 && sr.countedActive && !sr.restartPending {
			// Pinned while STOPPING (a `start --pin` that could not
			// interrupt the stop), it stopped with the pin up: the
			// release its activation made was put off by the pin, so
			// it is still counted active, and may hold its
			// dependencies. Settle it now.
			for _, dep := range sr.dependsOn {
				if dep.HoldingAcq {
					sr.propRelease = true
					sr.services.AddPropQueue(sr.self)
					break
				}
			}
			sr.countedActive = false
			sr.services.ServiceInactive(sr.self)
		}
	}
	if sr.pinnedStopped {
		sr.pinnedStopped = false
	}
}

// unpinDependencies has the dependencies this service's start pin
// reached recompute theirs, once the pin is gone.
func (sr *ServiceRecord) unpinDependencies() {
	// A milestone counts although it may be soft by now: the pin
	// reached its target while the edge was still hard, and the
	// target must recompute it (see carriesPin).
	for _, dep := range sr.dependsOn {
		if dep.carriesPin() {
			toRec := dep.To.Record()
			if toRec.deptPinnedStarted {
				toRec.propPinDpt = true
				sr.services.AddPropQueue(dep.To)
			}
		}
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
		} else if sr.state.Load() == StateStopped {
			// Nothing requires this service any more. If it was only
			// STOPPED because a restart-delay timer was going to bring
			// it back, that restart is now pointless — call it off, so
			// the active-service count it is still holding comes down.
			if sr.restartPending {
				sr.abandonPendingRestart()
			} else if sr.countedActive {
				// Stopped while still required: a start-pinned
				// dependent STARTING but waiting for another
				// dependency counts as stopped, and holds this one
				// until its own stop releases it, here. Stopped kept
				// the service counted for that holder.
				sr.countedActive = false
				sr.services.ServiceInactive(sr.self)
			}
		}
	}
}

// settleInactive runs the tail of a stop for a service that is not
// going to restart: it gives up the explicit activation (or, if it was
// held only by dependents, the active-service count directly). State is
// already STOPPED by the time this runs.
//
// Factored out of Stopped() because the restart-delay path reaches the
// same place later and by a different door — see abandonPendingRestart.
func (sr *ServiceRecord) settleInactive() {
	sr.self.BecomingInactive()

	if sr.startExplicit {
		sr.startExplicit = false
		sr.Release(false)
	} else if sr.requiredBy == 0 {
		// Nothing requires it, so it must not hold its dependencies
		// either. It could: a start pin puts off the release that the
		// count reaching 0 propagates, and Unpin used to make up for
		// it only on a STARTED service, so one unpinned while STARTING
		// (the shutdown unpins everything) stopped once started and
		// kept its dependencies up for good.
		for _, dep := range sr.dependsOn {
			if dep.HoldingAcq {
				sr.propRelease = true
				sr.services.AddPropQueue(sr.self)
				break
			}
		}
		if sr.countedActive {
			sr.countedActive = false
			sr.services.ServiceInactive(sr.self)
		}
	}
}

// abandonPendingRestart calls off a restart that Stopped() deferred to a
// restart-delay timer, and settles the service into the inactive state
// Stopped() would have reached had it known the restart was not coming.
// It is a no-op for a service with no such timer armed, which is every
// service most of the time.
//
// Why every stop door has to call this: a service parked on a
// restart-delay timer sits in STOPPED while still counted active,
// because to the state machine it is mid-restart. Stop(), Release() and
// doStop() are all guarded by `state != STOPPED`, so each of them finds
// nothing to do and returns — leaving the timer armed and the count up.
// The timer then declines to restart (desired is STOPPED now) and the
// count never comes back down. CountActiveServices never reaches zero
// again, so at shutdown the event loop waits out the entire emergency
// timeout and cannot name a blocker, because every record really is
// STOPPED. A single `slinitctl restart` of a type=process service was
// enough to arm it.
func (sr *ServiceRecord) abandonPendingRestart() {
	if !sr.restartPending {
		return
	}
	sr.restartPending = false
	sr.self.CancelPendingRestart()
	sr.desired.Store(StateStopped)
	// The restart that was to restart the services part of this one is
	// off, and this is the stop instead: it reaches them as any stop
	// asked of this service does (part-of).
	sr.partOfRestartOnStart = false
	sr.inPartOfWave = false
	sr.services.propagatePartOfStop(sr.self)
	// Drop the explicit activation the way Stop() does. settleInactive
	// otherwise hands off to Release(), which has nothing left to do on
	// a service that is already STOPPED and so would not release the
	// active count either.
	if sr.startExplicit {
		sr.startExplicit = false
		sr.requiredBy--
	}
	// Dependents that started while this service was stopping hold it
	// too. Break the soft links, as Stopped() does for a service that is
	// not restarting.
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
	if sr.requiredBy > 0 {
		// A hard dependent still holds it: it stays counted active,
		// still marked so, and the release that frees it settles it.
		// Settled now, that release would find a STOPPED service with
		// nothing pending, and the count would never come down.
		sr.restartPending = true
		// Tell that dependent, as doStop would (dinit stops the
		// hard dependents here): left waiting for a restart that
		// will not come, it stayed STARTING for good and never let
		// go.
		sr.stopDependents(false, false)
		return
	}
	sr.settleInactive()
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
			// A dependency already held is already counted. doStart's
			// re-attach can take the hold first: when this service is
			// STARTING and the dependency starts before this propagation
			// runs, the dependency attaches its soft dependents itself.
			// Requiring it again counted one dependent twice, and the
			// extra count was never released, so at shutdown the
			// dependency — and everything under it — never stopped
			// (boot waits-for gdm and polkitd, gdm waits-for polkitd).
			// dinit avoids the window by running its propagation queue
			// LIFO; this queue is FIFO, so the check has to be here.
			if !dep.IsOnlyOrdering() && !dep.HoldingAcq {
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
				if dep.carriesPin() {
					toRec := dep.To.Record()
					if toRec.deptPinnedStarted != deptPin {
						toRec.propPinDpt = true
						sr.services.AddPropQueue(dep.To)
					}
				}
			}

			if !sr.deptPinnedStarted && !sr.pinnedStarted {
				// STARTING too, as in Unpin.
				if st := sr.state.Load(); (sr.desired.Load() == StateStopped || sr.forceStop) &&
					(st == StateStarted || st == StateStarting) {
					sr.doStop(false)
				}
			}
		}
	}
}

// ExecuteTransition performs a state transition based on the current and desired states.
//
// The `!sr.waitingForDeps ||` short-circuit is the ported half of
// dinit's d24e6f9 (2026-09-07). Once the flag is cleared (in
// allDepsStarted / stopDependents), a re-entry of the transition
// no longer has to walk every dep to reach the same conclusion;
// the flag itself is authoritative. Paired with the `waitingForDeps
// = false` promotion in allDepsStarted (below) — together they make
// the flag reliable for outside observers (see startCheckDependencies's
// ordering-only-dependent check).
func (sr *ServiceRecord) ExecuteTransition() {
	if sr.state.Load() == StateStarting {
		if !sr.waitingForDeps || sr.checkDepsStarted() {
			sr.waitingForDeps = false
			sr.allDepsStarted()
		}
	} else if sr.state.Load() == StateStopping {
		if !sr.waitingForDeps || sr.stopCheckDependents() {
			sr.waitingForDeps = false
			sr.self.BringDown()
		}
	}
}

// --- Internal state machine helpers ---

func (sr *ServiceRecord) notifyListeners(event ServiceEvent) {
	// Journal emit runs unconditionally — a service with zero
	// registered listeners (typical between control-connection
	// disconnects) still produces state-transition events in the
	// journal so slinit-journalctl can reconstruct the boot timeline
	// later. The Emit call is a no-op if journal.SetGlobal was never
	// called (e.g. during unit tests), so this is safe pre-Init.
	sr.emitJournalStateEvent(event)

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
			sr.refuseStart(false)
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
		if sr.restartPending {
			// STOPPED, but only because a restart-delay timer is
			// holding the restart that belongs to this same activation.
			// The service is already counted active, so take over the
			// restart rather than counting it twice: disarm the timer
			// and fall through to initiateStart below.
			sr.self.CancelPendingRestart()
		} else if !sr.countedActive {
			sr.countedActive = true
			sr.services.ServiceActive(sr.self)
		}
		sr.propRequire = !sr.propRelease
		sr.propRelease = false
		if sr.propRequire {
			sr.services.AddPropQueue(sr.self)
		}
	}

	sr.initiateStart()
}

func (sr *ServiceRecord) initiateStart() {
	if len(sr.conflicts) > 0 || sr.services.anyConflicts.Load() {
		sr.services.stopConflicting(sr.self)
	}
	wave := sr.inPartOfWave
	sr.inPartOfWave = false
	if sr.partOfRestartOnStart {
		sr.partOfRestartOnStart = false
		sr.services.propagatePartOfRestart(sr.self, wave)
	}
	// A start is underway, so the service is no longer parked on a
	// restart-delay timer — whether this start IS that restart or took
	// it over. The state leaves STOPPED just below, which is also what
	// makes a timer that still fires bow out harmlessly.
	sr.restartPending = false
	sr.stopRestartAsked = false
	sr.startFailed = false
	sr.startGen.Add(1)
	// Clear the per-session Started()-emitted flag so the next
	// successful start emits its own boot-console line.
	sr.startedEmitted = false
	sr.startSkipped = false
	sr.startSkipReason = ""
	sr.startRequestTime = time.Now()
	// Mint a fresh invocation ID so every event this cycle emits
	// carries the same SLINIT_INVOCATION_ID. Matches systemd's
	// per-start UUID and gives `slinit-journalctl --list-invocations
	// -u UNIT` a stable dedupe key.
	sr.invocationID = newInvocationID()
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
	sr.depsStartedFrom = 0

	for _, dep := range sr.dependsOn {
		to := dep.To
		if dep.IsOnlyOrdering() && to.Record().state.Load() != StateStarting {
			continue
		}
		// A soft link this service does not hold, and is not about to
		// take (a fresh start's require is still queued), was broken
		// when the dependency stopped: the dependency is not coming
		// back for us, so waiting on it would never end. That is a
		// restart of the same activation (auto-restart, restart-delay,
		// a cancelled stop); a new start requires the dependency again.
		if !dep.IsHard() && !dep.HoldingAcq && !sr.propRequire {
			continue
		}
		if to.Record().state.Load() != StateStarted {
			dep.WaitingOn = true
			allStarted = false
		}
		// A hard dependency this start neither holds nor is about to
		// require (propRequire) would be waited on with nothing to start
		// it, for good. Two ways to get there: a restart coming back
		// from its restart delay after the dependency gave up and let go
		// of it (cancelDependentsStart), and a start that lands on this
		// service while it is STOPPING after its release already ran
		// (slinitctl --no-wait restart: doStart's STOPPING branch takes
		// no hold). A dependency that gave up for good (its start
		// failed, its restart limit is used up, an operator pinned it
		// stopped or marked it down) fails this start, as the
		// dependents already STARTING are failed then; one that was only
		// let go of is required again.
		if (dep.DepType == DepRegular || dep.DepType == DepPreparedBy) &&
			!dep.HoldingAcq && !sr.propRequire {
			dr := to.Record()
			if dr.state.Load() == StateStopped && dr.desired.Load() == StateStopped &&
				(dr.startFailed || dr.pinnedStopped || dr.markedDown) {
				sr.services.logger.Error("Service '%s': its dependency '%s' has failed or is held down, not starting",
					sr.serviceName, dr.serviceName)
				sr.propFailure = true
				sr.services.AddPropQueue(sr.self)
			} else {
				dr.Require()
				dep.HoldingAcq = true
			}
		}
	}

	for _, dept := range sr.dependents {
		if !dept.WaitingOn && dept.IsOnlyOrdering() {
			// Only force the dependent to wait if it is still
			// waiting for dependencies — a service that has
			// already reached its Starting-with-deps-resolved
			// phase should not get a stale WaitingOn flag stuck
			// against a dependency that just finished starting.
			// Port of dinit's 5fe1081 (2026-09-07): "Only apply
			// ordering constraints when pre service is waiting
			// for deps". No known misbehaviour without this;
			// consistency alignment with upstream.
			from := dept.From.Record()
			if from.state.Load() == StateStarting && from.waitingForDeps {
				dept.WaitingOn = true
				from.depsStartedFrom = 0
			}
		}
	}

	return allStarted
}

// checkDepsStarted reports whether no dependency is still waited on.
// It resumes where the last call stopped (depsStartedFrom): rescanning
// from the first edge made a start waiting on N dependencies that come
// up in order cost O(N^2).
func (sr *ServiceRecord) checkDepsStarted() bool {
	for ; sr.depsStartedFrom < len(sr.dependsOn); sr.depsStartedFrom++ {
		if sr.dependsOn[sr.depsStartedFrom].WaitingOn {
			return false
		}
	}
	return true
}

func (sr *ServiceRecord) allDepsStarted() {
	// Clear waitingForDeps BEFORE the console-queue branch (was after,
	// pre-d24e6f9). Reason: once we reach this point, deps are fully
	// resolved regardless of whether we're about to bring up or queue
	// for the console. Keeping the flag true across the queueForConsole
	// branch made outside observers (e.g. startCheckDependencies scanning
	// for ordering-only dependents) misclassify us as still waiting on
	// deps, planting stale WaitingOn flags on our ordering dependents.
	// Paired with the ExecuteTransition short-circuit above.
	sr.waitingForDeps = false

	if sr.Flags.StartsOnConsole && !sr.haveConsole {
		sr.queueForConsole()
		return
	}

	// Check start limiter (skip during shutdown — don't queue services)
	if limiter := sr.services.GetStartLimiter(); limiter != nil && !sr.services.IsShuttingDown() {
		ok, waitCh := limiter.Acquire(sr.self)
		if !ok {
			sr.waitingForStartSlot = true
			sr.startSlotCh = waitCh
			// Wait for slot in a goroutine to avoid blocking the queue
			go func() {
				<-waitCh
				sr.services.queueMu.Lock()
				defer sr.services.queueMu.Unlock()
				// A stop (or a failure) withdrew this wait while we
				// slept, and dropStartSlot settled the slot: the
				// service is no longer the one that asked for it.
				if !sr.waitingForStartSlot || sr.startSlotCh != waitCh {
					return
				}
				sr.waitingForStartSlot = false
				sr.startSlotCh = nil
				if sr.state.Load() != StateStarting {
					// The start ended some other way while queued: the
					// slot just granted is not needed, so give it back
					// rather than hold it until the slow threshold.
					sr.services.GetStartLimiter().Release(sr.self)
					return
				}
				sr.callBringUp()
				sr.services.processQueuesLocked()
			}()
			return
		}
	}

	sr.callBringUp()
}

// callBringUp is the shared entry that gates every service-type
// BringUp behind the OnConfirmSpawn hook (systemd.confirm_spawn
// parity). Placed here rather than in each ProcessService /
// TriggeredService / InternalService / BGProcessService /
// ScriptedService.BringUp so a single gate covers all five types —
// otherwise operators booting with slinit.confirm-spawn would see
// process services prompt but internal / scripted / triggered ones
// slip through unasked (which was the initial cut's behaviour and
// made the feature much less useful than intended).
//
// Skip semantics: OnConfirmSpawn returning false transitions the
// record to Stopping + failedToStart, so dependents cascade — the
// point of a debugging tool is that "skipped" reveals dependency
// structure, not that the skipped svc gets silently pretended-up.
func (sr *ServiceRecord) callBringUp() {
	// Crash-shell freeze: pkg/shutdown.spawnCrashShell flips this on
	// via SetCrashPause before it SIGHUPs the current /dev/console
	// owner, so a restart=yes tty svc does not spawn a competing shell
	// while sulogin is holding the tty. No state transition, no
	// failedToStart — the service just sits in whatever state it was
	// in; the process is about to syscall.Reboot anyway.
	if sr.services.IsCrashPaused() {
		return
	}
	if sr.services.OnConfirmSpawn != nil && !sr.services.OnConfirmSpawn(sr.serviceName) {
		sr.services.logger.Info("Service '%s': skipped by confirm-spawn", sr.serviceName)
		sr.state.Store(StateStopping)
		sr.refuseStart(true)
		return
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

	if len(sr.upholds) > 0 {
		sr.services.upholdOnStarted(sr.self)
	}

	// The run of each PREPARED_BY dependency this start came up with:
	// see the restart of them in Stopped.
	for _, dep := range sr.dependsOn {
		if dep.DepType == DepPreparedBy {
			dep.toStartGen = dep.To.Record().startGen.Load()
		}
	}

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
		if cb := sr.services.OnBootReady; cb != nil {
			// Off the lock: see the OnBootReady field.
			go cb()
		}
	}

	// Signal filesystem/logging readiness
	if sr.Flags.RWReady && !sr.services.RWReady() {
		sr.services.SetRWReady()
		sr.services.logger.Info("Filesystem is now read-write (service '%s')", sr.serviceName)
		if f := sr.services.OnRWReady; f != nil {
			// The utmp/wtmp boot record: queued with the other utmp
			// writes, off queueMu (utmpqueue.go).
			sr.services.utmpQ.push(f)
		}
	}
	if sr.Flags.LogReady && !sr.services.LogReady() {
		sr.services.SetLogReady()
		sr.services.logger.Info("Logging system is now ready (service '%s')", sr.serviceName)
	}

	if sr.Flags.NoBootMarker {
		// Suppress the "[ OK ] name" boot-console line — used for
		// milestone-style services (`boot` etc.) whose completion
		// is implied by the tree underneath. Main log still records
		// the transition.
		sr.services.logger.Info("Service '%s' started (marker suppressed)", sr.serviceName)
	} else {
		sr.services.logger.ServiceStarted(sr.serviceName)
	}
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
	// A cron run still under way keeps the service in STOPPING until it
	// ends, without holding the queue lock (see cronSlot.cronHold).
	if h, ok := sr.self.(interface{ cronHold() <-chan struct{} }); ok {
		if hold := h.cronHold(); hold != nil {
			sr.services.logger.Info("Service '%s': waiting for its cron-command to finish",
				sr.serviceName)
			go func() {
				<-hold
				sr.services.queueMu.Lock()
				defer sr.services.queueMu.Unlock()
				if sr.state.Load() == StateStopping {
					sr.Stopped()
				}
				sr.services.processQueuesLocked()
			}()
			return
		}
	}
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

	// Reclaim the service's own cgroup directory. Only when the service
	// has one of its own: with neither `cgroup` nor `slice` set,
	// EffectiveCgroupPath returns the daemon-wide default, which every
	// other unconfigured service shares and which must outlive them all.
	//
	// Failure is the normal case while anything is still inside, so it
	// is deliberately silent at anything above Debug.
	sr.removeOwnCgroup()

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

	// The next stop is a new failure for the restart limit.
	sr.stopRestartAsked = false

	willRestart := sr.desired.Load() == StateStarted && !sr.pinnedStopped
	if sr.desired.Load() == StateStarted && sr.pinnedStopped {
		// A dependent asked for a start while this service was
		// stopping; the stop pin refuses it now, as doStart refuses one
		// on a STOPPED service. Fail it: left alone, the dependents
		// waited for good, and their hold kept this service counted
		// active even once they let go of it, as a release reaching 0
		// on a STOPPED service does not mark it inactive.
		sr.desired.Store(StateStopped)
		sr.cancelDependentsStart(true)
	}
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
		// A dependent may already be waiting for this start: one with
		// restart = yes that this service's last exit stopped comes back
		// at once and asks for it again. Fail it, as a failed start
		// would; left alone it waited for good. (One still parked on
		// its restart delay fails when it comes back: see
		// startCheckDependencies.)
		sr.cancelDependentsStart(true)
		// systemd StartLimitAction=: escalate a rate-limit failure into a
		// system-level action (reboot/kexec/etc). Fires here — not from
		// chooseStoppedAction — so it's independent of the failure-action
		// path and always triggers when the operator asked for it.
		if act := sr.startLimitAction; act != ActionNone {
			if cb := sr.services.OnSystemAction; cb != nil {
				sr.services.logger.Info(
					"Service '%s': start-limit-action=%s — initiating system action",
					sr.serviceName, act)
				// Off the lock: see the OnSystemAction field.
				go cb(act, sr.rebootArgument)
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
		// slinitctl restart stops and starts as two requests, so willRestart
		// alone would call it a final deactivation, and the store is documented
		// to survive a restart.
		keep := willRestart || sr.restarting
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
	// A restart, automatic or asked for, restarts the services part of
	// this one when it starts again (initiateStart): after any
	// restart-delay, so one ordered after: this service does not come
	// up while it still waits out the delay. RestartMode=direct keeps
	// them up, as it keeps dependents up.
	rerun := sr.rerunJob
	sr.rerunJob = false
	if (willRestart || sr.restarting) && sr.restartMode != RestartModeDirect && !rerun {
		sr.partOfRestartOnStart = true
	}
	sr.restarting = false

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

	// One that comes back by itself needs nobody to start it again.
	if !willRestart && sr.services.anyUpholds.Load() && !sr.services.IsShuttingDown() {
		sr.services.upholdOnStopped(sr.self)
	}

	if willRestart {
		// Record this as a supervisor-driven restart for heartbeat /
		// health-signal accounting. First-boot BringUp does not count.
		if !rerun {
			sr.restarts.Add(1)
			sr.services.NoteRestart()
		}

		// Restart any PREPARED_BY dependencies first. They are hard deps
		// (IsHard returns true), so startCheckDependencies in initiateStart
		// below will see them in non-STARTED state and make us wait until
		// they reach STARTED again.
		//
		// One that started again since this service came up (its own
		// restart after the crash that stopped this one) is already
		// the fresh run: restarting it again killed that run
		// part-way, and spawned once more past the restart limit.
		for _, dep := range sr.dependsOn {
			if !rerun && dep.DepType == DepPreparedBy && dep.To.Record().startGen.Load() == dep.toStartGen {
				dep.To.Record().Restart()
			}
		}
		// Honour restart-delay / restart-delay-step / restart-delay-cap
		// on the willRestart path. If the service arms a private timer,
		// it will call initiateStart itself when the timer fires — we
		// bail out here so the two paths don't race. Services without
		// restart-delay semantics (default ServiceRecord) return false
		// and the immediate initiateStart runs as before.
		if !rerun && sr.self.ScheduleRestartWithBackoff() {
			sr.restartPending = true
		} else {
			sr.initiateStart()
		}
	} else {
		sr.settleInactive()
	}

	// systemd-style failure-action / success-action: pick whichever
	// applies and let main's shutdown initiator handle it. The hook is
	// invoked AFTER state is STOPPED so a reboot-action service is
	// reported as STOPPED to listeners that fire before the reboot.
	// Not for a start that failed because a dependency did, unless
	// failure-conditions counts that (systemd does not run OnFailure=
	// for a job that failed on a dependency). failure-action keeps its
	// older reading, which a setup may rely on.
	if len(sr.onFailure) > 0 && !willRestart && !sr.services.IsShuttingDown() && sr.stoppedAs() == endFailed &&
		(sr.stopReason != ReasonDepFailed || sr.failureConditions.Dependency) {
		sr.services.queueOnFailure(sr.self)
	}
	if action := sr.chooseStoppedAction(willRestart); action != ActionNone {
		if cb := sr.services.OnSystemAction; cb != nil {
			kind := "success"
			if action == sr.failureAction {
				kind = "failure"
			}
			sr.services.logger.Info(
				"Service '%s': %s-action=%s — initiating system action",
				sr.serviceName, kind, action)
			// Off the lock: see the OnSystemAction field.
			go cb(action, sr.rebootArgument)
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

// cancelDependentsStart fails the start of the hard dependents waiting
// for this service, lets the soft ones go on without it, and releases
// what every dependent holds on it.
//
// onlyWaiting skips a milestone dependent no longer waiting for this
// service: Stopped() calls it for a service that had started, so such a
// dependent's milestone was reached and its own start is under way.
// failedToStart passes false: a service that never started has no
// reached milestones, and dinit fails every STARTING milestone there.
func (sr *ServiceRecord) cancelDependentsStart(onlyWaiting bool) {
	for _, dept := range sr.dependents {
		if onlyWaiting && dept.DepType == DepMilestone && !dept.WaitingOn {
			continue
		}
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
}

// checkRestartOnce asks the restart limit whether the stop under way
// may restart, once per stop. doStop asks it for the service's own
// restart and stopDependents for the restart of its dependents (a
// dependency's limit limits them too); each call used to count the stop
// against the limit again, so restart-limit-count = 3 allowed 2
// restarts. The answer is forgotten when the stop ends (Stopped).
func (sr *ServiceRecord) checkRestartOnce() bool {
	if !sr.stopRestartAsked {
		sr.stopRestartAsked = true
		sr.stopRestartOK = sr.self.CheckRestart()
	}
	return sr.stopRestartOK
}

// refuseStart is failedToStart for a start an operator refused (a stop
// pin, a "no" at confirm-spawn): that is the operator's will, not a
// failure, so an earlier stop request stands.
func (sr *ServiceRecord) refuseStart(immediateStop bool) {
	marked := sr.services.OperatorStopRequested()
	sr.failedToStart(false, immediateStop)
	if marked {
		sr.services.MarkOperatorStop()
	}
}

// failedToStart handles start failure.
func (sr *ServiceRecord) failedToStart(depFailed bool, immediateStop bool) {
	// A start that failed on its own is a failure, not a stop on
	// request: a container that ends now must not report an operator's
	// earlier stop (a restart whose start failed after an upgrade
	// removed the binary exited 0). A dependent failing with it changes
	// nothing more.
	if !depFailed {
		sr.services.ClearOperatorStop()
	}
	// Release start limiter slot or cancel waiting
	sr.dropStartSlot()

	sr.desired.Store(StateStopped)

	if sr.waitingForConsole {
		sr.services.UnqueueConsole(sr.self)
		sr.waitingForConsole = false
	}

	if sr.startExplicit {
		sr.startExplicit = false
		sr.Release(false)
	}

	sr.cancelDependentsStart(false)

	// A start that failed is over: STOPPING until the process, if any,
	// is gone (dinit's failed_to_start). Left in STARTING, the process's
	// exit was taken for another failed start and reported again.
	if sr.state.Load() != StateStopped {
		sr.state.Store(StateStopping)
	}

	sr.startFailed = true
	sr.services.logger.ServiceFailed(sr.serviceName, depFailed)
	sr.notifyListeners(EventFailedStart)
	if sr.pinnedStarted {
		sr.pinnedStarted = false
		// The pin reached the dependencies too: dropped here alone, it
		// kept them pinned by a dependent that pinned nothing, and they
		// could not be stopped again, at shutdown either.
		if !sr.deptPinnedStarted {
			sr.unpinDependencies()
		}
	}

	if immediateStop {
		sr.Stopped()
	}
}

func (sr *ServiceRecord) doStop(withRestart bool) {
	if sr.IsStartPinned() {
		// No stop: a restart answer a watchdog or health check asked
		// for is not this service's next failure.
		sr.stopRestartAsked = false
		return
	}

	sr.inAutoRestart = false
	sr.inUserRestart = false

	// A stop that was asked for, and is not the first half of a
	// restart, stops the services part of this one; a crash does not
	// (desired is still STARTED then), as systemd propagates stop jobs
	// and not a unit's own death.
	if !withRestart && !sr.restarting && sr.desired.Load() == StateStopped {
		sr.partOfRestartOnStart = false
		sr.inPartOfWave = false
		sr.services.propagatePartOfStop(sr.self)
	}

	forRestart := withRestart
	// systemd RestartMode=direct: keep dependents up through this
	// service's restart cycle. `withRestart` still gates the parent
	// itself, only the cascade to dependents is suppressed.
	restartDeps := withRestart && sr.restartMode != RestartModeDirect && !sr.rerunJob

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
		autoRestart := sr.autoRestartThisRun()

		// systemd RestartForceExitStatus: codes that force a restart
		// regardless of the `restart =` setting. Applied FIRST so
		// even a normal-exit code entry gets overridden — the
		// operator's declared "always come back on THIS code" wins
		// over both `restart = no` and `normal-exit`.
		if !sr.onceRun && sr.desired.Load() == StateStarted && sr.IsForceRestartExit(exitStatus) {
			wantedRestart = true
			forRestart = sr.checkRestartOnce()
			sr.inAutoRestart = forRestart
			// Skip the per-mode auto-restart block below — the force
			// path already decided.
			sr.restartLimitExhausted = wantedRestart && !forRestart
			goto forceHandled
		}

		// Check for auto-restart
		if autoRestart == RestartAlways && sr.desired.Load() == StateStarted {
			if !normal {
				wantedRestart = true
				forRestart = sr.checkRestartOnce()
				sr.inAutoRestart = forRestart
			}
		} else if autoRestart == RestartOnFailure && sr.desired.Load() == StateStarted {
			// failure-conditions decides what "failure" means here;
			// its default is the exit-status reading below.
			if sr.stopReason == ReasonDepFailed && sr.failureConditions.Dependency {
				wantedRestart = true
				forRestart = !sr.depRestartDenied && sr.checkRestartOnce()
				sr.inAutoRestart = forRestart
				sr.depRestartDenied = false
			} else if sr.stopReason == ReasonFailed ||
				(!normal && sr.processEndIsFailure(exitStatus, restartableFailure(exitStatus))) {
				wantedRestart = true
				forRestart = sr.checkRestartOnce()
				sr.inAutoRestart = forRestart
			}
		} else if autoRestart == RestartOnSuccess && sr.desired.Load() == StateStarted {
			if !normal {
				// The inverse of on-failure, and deliberately strict
				// about what counts: only a real exit with code 0.
				//
				// A signal is not a clean exit, whatever the signal —
				// launchd's wording is "exits and with an exit status of
				// zero", and a job killed by SIGTERM has not finished its
				// work. And Vanished carries no status at all: a daemon
				// that disappeared cannot be said to have succeeded, so
				// the cycle ends rather than looping on an unknown.
				if exitStatus.Exited() && exitStatus.ExitCode() == 0 {
					wantedRestart = true
					forRestart = sr.checkRestartOnce()
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
			if !sr.waitingForDeps && !sr.waitingForConsole && !sr.waitingForStartSlot {
				if !sr.self.CanInterruptStart() {
					return
				}
				if !sr.self.InterruptStart() {
					sr.notifyListeners(EventStartCancelled)
					return
				}
				// The start ends here, without Started or
				// failedToStart: give its slot back.
				sr.dropStartSlot()
			} else if sr.waitingForConsole {
				sr.services.UnqueueConsole(sr.self)
				sr.waitingForConsole = false
			} else if sr.waitingForStartSlot {
				// Nothing has been launched yet, so like the
				// console wait it can always be interrupted.
				sr.dropStartSlot()
			}

			sr.notifyListeners(EventStartCancelled)
		} else {
			return
		}
	}

	sr.state.Store(StateStopping)
	// A pressure event is of no use once the service is on its way down,
	// and closing the triggers now keeps their teardown off the rmdir in
	// Stopped (see psiWatcher).
	sr.cancelPSIWatcher()
	sr.waitingForDeps = !allDepsStopped
	if allDepsStopped {
		sr.services.AddTransitionQueue(sr.self)
	}
}

// dropStartSlot gives back what the start limiter holds for this
// service: a queued wait is withdrawn (waking its goroutine, which then
// sees the wait is gone), and a slot already granted is released.
func (sr *ServiceRecord) dropStartSlot() {
	limiter := sr.services.GetStartLimiter()
	if limiter == nil {
		return
	}
	if sr.waitingForStartSlot {
		sr.waitingForStartSlot = false
		sr.startSlotCh = nil
		if limiter.CancelWait(sr.self) {
			return
		}
	}
	limiter.Release(sr.self)
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
		// The dependent's actual state, not HoldingAcq. HoldingAcq is
		// released when the dependent *begins* stopping, so reading it
		// here answered "has it been asked to stop", not "has it
		// stopped" — and we would go down underneath a hard dependent
		// still in STOPPING. dinit carried the same bug and fixed it in
		// 5409175 (its issue #591); neither project's tests caught it.
		//
		// WaitingOn still means restarting: the flag is cleared when we
		// stop and is only set again if the dependent tries to come back
		// up, so such a dependent counts as stopped for this decision.
		if dept.IsHard() && !dept.WaitingOn &&
			dept.From.Record().state.Load() != StateStopped {
			return false
		}
	}
	return true
}

func (sr *ServiceRecord) stopDependents(forRestart bool, restartDeps bool) bool {
	allStopped := true

	// Restarting a dependent restarts this service with it, so what limits
	// this one has to limit that: otherwise a dependency that dies as soon as
	// it starts, and has no restart of its own to count, loops for ever. Asked
	// once per stop, however many dependents, and inherited down a chain.
	depFailure, depRestartDenied := false, false
	if sr.forceStop && sr.desired.Load() != StateStopped && sr.stopIsFailure() {
		depFailure = true
		depRestartDenied = sr.depRestartDenied || !sr.checkRestartOnce()
	}

	for _, dept := range sr.dependents {
		if dept.IsHard() {
			depFrom := dept.From.Record()

			if !depFrom.IsFundamentallyStopped() {
				allStopped = false
			}

			// A dependent that is already STOPPED has nothing to stop
			// and keeps the reason it stopped for (dinit: continue).
			// One parked on a restart-delay timer is still active and
			// still has to learn it is not wanted any more.
			if depFrom.state.Load() == StateStopped && !depFrom.restartPending {
				continue
			}

			if sr.forceStop {
				if sr.desired.Load() == StateStopped {
					// The dependent stops for this service's reason: it
					// failed only if this service did (dinit's
					// is_dept_failure).
					if sr.stopIsFailure() {
						depFrom.stopReason = ReasonDepFailed
					} else {
						depFrom.stopReason = ReasonNormal
					}
					depFrom.UnrecoverableStop()
				} else {
					// The path a dying dependency takes: its desired
					// state is still STARTED here. dependency-fails is
					// decided on this branch in dinit too.
					if depFrom.failureConditions.Dependency {
						if depFailure {
							depFrom.stopReason = ReasonDepFailed
							depFrom.depRestartDenied = depRestartDenied
						} else if depFrom.stopReason != ReasonDepFailed ||
							depFrom.state.Load() != StateStopping {
							// A dependent still stopping for this failure
							// keeps it. A restart that outruns the dependent
							// brings this service here a second time, its
							// exit status gone, looking like a clean stop.
							depFrom.stopReason = ReasonNormal
						}
					}
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
	// Guard against double-enqueue if allDepsStarted re-enters while
	// we are still waiting for the console — that would corrupt the
	// console queue (same service appears twice, second PullConsoleQueue
	// finds a service that has already released the console). Port of
	// dinit's a000e76 (2026-09-07).
	if sr.waitingForConsole {
		return
	}
	sr.waitingForConsole = true
	sr.services.AppendConsoleQueue(sr.self)
}

func (sr *ServiceRecord) releaseConsole() {
	sr.haveConsole = false
	sr.services.consoleHeld = false
	sr.services.PullConsoleQueue()
}

// AcquiredConsole is called when the console becomes available.
func (sr *ServiceRecord) AcquiredConsole() {
	sr.waitingForConsole = false
	sr.haveConsole = true
	sr.services.consoleHeld = true

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
	// The run of a PREPARED_BY dependency that is current when the edge
	// comes, so a dependent already up (add-dep, a reload) still has
	// it restarted at its own next restart: see Started and Stopped.
	if depType == DepPreparedBy {
		dep.toStartGen = toRec.startGen.Load()
	}

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
	sr.depsStartedFrom = 0

	// A pin that came down this edge has to be recomputed, as Unpin
	// does: left alone, the target stayed pinned by a dependent it no
	// longer had, and could not be stopped again, at shutdown either.
	if dep.carriesPin() && toRec.deptPinnedStarted {
		toRec.propPinDpt = true
		sr.services.AddPropQueue(dep.To)
	}
	// A target stopping may have been waiting for this dependent to
	// stop: let it check again, as dependentStopped does, or it waited
	// for a dependent it no longer had.
	if toRec.state.Load() == StateStopping && toRec.waitingForDeps {
		sr.services.AddTransitionQueue(dep.To)
	}

	// A start waiting on this dependency has nothing left to wait for
	// from it: let the start re-check the dependencies it still has,
	// as stopDependents does when it breaks a soft link. dinit's
	// rm_dep has the same gap.
	if dep.WaitingOn {
		dep.WaitingOn = false
		sr.dependencyStarted()
	}
}

// SetDependents replaces the dependents slice (used during reload to transfer dependents).
func (sr *ServiceRecord) SetDependents(deps []*ServiceDep) {
	sr.dependents = deps
}

// ClearDependencies removes all dependencies without updating the target's dependents.
func (sr *ServiceRecord) ClearDependencies() {
	sr.dependsOn = nil
	sr.depsStartedFrom = 0
}

// HasLoneRef returns true if this service has no significant references beyond
// the given handleCount (control connection handles). A service can be unloaded
// only if all its dependents are ordering-only (BEFORE/AFTER).
//
// A log consumer does not hold the service: UnloadService hands the pipe
// and the link to a placeholder, as dinit's has_lone_ref allows.
func (sr *ServiceRecord) HasLoneRef(handleCount int) bool {
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
	// A record about to leave the set must not leave a restart-delay
	// timer armed behind it, nor the active-service count it is still
	// holding — GetActiveServiceInfo would no longer be able to name it
	// as a blocker, since it is about to stop being a record at all.
	sr.abandonPendingRestart()

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
	sr.depsStartedFrom = 0

	// Remove ourselves from each dependent's dependsOn list
	for _, dept := range sr.dependents {
		fromRec := dept.From.Record()
		for j, d := range fromRec.dependsOn {
			if d == dept {
				fromRec.dependsOn = append(fromRec.dependsOn[:j], fromRec.dependsOn[j+1:]...)
				fromRec.depsStartedFrom = 0
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

// rmDepRecord removes this exact dependency record, by identity rather
// than by (target, type) pair. Returns false if it is not ours.
// Caller must hold the graph lock.
func (sr *ServiceRecord) rmDepRecord(dep *ServiceDep) bool {
	for i, d := range sr.dependsOn {
		if d == dep {
			sr.rmDepByIndex(i)
			return true
		}
	}
	return false
}

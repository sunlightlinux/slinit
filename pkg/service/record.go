package service

import (
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
)

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
	BringUp() bool   // start the service; returns false on failure
	BringDown()      // stop the service
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
	recordType  ServiceType

	// State
	state   ServiceState
	desired ServiceState

	// Flags
	autoRestart    AutoRestartMode
	smoothRecovery bool

	// Pins
	pinnedStopped     bool
	pinnedStarted     bool
	deptPinnedStarted bool
	markedDown        bool // 'down' file exists — don't auto-start

	// Waiting flags
	waitingForDeps    bool
	waitingForConsole  bool
	haveConsole        bool
	startExplicit      bool
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
	termSignal    syscall.Signal
	socketPath    string   // primary socket path (for backwards compat)
	socketPaths   []string // all socket-listen paths (for multiple sockets)
	socketPerms   int
	socketUID     int
	socketGID     int
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
	sharedLoggerName string // name of the shared logger service (empty if not used)

	// Runtime environment variables (set via control protocol)
	extraEnv map[string]string

	// Process attributes (applied post-fork)
	nice        *int
	oomScoreAdj *int
	noNewPrivs  bool
	ioPrioClass int
	ioPrioLevel int
	cgroupPath  string
	rlimits     []process.Rlimit
	ambientCaps []uintptr
	securebits  uint32
	cpuAffinity []uint
	cloneflags  uintptr              // namespace clone flags (CLONE_NEWPID, CLONE_NEWNS, etc.)
	uidMappings []syscall.SysProcIDMap // user namespace UID mappings
	gidMappings []syscall.SysProcIDMap // user namespace GID mappings

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
}

// NewServiceRecord creates a new ServiceRecord with default values.
func NewServiceRecord(self Service, set *ServiceSet, name string, recordType ServiceType) *ServiceRecord {
	return &ServiceRecord{
		self:        self,
		serviceName: name,
		recordType:  recordType,
		state:       StateStopped,
		desired:     StateStopped,
		autoRestart: RestartNever,
		termSignal:  syscall.SIGTERM,
		services:    set,
	}
}

// --- Interface implementation methods ---

func (sr *ServiceRecord) Name() string               { return sr.serviceName }
func (sr *ServiceRecord) ServiceDir() string          { return sr.serviceDir }
func (sr *ServiceRecord) SetServiceDir(dir string)    { sr.serviceDir = dir }
func (sr *ServiceRecord) LoadModTime() time.Time       { return sr.loadModTime }
func (sr *ServiceRecord) SetLoadModTime(t time.Time)   { sr.loadModTime = t }
func (sr *ServiceRecord) Type() ServiceType           { return sr.recordType }
func (sr *ServiceRecord) State() ServiceState      { return sr.state }
func (sr *ServiceRecord) TargetState() ServiceState { return sr.desired }
func (sr *ServiceRecord) StopReason() StoppedReason { return sr.stopReason }
func (sr *ServiceRecord) RequiredBy() int          { return sr.requiredBy }
func (sr *ServiceRecord) Dependencies() []*ServiceDep { return sr.dependsOn }
func (sr *ServiceRecord) Dependents() []*ServiceDep   { return sr.dependents }
func (sr *ServiceRecord) DepDepth() int                { return sr.depDepth }
func (sr *ServiceRecord) SetDepDepth(d int)            { sr.depDepth = d }
func (sr *ServiceRecord) Record() *ServiceRecord   { return sr }
func (sr *ServiceRecord) PID() int                 { return -1 }
func (sr *ServiceRecord) GetExitStatus() ExitStatus { return ExitStatus{} }
func (sr *ServiceRecord) BecomingInactive()        {}
func (sr *ServiceRecord) CheckRestart() bool       { return true }
func (sr *ServiceRecord) GetSmoothRecovery() bool  { return sr.smoothRecovery }

// UnrecoverableStop forces the service to stop without possibility of restart.
func (sr *ServiceRecord) UnrecoverableStop() {
	sr.desired = StateStopped
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
func (sr *ServiceRecord) SetChainTo(name string)              { sr.chainTo = name }
func (sr *ServiceRecord) SetServiceDscDir(dir string)         { sr.serviceDscDir = dir }
func (sr *ServiceRecord) SetTermSignal(sig syscall.Signal)     { sr.termSignal = sig }

func (sr *ServiceRecord) SetFlags(flags ServiceFlags) { sr.Flags = flags }
func (sr *ServiceRecord) SetProvides(name string)     { sr.provides = name }
func (sr *ServiceRecord) Provides() string             { return sr.provides }
func (sr *ServiceRecord) SetEnableVia(name string)     { sr.enableVia = name }
func (sr *ServiceRecord) EnableVia() string             { return sr.enableVia }

func (sr *ServiceRecord) SetLogConsumer(svc Service)   { sr.logConsumer = svc }
func (sr *ServiceRecord) LogConsumer() Service         { return sr.logConsumer }
func (sr *ServiceRecord) SetConsumerFor(svc Service)   { sr.consumerFor = svc }
func (sr *ServiceRecord) ConsumerFor() Service         { return sr.consumerFor }
func (sr *ServiceRecord) SetSharedLoggerName(name string) { sr.sharedLoggerName = name }
func (sr *ServiceRecord) SharedLoggerName() string        { return sr.sharedLoggerName }
func (sr *ServiceRecord) OutputPipeW() *os.File        { return sr.outputPipeW }
func (sr *ServiceRecord) OutputPipeR() *os.File        { return sr.outputPipeR }

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

func (sr *ServiceRecord) IsMarkedActive() bool   { return sr.startExplicit }
func (sr *ServiceRecord) IsStartPinned() bool    { return sr.pinnedStarted || sr.deptPinnedStarted }
func (sr *ServiceRecord) IsStopPinned() bool     { return sr.pinnedStopped }
func (sr *ServiceRecord) DidStartFail() bool     { return sr.startFailed }
func (sr *ServiceRecord) WasStartSkipped() bool  { return sr.startSkipped }
func (sr *ServiceRecord) IsLoading() bool        { return sr.isLoading }
func (sr *ServiceRecord) HasConsole() bool       { return sr.haveConsole }
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
	return result
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
	return env
}

// --- Process attribute setters ---

func (sr *ServiceRecord) SetNice(n *int)                    { sr.nice = n }
func (sr *ServiceRecord) SetOOMScoreAdj(n *int)             { sr.oomScoreAdj = n }
func (sr *ServiceRecord) SetNoNewPrivs(v bool)              { sr.noNewPrivs = v }
func (sr *ServiceRecord) SetIOPrio(class, level int)        { sr.ioPrioClass = class; sr.ioPrioLevel = level }
func (sr *ServiceRecord) SetCgroupPath(p string)            { sr.cgroupPath = p }
func (sr *ServiceRecord) SetRlimits(rl []process.Rlimit)    { sr.rlimits = rl }
func (sr *ServiceRecord) AddRlimit(rl process.Rlimit)       { sr.rlimits = append(sr.rlimits, rl) }
func (sr *ServiceRecord) SetAmbientCaps(caps []uintptr)      { sr.ambientCaps = caps }
func (sr *ServiceRecord) SetSecurebits(bits uint32)           { sr.securebits = bits }
func (sr *ServiceRecord) SetCPUAffinity(cpus []uint)          { sr.cpuAffinity = cpus }
func (sr *ServiceRecord) SetCloneflags(flags uintptr)          { sr.cloneflags = flags }
func (sr *ServiceRecord) SetUidMappings(m []syscall.SysProcIDMap) { sr.uidMappings = m }
func (sr *ServiceRecord) SetGidMappings(m []syscall.SysProcIDMap) { sr.gidMappings = m }

// EffectiveCgroupPath returns the cgroup path for this service,
// falling back to the daemon default. Empty if neither is set.
func (sr *ServiceRecord) EffectiveCgroupPath() string {
	if sr.cgroupPath != "" {
		return sr.cgroupPath
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
	params.CgroupPath = sr.cgroupPath
	if params.CgroupPath == "" {
		params.CgroupPath = sr.services.DefaultCgroupPath()
	}
	params.Rlimits = sr.rlimits
	params.AmbientCaps = sr.ambientCaps
	params.Securebits = sr.securebits
	params.CPUAffinity = sr.cpuAffinity
	if len(params.CPUAffinity) == 0 {
		params.CPUAffinity = sr.services.DefaultCPUAffinity()
	}
	params.Cloneflags = sr.cloneflags
	params.UidMappings = sr.uidMappings
	params.GidMappings = sr.gidMappings

	// Inject dinit-compatible query env vars
	params.Env = append(params.Env, "SLINIT_SERVICENAME="+sr.serviceName)
	if sr.serviceDir != "" {
		params.Env = append(params.Env, "SLINIT_SERVICEDSCDIR="+sr.serviceDir)
	}
}

// Default log buffer implementations (overridden by process-based services)
func (sr *ServiceRecord) GetLogBuffer() *LogBuffer { return nil }
func (sr *ServiceRecord) GetLogType() LogType      { return LogNone }

// Boot timing getters
func (sr *ServiceRecord) StartRequestTime() time.Time { return sr.startRequestTime }
func (sr *ServiceRecord) StartedTime() time.Time      { return sr.startedTime }
func (sr *ServiceRecord) StoppedTime() time.Time       { return sr.stoppedTime }

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
	return sr.state == StateStopped ||
		(sr.state == StateStarting && sr.waitingForDeps)
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

	found := false
	for _, dept := range sr.dependents {
		if dept.IsOnlyOrdering() {
			continue
		}
		from := dept.From
		fromState := from.State()
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
		sr.desired = StateStopped
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

	if bringDown && sr.state != StateStopped {
		sr.stopReason = ReasonNormal
		sr.doStop(false)
	}
}

// Restart restarts the service. Returns true if restart was issued.
func (sr *ServiceRecord) Restart() bool {
	if sr.state == StateStarted {
		sr.stopReason = ReasonNormal
		sr.forceStop = true
		sr.doStop(true)
		return true
	}
	return false
}

// ForcedStop marks this service and all dependents for forced stop.
func (sr *ServiceRecord) ForcedStop() {
	if sr.state != StateStopped {
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

		if sr.state == StateStarted {
			if sr.requiredBy == 0 {
				sr.propRelease = true
				sr.services.AddPropQueue(sr.self)
			}
			if sr.desired == StateStopped || sr.forceStop {
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
func (sr *ServiceRecord) Require() {
	sr.requiredBy++
	if sr.requiredBy == 1 {
		if sr.state != StateStarting && sr.state != StateStarted {
			sr.propStart = true
			sr.services.AddPropQueue(sr.self)
		}
	}
}

// Release decrements the required_by count and triggers stop if appropriate.
func (sr *ServiceRecord) Release(issueStop bool) {
	sr.requiredBy--
	if sr.requiredBy == 0 {
		if sr.state == StateStopping {
			if sr.desired == StateStarted && !sr.IsStartPinned() {
				sr.notifyListeners(EventStartCancelled)
			}
		}
		sr.desired = StateStopped

		if sr.IsStartPinned() {
			return
		}

		sr.propRelease = !sr.propRequire
		sr.propRequire = false
		if sr.propRelease {
			sr.services.AddPropQueue(sr.self)
		}

		if sr.state != StateStopped && sr.state != StateStopping && issueStop {
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
		sr.state = StateStopped
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
				if (sr.desired == StateStopped || sr.forceStop) && sr.state == StateStarted {
					sr.doStop(false)
				}
			}
		}
	}
}

// ExecuteTransition performs a state transition based on the current and desired states.
func (sr *ServiceRecord) ExecuteTransition() {
	if sr.state == StateStarting {
		if sr.checkDepsStarted() {
			sr.waitingForDeps = false
			sr.allDepsStarted()
		}
	} else if sr.state == StateStopping {
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
	wasActive := sr.state != StateStopped

	sr.desired = StateStarted

	if sr.pinnedStopped {
		if !wasActive {
			sr.failedToStart(false, false)
		}
		return
	}

	// 'down' marker prevents auto-start (e.g., as dependency)
	// Explicit Start() clears markedDown before calling doStart()
	if sr.markedDown {
		sr.desired = StateStopped
		return
	}

	// Re-attach soft dependents when starting again
	if !wasActive {
		for _, dept := range sr.dependents {
			if !dept.IsHard() {
				deptState := dept.From.Record().state
				if !dept.HoldingAcq &&
					(deptState == StateStarted || deptState == StateStarting) {
					dept.HoldingAcq = true
					sr.requiredBy++
				}
			}
		}
	}

	if wasActive {
		if sr.state != StateStopping {
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
	sr.startSkipped = false
	sr.startRequestTime = time.Now()
	sr.state = StateStarting
	sr.waitingForDeps = true

	if sr.startCheckDependencies() {
		sr.services.AddTransitionQueue(sr.self)
	}
}

func (sr *ServiceRecord) startCheckDependencies() bool {
	allStarted := true

	for _, dep := range sr.dependsOn {
		to := dep.To
		if dep.IsOnlyOrdering() && to.State() != StateStarting {
			continue
		}
		if to.State() != StateStarted {
			dep.WaitingOn = true
			allStarted = false
		}
	}

	for _, dept := range sr.dependents {
		if !dept.WaitingOn && dept.IsOnlyOrdering() {
			if dept.From.State() == StateStarting {
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
					sr.state = StateStopping
					sr.failedToStart(false, true)
				}
				sr.services.processQueuesLocked()
				sr.services.queueMu.Unlock()
			}()
			return
		}
	}

	if !sr.self.BringUp() {
		sr.state = StateStopping
		sr.failedToStart(false, true)
	}
}

// Started is called when the service has successfully started.
func (sr *ServiceRecord) Started() {
	// Release start limiter slot
	if limiter := sr.services.GetStartLimiter(); limiter != nil {
		limiter.Release(sr.self)
	}

	if sr.haveConsole && !sr.Flags.RunsOnConsole {
		sr.releaseConsole()
	}

	sr.startedTime = time.Now()

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
	sr.state = StateStarted
	sr.notifyListeners(EventStarted)

	if sr.forceStop || sr.desired == StateStopped {
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

	if sr.haveConsole {
		sr.releaseConsole()
	}

	sr.forceStop = false

	willRestart := sr.desired == StateStarted && !sr.pinnedStopped

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

	sr.state = StateStopped

	if willRestart {
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

	sr.desired = StateStopped

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
		case DepRegular, DepMilestone:
			if dept.From.State() == StateStarting {
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
	restartDeps := withRestart

	if !withRestart {
		// Check for auto-restart
		if sr.autoRestart == RestartAlways && sr.desired == StateStarted {
			forRestart = sr.self.CheckRestart()
			sr.inAutoRestart = forRestart
		} else if sr.autoRestart == RestartOnFailure && sr.desired == StateStarted {
			exitStatus := sr.self.GetExitStatus()
			if exitStatus.Signaled() {
				// Don't auto-restart for administrative signals (matching dinit)
				sig := exitStatus.Signal()
				if sig != syscall.SIGHUP && sig != syscall.SIGINT &&
					sig != syscall.SIGUSR1 && sig != syscall.SIGUSR2 &&
					sig != syscall.SIGTERM {
					forRestart = sr.self.CheckRestart()
					sr.inAutoRestart = forRestart
				}
			} else if exitStatus.Exited() && exitStatus.ExitCode() != 0 {
				forRestart = sr.self.CheckRestart()
				sr.inAutoRestart = forRestart
			}
		}
	}

	// If we won't restart, release explicit activation
	if !forRestart {
		if sr.startExplicit {
			sr.startExplicit = false
			sr.Release(false)
		}
	}

	allDepsStopped := sr.stopDependents(forRestart, restartDeps)

	if sr.state != StateStarted {
		if sr.state == StateStarting {
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

	sr.state = StateStopping
	sr.waitingForDeps = !allDepsStopped
	if allDepsStopped {
		sr.services.AddTransitionQueue(sr.self)
	}
}

func (sr *ServiceRecord) dependencyStarted() {
	if (sr.state == StateStarting || sr.state == StateStarted) && sr.waitingForDeps {
		sr.services.AddTransitionQueue(sr.self)
	}
}

func (sr *ServiceRecord) dependentStopped() {
	if sr.state == StateStopping && sr.waitingForDeps {
		sr.services.AddTransitionQueue(sr.self)
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
				if sr.desired == StateStopped {
					depFrom.stopReason = ReasonDepFailed
					depFrom.UnrecoverableStop()
				} else {
					depFrom.ForcedStop()
				}
			}

			if dept.From.State() != StateStopped {
				if sr.desired == StateStopped {
					if depFrom.desired != StateStopped {
						depFrom.desired = StateStopped
						if depFrom.startExplicit {
							depFrom.startExplicit = false
							depFrom.Release(true)
						}
						depFrom.propStop = true
						sr.services.AddPropQueue(dept.From)
					}
				} else if restartDeps && dept.From.State() != StateStopping {
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

	if sr.state != StateStarting {
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
		if depType == DepRegular ||
			to.State() == StateStarted ||
			to.State() == StateStarting {
			if sr.state == StateStarting || sr.state == StateStarted {
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

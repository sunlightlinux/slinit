package service

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// ServiceLogger is the interface for logging service events.
type ServiceLogger interface {
	ServiceStarted(name string)
	ServiceStopped(name string)
	ServiceFailed(name string, depFailed bool)
	Error(format string, args ...interface{})
	Info(format string, args ...interface{})
}

// ServiceLoader is the interface for loading service descriptions from files.
type ServiceLoader interface {
	LoadService(name string) (Service, error)
	ReloadService(svc Service) (Service, error)
	ServiceDirs() []string
}

// ServiceNotFound is returned when a requested service cannot be found.
type ServiceNotFound struct {
	Name string
}

func (e *ServiceNotFound) Error() string {
	return fmt.Sprintf("service not found: %s", e.Name)
}

// EnvListener is notified when the global environment changes (setenv/unsetenv).
type EnvListener interface {
	EnvEvent(varString string, override bool)
}

// ServiceSet manages all loaded services and the processing queues.
type ServiceSet struct {
	mu             sync.RWMutex
	records        map[string]Service
	aliases        map[string]Service // provides → service mapping
	activeServices int
	restartEnabled bool
	shutdownType   ShutdownType

	// queueMu protects the processing queues, console queue, and
	// activeServices counter. It is held across entire ProcessQueues
	// drain loops and at top-level entry points (StartService,
	// StopService, etc.) so that internal callbacks (AddPropQueue,
	// AddTransitionQueue, ServiceActive, ServiceInactive) can be
	// called without re-locking.
	queueMu sync.Mutex

	// Processing queues
	propQueue    []Service // propagation queue
	stopQueue    []Service // transition/stop queue
	consoleQueue []Service // console access queue

	// Service loader
	loader ServiceLoader

	// Logger
	logger ServiceLogger

	// Boot timing
	bootStartTime   time.Time     // when slinit started (userspace begins)
	bootReadyTime   time.Time     // when boot service reached STARTED
	bootServiceName string        // name of the boot target service
	kernelUptime    time.Duration // kernel uptime at slinit start

	// Filesystem/logging readiness flags (set by services with starts-rwfs / starts-log)
	rwReady  bool
	logReady bool

	// OnPassCSFD is called when a service with pass-cs-fd creates a socketpair.
	// The callback receives the server-end net.Conn and should spawn a control
	// connection handler. Set by the control server at startup.
	OnPassCSFD func(conn net.Conn)

	// UTMP callbacks — wired by main.go to utmp package functions.
	// Keeping these as callbacks avoids a cgo dependency in the service package.
	OnUtmpCreate func(id, line string, pid int)
	OnUtmpClear  func(id, line string)
	OnRWReady   func() // called when starts-rwfs service reaches STARTED
	OnBootReady func() // called when boot service reaches STARTED (for --ready-fd)

	// Global daemon-level environment (from --env-file/-e)
	// Protected by envMu for concurrent access from control socket goroutines.
	// globalEnvVer is bumped on every mutation; readers cache (snapshot, ver)
	// and skip re-copy if version matches.
	envMu          sync.Mutex
	globalEnv      []string
	globalEnvVer   uint64 // monotonically increasing version
	globalEnvSnap  []string // cached snapshot
	globalEnvSnapV uint64   // version of cached snapshot
	envListeners   []EnvListener

	// Default cgroup base path (from --cgroup-path/-b)
	defaultCgroupPath string

	// Default CPU affinity (from --cpu-affinity/-a)
	defaultCPUAffinity []uint

	// Ready notification fd (from --ready-fd/-F), -1 if unset
	readyFD int

	// Notification channel: signaled when a service becomes inactive
	inactiveCh chan struct{}
}

// NewServiceSet creates a new ServiceSet.
func NewServiceSet(logger ServiceLogger) *ServiceSet {
	return &ServiceSet{
		records:        make(map[string]Service),
		aliases:        make(map[string]Service),
		restartEnabled: true,
		logger:         logger,
		readyFD:        -1,
	}
}

// SetLoader sets the service loader for this set.
func (ss *ServiceSet) SetLoader(loader ServiceLoader) {
	ss.loader = loader
}

// FindService locates an existing service by name or alias (provides).
// If findPlaceholders is false, placeholder services are excluded.
func (ss *ServiceSet) FindService(name string, findPlaceholders bool) Service {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	svc, ok := ss.records[name]
	if !ok {
		// Check aliases
		svc, ok = ss.aliases[name]
		if !ok {
			return nil
		}
	}
	if !findPlaceholders && svc.Type() == TypePlaceholder {
		return nil
	}
	return svc
}

// LoadService loads a service (and its dependencies) by name.
func (ss *ServiceSet) LoadService(name string) (Service, error) {
	if svc := ss.FindService(name, false); svc != nil {
		return svc, nil
	}
	if ss.loader != nil {
		return ss.loader.LoadService(name)
	}
	return nil, &ServiceNotFound{Name: name}
}

// GetLoader returns the service loader.
func (ss *ServiceSet) GetLoader() ServiceLoader { return ss.loader }

// ReplaceService atomically replaces an old service with a new one in the set.
func (ss *ServiceSet) ReplaceService(oldSvc, newSvc Service) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	// Remove old alias
	if alias := oldSvc.Record().Provides(); alias != "" {
		delete(ss.aliases, alias)
	}
	ss.records[oldSvc.Name()] = newSvc
	// Register new alias
	if alias := newSvc.Record().Provides(); alias != "" {
		ss.aliases[alias] = newSvc
	}
}

// AddService adds a service to the set. If the service has a provides
// alias, it is also registered for lookup by alias name.
func (ss *ServiceSet) AddService(svc Service) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.records[svc.Name()] = svc
	if alias := svc.Record().Provides(); alias != "" {
		ss.aliases[alias] = svc
	}
}

// RegisterAlias registers a provides alias for a service.
func (ss *ServiceSet) RegisterAlias(alias string, svc Service) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.aliases[alias] = svc
}

// RemoveService removes a service from the set.
func (ss *ServiceSet) RemoveService(svc Service) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	delete(ss.records, svc.Name())
	if alias := svc.Record().Provides(); alias != "" {
		delete(ss.aliases, alias)
	}
}

// UnloadService removes a service from the set after cleaning up all dependency links.
// The service must be STOPPED before calling this.
func (ss *ServiceSet) UnloadService(svc Service) {
	svc.Record().PrepareForUnload()
	ss.RemoveService(svc)
}

// ListServices returns all loaded services.
func (ss *ServiceSet) ListServices() []Service {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	result := make([]Service, 0, len(ss.records))
	for _, svc := range ss.records {
		result = append(result, svc)
	}
	return result
}

// StartService starts a service and processes queues.
func (ss *ServiceSet) StartService(svc Service) {
	ss.queueMu.Lock()
	defer ss.queueMu.Unlock()
	svc.Start()
	ss.processQueuesLocked()
}

// WakeService starts a service without marking it active (re-attaches to
// active dependents). Returns false if no active dependents were found.
func (ss *ServiceSet) WakeService(svc Service) bool {
	ss.queueMu.Lock()
	defer ss.queueMu.Unlock()
	ok := svc.Record().Wake()
	ss.processQueuesLocked()
	return ok
}

// StopService stops a service and processes queues.
func (ss *ServiceSet) StopService(svc Service) {
	ss.queueMu.Lock()
	defer ss.queueMu.Unlock()
	svc.Stop(true)
	ss.processQueuesLocked()
}

// ForceStopService force-stops a service and all its dependents.
func (ss *ServiceSet) ForceStopService(svc Service) {
	ss.queueMu.Lock()
	defer ss.queueMu.Unlock()
	svc.Record().ForcedStop()
	ss.processQueuesLocked()
}

// StopAllServices stops all services (for shutdown).
func (ss *ServiceSet) StopAllServices(shutdownType ShutdownType) {
	// Snapshot services under read lock to avoid racing with concurrent
	// AddService/RemoveService calls from control socket goroutines.
	ss.mu.RLock()
	snapshot := make([]Service, 0, len(ss.records))
	for _, svc := range ss.records {
		snapshot = append(snapshot, svc)
	}
	ss.mu.RUnlock()

	ss.queueMu.Lock()
	defer ss.queueMu.Unlock()
	ss.restartEnabled = false
	ss.shutdownType = shutdownType
	for _, svc := range snapshot {
		svc.Stop(false)
		svc.Unpin()
	}
	ss.processQueuesLocked()
}

// --- Queue management ---

// AddPropQueue adds a service to the propagation queue.
func (ss *ServiceSet) AddPropQueue(svc Service) {
	rec := svc.Record()
	if !rec.InPropQueue {
		rec.InPropQueue = true
		ss.propQueue = append(ss.propQueue, svc)
	}
}

// AddTransitionQueue adds a service to the transition queue.
func (ss *ServiceSet) AddTransitionQueue(svc Service) {
	rec := svc.Record()
	if !rec.InStopQueue {
		rec.InStopQueue = true
		ss.stopQueue = append(ss.stopQueue, svc)
	}
}

// ProcessQueues drains both propagation and transition queues until empty.
// This is the public entry point — it acquires queueMu. Internal callers
// that already hold queueMu must use processQueuesLocked instead.
func (ss *ServiceSet) ProcessQueues() {
	ss.queueMu.Lock()
	defer ss.queueMu.Unlock()
	ss.processQueuesLocked()
}

// processQueuesLocked is the core scheduling loop. Caller must hold queueMu.
func (ss *ServiceSet) processQueuesLocked() {
	for len(ss.propQueue) > 0 || len(ss.stopQueue) > 0 {
		// Drain propagation queue using index to avoid reslicing overhead
		pq := ss.propQueue
		ss.propQueue = nil
		for i := range pq {
			svc := pq[i]
			pq[i] = nil // allow GC
			svc.Record().InPropQueue = false
			svc.Record().DoPropagation()
		}

		if len(ss.stopQueue) > 0 {
			svc := ss.stopQueue[0]
			ss.stopQueue[0] = nil
			ss.stopQueue = ss.stopQueue[1:]
			svc.Record().InStopQueue = false
			svc.Record().ExecuteTransition()
		}
	}
	ss.stopQueue = nil
}

// --- Console queue ---

// AppendConsoleQueue adds a service to the console wait queue.
func (ss *ServiceSet) AppendConsoleQueue(svc Service) {
	ss.consoleQueue = append(ss.consoleQueue, svc)
}

// PullConsoleQueue dispatches the next service waiting for the console.
func (ss *ServiceSet) PullConsoleQueue() {
	if len(ss.consoleQueue) == 0 {
		return
	}
	front := ss.consoleQueue[0]
	ss.consoleQueue = ss.consoleQueue[1:]
	front.Record().AcquiredConsole()
}

// UnqueueConsole removes a service from the console queue.
func (ss *ServiceSet) UnqueueConsole(svc Service) {
	for i, s := range ss.consoleQueue {
		if s == svc {
			last := len(ss.consoleQueue) - 1
			ss.consoleQueue[i] = ss.consoleQueue[last]
			ss.consoleQueue[last] = nil
			ss.consoleQueue = ss.consoleQueue[:last]
			return
		}
	}
}

// --- Active service tracking ---

// ServiceActive increments the active service count.
func (ss *ServiceSet) ServiceActive(svc Service) {
	ss.activeServices++
}

// ServiceInactive decrements the active service count.
func (ss *ServiceSet) ServiceInactive(svc Service) {
	ss.activeServices--
	// Notify event loop that a service became inactive
	if ss.inactiveCh != nil {
		select {
		case ss.inactiveCh <- struct{}{}:
		default:
		}
	}
}

// CountActiveServices returns the number of active services.
func (ss *ServiceSet) CountActiveServices() int {
	ss.queueMu.Lock()
	defer ss.queueMu.Unlock()
	return ss.activeServices
}

// InactiveCh returns a channel that receives a signal when any service
// becomes inactive. The event loop selects on this to detect shutdown completion.
func (ss *ServiceSet) InactiveCh() <-chan struct{} {
	if ss.inactiveCh == nil {
		ss.inactiveCh = make(chan struct{}, 1)
	}
	return ss.inactiveCh
}

// IsShuttingDown returns true if automatic restart is disabled (shutdown in progress).
func (ss *ServiceSet) IsShuttingDown() bool {
	return !ss.restartEnabled
}

// GetShutdownType returns the current shutdown type.
func (ss *ServiceSet) GetShutdownType() ShutdownType {
	return ss.shutdownType
}

// --- Boot timing ---

func (ss *ServiceSet) SetBootStartTime(t time.Time)   { ss.bootStartTime = t }
func (ss *ServiceSet) SetBootServiceName(name string)  { ss.bootServiceName = name }
func (ss *ServiceSet) SetKernelUptime(d time.Duration) { ss.kernelUptime = d }

func (ss *ServiceSet) BootStartTime() time.Time   { return ss.bootStartTime }
func (ss *ServiceSet) BootReadyTime() time.Time    { return ss.bootReadyTime }
func (ss *ServiceSet) BootServiceName() string     { return ss.bootServiceName }
func (ss *ServiceSet) KernelUptime() time.Duration { return ss.kernelUptime }

// ResetBootTiming resets boot timing for a fresh boot cycle (e.g., after recovery).
// Sets bootStartTime to now and clears bootReadyTime so it will be set again
// when the boot service next reaches STARTED.
func (ss *ServiceSet) ResetBootTiming() {
	ss.bootStartTime = time.Now()
	ss.bootReadyTime = time.Time{}
}

// --- Global daemon settings ---

func (ss *ServiceSet) SetGlobalEnv(env []string) {
	ss.envMu.Lock()
	defer ss.envMu.Unlock()
	ss.globalEnv = env
	ss.globalEnvVer++
}

// GlobalEnv returns a snapshot of the global environment.
// Uses copy-on-write: the snapshot is cached and only rebuilt when the env changes.
func (ss *ServiceSet) GlobalEnv() []string {
	ss.envMu.Lock()
	defer ss.envMu.Unlock()
	if ss.globalEnvSnapV == ss.globalEnvVer && ss.globalEnvSnap != nil {
		return ss.globalEnvSnap
	}
	snap := make([]string, len(ss.globalEnv))
	copy(snap, ss.globalEnv)
	ss.globalEnvSnap = snap
	ss.globalEnvSnapV = ss.globalEnvVer
	return snap
}

// GlobalSetEnv sets a global environment variable and notifies listeners.
func (ss *ServiceSet) GlobalSetEnv(key, value string) {
	ss.envMu.Lock()
	varStr := key + "=" + value
	override := false
	for i, e := range ss.globalEnv {
		if k, _, ok := strings.Cut(e, "="); ok && k == key {
			override = true
			ss.globalEnv[i] = varStr
			ss.globalEnvVer++
			listeners := ss.copyEnvListeners()
			ss.envMu.Unlock()
			ss.notifyEnvListenersSnapshot(listeners, varStr, override)
			return
		}
	}
	ss.globalEnv = append(ss.globalEnv, varStr)
	ss.globalEnvVer++
	listeners := ss.copyEnvListeners()
	ss.envMu.Unlock()
	ss.notifyEnvListenersSnapshot(listeners, varStr, override)
}

// GlobalUnsetEnv removes a global environment variable and notifies listeners.
func (ss *ServiceSet) GlobalUnsetEnv(key string) {
	ss.envMu.Lock()
	for i, e := range ss.globalEnv {
		if k, _, ok := strings.Cut(e, "="); ok && k == key {
			ss.globalEnv = append(ss.globalEnv[:i], ss.globalEnv[i+1:]...)
			ss.globalEnvVer++
			listeners := ss.copyEnvListeners()
			ss.envMu.Unlock()
			ss.notifyEnvListenersSnapshot(listeners, key, true)
			return
		}
	}
	ss.envMu.Unlock()
}

// AddEnvListener registers a listener for global env changes.
func (ss *ServiceSet) AddEnvListener(l EnvListener) {
	ss.envMu.Lock()
	defer ss.envMu.Unlock()
	ss.envListeners = append(ss.envListeners, l)
}

// RemoveEnvListener unregisters a listener for global env changes.
func (ss *ServiceSet) RemoveEnvListener(l EnvListener) {
	ss.envMu.Lock()
	defer ss.envMu.Unlock()
	for i, existing := range ss.envListeners {
		if existing == l {
			last := len(ss.envListeners) - 1
			ss.envListeners[i] = ss.envListeners[last]
			ss.envListeners[last] = nil
			ss.envListeners = ss.envListeners[:last]
			return
		}
	}
}

// copyEnvListeners returns a snapshot of the env listeners slice. Caller must hold envMu.
func (ss *ServiceSet) copyEnvListeners() []EnvListener {
	snapshot := make([]EnvListener, len(ss.envListeners))
	copy(snapshot, ss.envListeners)
	return snapshot
}

// notifyEnvListenersSnapshot notifies a snapshot of listeners outside the lock.
func (ss *ServiceSet) notifyEnvListenersSnapshot(listeners []EnvListener, varString string, override bool) {
	for _, l := range listeners {
		l.EnvEvent(varString, override)
	}
}

func (ss *ServiceSet) SetDefaultCgroupPath(p string)    { ss.defaultCgroupPath = p }
func (ss *ServiceSet) DefaultCgroupPath() string        { return ss.defaultCgroupPath }
func (ss *ServiceSet) SetDefaultCPUAffinity(cpus []uint) { ss.defaultCPUAffinity = cpus }
func (ss *ServiceSet) DefaultCPUAffinity() []uint       { return ss.defaultCPUAffinity }
func (ss *ServiceSet) SetReadyFD(fd int)                { ss.readyFD = fd }
func (ss *ServiceSet) ReadyFD() int                     { return ss.readyFD }

// RWReady returns true when a service with starts-rwfs has reached STARTED.
func (ss *ServiceSet) RWReady() bool { return ss.rwReady }

// LogReady returns true when a service with starts-log has reached STARTED.
func (ss *ServiceSet) LogReady() bool { return ss.logReady }

// SetRWReady marks the filesystem as read-write ready.
func (ss *ServiceSet) SetRWReady() { ss.rwReady = true }

// SetLogReady marks the logging system as ready.
func (ss *ServiceSet) SetLogReady() { ss.logReady = true }

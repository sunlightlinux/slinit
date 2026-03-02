package service

import (
	"fmt"
	"net"
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

// ServiceSet manages all loaded services and the processing queues.
type ServiceSet struct {
	records        map[string]Service
	aliases        map[string]Service // provides → service mapping
	activeServices int
	restartEnabled bool
	shutdownType   ShutdownType

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
	globalEnv []string

	// Default cgroup base path (from --cgroup-path/-b)
	defaultCgroupPath string

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
	ss.records[svc.Name()] = svc
	if alias := svc.Record().Provides(); alias != "" {
		ss.aliases[alias] = svc
	}
}

// RemoveService removes a service from the set.
func (ss *ServiceSet) RemoveService(svc Service) {
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
	result := make([]Service, 0, len(ss.records))
	for _, svc := range ss.records {
		result = append(result, svc)
	}
	return result
}

// StartService starts a service and processes queues.
func (ss *ServiceSet) StartService(svc Service) {
	svc.Start()
	ss.ProcessQueues()
}

// WakeService starts a service without marking it active (re-attaches to
// active dependents). Returns false if no active dependents were found.
func (ss *ServiceSet) WakeService(svc Service) bool {
	ok := svc.Record().Wake()
	ss.ProcessQueues()
	return ok
}

// StopService stops a service and processes queues.
func (ss *ServiceSet) StopService(svc Service) {
	svc.Stop(true)
	ss.ProcessQueues()
}

// ForceStopService force-stops a service and all its dependents.
func (ss *ServiceSet) ForceStopService(svc Service) {
	svc.Record().ForcedStop()
	ss.ProcessQueues()
}

// StopAllServices stops all services (for shutdown).
func (ss *ServiceSet) StopAllServices(shutdownType ShutdownType) {
	ss.restartEnabled = false
	ss.shutdownType = shutdownType
	for _, svc := range ss.records {
		svc.Stop(false)
		svc.Unpin()
	}
	ss.ProcessQueues()
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
// This is the core scheduling loop that replaces dinit's processQueues().
func (ss *ServiceSet) ProcessQueues() {
	for len(ss.propQueue) > 0 || len(ss.stopQueue) > 0 {
		for len(ss.propQueue) > 0 {
			svc := ss.propQueue[0]
			ss.propQueue = ss.propQueue[1:]
			svc.Record().InPropQueue = false
			svc.Record().DoPropagation()
		}
		if len(ss.stopQueue) > 0 {
			svc := ss.stopQueue[0]
			ss.stopQueue = ss.stopQueue[1:]
			svc.Record().InStopQueue = false
			svc.Record().ExecuteTransition()
		}
	}
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
			ss.consoleQueue = append(ss.consoleQueue[:i], ss.consoleQueue[i+1:]...)
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

// --- Global daemon settings ---

func (ss *ServiceSet) SetGlobalEnv(env []string)       { ss.globalEnv = env }
func (ss *ServiceSet) GlobalEnv() []string              { return ss.globalEnv }
func (ss *ServiceSet) SetDefaultCgroupPath(p string)    { ss.defaultCgroupPath = p }
func (ss *ServiceSet) DefaultCgroupPath() string        { return ss.defaultCgroupPath }
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

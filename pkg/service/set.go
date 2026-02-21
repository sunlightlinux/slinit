package service

import (
	"fmt"
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
}

// NewServiceSet creates a new ServiceSet.
func NewServiceSet(logger ServiceLogger) *ServiceSet {
	return &ServiceSet{
		records:        make(map[string]Service),
		restartEnabled: true,
		logger:         logger,
	}
}

// SetLoader sets the service loader for this set.
func (ss *ServiceSet) SetLoader(loader ServiceLoader) {
	ss.loader = loader
}

// FindService locates an existing service by name.
// If findPlaceholders is false, placeholder services are excluded.
func (ss *ServiceSet) FindService(name string, findPlaceholders bool) Service {
	svc, ok := ss.records[name]
	if !ok {
		return nil
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

// AddService adds a service to the set.
func (ss *ServiceSet) AddService(svc Service) {
	ss.records[svc.Name()] = svc
}

// RemoveService removes a service from the set.
func (ss *ServiceSet) RemoveService(svc Service) {
	delete(ss.records, svc.Name())
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

// StopService stops a service and processes queues.
func (ss *ServiceSet) StopService(svc Service) {
	svc.Stop(true)
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
}

// CountActiveServices returns the number of active services.
func (ss *ServiceSet) CountActiveServices() int {
	return ss.activeServices
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

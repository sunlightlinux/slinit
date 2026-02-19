// Package service implements the core service management types and state machine
// for the slinit init system / service manager.
package service

import (
	"fmt"
	"syscall"
)

// ServiceState represents the current state of a service.
type ServiceState uint8

const (
	StateStopped  ServiceState = iota // Service is not running
	StateStarting                     // Service is starting
	StateStarted                      // Service is running
	StateStopping                     // Service is stopping
)

func (s ServiceState) String() string {
	switch s {
	case StateStopped:
		return "STOPPED"
	case StateStarting:
		return "STARTING"
	case StateStarted:
		return "STARTED"
	case StateStopping:
		return "STOPPING"
	default:
		return fmt.Sprintf("ServiceState(%d)", s)
	}
}

// IsFinal returns true if this is a final state (STOPPED or STARTED).
func (s ServiceState) IsFinal() bool {
	return s == StateStopped || s == StateStarted
}

// ServiceType identifies the kind of service.
type ServiceType uint8

const (
	TypePlaceholder ServiceType = iota // Placeholder service, used during loading/reloading
	TypeProcess                        // Long-running monitored process
	TypeBGProcess                      // Self-backgrounding daemon process
	TypeScripted                       // Start/stop via external commands
	TypeInternal                       // No external process
	TypeTriggered                      // Externally triggered service
)

func (t ServiceType) String() string {
	switch t {
	case TypePlaceholder:
		return "placeholder"
	case TypeProcess:
		return "process"
	case TypeBGProcess:
		return "bgprocess"
	case TypeScripted:
		return "scripted"
	case TypeInternal:
		return "internal"
	case TypeTriggered:
		return "triggered"
	default:
		return fmt.Sprintf("ServiceType(%d)", t)
	}
}

// DependencyType identifies the kind of dependency relationship.
type DependencyType uint8

const (
	DepRegular   DependencyType = iota // Hard dependency
	DepSoft                            // Parallel start, failure/stop doesn't affect dependent
	DepWaitsFor                        // Like soft, but dependent waits for start/fail
	DepMilestone                       // Must start successfully, then becomes soft
	DepBefore                          // Ordering: this starts before target
	DepAfter                           // Ordering: this starts after target
)

func (d DependencyType) String() string {
	switch d {
	case DepRegular:
		return "regular"
	case DepSoft:
		return "soft"
	case DepWaitsFor:
		return "waits-for"
	case DepMilestone:
		return "milestone"
	case DepBefore:
		return "before"
	case DepAfter:
		return "after"
	default:
		return fmt.Sprintf("DependencyType(%d)", d)
	}
}

// ServiceEvent represents a service lifecycle event.
type ServiceEvent uint8

const (
	EventStarted       ServiceEvent = iota // Service reached STARTED state
	EventStopped                           // Service reached STOPPED state
	EventFailedStart                       // Service failed to start
	EventStartCancelled                    // Start was cancelled by a stop request
	EventStopCancelled                     // Stop was cancelled by a start request
)

func (e ServiceEvent) String() string {
	switch e {
	case EventStarted:
		return "STARTED"
	case EventStopped:
		return "STOPPED"
	case EventFailedStart:
		return "FAILEDSTART"
	case EventStartCancelled:
		return "STARTCANCELLED"
	case EventStopCancelled:
		return "STOPCANCELLED"
	default:
		return fmt.Sprintf("ServiceEvent(%d)", e)
	}
}

// ShutdownType represents shutdown modes.
type ShutdownType uint8

const (
	ShutdownNone       ShutdownType = iota // No explicit shutdown
	ShutdownRemain                         // Continue running with no services
	ShutdownHalt                           // Halt system without powering down
	ShutdownPoweroff                       // Power off system
	ShutdownReboot                         // Reboot system
	ShutdownSoftReboot                     // Reboot slinit only
)

func (s ShutdownType) String() string {
	switch s {
	case ShutdownNone:
		return "none"
	case ShutdownRemain:
		return "remain"
	case ShutdownHalt:
		return "halt"
	case ShutdownPoweroff:
		return "poweroff"
	case ShutdownReboot:
		return "reboot"
	case ShutdownSoftReboot:
		return "softreboot"
	default:
		return fmt.Sprintf("ShutdownType(%d)", s)
	}
}

// StoppedReason explains why a service stopped.
type StoppedReason uint8

const (
	ReasonNormal     StoppedReason = iota // Normal stop
	ReasonDepRestart                      // Hard dependency was restarted
	ReasonDepFailed                       // Dependency failed to start
	ReasonFailed                          // Failed to start (process terminated)
	ReasonExecFailed                      // Failed to start (couldn't launch process)
	ReasonTimedOut                        // Timed out when starting
	ReasonTerminated                      // Process terminated after starting
)

func (r StoppedReason) String() string {
	switch r {
	case ReasonNormal:
		return "normal"
	case ReasonDepRestart:
		return "dependency-restart"
	case ReasonDepFailed:
		return "dependency-failed"
	case ReasonFailed:
		return "failed"
	case ReasonExecFailed:
		return "exec-failed"
	case ReasonTimedOut:
		return "timed-out"
	case ReasonTerminated:
		return "terminated"
	default:
		return fmt.Sprintf("StoppedReason(%d)", r)
	}
}

// DidFinish returns true if the reason indicates the service ran and then terminated.
func (r StoppedReason) DidFinish() bool {
	return r == ReasonTerminated
}

// AutoRestartMode controls restart behavior.
type AutoRestartMode uint8

const (
	RestartNever     AutoRestartMode = iota // Never automatically restart
	RestartAlways                           // Always restart
	RestartOnFailure                        // Only restart when process fails
)

func (a AutoRestartMode) String() string {
	switch a {
	case RestartNever:
		return "never"
	case RestartAlways:
		return "always"
	case RestartOnFailure:
		return "on-failure"
	default:
		return fmt.Sprintf("AutoRestartMode(%d)", a)
	}
}

// LogType identifies the type of logging output.
type LogType uint8

const (
	LogNone   LogType = iota // Discard all output
	LogFile                  // Log to a file
	LogBuffer                // Log to a memory buffer
	LogPipe                  // Pipe to another process (service)
)

// ExitStatus holds the exit status of a child process.
type ExitStatus struct {
	WaitStatus syscall.WaitStatus
	// Whether the status has been set
	HasStatus bool
}

// Exited returns true if the process exited normally.
func (e ExitStatus) Exited() bool {
	return e.HasStatus && e.WaitStatus.Exited()
}

// ExitCode returns the exit code if the process exited normally.
func (e ExitStatus) ExitCode() int {
	if e.Exited() {
		return e.WaitStatus.ExitStatus()
	}
	return -1
}

// Signaled returns true if the process was killed by a signal.
func (e ExitStatus) Signaled() bool {
	return e.HasStatus && e.WaitStatus.Signaled()
}

// Signal returns the signal that killed the process.
func (e ExitStatus) Signal() syscall.Signal {
	return e.WaitStatus.Signal()
}

// ServiceFlags holds behavioral flags for a service.
type ServiceFlags struct {
	RWReady            bool // Filesystem is ready when this service starts
	LogReady           bool // Logging is ready when this service starts
	RunsOnConsole      bool // Service runs on the console
	StartsOnConsole    bool // Service uses console during startup
	SharesConsole      bool // Service shares the console
	PassCSFD           bool // Pass control socket fd to child
	StartInterruptible bool // Startup can be interrupted
	Skippable          bool // Service can be skipped during boot
	SignalProcessOnly  bool // Only signal the process, not the process group
	AlwaysChain        bool // Always chain to the next service
	KillAllOnStop      bool // Kill all processes in cgroup on stop
}

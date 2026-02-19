package service

import (
	"syscall"
	"time"

	"github.com/IonutNechita/slinit/pkg/process"
)

const (
	defaultStopTimeout    = 10 * time.Second
	defaultStartTimeout   = 60 * time.Second
	defaultRestartDelay   = 200 * time.Millisecond
	defaultRestartInterval = 10 * time.Second
	defaultMaxRestarts    = 3
)

// ProcessService manages a long-running process.
type ProcessService struct {
	ServiceRecord

	// Command configuration
	command     []string
	stopCommand []string
	workingDir  string
	envFile     string

	// Credentials
	runAsUID uint32
	runAsGID uint32

	// Process state
	pid        int
	exitStatus ExitStatus
	procHandle process.ProcessHandle

	// Timer for start/stop/restart timeouts
	processTimer *time.Timer
	timerPurpose timerPurpose

	// Timeout configuration
	startTimeout time.Duration
	stopTimeout  time.Duration
	restartDelay time.Duration

	// Restart rate limiting
	restartInterval      time.Duration
	maxRestartCount      int
	restartIntervalTime  time.Time
	restartIntervalCount int
	lastStartTime        time.Time

	// State tracking
	stopIssued       bool
	doingSmoothRecov bool

	// Channels for monitoring goroutine coordination
	doneCh        chan struct{} // closed when monitoring goroutine should stop
	timerUpdateCh chan struct{} // signaled when a new timer is armed
}

type timerPurpose uint8

const (
	timerNone timerPurpose = iota
	timerStartTimeout
	timerStopTimeout
	timerRestartDelay
)

// NewProcessService creates a new process service.
func NewProcessService(set *ServiceSet, name string) *ProcessService {
	svc := &ProcessService{
		stopTimeout:     defaultStopTimeout,
		startTimeout:    defaultStartTimeout,
		restartDelay:    defaultRestartDelay,
		restartInterval: defaultRestartInterval,
		maxRestartCount: defaultMaxRestarts,
	}
	svc.ServiceRecord = *NewServiceRecord(svc, set, name, TypeProcess)
	return svc
}

// SetCommand sets the startup command.
func (s *ProcessService) SetCommand(cmd []string) { s.command = cmd }

// SetStopCommand sets the stop command.
func (s *ProcessService) SetStopCommand(cmd []string) { s.stopCommand = cmd }

// SetWorkingDir sets the working directory.
func (s *ProcessService) SetWorkingDir(dir string) { s.workingDir = dir }

// SetEnvFile sets the environment file path.
func (s *ProcessService) SetEnvFile(path string) { s.envFile = path }

// SetRunAs sets the UID and GID to run the process as.
func (s *ProcessService) SetRunAs(uid, gid uint32) {
	s.runAsUID = uid
	s.runAsGID = gid
}

// SetStartTimeout sets the start timeout.
func (s *ProcessService) SetStartTimeout(d time.Duration) { s.startTimeout = d }

// SetStopTimeout sets the stop timeout.
func (s *ProcessService) SetStopTimeout(d time.Duration) { s.stopTimeout = d }

// SetRestartDelay sets the minimum delay between restarts.
func (s *ProcessService) SetRestartDelay(d time.Duration) { s.restartDelay = d }

// SetRestartLimits sets the restart rate limiting parameters.
func (s *ProcessService) SetRestartLimits(interval time.Duration, maxCount int) {
	s.restartInterval = interval
	s.maxRestartCount = maxCount
}

// PID returns the process ID of the running service.
func (s *ProcessService) PID() int { return s.pid }

// GetExitStatus returns the exit status of the last process.
func (s *ProcessService) GetExitStatus() ExitStatus { return s.exitStatus }

// BringUp starts the service process.
func (s *ProcessService) BringUp() bool {
	if len(s.command) == 0 {
		s.services.logger.Error("Service '%s': no command specified", s.serviceName)
		return false
	}

	if err := s.startProcess(); err != nil {
		s.services.logger.Error("Service '%s': failed to start: %v", s.serviceName, err)
		return false
	}

	// Arm start timeout if configured
	if s.startTimeout > 0 {
		s.armTimer(s.startTimeout, timerStartTimeout)
	}

	return true
}

// BringDown stops the service process.
func (s *ProcessService) BringDown() {
	if s.pid <= 0 {
		// Process already dead
		s.cancelTimer()
		s.Stopped()
		return
	}

	if s.stopIssued {
		return
	}

	// Send termination signal
	sig := s.termSignal
	if sig == 0 {
		sig = syscall.SIGTERM
	}

	s.services.logger.Info("Service '%s': sending %v to process %d",
		s.serviceName, sig, s.pid)

	err := process.SignalProcess(s.pid, sig, s.Flags.SignalProcessOnly)
	if err != nil {
		s.services.logger.Error("Service '%s': failed to signal process: %v",
			s.serviceName, err)
	}

	s.stopIssued = true

	// Arm stop timeout for SIGKILL escalation
	if s.stopTimeout > 0 {
		s.armTimer(s.stopTimeout, timerStopTimeout)
	}
}

// CanInterruptStart returns true if the starting process can be interrupted.
func (s *ProcessService) CanInterruptStart() bool {
	if s.waitingForDeps {
		return true
	}
	// Can interrupt if process is running (we'll send SIGINT)
	return s.pid > 0
}

// InterruptStart cancels the start by sending SIGINT to the process.
func (s *ProcessService) InterruptStart() bool {
	if s.waitingForDeps {
		return true
	}

	if s.pid > 0 {
		s.services.logger.Info("Service '%s': interrupting start (SIGINT to %d)",
			s.serviceName, s.pid)
		process.SignalProcess(s.pid, syscall.SIGINT, s.Flags.SignalProcessOnly)
		// Can't immediately transition; need to wait for process to die
		return false
	}

	return true
}

// CheckRestart checks if the service should auto-restart (rate limiting).
func (s *ProcessService) CheckRestart() bool {
	if s.maxRestartCount <= 0 {
		return true
	}

	now := time.Now()
	elapsed := now.Sub(s.restartIntervalTime)

	if elapsed < s.restartInterval {
		// Still in the limiting interval
		if s.restartIntervalCount >= s.maxRestartCount {
			s.services.logger.Error("Service '%s': restarting too quickly, stopping",
				s.serviceName)
			return false
		}
		s.restartIntervalCount++
	} else {
		// New interval
		s.restartIntervalTime = now
		s.restartIntervalCount = 1
	}

	return true
}

// startProcess forks and execs the service process.
func (s *ProcessService) startProcess() error {
	s.lastStartTime = time.Now()
	s.stopIssued = false
	s.exitStatus = ExitStatus{}

	params := process.ExecParams{
		Command:           s.command,
		WorkingDir:        s.workingDir,
		TermSignal:        s.termSignal,
		OnConsole:         s.Flags.RunsOnConsole || s.Flags.StartsOnConsole,
		SignalProcessOnly: s.Flags.SignalProcessOnly,
		RunAsUID:          s.runAsUID,
		RunAsGID:          s.runAsGID,
	}

	pid, exitCh, err := process.StartProcess(params)
	if err != nil {
		return err
	}

	s.pid = pid
	s.procHandle = process.ProcessHandle{PID: pid, ExitCh: exitCh}

	// Start monitoring goroutine
	s.doneCh = make(chan struct{})
	s.timerUpdateCh = make(chan struct{}, 1)
	go s.monitorProcess(exitCh)

	// Process started successfully - mark as started immediately
	// (In Phase 3, readiness notification will delay this)
	s.cancelTimer()
	s.Started()

	return nil
}

// monitorProcess runs in a goroutine, waiting for the process to exit
// or for timers to fire.
func (s *ProcessService) monitorProcess(exitCh <-chan process.ChildExit) {
	for {
		select {
		case exit, ok := <-exitCh:
			if !ok {
				return
			}
			s.handleChildExit(exit)
			return

		case <-s.getTimerChan():
			s.handleTimerExpired()

		case <-s.timerUpdateCh:
			// Timer was armed from outside; re-enter select to pick up new timer channel
			continue

		case <-s.doneCh:
			return
		}
	}
}

// handleChildExit processes a child process termination.
func (s *ProcessService) handleChildExit(exit process.ChildExit) {
	s.exitStatus = ExitStatus{
		WaitStatus: exit.Status,
		HasStatus:  true,
	}
	s.pid = 0
	s.procHandle.Clear()
	s.cancelTimer()

	if exit.ExecErr != nil {
		// Process failed during exec/setup
		s.services.logger.Error("Service '%s': exec failed: %v",
			s.serviceName, exit.ExecErr)
		s.stopReason = ReasonExecFailed
		s.state = StateStopping
		s.failedToStart(false, true)
		s.services.ProcessQueues()
		return
	}

	state := s.state

	switch state {
	case StateStarting:
		// Process died while we thought it was starting
		s.services.logger.Error("Service '%s': process exited during startup (status: %v)",
			s.serviceName, exit.Status)
		s.stopReason = ReasonFailed
		s.failedToStart(false, true)
		s.services.ProcessQueues()

	case StateStopping:
		// Expected - we asked it to stop
		s.stopIssued = false
		s.Stopped()
		s.services.ProcessQueues()

	case StateStarted:
		// Unexpected termination
		if exit.Exited() {
			s.services.logger.Error("Service '%s': process exited with code %d",
				s.serviceName, exit.Status.ExitStatus())
		} else if exit.Signaled() {
			s.services.logger.Error("Service '%s': process killed by signal %v",
				s.serviceName, exit.Status.Signal())
		}

		if s.smoothRecovery && s.CheckRestart() {
			// Smooth recovery: restart without notifying dependents
			s.doingSmoothRecov = true
			s.doSmoothRecovery()
		} else {
			// Handle unexpected termination through normal path
			s.handleUnexpectedTermination()
		}
	}
}

// handleUnexpectedTermination handles when a started process dies unexpectedly.
func (s *ProcessService) handleUnexpectedTermination() {
	s.stopReason = ReasonTerminated
	s.forceStop = true

	s.doStop(false)
	s.services.ProcessQueues()

	// If after processing queues we're still stopping and desired is STARTED,
	// the restart will be handled by the state machine
	if s.state == StateStopping && s.desired == StateStarted && !s.IsStartPinned() {
		s.initiateStart()
		s.services.ProcessQueues()
	}
}

// doSmoothRecovery restarts the process without affecting dependents.
func (s *ProcessService) doSmoothRecovery() {
	s.services.logger.Info("Service '%s': smooth recovery - restarting process",
		s.serviceName)

	now := time.Now()
	elapsed := now.Sub(s.lastStartTime)

	if elapsed >= s.restartDelay {
		// Can restart immediately
		if err := s.startProcess(); err != nil {
			s.services.logger.Error("Service '%s': smooth recovery failed: %v",
				s.serviceName, err)
			s.doingSmoothRecov = false
			s.handleUnexpectedTermination()
		} else {
			s.doingSmoothRecov = false
		}
	} else {
		// Need to delay restart
		delay := s.restartDelay - elapsed
		s.armTimer(delay, timerRestartDelay)
	}
}

// handleTimerExpired processes a timer expiration.
func (s *ProcessService) handleTimerExpired() {
	purpose := s.timerPurpose
	s.timerPurpose = timerNone

	switch purpose {
	case timerStartTimeout:
		// Start timeout expired
		if s.pid > 0 {
			s.services.logger.Error("Service '%s': start timeout exceeded, sending SIGINT",
				s.serviceName)
			process.SignalProcess(s.pid, syscall.SIGINT, s.Flags.SignalProcessOnly)
			s.stopReason = ReasonTimedOut
			s.failedToStart(false, false) // Don't immediately stop, wait for process
		}

	case timerStopTimeout:
		// Stop timeout expired - escalate to SIGKILL
		if s.pid > 0 {
			s.services.logger.Error("Service '%s': stop timeout exceeded, sending SIGKILL",
				s.serviceName)
			process.SignalProcess(s.pid, syscall.SIGKILL, false) // Always kill group
		}

	case timerRestartDelay:
		// Restart delay expired
		if s.doingSmoothRecov {
			if err := s.startProcess(); err != nil {
				s.services.logger.Error("Service '%s': restart failed: %v",
					s.serviceName, err)
				s.doingSmoothRecov = false
				s.handleUnexpectedTermination()
			} else {
				s.doingSmoothRecov = false
			}
		}
	}
}

// Timer helpers
func (s *ProcessService) armTimer(d time.Duration, purpose timerPurpose) {
	s.cancelTimer()
	s.processTimer = time.NewTimer(d)
	s.timerPurpose = purpose

	// Notify monitoring goroutine that a new timer is armed
	if s.timerUpdateCh != nil {
		select {
		case s.timerUpdateCh <- struct{}{}:
		default:
		}
	}
}

func (s *ProcessService) cancelTimer() {
	if s.processTimer != nil {
		s.processTimer.Stop()
		s.processTimer = nil
	}
	s.timerPurpose = timerNone
}

func (s *ProcessService) getTimerChan() <-chan time.Time {
	if s.processTimer != nil {
		return s.processTimer.C
	}
	return nil
}

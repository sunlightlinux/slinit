package service

import (
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// ScriptedService is a service controlled by external start/stop commands.
// The service is considered started when the start command exits with code 0,
// and stopped when the stop command exits.
type ScriptedService struct {
	ServiceRecord

	// Commands
	startCommand []string
	stopCommand  []string
	workingDir   string

	// Credentials
	runAsUID uint32
	runAsGID uint32

	// Process tracking
	startPID    int
	stopPID     int
	startHandle process.ProcessHandle
	stopHandle  process.ProcessHandle

	// Timeouts
	startTimeout time.Duration
	stopTimeout  time.Duration

	// Timer
	processTimer *time.Timer
	timerPurpose scriptedTimerPurpose

	// Monitoring
	doneCh        chan struct{}
	timerUpdateCh chan struct{} // signaled when a new timer is armed
}

type scriptedTimerPurpose uint8

const (
	scriptedTimerNone scriptedTimerPurpose = iota
	scriptedTimerStartTimeout
	scriptedTimerStopTimeout
)

// NewScriptedService creates a new scripted service.
func NewScriptedService(set *ServiceSet, name string) *ScriptedService {
	svc := &ScriptedService{
		startTimeout: defaultStartTimeout,
		stopTimeout:  defaultStopTimeout,
	}
	svc.ServiceRecord = *NewServiceRecord(svc, set, name, TypeScripted)
	return svc
}

// SetStartCommand sets the start command.
func (s *ScriptedService) SetStartCommand(cmd []string) { s.startCommand = cmd }

// SetStopCommand sets the stop command.
func (s *ScriptedService) SetStopCommand(cmd []string) { s.stopCommand = cmd }

// SetWorkingDir sets the working directory.
func (s *ScriptedService) SetWorkingDir(dir string) { s.workingDir = dir }

// SetRunAs sets the UID and GID to run commands as.
func (s *ScriptedService) SetRunAs(uid, gid uint32) {
	s.runAsUID = uid
	s.runAsGID = gid
}

// SetStartTimeout sets the start command timeout.
func (s *ScriptedService) SetStartTimeout(d time.Duration) { s.startTimeout = d }

// SetStopTimeout sets the stop command timeout.
func (s *ScriptedService) SetStopTimeout(d time.Duration) { s.stopTimeout = d }

// PID returns the PID of the currently running command (start or stop).
func (s *ScriptedService) PID() int {
	if s.startPID > 0 {
		return s.startPID
	}
	return s.stopPID
}

// BringUp runs the start command.
func (s *ScriptedService) BringUp() bool {
	if len(s.startCommand) == 0 {
		// No start command = started immediately (like internal)
		s.Started()
		return true
	}

	params := process.ExecParams{
		Command:    s.startCommand,
		WorkingDir: s.workingDir,
		RunAsUID:   s.runAsUID,
		RunAsGID:   s.runAsGID,
	}

	pid, exitCh, err := process.StartProcess(params)
	if err != nil {
		s.services.logger.Error("Service '%s': failed to run start command: %v",
			s.serviceName, err)
		return false
	}

	s.startPID = pid
	s.startHandle = process.ProcessHandle{PID: pid, ExitCh: exitCh}

	// Monitor the start command
	s.doneCh = make(chan struct{})
	s.timerUpdateCh = make(chan struct{}, 1)
	go s.monitorStart(exitCh)

	// Arm start timeout
	if s.startTimeout > 0 {
		s.armTimer(s.startTimeout, scriptedTimerStartTimeout)
	}

	return true
}

// BringDown runs the stop command.
func (s *ScriptedService) BringDown() {
	if len(s.stopCommand) == 0 {
		// No stop command = stopped immediately
		s.Stopped()
		return
	}

	params := process.ExecParams{
		Command:    s.stopCommand,
		WorkingDir: s.workingDir,
		RunAsUID:   s.runAsUID,
		RunAsGID:   s.runAsGID,
	}

	pid, exitCh, err := process.StartProcess(params)
	if err != nil {
		s.services.logger.Error("Service '%s': failed to run stop command: %v",
			s.serviceName, err)
		// Stop anyway
		s.Stopped()
		return
	}

	s.stopPID = pid
	s.stopHandle = process.ProcessHandle{PID: pid, ExitCh: exitCh}

	// Monitor the stop command
	s.doneCh = make(chan struct{})
	s.timerUpdateCh = make(chan struct{}, 1)
	go s.monitorStop(exitCh)

	// Arm stop timeout
	if s.stopTimeout > 0 {
		s.armTimer(s.stopTimeout, scriptedTimerStopTimeout)
	}
}

// CanInterruptStart returns true if the start command can be interrupted.
func (s *ScriptedService) CanInterruptStart() bool {
	if s.waitingForDeps {
		return true
	}
	return s.Flags.StartInterruptible
}

// InterruptStart sends SIGINT to the start command.
func (s *ScriptedService) InterruptStart() bool {
	if s.waitingForDeps {
		return true
	}

	if s.startPID > 0 && s.Flags.StartInterruptible {
		process.SignalProcess(s.startPID, 2, false) // SIGINT
		return false // Wait for it to die
	}

	return s.startPID <= 0
}

func (s *ScriptedService) monitorStart(exitCh <-chan process.ChildExit) {
	for {
		select {
		case exit, ok := <-exitCh:
			if !ok {
				return
			}
			s.handleStartExit(exit)
			return

		case <-s.getTimerChan():
			s.handleTimerExpired()

		case <-s.timerUpdateCh:
			// Timer was armed; re-enter select to pick up new timer channel
			continue

		case <-s.doneCh:
			return
		}
	}
}

func (s *ScriptedService) monitorStop(exitCh <-chan process.ChildExit) {
	for {
		select {
		case exit, ok := <-exitCh:
			if !ok {
				return
			}
			s.handleStopExit(exit)
			return

		case <-s.getTimerChan():
			s.handleTimerExpired()

		case <-s.timerUpdateCh:
			// Timer was armed; re-enter select to pick up new timer channel
			continue

		case <-s.doneCh:
			return
		}
	}
}

func (s *ScriptedService) handleStartExit(exit process.ChildExit) {
	s.startPID = 0
	s.startHandle.Clear()
	s.cancelTimer()

	if exit.ExecErr != nil {
		s.services.logger.Error("Service '%s': start command exec failed: %v",
			s.serviceName, exit.ExecErr)
		s.stopReason = ReasonExecFailed
		s.failedToStart(false, true)
		s.services.ProcessQueues()
		return
	}

	if exit.ExitedClean() {
		// Start command succeeded
		s.Started()
		s.services.ProcessQueues()
	} else {
		// Start command failed
		exitCode := -1
		if exit.Exited() {
			exitCode = exit.Status.ExitStatus()
		}
		s.services.logger.Error("Service '%s': start command failed (exit code: %d)",
			s.serviceName, exitCode)
		s.stopReason = ReasonFailed
		s.failedToStart(false, true)
		s.services.ProcessQueues()
	}
}

func (s *ScriptedService) handleStopExit(exit process.ChildExit) {
	s.stopPID = 0
	s.stopHandle.Clear()
	s.cancelTimer()

	if !exit.ExitedClean() {
		s.services.logger.Error("Service '%s': stop command failed (status: %v)",
			s.serviceName, exit.Status)
	}

	// Whether stop command succeeded or not, the service is stopped
	s.Stopped()
	s.services.ProcessQueues()
}

func (s *ScriptedService) handleTimerExpired() {
	purpose := s.timerPurpose
	s.timerPurpose = scriptedTimerNone

	switch purpose {
	case scriptedTimerStartTimeout:
		if s.startPID > 0 {
			s.services.logger.Error("Service '%s': start command timeout, sending SIGKILL",
				s.serviceName)
			process.SignalProcess(s.startPID, 9, false) // SIGKILL
		}

	case scriptedTimerStopTimeout:
		if s.stopPID > 0 {
			s.services.logger.Error("Service '%s': stop command timeout, sending SIGKILL",
				s.serviceName)
			process.SignalProcess(s.stopPID, 9, false) // SIGKILL
		}
	}
}

// Timer helpers
func (s *ScriptedService) armTimer(d time.Duration, purpose scriptedTimerPurpose) {
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

func (s *ScriptedService) cancelTimer() {
	if s.processTimer != nil {
		s.processTimer.Stop()
		s.processTimer = nil
	}
	s.timerPurpose = scriptedTimerNone
}

func (s *ScriptedService) getTimerChan() <-chan time.Time {
	if s.processTimer != nil {
		return s.processTimer.C
	}
	return nil
}

package service

import (
	"os"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
)

const (
	// daemonPollInterval is how often we check if the daemon process is alive.
	daemonPollInterval = 1 * time.Second
)

// BGProcessService manages a self-backgrounding daemon process.
// The lifecycle is: launch command → launcher forks and exits → read PID file
// to discover the daemon PID → monitor daemon via polling.
type BGProcessService struct {
	ServiceRecord

	// Command configuration
	command     []string
	stopCommand []string
	workingDir  string
	envFile     string

	// PID file path (required)
	pidFile string

	// Credentials
	runAsUID uint32
	runAsGID uint32

	// Process state
	launcherPID int
	daemonPID   int
	exitStatus  ExitStatus
	procHandle  process.ProcessHandle

	// Timer for start/stop/restart timeouts
	processTimer *time.Timer
	timerPurpose bgTimerPurpose

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

	// Log buffering
	logType   LogType
	logBufMax int
	logBuf    *LogBuffer

	// Channels for monitoring goroutine coordination
	doneCh        chan struct{}
	timerUpdateCh chan struct{}
}

type bgTimerPurpose uint8

const (
	bgTimerNone bgTimerPurpose = iota
	bgTimerStartTimeout
	bgTimerStopTimeout
	bgTimerRestartDelay
)

// NewBGProcessService creates a new background process service.
func NewBGProcessService(set *ServiceSet, name string) *BGProcessService {
	svc := &BGProcessService{
		stopTimeout:     defaultStopTimeout,
		startTimeout:    defaultStartTimeout,
		restartDelay:    defaultRestartDelay,
		restartInterval: defaultRestartInterval,
		maxRestartCount: defaultMaxRestarts,
	}
	svc.ServiceRecord = *NewServiceRecord(svc, set, name, TypeBGProcess)
	return svc
}

// Setters

func (s *BGProcessService) SetCommand(cmd []string)        { s.command = cmd }
func (s *BGProcessService) SetStopCommand(cmd []string)     { s.stopCommand = cmd }
func (s *BGProcessService) SetWorkingDir(dir string)        { s.workingDir = dir }
func (s *BGProcessService) SetEnvFile(path string)          { s.envFile = path }
func (s *BGProcessService) SetPIDFile(path string)          { s.pidFile = path }
func (s *BGProcessService) SetRunAs(uid, gid uint32)        { s.runAsUID = uid; s.runAsGID = gid }
func (s *BGProcessService) SetStartTimeout(d time.Duration) { s.startTimeout = d }
func (s *BGProcessService) SetStopTimeout(d time.Duration)  { s.stopTimeout = d }
func (s *BGProcessService) SetRestartDelay(d time.Duration) { s.restartDelay = d }

// SetLogType sets the log output type.
func (s *BGProcessService) SetLogType(lt LogType) { s.logType = lt }

// SetLogBufMax sets the maximum log buffer size.
func (s *BGProcessService) SetLogBufMax(n int) { s.logBufMax = n }

// GetLogBuffer returns the log buffer (overrides ServiceRecord default).
func (s *BGProcessService) GetLogBuffer() *LogBuffer { return s.logBuf }

// GetLogType returns the log type (overrides ServiceRecord default).
func (s *BGProcessService) GetLogType() LogType { return s.logType }

func (s *BGProcessService) SetRestartLimits(interval time.Duration, maxCount int) {
	s.restartInterval = interval
	s.maxRestartCount = maxCount
}

// PID returns the daemon PID if known, otherwise the launcher PID.
func (s *BGProcessService) PID() int {
	if s.daemonPID > 0 {
		return s.daemonPID
	}
	return s.launcherPID
}

// GetExitStatus returns the exit status of the last process.
func (s *BGProcessService) GetExitStatus() ExitStatus { return s.exitStatus }

// BringUp launches the background process command.
// Unlike ProcessService, does NOT call Started() immediately.
// Waits for the launcher to exit and then reads the PID file.
func (s *BGProcessService) BringUp() bool {
	if len(s.command) == 0 {
		s.services.logger.Error("Service '%s': no command specified", s.serviceName)
		return false
	}

	if s.pidFile == "" {
		s.services.logger.Error("Service '%s': no pid-file specified for bgprocess", s.serviceName)
		return false
	}

	s.lastStartTime = time.Now()
	s.stopIssued = false
	s.exitStatus = ExitStatus{}
	s.daemonPID = 0

	// Set up log buffer pipe if configured
	var outputPipe *os.File
	if s.logType == LogToBuffer {
		if s.logBuf == nil {
			s.logBuf = NewLogBuffer(s.logBufMax)
		} else {
			s.logBuf.AppendRestartMarker()
		}
		var pipeErr error
		outputPipe, pipeErr = s.logBuf.CreatePipe()
		if pipeErr != nil {
			s.services.logger.Error("Service '%s': failed to create log pipe: %v",
				s.serviceName, pipeErr)
			outputPipe = nil
		}
	}

	params := process.ExecParams{
		Command:           s.command,
		WorkingDir:        s.workingDir,
		TermSignal:        s.termSignal,
		SignalProcessOnly: s.Flags.SignalProcessOnly,
		RunAsUID:          s.runAsUID,
		RunAsGID:          s.runAsGID,
		OutputPipe:        outputPipe,
	}

	pid, exitCh, err := process.StartProcess(params)
	if err != nil {
		if outputPipe != nil {
			s.logBuf.CloseWriteEnd()
		}
		s.services.logger.Error("Service '%s': failed to start launcher: %v", s.serviceName, err)
		return false
	}

	if outputPipe != nil {
		s.logBuf.CloseWriteEnd()
		s.logBuf.StartReader()
	}

	s.launcherPID = pid
	s.procHandle = process.ProcessHandle{PID: pid, ExitCh: exitCh}

	// Start monitoring goroutine for the launcher process
	s.doneCh = make(chan struct{})
	s.timerUpdateCh = make(chan struct{}, 1)
	go s.monitorLauncher(exitCh)

	// Arm start timeout if configured
	if s.startTimeout > 0 {
		s.armTimer(s.startTimeout, bgTimerStartTimeout)
	}

	return true
}

// BringDown stops the daemon process.
func (s *BGProcessService) BringDown() {
	pid := s.daemonPID
	if pid <= 0 {
		pid = s.launcherPID
	}
	if pid <= 0 {
		s.cancelTimer()
		s.Stopped()
		return
	}

	if s.stopIssued {
		return
	}

	sig := s.termSignal
	if sig == 0 {
		sig = syscall.SIGTERM
	}

	s.services.logger.Info("Service '%s': sending %v to process %d",
		s.serviceName, sig, pid)

	// For bgprocess, signal only the daemon PID (not process group)
	err := process.SignalProcess(pid, sig, true)
	if err != nil {
		s.services.logger.Error("Service '%s': failed to signal process: %v",
			s.serviceName, err)
	}

	s.stopIssued = true

	if s.stopTimeout > 0 {
		s.armTimer(s.stopTimeout, bgTimerStopTimeout)
	}
}

// CanInterruptStart returns true if the starting process can be interrupted.
func (s *BGProcessService) CanInterruptStart() bool {
	if s.waitingForDeps {
		return true
	}
	return s.launcherPID > 0
}

// InterruptStart cancels the start by sending SIGINT to the launcher.
func (s *BGProcessService) InterruptStart() bool {
	if s.waitingForDeps {
		return true
	}
	if s.launcherPID > 0 {
		process.SignalProcess(s.launcherPID, syscall.SIGINT, false)
		return false
	}
	return true
}

// CheckRestart checks if the service should auto-restart (rate limiting).
func (s *BGProcessService) CheckRestart() bool {
	if s.maxRestartCount <= 0 {
		return true
	}

	now := time.Now()
	elapsed := now.Sub(s.restartIntervalTime)

	if elapsed < s.restartInterval {
		if s.restartIntervalCount >= s.maxRestartCount {
			s.services.logger.Error("Service '%s': restarting too quickly, stopping",
				s.serviceName)
			return false
		}
		s.restartIntervalCount++
	} else {
		s.restartIntervalTime = now
		s.restartIntervalCount = 1
	}

	return true
}

// monitorLauncher waits for the launcher process to exit, then reads
// the PID file and starts monitoring the daemon.
func (s *BGProcessService) monitorLauncher(exitCh <-chan process.ChildExit) {
	for {
		select {
		case exit, ok := <-exitCh:
			if !ok {
				return
			}
			s.handleLauncherExit(exit)
			return

		case <-s.getTimerChan():
			s.handleTimerExpired()

		case <-s.timerUpdateCh:
			continue

		case <-s.doneCh:
			return
		}
	}
}

// handleLauncherExit processes the launcher process termination.
func (s *BGProcessService) handleLauncherExit(exit process.ChildExit) {
	s.launcherPID = 0
	s.procHandle.Clear()

	if exit.ExecErr != nil {
		s.services.logger.Error("Service '%s': launcher exec failed: %v",
			s.serviceName, exit.ExecErr)
		s.cancelTimer()
		s.stopReason = ReasonExecFailed
		s.state = StateStopping
		s.failedToStart(false, true)
		s.services.ProcessQueues()
		return
	}

	if !exit.ExitedClean() {
		exitCode := -1
		if exit.Exited() {
			exitCode = exit.Status.ExitStatus()
		}
		s.services.logger.Error("Service '%s': launcher exited with code %d",
			s.serviceName, exitCode)
		s.cancelTimer()
		s.stopReason = ReasonFailed
		s.failedToStart(false, true)
		s.services.ProcessQueues()
		return
	}

	// Launcher exited cleanly - read PID file to find the daemon
	pid, result, err := process.ReadPIDFile(s.pidFile)
	if result == process.PIDResultFailed {
		s.services.logger.Error("Service '%s': failed to read PID file '%s': %v",
			s.serviceName, s.pidFile, err)
		s.cancelTimer()
		s.stopReason = ReasonFailed
		s.failedToStart(false, true)
		s.services.ProcessQueues()
		return
	}

	if result == process.PIDResultTerminated {
		s.services.logger.Error("Service '%s': daemon (PID %d) already terminated",
			s.serviceName, pid)
		s.cancelTimer()
		s.stopReason = ReasonFailed
		s.failedToStart(false, true)
		s.services.ProcessQueues()
		return
	}

	// PIDResultOK - daemon is running
	s.daemonPID = pid
	s.cancelTimer()
	s.Started()
	s.services.ProcessQueues()

	// Start monitoring the daemon process
	go s.monitorDaemon()
}

// monitorDaemon polls for daemon process existence.
func (s *BGProcessService) monitorDaemon() {
	ticker := time.NewTicker(daemonPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			err := syscall.Kill(s.daemonPID, 0)
			if err != nil {
				// Process is gone
				s.handleDaemonTermination()
				return
			}

		case <-s.getTimerChan():
			s.handleTimerExpired()

		case <-s.timerUpdateCh:
			continue

		case <-s.doneCh:
			return
		}
	}
}

// handleDaemonTermination handles when the daemon process disappears.
func (s *BGProcessService) handleDaemonTermination() {
	s.services.logger.Error("Service '%s': daemon process %d terminated",
		s.serviceName, s.daemonPID)

	s.daemonPID = 0
	s.cancelTimer()

	state := s.state

	switch state {
	case StateStopping:
		s.stopIssued = false
		s.Stopped()
		s.services.ProcessQueues()

	case StateStarted:
		if s.smoothRecovery && s.CheckRestart() {
			s.doingSmoothRecov = true
			s.doSmoothRecovery()
		} else {
			s.handleUnexpectedTermination()
		}
	}
}

// handleUnexpectedTermination handles when a started daemon dies unexpectedly.
func (s *BGProcessService) handleUnexpectedTermination() {
	s.stopReason = ReasonTerminated
	s.forceStop = true

	s.doStop(false)
	s.services.ProcessQueues()

	if s.state == StateStopping && s.desired == StateStarted && !s.IsStartPinned() {
		s.initiateStart()
		s.services.ProcessQueues()
	}
}

// doSmoothRecovery restarts the bgprocess without affecting dependents.
func (s *BGProcessService) doSmoothRecovery() {
	s.services.logger.Info("Service '%s': smooth recovery - restarting bgprocess",
		s.serviceName)

	now := time.Now()
	elapsed := now.Sub(s.lastStartTime)

	if elapsed >= s.restartDelay {
		if !s.self.BringUp() {
			s.doingSmoothRecov = false
			s.handleUnexpectedTermination()
		} else {
			s.doingSmoothRecov = false
		}
	} else {
		delay := s.restartDelay - elapsed
		s.armTimer(delay, bgTimerRestartDelay)
	}
}

// handleTimerExpired processes a timer expiration.
func (s *BGProcessService) handleTimerExpired() {
	purpose := s.timerPurpose
	s.timerPurpose = bgTimerNone

	switch purpose {
	case bgTimerStartTimeout:
		pid := s.launcherPID
		if pid <= 0 {
			pid = s.daemonPID
		}
		if pid > 0 {
			s.services.logger.Error("Service '%s': start timeout exceeded, sending SIGINT",
				s.serviceName)
			process.SignalProcess(pid, syscall.SIGINT, false)
			s.stopReason = ReasonTimedOut
			s.failedToStart(false, false)
		}

	case bgTimerStopTimeout:
		pid := s.daemonPID
		if pid > 0 {
			s.services.logger.Error("Service '%s': stop timeout exceeded, sending SIGKILL",
				s.serviceName)
			process.SignalProcess(pid, syscall.SIGKILL, false)
		}

	case bgTimerRestartDelay:
		if s.doingSmoothRecov {
			if !s.self.BringUp() {
				s.doingSmoothRecov = false
				s.handleUnexpectedTermination()
			} else {
				s.doingSmoothRecov = false
			}
		}
	}
}

// Timer helpers

func (s *BGProcessService) armTimer(d time.Duration, purpose bgTimerPurpose) {
	s.cancelTimer()
	s.processTimer = time.NewTimer(d)
	s.timerPurpose = purpose

	if s.timerUpdateCh != nil {
		select {
		case s.timerUpdateCh <- struct{}{}:
		default:
		}
	}
}

func (s *BGProcessService) cancelTimer() {
	if s.processTimer != nil {
		s.processTimer.Stop()
		s.processTimer = nil
	}
	s.timerPurpose = bgTimerNone
}

func (s *BGProcessService) getTimerChan() <-chan time.Time {
	if s.processTimer != nil {
		return s.processTimer.C
	}
	return nil
}

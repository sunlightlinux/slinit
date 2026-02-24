package service

import (
	"fmt"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
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

	// Socket activation
	socketFD *os.File // pre-opened listening socket (nil if no socket-listen)

	// Readiness notification
	readyNotifyFD  int      // fd number child writes to (-1 if none)
	readyNotifyVar string   // env var name ("" if none)
	readyPipeRead  *os.File // read-end of notification pipe (parent watches)
	readyCh        chan bool // receives true=ready, false=EOF/error

	// Log buffering
	logType   LogType
	logBufMax int
	logBuf    *LogBuffer

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
		readyNotifyFD:   -1,
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

// SetLogType sets the log output type.
func (s *ProcessService) SetLogType(lt LogType) { s.logType = lt }

// SetLogBufMax sets the maximum log buffer size.
func (s *ProcessService) SetLogBufMax(n int) { s.logBufMax = n }

// GetLogBuffer returns the log buffer (overrides ServiceRecord default).
func (s *ProcessService) GetLogBuffer() *LogBuffer { return s.logBuf }

// GetLogType returns the log type (overrides ServiceRecord default).
func (s *ProcessService) GetLogType() LogType { return s.logType }

// SetTestLogBuffer sets the log buffer directly (for testing only).
func (s *ProcessService) SetTestLogBuffer(lb *LogBuffer) { s.logBuf = lb }

// SetReadyNotification configures readiness notification.
// fd is the file descriptor number for pipefd: mode (-1 if unused).
// varName is the environment variable name for pipevar: mode ("" if unused).
func (s *ProcessService) SetReadyNotification(fd int, varName string) {
	s.readyNotifyFD = fd
	s.readyNotifyVar = varName
}

// HasReadyNotification returns true if readiness notification is configured.
func (s *ProcessService) HasReadyNotification() bool {
	return s.readyNotifyFD >= 0 || s.readyNotifyVar != ""
}

// openSocket creates and binds a Unix listening socket for socket activation.
func (s *ProcessService) openSocket() error {
	if s.socketPath == "" || s.socketFD != nil {
		return nil
	}

	// Check if file exists at socket path
	info, err := os.Stat(s.socketPath)
	if err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("activation socket file exists and is not a socket: %s", s.socketPath)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("error checking activation socket: %v", err)
	}

	// Remove stale socket file
	os.Remove(s.socketPath)

	// Create Unix listener
	addr := &net.UnixAddr{Name: s.socketPath, Net: "unix"}
	unixListener, err := net.ListenUnix("unix", addr)
	if err != nil {
		return fmt.Errorf("error creating activation socket: %v", err)
	}

	// Don't remove socket file when closing the listener
	unixListener.SetUnlinkOnClose(false)

	// Extract raw fd (File() returns a dup'd fd)
	fd, err := unixListener.File()
	if err != nil {
		unixListener.Close()
		return fmt.Errorf("error getting activation socket fd: %v", err)
	}
	unixListener.Close() // close the net.Listener; we keep the dup'd fd

	// Set permissions
	if s.socketPerms != 0 {
		if err := os.Chmod(s.socketPath, os.FileMode(s.socketPerms)); err != nil {
			fd.Close()
			return fmt.Errorf("error setting activation socket permissions: %v", err)
		}
	}

	// Set ownership
	if s.socketUID >= 0 || s.socketGID >= 0 {
		uid := s.socketUID
		gid := s.socketGID
		if uid < 0 {
			uid = -1 // -1 means don't change
		}
		if gid < 0 {
			gid = -1
		}
		if err := os.Chown(s.socketPath, uid, gid); err != nil {
			fd.Close()
			return fmt.Errorf("error setting activation socket owner: %v", err)
		}
	}

	s.socketFD = fd
	return nil
}

// closeSocket closes the activation socket and removes the socket file.
func (s *ProcessService) closeSocket() {
	if s.socketFD != nil {
		s.socketFD.Close()
		s.socketFD = nil
	}
	if s.socketPath != "" {
		os.Remove(s.socketPath)
	}
}

// BecomingInactive is called when the service won't restart. Cleans up socket.
func (s *ProcessService) BecomingInactive() {
	s.closeSocket()
}

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

	// Open activation socket before starting the process
	if err := s.openSocket(); err != nil {
		s.services.logger.Error("Service '%s': %v", s.serviceName, err)
		return false
	}

	if err := s.startProcess(); err != nil {
		s.services.logger.Error("Service '%s': failed to start: %v", s.serviceName, err)
		return false
	}

	// Note: startProcess() calls Started() immediately (no readiness protocol),
	// so no start timeout is needed here. If a readiness protocol is added later,
	// the start timeout should be armed BEFORE startProcess() and cancelled
	// inside startProcess() when readiness is confirmed.

	return true
}

// BringDown stops the service process.
func (s *ProcessService) BringDown() {
	// Close readiness pipe if still open (no longer waiting for readiness)
	s.closeReadyPipe()

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

	// Set up readiness notification pipe if configured
	var notifyPipeWrite *os.File
	if s.HasReadyNotification() {
		pr, pw, err := os.Pipe()
		if err != nil {
			if outputPipe != nil {
				s.logBuf.CloseWriteEnd()
			}
			return err
		}
		s.readyPipeRead = pr
		notifyPipeWrite = pw
	}

	params := process.ExecParams{
		Command:           s.command,
		WorkingDir:        s.workingDir,
		TermSignal:        s.termSignal,
		OnConsole:         s.Flags.RunsOnConsole || s.Flags.StartsOnConsole,
		SignalProcessOnly: s.Flags.SignalProcessOnly,
		RunAsUID:          s.runAsUID,
		RunAsGID:          s.runAsGID,
		OutputPipe:        outputPipe,
		SocketFD:          s.socketFD,
		NotifyPipe:        notifyPipeWrite,
		ForceNotifyFD:     s.readyNotifyFD,
		NotifyVar:         s.readyNotifyVar,
	}

	pid, exitCh, err := process.StartProcess(params)
	if err != nil {
		if outputPipe != nil {
			s.logBuf.CloseWriteEnd()
		}
		if notifyPipeWrite != nil {
			notifyPipeWrite.Close()
			s.readyPipeRead.Close()
			s.readyPipeRead = nil
		}
		return err
	}

	// Close parent's write end and start reader goroutine
	if outputPipe != nil {
		s.logBuf.CloseWriteEnd()
		s.logBuf.StartReader()
	}

	// Close parent's write end of notification pipe
	if notifyPipeWrite != nil {
		notifyPipeWrite.Close()
	}

	s.pid = pid
	s.procHandle = process.ProcessHandle{PID: pid, ExitCh: exitCh}

	// Start monitoring goroutine
	s.doneCh = make(chan struct{})
	s.timerUpdateCh = make(chan struct{}, 1)

	// If readiness notification is configured, start a reader goroutine
	// on the notification pipe and defer Started() until data arrives.
	if s.readyPipeRead != nil {
		s.readyCh = make(chan bool, 1)
		go s.watchReadyPipe()
		go s.monitorProcess(exitCh)

		// Arm start timeout while waiting for readiness
		if s.startTimeout > 0 {
			s.armTimer(s.startTimeout, timerStartTimeout)
		}
	} else {
		go s.monitorProcess(exitCh)

		// No readiness protocol - mark as started immediately
		s.cancelTimer()
		s.Started()
	}

	return nil
}

// watchReadyPipe monitors the read-end of the readiness notification pipe.
// Sends true on readyCh if data is received, false if EOF/error.
func (s *ProcessService) watchReadyPipe() {
	buf := make([]byte, 128)
	n, _ := s.readyPipeRead.Read(buf)
	if n > 0 {
		s.readyCh <- true
	} else {
		s.readyCh <- false
	}
}

// monitorProcess runs in a goroutine, waiting for the process to exit,
// readiness notification, or for timers to fire.
func (s *ProcessService) monitorProcess(exitCh <-chan process.ChildExit) {
	for {
		select {
		case exit, ok := <-exitCh:
			if !ok {
				return
			}
			s.handleChildExit(exit)
			return

		case ready, ok := <-s.getReadyChan():
			if !ok {
				continue
			}
			s.handleReadyNotification(ready)

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

// getReadyChan returns the readiness notification channel, or nil if not active.
func (s *ProcessService) getReadyChan() <-chan bool {
	return s.readyCh
}

// handleReadyNotification processes readiness notification from the pipe.
func (s *ProcessService) handleReadyNotification(ready bool) {
	// Close the read-end pipe
	s.closeReadyPipe()

	// Nil the channel so we don't select on it again
	s.readyCh = nil

	if s.state != StateStarting {
		// Not in STARTING state; ignore readiness signal
		return
	}

	if ready {
		// Child signaled readiness
		s.cancelTimer()
		s.services.logger.Info("Service '%s': readiness notification received", s.serviceName)
		s.Started()
		s.services.ProcessQueues()
	} else {
		// EOF without data - child closed pipe without writing
		s.services.logger.Error("Service '%s': readiness pipe closed without notification", s.serviceName)
		s.cancelTimer()
		s.stopReason = ReasonFailed
		s.failedToStart(false, false)
		s.services.ProcessQueues()
	}
}

// closeReadyPipe closes the read-end of the notification pipe if open.
func (s *ProcessService) closeReadyPipe() {
	if s.readyPipeRead != nil {
		s.readyPipeRead.Close()
		s.readyPipeRead = nil
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
	s.closeReadyPipe()

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
	s.closeReadyPipe()
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

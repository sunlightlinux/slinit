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
	stopPID    int // PID of stop-command process (0 if none)
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

	// Log output
	logType      LogType
	logBufMax    int
	logBuf       *LogBuffer
	logFile      string
	logFilePerms int
	logFileUID   int
	logFileGID   int

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

// SetLogFileDetails sets the logfile path, permissions, and ownership.
func (s *ProcessService) SetLogFileDetails(path string, perms, uid, gid int) {
	s.logFile = path
	s.logFilePerms = perms
	s.logFileUID = uid
	s.logFileGID = gid
}

// GetLogFile returns the logfile path.
func (s *ProcessService) GetLogFile() string { return s.logFile }

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
	s.closeDoneCh()
	s.closeSocket()
	s.CloseOutputPipe()
}

// closeDoneCh signals the monitoring goroutine to stop and resets the channel.
func (s *ProcessService) closeDoneCh() {
	if s.doneCh != nil {
		close(s.doneCh)
		s.doneCh = nil
	}
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
// If a stop-command is configured, it is executed first. If it fails to
// start, we fall back to sending the termination signal directly.
func (s *ProcessService) BringDown() {
	// Close readiness pipe if still open (no longer waiting for readiness)
	s.closeReadyPipe()

	if s.pid <= 0 {
		// Process already dead
		s.cancelTimer()
		s.Stopped()
		return
	}

	if s.stopPID > 0 || s.stopIssued {
		return
	}

	// Try stop-command first (like dinit's process_service::bring_down)
	if len(s.stopCommand) > 0 {
		if s.execStopCommand() {
			s.stopIssued = true
			if s.stopTimeout > 0 {
				s.armTimer(s.stopTimeout, timerStopTimeout)
			}
			return
		}
		// stop-command failed to start; fall through to signal
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

// execStopCommand starts the stop-command process. Returns true if it was
// launched successfully. The stop-command runs independently; when it exits,
// the monitoring goroutine receives the event via stopExitCh and then signals
// the main process if it is still alive.
func (s *ProcessService) execStopCommand() bool {
	params := process.ExecParams{
		Command:    s.stopCommand,
		WorkingDir: s.workingDir,
		Env:        s.buildEnv(),
	}
	s.Record().ApplyProcessAttrs(&params)

	pid, exitCh, err := process.StartProcess(params)
	if err != nil {
		s.services.logger.Error("Service '%s': failed to start stop-command: %v",
			s.serviceName, err)
		return false
	}

	s.stopPID = pid
	s.services.logger.Info("Service '%s': stop-command started (pid %d)", s.serviceName, pid)

	// Monitor stop-command in a goroutine
	go func() {
		exit := <-exitCh
		s.stopPID = 0
		process.KillProcessGroup(exit.PID)

		if exit.Exited() && exit.Status.ExitStatus() == 0 {
			s.services.logger.Info("Service '%s': stop-command completed successfully",
				s.serviceName)
		} else {
			s.services.logger.Error("Service '%s': stop-command exited with status %v, sending signal",
				s.serviceName, exit.Status)
			// Stop-command failed — send signal to main process as fallback
			if s.pid > 0 {
				sig := s.termSignal
				if sig == 0 {
					sig = syscall.SIGTERM
				}
				process.SignalProcess(s.pid, sig, s.Flags.SignalProcessOnly)
			}
		}
	}()

	return true
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

// buildEnv merges env-file variables and runtime extraEnv into a slice for ExecParams.
func (s *ProcessService) buildEnv() []string {
	var env []string
	// Global daemon-level env (--env-file/-e) first, can be overridden
	env = append(env, s.services.GlobalEnv()...)
	if s.envFile != "" {
		if fileEnv, err := process.ReadEnvFile(s.envFile); err == nil {
			for k, v := range fileEnv {
				env = append(env, k+"="+v)
			}
		} else {
			s.services.logger.Error("Service '%s': failed to read env-file '%s': %v",
				s.serviceName, s.envFile, err)
		}
	}
	env = append(env, s.Record().BuildEnvSlice()...)
	return env
}

// startProcess forks and execs the service process.
func (s *ProcessService) startProcess() error {
	s.lastStartTime = time.Now()
	s.stopIssued = false
	s.exitStatus = ExitStatus{}

	// Set up output pipe based on log type
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
	} else if s.logType == LogToPipe {
		if err := s.EnsureOutputPipe(); err != nil {
			return err
		}
		outputPipe = s.outputPipeW
	} else if s.logType == LogToFile && s.logFile != "" {
		f, err := os.OpenFile(s.logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, os.FileMode(s.logFilePerms))
		if err != nil {
			return fmt.Errorf("failed to open logfile '%s': %w", s.logFile, err)
		}
		if s.logFileUID >= 0 || s.logFileGID >= 0 {
			_ = os.Chown(s.logFile, s.logFileUID, s.logFileGID)
		}
		outputPipe = f
	}

	// Set up input pipe (consumer-of)
	var inputPipe *os.File
	if s.consumerFor != nil {
		if err := s.consumerFor.Record().EnsureOutputPipe(); err != nil {
			s.services.logger.Error("Service '%s': failed to get producer pipe: %v",
				s.serviceName, err)
		} else {
			inputPipe = s.consumerFor.Record().OutputPipeR()
		}
	}

	// Set up readiness notification pipe if configured
	var notifyPipeWrite *os.File
	if s.HasReadyNotification() {
		pr, pw, err := os.Pipe()
		if err != nil {
			if outputPipe != nil && s.logType == LogToBuffer {
				s.logBuf.CloseWriteEnd()
			} else if outputPipe != nil && s.logType == LogToFile {
				outputPipe.Close()
			}
			return err
		}
		s.readyPipeRead = pr
		notifyPipeWrite = pw
	}

	// Set up control socket pair if pass-cs-fd is enabled
	var csClientFD *os.File
	var csServerConn net.Conn
	if s.Flags.PassCSFD {
		fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
		if err != nil {
			s.services.logger.Error("Service '%s': socketpair failed: %v", s.serviceName, err)
		} else {
			csClientFD = os.NewFile(uintptr(fds[1]), "cs-client")
			serverFile := os.NewFile(uintptr(fds[0]), "cs-server")
			csServerConn, err = net.FileConn(serverFile)
			serverFile.Close() // FileConn dups the fd
			if err != nil {
				s.services.logger.Error("Service '%s': FileConn failed: %v", s.serviceName, err)
				csClientFD.Close()
				csClientFD = nil
				csServerConn = nil
			}
		}
	}

	params := process.ExecParams{
		Command:           s.command,
		WorkingDir:        s.workingDir,
		Env:               s.buildEnv(),
		TermSignal:        s.termSignal,
		OnConsole:         s.Flags.RunsOnConsole || s.Flags.StartsOnConsole,
		UnmaskSigint:      s.Flags.UnmaskIntr,
		SignalProcessOnly: s.Flags.SignalProcessOnly,
		RunAsUID:          s.runAsUID,
		RunAsGID:          s.runAsGID,
		OutputPipe:        outputPipe,
		InputPipe:         inputPipe,
		SocketFD:          s.socketFD,
		ControlSocketFD:   csClientFD,
		NotifyPipe:        notifyPipeWrite,
		ForceNotifyFD:     s.readyNotifyFD,
		NotifyVar:         s.readyNotifyVar,
	}
	s.Record().ApplyProcessAttrs(&params)

	pid, exitCh, err := process.StartProcess(params)
	if err != nil {
		if outputPipe != nil && s.logType == LogToBuffer {
			s.logBuf.CloseWriteEnd()
		} else if outputPipe != nil && s.logType == LogToFile {
			outputPipe.Close()
		}
		if notifyPipeWrite != nil {
			notifyPipeWrite.Close()
			s.readyPipeRead.Close()
			s.readyPipeRead = nil
		}
		if csClientFD != nil {
			csClientFD.Close()
		}
		if csServerConn != nil {
			csServerConn.Close()
		}
		return err
	}

	// Close parent's copy of output fd after fork.
	// For LogToPipe, the parent keeps both pipe ends open across restarts.
	if outputPipe != nil && s.logType == LogToBuffer {
		s.logBuf.CloseWriteEnd()
		s.logBuf.StartReader()
	} else if outputPipe != nil && s.logType == LogToFile {
		outputPipe.Close()
	}

	// Close parent's write end of notification pipe
	if notifyPipeWrite != nil {
		notifyPipeWrite.Close()
	}

	// Close parent's copy of the client-end control socket (child has it)
	if csClientFD != nil {
		csClientFD.Close()
	}
	// Spawn a control connection handler on the server-end socket
	if csServerConn != nil && s.services.OnPassCSFD != nil {
		s.services.OnPassCSFD(csServerConn)
	} else if csServerConn != nil {
		csServerConn.Close()
	}

	s.pid = pid
	s.procHandle = process.ProcessHandle{PID: pid, ExitCh: exitCh}

	// Create utmp entry if inittab-id or inittab-line is configured
	if s.HasUtmp() && s.services.OnUtmpCreate != nil {
		s.services.OnUtmpCreate(s.inittabID, s.inittabLine, pid)
	}

	// Start monitoring goroutine
	s.closeDoneCh()
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
	if exit.ExecErr != nil {
		s.exitStatus.ExecFailed = true
		s.exitStatus.ExecStage = uint8(exit.ExecErr.Stage)
		s.exitStatus.ExecErrno = extractErrno(exit.ExecErr.Err)
	}

	// Kill any remaining processes in the child's process group
	// (e.g., orphaned sleep, background scripts spawned by the shell).
	// The lead process is already reaped; wait4(-pgid) only targets
	// group members, so this is safe for other managed services.
	process.KillProcessGroup(exit.PID)

	// Clear utmp entry
	if s.HasUtmp() && s.services.OnUtmpClear != nil {
		s.services.OnUtmpClear(s.inittabID, s.inittabLine)
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
		// Also kill stop-command if still running
		if s.stopPID > 0 {
			s.services.logger.Error("Service '%s': killing stop-command (pid %d)",
				s.serviceName, s.stopPID)
			process.SignalProcess(s.stopPID, syscall.SIGKILL, false)
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
		if !s.processTimer.Stop() {
			// Drain the channel to prevent stale timer events
			select {
			case <-s.processTimer.C:
			default:
			}
		}
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

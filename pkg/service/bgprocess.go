package service

import (
	"bytes"
	"os"
	"strconv"
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
	stopPID     int // PID of stop-command process (0 if none)
	exitStatus  ExitStatus
	procHandle  process.ProcessHandle

	// Timer for start/stop/restart timeouts
	processTimer *time.Timer
	timerPurpose bgTimerPurpose

	// Timeout configuration
	startTimeout time.Duration
	stopTimeout  time.Duration
	restartDelay time.Duration

	// Progressive restart backoff (OpenRC-compatible, linear additive)
	restartDelayStep    time.Duration
	restartDelayCap     time.Duration
	currentRestartDelay time.Duration

	// Restart rate limiting
	restartInterval      time.Duration
	maxRestartCount      int
	restartIntervalTime  time.Time
	restartIntervalCount int
	lastStartTime        time.Time

	// State tracking
	stopIssued       bool
	doingSmoothRecov bool

	// Log output
	logType      LogType
	logBufMax    int
	logBuf       *LogBuffer
	logFile      string
	logFilePerms int
	logFileUID   int
	logFileGID   int

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

// killCgroupTree sends a signal to all processes in the service's cgroup.
func (s *BGProcessService) killCgroupTree(sig syscall.Signal) {
	cgPath := s.EffectiveCgroupPath()
	if cgPath == "" {
		return
	}
	if err := process.KillCgroup(cgPath, sig); err != nil {
		s.services.logger.Error("Service '%s': cgroup kill (%v): %v",
			s.serviceName, sig, err)
	}
}

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
func (s *BGProcessService) GetPIDFile() string               { return s.pidFile }
func (s *BGProcessService) SetRunAs(uid, gid uint32)        { s.runAsUID = uid; s.runAsGID = gid }
func (s *BGProcessService) SetStartTimeout(d time.Duration) { s.startTimeout = d }
func (s *BGProcessService) SetStopTimeout(d time.Duration)  { s.stopTimeout = d }
func (s *BGProcessService) SetRestartDelay(d time.Duration) { s.restartDelay = d }

// SetRestartBackoff configures progressive (linear additive) restart backoff.
func (s *BGProcessService) SetRestartBackoff(step, cap time.Duration) {
	s.restartDelayStep = step
	s.restartDelayCap = cap
}

// nextRestartDelay returns the delay to use for the next restart and advances
// the progressive backoff counter. When step <= 0, always returns restartDelay.
func (s *BGProcessService) nextRestartDelay() time.Duration {
	if s.restartDelayStep <= 0 {
		return s.restartDelay
	}
	if s.currentRestartDelay < s.restartDelay {
		s.currentRestartDelay = s.restartDelay
	}
	delay := s.currentRestartDelay
	next := delay + s.restartDelayStep
	capDelay := s.restartDelayCap
	if capDelay <= 0 {
		capDelay = 60 * time.Second
	}
	if next > capDelay {
		next = capDelay
	}
	s.currentRestartDelay = next
	return delay
}

// BecomingInactive is called when the service won't restart. Cleans up pipe.
func (s *BGProcessService) BecomingInactive() {
	s.closeDoneCh()
	s.CloseOutputPipe()
}

// closeDoneCh signals the monitoring goroutine to stop and resets the channel.
func (s *BGProcessService) closeDoneCh() {
	if s.doneCh != nil {
		close(s.doneCh)
		s.doneCh = nil
	}
}

// SetLogType sets the log output type.
func (s *BGProcessService) SetLogType(lt LogType) { s.logType = lt }

// SetLogBufMax sets the maximum log buffer size.
func (s *BGProcessService) SetLogBufMax(n int) { s.logBufMax = n }

// SetLogFileDetails sets the logfile path, permissions, and ownership.
func (s *BGProcessService) SetLogFileDetails(path string, perms, uid, gid int) {
	s.logFile = path
	s.logFilePerms = perms
	s.logFileUID = uid
	s.logFileGID = gid
}

// GetLogFile returns the logfile path.
func (s *BGProcessService) GetLogFile() string { return s.logFile }

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

// buildEnv merges env-file variables and runtime extraEnv into a pre-allocated slice.
func (s *BGProcessService) buildEnv() []string {
	return s.Record().BuildEnvWithFile(s.envFile)
}

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

	// Fail-fast pre-start check: required_files / required_dirs must exist
	// before fork/exec. See ProcessService.BringUp.
	if err := s.CheckRequiredPaths(); err != nil {
		s.services.logger.Error("Service '%s': %v", s.serviceName, err)
		return false
	}

	s.lastStartTime = time.Now()
	s.stopIssued = false
	s.exitStatus = ExitStatus{}
	s.daemonPID = 0

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
	} else if s.logType == LogToPipe && s.sharedLoggerName != "" {
		mux := s.services.GetSharedLogMux(s.sharedLoggerName)
		if mux != nil {
			pipeW, err := mux.AddProducer(s.serviceName)
			if err != nil {
				s.services.logger.Error("Service '%s': failed to add to shared-logger '%s': %v",
					s.serviceName, s.sharedLoggerName, err)
			} else {
				outputPipe = pipeW
			}
		}
	} else if s.logType == LogToPipe {
		if err := s.EnsureOutputPipe(); err != nil {
			s.services.logger.Error("Service '%s': failed to create output pipe: %v",
				s.serviceName, err)
			return false
		}
		outputPipe = s.outputPipeW
	} else if s.logType == LogToFile && s.logFile != "" {
		f, err := os.OpenFile(s.logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, os.FileMode(s.logFilePerms))
		if err != nil {
			s.services.logger.Error("Service '%s': failed to open logfile '%s': %v",
				s.serviceName, s.logFile, err)
			return false
		}
		if s.logFileUID >= 0 || s.logFileGID >= 0 {
			_ = os.Chown(s.logFile, s.logFileUID, s.logFileGID)
		}
		outputPipe = f
	}

	// Set up input pipe (consumer-of or shared-logger mux)
	var inputPipe *os.File
	if s.consumerFor != nil {
		if err := s.consumerFor.Record().EnsureOutputPipe(); err != nil {
			s.services.logger.Error("Service '%s': failed to get producer pipe: %v",
				s.serviceName, err)
		} else {
			inputPipe = s.consumerFor.Record().OutputPipeR()
		}
	} else if mux := s.services.GetSharedLogMux(s.serviceName); mux != nil {
		inputPipe = mux.InputPipe()
	}

	params := process.ExecParams{
		Command:           s.command,
		WorkingDir:        s.workingDir,
		Env:               s.buildEnv(),
		TermSignal:        s.termSignal,
		SignalProcessOnly: s.Flags.SignalProcessOnly,
		RunAsUID:          s.runAsUID,
		RunAsGID:          s.runAsGID,
		OutputPipe:        outputPipe,
		InputPipe:         inputPipe,
	}
	s.Record().ApplyProcessAttrs(&params)

	pid, exitCh, err := process.StartProcess(params)
	if err != nil {
		if outputPipe != nil && s.logType == LogToBuffer {
			s.logBuf.CloseWriteEnd()
		} else if outputPipe != nil && s.logType == LogToFile {
			outputPipe.Close()
		}
		s.services.logger.Error("Service '%s': failed to start launcher: %v", s.serviceName, err)
		return false
	}

	if outputPipe != nil && s.logType == LogToBuffer {
		s.logBuf.CloseWriteEnd()
		s.logBuf.StartReader()
	} else if outputPipe != nil && s.logType == LogToFile {
		outputPipe.Close()
	}

	s.launcherPID = pid
	s.procHandle = process.ProcessHandle{PID: pid, ExitCh: exitCh}

	// Start monitoring goroutine for the launcher process
	s.closeDoneCh()
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
// If a stop-command is configured, it is executed first.
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

	if s.stopPID > 0 || s.stopIssued {
		return
	}

	// Try stop-command first
	if len(s.stopCommand) > 0 {
		if s.execStopCommand() {
			s.stopIssued = true
			if s.stopTimeout > 0 {
				s.armTimer(s.stopTimeout, bgTimerStopTimeout)
			}
			return
		}
		// stop-command failed to start; fall through to signal
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

	// Kill entire cgroup process tree if configured
	if s.Flags.KillAllOnStop {
		s.killCgroupTree(sig)
	}

	if s.stopTimeout > 0 {
		s.armTimer(s.stopTimeout, bgTimerStopTimeout)
	}
}

// execStopCommand starts the stop-command process for BGProcessService.
func (s *BGProcessService) execStopCommand() bool {
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

	go func() {
		exit := <-exitCh
		s.stopPID = 0
		process.KillProcessGroup(exit.PID)

		if exit.Exited() && exit.Status.ExitStatus() == 0 {
			s.services.logger.Info("Service '%s': stop-command completed successfully",
				s.serviceName)
			// Stop-command succeeded — now send term signal to daemon process
			daemonPID := s.daemonPID
			if daemonPID <= 0 {
				daemonPID = s.launcherPID
			}
			if daemonPID > 0 {
				sig := s.termSignal
				if sig == 0 {
					sig = syscall.SIGTERM
				}
				process.SignalProcess(daemonPID, sig, true)
			}
		} else {
			s.services.logger.Error("Service '%s': stop-command exited with status %v, sending signal",
				s.serviceName, exit.Status)
			daemonPID := s.daemonPID
			if daemonPID <= 0 {
				daemonPID = s.launcherPID
			}
			if daemonPID > 0 {
				sig := s.termSignal
				if sig == 0 {
					sig = syscall.SIGTERM
				}
				process.SignalProcess(daemonPID, sig, true)
			}
		}
	}()

	return true
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
	now := time.Now()

	if s.maxRestartCount > 0 {
		elapsed := now.Sub(s.restartIntervalTime)

		if elapsed < s.restartInterval {
			if s.restartIntervalCount >= s.maxRestartCount {
				s.services.logger.Error("Service '%s': restarting too quickly, stopping",
					s.serviceName)
				return false
			}
			s.restartIntervalCount++
		} else {
			// Stable period: reset progressive backoff
			s.restartIntervalTime = now
			s.restartIntervalCount = 1
			s.currentRestartDelay = s.restartDelay
		}
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
	// Kill remaining process group members from the launcher
	process.KillProcessGroup(exit.PID)

	// Kill entire cgroup tree to clean up orphaned processes
	if s.Flags.KillAllOnStop {
		s.killCgroupTree(syscall.SIGKILL)
	}

	s.launcherPID = 0
	s.procHandle.Clear()

	// Record exit status
	s.exitStatus = ExitStatus{
		WaitStatus: exit.Status,
		HasStatus:  true,
	}
	if exit.ExecErr != nil {
		s.exitStatus.ExecFailed = true
		s.exitStatus.ExecStage = uint8(exit.ExecErr.Stage)
		s.exitStatus.ExecErrno = extractErrno(exit.ExecErr.Err)
	}

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

	// Create utmp entry for the daemon process
	if s.HasUtmp() && s.services.OnUtmpCreate != nil {
		s.services.OnUtmpCreate(s.inittabID, s.inittabLine, pid)
	}

	s.cancelTimer()
	s.Started()
	s.services.ProcessQueues()

	// Start monitoring the daemon process
	go s.monitorDaemon()
}

// monitorDaemon polls for daemon process existence.
// Uses /proc/PID/stat start time to detect PID recycling.
func (s *BGProcessService) monitorDaemon() {
	if s.daemonPID <= 0 {
		s.services.logger.Error("Service '%s': monitorDaemon called with invalid PID %d",
			s.serviceName, s.daemonPID)
		s.handleDaemonTermination()
		return
	}

	// Record the process start time to detect PID recycling.
	origStartTime := readProcStartTime(s.daemonPID)

	ticker := time.NewTicker(daemonPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if s.daemonPID <= 0 {
				s.handleDaemonTermination()
				return
			}
			err := syscall.Kill(s.daemonPID, 0)
			if err != nil {
				// Process is gone
				s.handleDaemonTermination()
				return
			}
			// Guard against PID recycling: if the start time changed,
			// a different process now occupies this PID.
			if origStartTime != "" {
				curStartTime := readProcStartTime(s.daemonPID)
				if curStartTime != "" && curStartTime != origStartTime {
					s.services.logger.Error("Service '%s': PID %d was recycled (start time changed), treating as terminated",
						s.serviceName, s.daemonPID)
					s.handleDaemonTermination()
					return
				}
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

	// Clear utmp entry
	if s.HasUtmp() && s.services.OnUtmpClear != nil {
		s.services.OnUtmpClear(s.inittabID, s.inittabLine)
	}

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
	effectiveDelay := s.nextRestartDelay()
	if s.restartDelayStep > 0 && effectiveDelay > s.restartDelay {
		s.services.logger.Info("Service '%s': smooth recovery - restarting bgprocess (backoff %v)",
			s.serviceName, effectiveDelay)
	} else {
		s.services.logger.Info("Service '%s': smooth recovery - restarting bgprocess",
			s.serviceName)
	}

	now := time.Now()
	elapsed := now.Sub(s.lastStartTime)

	if elapsed >= effectiveDelay {
		if !s.self.BringUp() {
			s.doingSmoothRecov = false
			s.handleUnexpectedTermination()
		} else {
			s.doingSmoothRecov = false
		}
	} else {
		delay := effectiveDelay - elapsed
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
		// Kill entire cgroup tree on SIGKILL escalation
		if s.Flags.KillAllOnStop {
			s.killCgroupTree(syscall.SIGKILL)
		}
		if s.stopPID > 0 {
			s.services.logger.Error("Service '%s': killing stop-command (pid %d)",
				s.serviceName, s.stopPID)
			process.SignalProcess(s.stopPID, syscall.SIGKILL, false)
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
		if !s.processTimer.Stop() {
			// Drain the channel to prevent stale timer events
			select {
			case <-s.processTimer.C:
			default:
			}
		}
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

// readProcStartTime reads field 22 (starttime) from /proc/PID/stat.
// This value is the process start time in clock ticks since boot and is
// unique enough (combined with PID) to detect PID recycling.
// Returns "" on any error.
func readProcStartTime(pid int) string {
	// Build path without fmt.Sprintf; use stack buffer for /proc/PID/stat read
	path := "/proc/" + strconv.Itoa(pid) + "/stat"
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	var buf [512]byte // /proc/PID/stat is typically <400 bytes
	n, _ := f.Read(buf[:])
	f.Close()
	if n <= 0 {
		return ""
	}
	data := buf[:n]
	// /proc/PID/stat format: pid (comm) state ... field22 ...
	// comm can contain spaces and parentheses, so find the last ')'.
	idx := bytes.LastIndexByte(data, ')')
	if idx < 0 || idx+2 >= len(data) {
		return ""
	}
	// Skip past ") " and count fields to index 19 (starttime)
	rest := data[idx+2:]
	fieldIdx := 0
	i := 0
	for i < len(rest) && fieldIdx < 20 {
		// Skip whitespace
		for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t') {
			i++
		}
		if i >= len(rest) {
			break
		}
		start := i
		// Skip field content
		for i < len(rest) && rest[i] != ' ' && rest[i] != '\t' && rest[i] != '\n' {
			i++
		}
		if fieldIdx == 19 {
			return string(rest[start:i])
		}
		fieldIdx++
	}
	return ""
}

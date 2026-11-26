package service

import (
	"os"
	"sync/atomic"
	"syscall"
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
	runAsUID          uint32
	runAsGID          uint32
	supplementaryGIDs []uint32

	// Process tracking
	startPID    int
	stopPID     int
	startHandle process.ProcessHandle
	stopHandle  process.ProcessHandle

	// pidPublished mirrors the PID that PID() reports, for readers that
	// cannot take queueMu (the rusage query); see ProcessService. Written
	// by publishPID at the four assignment sites of startPID/stopPID.
	pidPublished atomic.Int64

	// Timeouts
	startTimeout time.Duration
	stopTimeout  time.Duration

	// Timer
	processTimer *time.Timer
	timerPurpose scriptedTimerPurpose

	// Log output
	logType      LogType
	logBufMax    int
	logBuf       *LogBuffer
	logFile      string
	logFilePerms int
	logFileUID   int
	logFileGID   int

	// Last wait status of whichever script ran — the start command, or
	// the stop command once that has run. Cleared at the top of every
	// start, which is the invariant the restart policy relies on (see
	// ExitStatus.Vanished): a status left over from the previous cycle
	// would be taken for this one's.
	//
	// This type had none, so GetExitStatus() fell through to the record's
	// default, which returns an empty struct. dinit's scripted_service
	// derives from base_process_service and has a real one, and the
	// absence showed: `slinitctl status` reported no exit code and
	// si_code 0 for a scripted service whose command had just failed with
	// one, and `normal-exit` could never match.
	exitStatus ExitStatus

	// startTimedOut records that this start attempt's start-timeout
	// fired. Per attempt, unlike stopReason, which outlives the attempt
	// and must not decide how the next one ends. Cleared in BringUp.
	startTimedOut bool

	// interruptingStart records that the start command was told to give
	// up — by a stop or by the start-timeout — so that its exit, in
	// STOPPING, ends the cancelled start rather than a stop command (as
	// dinit's scripted_service::interrupting_start).
	interruptingStart bool

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

// GetExitStatus returns the last status recorded by recordExit.
func (s *ScriptedService) GetExitStatus() ExitStatus { return s.exitStatus }

// recordExit stores a script's wait status. Caller holds queueMu.
//
// Both scripts land here, so the status describes whichever ran last —
// the same single-slot behaviour dinit's base_process_service has, where
// a stopped scripted service reports its stop script's status.
func (s *ScriptedService) recordExit(exit process.ChildExit) {
	s.exitStatus = ExitStatus{
		WaitStatus: exit.Status,
		HasStatus:  true,
	}
	if exit.ExecErr != nil {
		s.exitStatus.ExecFailed = true
		s.exitStatus.ExecStage = uint8(exit.ExecErr.Stage)
		s.exitStatus.ExecErrno = extractErrno(exit.ExecErr.Err)
	}
}

// killCgroupTree sends a signal to all processes in the service's cgroup.
func (s *ScriptedService) killCgroupTree(sig syscall.Signal) {
	cgPath := s.OwnCgroupPath()
	if cgPath == "" {
		return
	}
	if err := process.KillCgroup(cgPath, sig); err != nil {
		s.services.logger.Error("Service '%s': cgroup kill (%v): %v",
			s.serviceName, sig, err)
	}
}

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

// SetSupplementaryGroups sets the child's supplementary group set.
func (s *ScriptedService) SetSupplementaryGroups(gids []uint32) {
	s.supplementaryGIDs = gids
}

// effectiveRunAsUID returns the dynamic-user UID when allocated,
// otherwise the configured runAsUID.
func (s *ScriptedService) effectiveRunAsUID() uint32 {
	if uid := s.Record().DynamicUID(); uid != 0 {
		return uid
	}
	return s.runAsUID
}

// effectiveRunAsGID mirrors effectiveRunAsUID for the GID.
func (s *ScriptedService) effectiveRunAsGID() uint32 {
	if uid := s.Record().DynamicUID(); uid != 0 {
		return uid
	}
	return s.runAsGID
}

// SetLogType sets the log output type.
func (s *ScriptedService) SetLogType(lt LogType) { s.logType = lt }

// SetLogBufMax sets the maximum log buffer size.
func (s *ScriptedService) SetLogBufMax(n int) { s.logBufMax = n }

// SetLogFileDetails sets the logfile path, permissions, and ownership.
func (s *ScriptedService) SetLogFileDetails(path string, perms, uid, gid int) {
	s.logFile = path
	s.logFilePerms = perms
	s.logFileUID = uid
	s.logFileGID = gid
}

// GetLogFile returns the logfile path.
func (s *ScriptedService) GetLogFile() string { return s.logFile }

// GetLogBuffer returns the log buffer (overrides ServiceRecord default).
func (s *ScriptedService) GetLogBuffer() *LogBuffer { return s.logBuf }

// GetLogType returns the log type (overrides ServiceRecord default).
func (s *ScriptedService) GetLogType() LogType { return s.logType }

// BecomingInactive is called when the service won't restart. Cleans up pipe.
func (s *ScriptedService) BecomingInactive() {
	// The log-type=pipe pipe stays open (see ProcessService.BecomingInactive).
	s.closeDoneCh()
}

// closeDoneCh signals the monitoring goroutine to stop and resets the channel.
func (s *ScriptedService) closeDoneCh() {
	if s.doneCh != nil {
		close(s.doneCh)
		s.doneCh = nil
	}
}

// SetStartTimeout sets the start command timeout.
func (s *ScriptedService) SetStartTimeout(d time.Duration) { s.startTimeout = d }

// SetStopTimeout sets the stop command timeout.
func (s *ScriptedService) SetStopTimeout(d time.Duration) { s.stopTimeout = d }

// PID returns the PID of the currently running command (start or stop).
func (s *ScriptedService) PID() int {
	// See ProcessService.PID() — same reentrancy rationale, and the same
	// atomic mirror rather than a bare read of startPID/stopPID.
	return int(s.pidPublished.Load())
}

// publishPID keeps the mirror in step with whichever pid PID() should
// report: the start command's, else the stop command's.
func (s *ScriptedService) publishPID() {
	if s.startPID > 0 {
		s.pidPublished.Store(int64(s.startPID))
		return
	}
	s.pidPublished.Store(int64(s.stopPID))
}

// runningStopCommand reports whether this service's current PID belongs
// to a stop-command rather than to its own workload — the condition
// under which PID() reports the stop-command (see PID above).
//
// Read without queueMu for the same reason PID() gives: the callers run
// inside locks that make taking it unsafe, and a stale answer is
// harmless here. Reading true a moment after the command exited costs a
// signal to a pid that killStopCommand will then decline to send.
func (s *ScriptedService) runningStopCommand() bool {
	return s.startPID <= 0 && s.stopPID > 0
}

// killStopCommand SIGKILLs a stop-command that is still running when its
// grace expires. Called from the immediate-kill shutdown path, a grace
// period after the rest of the services were killed.
//
// grace is passed in rather than read from the package variable so the
// calling goroutine touches no shared state once it is running.
//
// The pid is re-read under queueMu — the lock its writer holds — so a
// command that finished during the grace is left alone rather than
// signalled at a number that may now belong to something else.
func (s *ScriptedService) killStopCommand(grace time.Duration) {
	s.services.queueMu.Lock()
	pid := s.stopPID
	s.services.queueMu.Unlock()
	if pid <= 0 {
		return
	}
	s.services.logger.Error("Service '%s': stop command still running after %v, sending SIGKILL",
		s.serviceName, grace)
	process.SignalProcess(pid, 9, false) // SIGKILL
}

// BringUp runs the start command.
func (s *ScriptedService) BringUp() bool {
	if len(s.startCommand) == 0 {
		// No start command = started immediately (like internal)
		s.Started()
		return true
	}

	s.exitStatus = ExitStatus{}
	s.startTimedOut = false
	s.interruptingStart = false

	if err := s.Record().resolveDeferredRunAs(); err != nil {
		s.services.logger.Error("Service '%s': %v", s.serviceName, err)
		return false
	}

	// Dynamic-user allocation (#13). Mirrors ProcessService.BringUp:
	// happens before any UID-dependent setup.
	if err := s.Record().allocateDynamicUID(); err != nil {
		s.services.logger.Error("Service '%s': dynamic-user: %v", s.serviceName, err)
		return false
	}

	// Evaluate systemd-style start preconditions before doing any work.
	switch outcome, reason := s.CheckPredicates(); outcome {
	case PredFailed:
		s.services.logger.Error("Service '%s': %s", s.serviceName, reason)
		return false
	case PredSkip:
		s.services.logger.Info("Service '%s': skipped (%s)", s.serviceName, reason)
		s.markSkippedStartReason(reason)
		return true
	}

	// Fail-fast pre-start check: required_files / required_dirs must exist
	// before we run the start script. See ProcessService.BringUp.
	if err := s.CheckRequiredPaths(); err != nil {
		s.services.logger.Error("Service '%s': %v", s.serviceName, err)
		return false
	}

	// Set up output pipe based on log type
	var outputPipe *os.File
	if s.logType == LogToBuffer {
		if s.logBuf == nil {
			s.logBuf = NewLogBuffer(s.logBufMax)
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
		// O_NOFOLLOW + fchown-via-fd: close the symlink-swap TOCTOU
		// where root slinit could be tricked into writing/chowning an
		// attacker-pointed path.
		f, err := os.OpenFile(s.logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, os.FileMode(s.logFilePerms))
		if err != nil {
			s.services.logger.Error("Service '%s': failed to open logfile '%s': %v",
				s.serviceName, s.logFile, err)
			return false
		}
		if s.logFileUID >= 0 || s.logFileGID >= 0 {
			_ = f.Chown(s.logFileUID, s.logFileGID)
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
		Command:           s.startCommand,
		WorkingDir:        s.workingDir,
		Env:               s.Record().BuildFullEnv(),
		RunAsUID:          s.effectiveRunAsUID(),
		RunAsGID:          s.effectiveRunAsGID(),
		SupplementaryGIDs: s.supplementaryGIDs,
		OutputPipe:        outputPipe,
		InputPipe:         inputPipe,
	}
	s.Record().ApplyProcessAttrs(&params)

	pid, exitCh, err := process.StartProcess(params)
	if err != nil {
		s.exitStatus = noteStartExecFailure(s.Record(), err)
		if outputPipe != nil && s.logType == LogToBuffer {
			s.logBuf.CloseWriteEnd()
		} else if outputPipe != nil && s.logType == LogToFile {
			outputPipe.Close()
		}
		s.services.logger.Error("Service '%s': failed to run start command: %v",
			s.serviceName, err)
		return false
	}

	if outputPipe != nil && s.logType == LogToBuffer {
		s.logBuf.CloseWriteEnd()
		s.logBuf.StartReader()
	} else if outputPipe != nil && s.logType == LogToFile {
		outputPipe.Close()
	}

	s.startPID = pid
	s.publishPID()
	s.startHandle = process.ProcessHandle{PID: pid, ExitCh: exitCh}

	// Create utmp entry for the start process
	if s.HasUtmp() {
		s.services.utmpCreate(s.inittabID, s.inittabLine, s.Record().UtmpMode(), pid)
	}

	// Monitor the start command
	s.closeDoneCh()
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
		Command:           s.stopCommand,
		WorkingDir:        s.workingDir,
		Env:               s.Record().BuildFullEnv(),
		RunAsUID:          s.effectiveRunAsUID(),
		RunAsGID:          s.effectiveRunAsGID(),
		SupplementaryGIDs: s.supplementaryGIDs,
	}
	s.Record().ApplyProcessAttrs(&params)
	if s.privHooks.Stop {
		params.FullyPrivileged()
	}
	// Use the credentials the main command was started with; the
	// service's stop removes them.
	params.ReuseCredentials = true

	pid, exitCh, err := process.StartProcess(params)
	if err != nil {
		s.services.logger.Error("Service '%s': failed to run stop command: %v",
			s.serviceName, err)
		// Stop anyway
		s.Stopped()
		return
	}

	s.stopPID = pid
	s.publishPID()
	s.stopHandle = process.ProcessHandle{PID: pid, ExitCh: exitCh}

	// Monitor the stop command
	s.closeDoneCh()
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

// InterruptStart sends SIGINT to the start command. With a command to
// wait for it returns false, leaving the service STOPPING (see
// stopInterruptedStart), as dinit's issue_start_interrupt does.
func (s *ScriptedService) InterruptStart() bool {
	if s.waitingForDeps {
		return true
	}

	if s.startPID > 0 && s.Flags.StartInterruptible {
		s.services.logger.Info("Service '%s': interrupting start (SIGINT to %d)",
			s.serviceName, s.startPID)
		process.SignalProcess(s.startPID, syscall.SIGINT, false)
		s.stopInterruptedStart()
		return false // Wait for it to die
	}

	return s.startPID <= 0
}

// stopInterruptedStart moves a start whose command has just been told to
// give up — by a stop arriving while STARTING, or by the start-timeout —
// to STOPPING with the stop-timeout armed, as dinit's
// issue_start_interrupt does. The command's exit then ends the start
// (handleStartExit), and one that ignores the signal is killed by the
// stop-timeout. Left in STARTING, a cancelled start was reported as a
// failure when the command died of the SIGINT, the stop command never
// ran, and a command ignoring SIGINT held the service there for good.
func (s *ScriptedService) stopInterruptedStart() {
	s.interruptingStart = true
	if s.stopTimeout > 0 {
		s.armTimer(s.stopTimeout, scriptedTimerStopTimeout)
	} else {
		s.cancelTimer()
	}
	s.state.Store(StateStopping)
	// The service has left STARTING: give its start slot back.
	s.dropStartSlot()
}

func (s *ScriptedService) monitorStart(exitCh <-chan process.ChildExit) {
	for {
		timer := s.getTimer()
		select {
		case exit, ok := <-exitCh:
			if !ok {
				return
			}
			s.handleStartExit(exit)
			return

		case <-timerC(timer):
			s.handleTimerExpired(timer)

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
		timer := s.getTimer()
		select {
		case exit, ok := <-exitCh:
			if !ok {
				return
			}
			s.handleStopExit(exit)
			return

		case <-timerC(timer):
			s.handleTimerExpired(timer)

		case <-s.timerUpdateCh:
			// Timer was armed; re-enter select to pick up new timer channel
			continue

		case <-s.doneCh:
			return
		}
	}
}

// handleStartExit processes start-command termination. Runs in the
// monitorStart goroutine; acquires queueMu.
func (s *ScriptedService) handleStartExit(exit process.ChildExit) {
	// Kill remaining process group members (orphaned children of the script)
	process.KillProcessGroup(exit.PID)

	// Kill entire cgroup tree to clean up orphaned processes
	if s.Flags.KillAllOnStop {
		s.killCgroupTree(syscall.SIGKILL)
	}

	// Clear utmp entry
	if s.HasUtmp() {
		s.services.utmpClear(s.inittabID, s.inittabLine)
	}

	s.services.queueMu.Lock()
	defer s.services.queueMu.Unlock()

	s.startPID = 0
	s.publishPID()
	s.startHandle.Clear()
	s.cancelTimer()
	s.recordExit(exit)

	if s.interruptingStart && s.state.Load() == StateStopping {
		s.endInterruptedStart(exit)
		s.services.processQueuesLocked()
		return
	}

	if exit.ExecErr != nil {
		s.services.logger.Error("Service '%s': start command exec failed: %v",
			s.serviceName, exit.ExecErr)
		s.stopReason = ReasonExecFailed
		s.failedToStart(false, true)
		s.services.processQueuesLocked()
		return
	}

	if exit.ExitedClean() {
		// Start command succeeded — but if a timeout already fired,
		// the service should still be treated as failed.
		if s.startTimedOut {
			s.services.logger.Error("Service '%s': start command completed after timeout",
				s.serviceName)
			s.failedToStart(false, true)
			s.services.processQueuesLocked()
			return
		}
		// Start command succeeded
		s.Started()
		s.services.processQueuesLocked()
	} else {
		// Start command failed
		exitCode := -1
		if exit.Exited() {
			exitCode = exit.Status.ExitStatus()
		}
		s.services.logger.Error("Service '%s': start command failed (exit code: %d)",
			s.serviceName, exitCode)
		if !s.startTimedOut {
			s.stopReason = ReasonFailed
		}
		s.failedToStart(false, true)
		s.services.processQueuesLocked()
	}
}

// endInterruptedStart finishes a start that was interrupted, once its
// command has exited. As in dinit's scripted_service::handle_exit_status:
// a command that exited with a failure status is taken to need no
// cleanup, and the service is stopped; one that completed, or died of
// the signal, may have set something up, so the stop command runs.
// Caller holds queueMu.
func (s *ScriptedService) endInterruptedStart(exit process.ChildExit) {
	s.interruptingStart = false
	switch {
	case exit.ExecErr != nil:
		s.Stopped()
	case exit.Exited() && exit.Status.ExitStatus() != 0:
		s.services.logger.Info("Service '%s': start cancelled; exit code %d",
			s.serviceName, exit.Status.ExitStatus())
		s.Stopped()
	default:
		if exit.Signaled() {
			s.services.logger.Info("Service '%s': start cancelled from signal %d",
				s.serviceName, int(exit.Status.Signal()))
		}
		s.BringDown()
	}
}

// handleStopExit processes stop-command termination. Runs in the
// monitorStop goroutine; acquires queueMu.
func (s *ScriptedService) handleStopExit(exit process.ChildExit) {
	// Kill remaining process group members
	process.KillProcessGroup(exit.PID)

	// Kill entire cgroup tree to clean up orphaned processes
	if s.Flags.KillAllOnStop {
		s.killCgroupTree(syscall.SIGKILL)
	}

	s.services.queueMu.Lock()
	defer s.services.queueMu.Unlock()

	s.stopPID = 0
	s.publishPID()
	s.stopHandle.Clear()
	s.cancelTimer()
	s.recordExit(exit)

	if !exit.ExitedClean() {
		// Decode the raw wait(2) status word into the operator-visible
		// form. Without this the log line reads "status: 1280" instead
		// of "exit code 5" (1280 = 5<<8, i.e. exit_status=5 signal=0),
		// which is confusing every time.
		s.services.logger.Error("Service '%s': stop command failed (%s)",
			s.serviceName, exitDetail(exit))
	}

	// Whether stop command succeeded or not, the service is stopped
	s.Stopped()
	s.services.processQueuesLocked()
}

// handleTimerExpired processes a timer expiration. Runs in a monitor
// goroutine; acquires queueMu.
func (s *ScriptedService) handleTimerExpired(timer *time.Timer) {
	s.services.queueMu.Lock()
	defer s.services.queueMu.Unlock()

	// A tick received just before a stop or restart re-armed the timer
	// belongs to the timer it replaced: acting on it would give the new
	// purpose its deadline early (a stop escalated to SIGKILL at once).
	if timer != s.processTimer {
		return
	}

	purpose := s.timerPurpose
	s.timerPurpose = scriptedTimerNone

	switch purpose {
	case scriptedTimerStartTimeout:
		s.stopReason = ReasonTimedOut
		s.startTimedOut = true
		if s.startPID <= 0 {
			s.failedToStart(false, true)
			s.services.processQueuesLocked()
			return
		}
		// Cancel the start as a stop would, then fail it: STOPPING with
		// the stop-timeout armed, the command's exit ending the start
		// and, if it died of the signal, running the stop command
		// (dinit's start_timed_out). The SIGKILL is left to the
		// stop-timeout, for a command that ignores SIGINT.
		s.services.logger.Error("Service '%s': start command timeout, sending SIGINT",
			s.serviceName)
		process.SignalProcess(s.startPID, syscall.SIGINT, false)
		s.stopInterruptedStart()
		if s.stopTimeout <= 0 {
			// No stop-timeout to escalate on (0 means "never", as for
			// an operator stop). A timed-out start must still end: 4.0.2
			// killed it at the start-timeout, so give the command one
			// more start-timeout to honour SIGINT, then SIGKILL.
			s.armTimer(s.startTimeout, scriptedTimerStopTimeout)
		}
		s.failedToStart(false, false)
		s.services.processQueuesLocked()

	case scriptedTimerStopTimeout:
		if s.stopPID > 0 {
			s.services.logger.Error("Service '%s': stop command timeout, sending SIGKILL",
				s.serviceName)
			process.SignalProcess(s.stopPID, 9, false) // SIGKILL
		} else if s.startPID > 0 {
			// Still the start command: an interrupted start it ignored.
			s.services.logger.Error("Service '%s': interrupted start command still running at stop timeout, sending SIGKILL",
				s.serviceName)
			process.SignalProcess(s.startPID, syscall.SIGKILL, false)
		}
		// Kill entire cgroup tree on SIGKILL escalation
		if s.Flags.KillAllOnStop {
			s.killCgroupTree(syscall.SIGKILL)
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
		if !s.processTimer.Stop() {
			// Drain the channel to prevent stale timer events
			select {
			case <-s.processTimer.C:
			default:
			}
		}
		s.processTimer = nil
	}
	s.timerPurpose = scriptedTimerNone
}

// getTimer returns the armed timer, or nil. The monitor hands it to
// handleTimerExpired with its tick.
func (s *ScriptedService) getTimer() *time.Timer {
	s.services.queueMu.RLock()
	defer s.services.queueMu.RUnlock()
	return s.processTimer
}

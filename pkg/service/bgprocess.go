package service

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
)

const (
	// daemonPollInterval is how often we check if the daemon process is alive.
	daemonPollInterval = 1 * time.Second
	// pidFilePollInterval is how often we re-check for a forking daemon's
	// pid file after its launcher has exited. Measured windows with real
	// daemons are tens to a few hundred milliseconds, so this resolves
	// most starts in one or two ticks without busy-waiting.
	pidFilePollInterval = 20 * time.Millisecond
)

// BGProcessService manages a self-backgrounding daemon process.
// The lifecycle is: launch command → launcher forks and exits → read PID file
// to discover the daemon PID → monitor daemon via polling.
type BGProcessService struct {
	ServiceRecord

	// Command configuration
	command     []string
	argv0       string // override argv[0] presented to the exec'd binary (runit chpst -b)
	stopCommand []string
	workingDir  string
	envFile     string

	// PID file path (required)
	pidFile string

	// followPID adopts a process slinit did not start: the path of a pid
	// file written by somebody else. Set, it makes `command` optional and
	// turns BringUp into an attach rather than a launch.
	followPID string

	// Credentials
	runAsUID          uint32
	runAsGID          uint32
	supplementaryGIDs []uint32

	// Process state
	launcherPID int
	daemonPID   int

	// pidPublished mirrors the PID that PID() reports, for readers that
	// cannot take queueMu — the status encoders, and the log rotator's
	// GetPID callback. ProcessService has carried this for the same
	// reason; bgprocess did not, so PID() read daemonPID bare while
	// monitorDaemon's termination path wrote it under the lock. `go test
	// -race` reports it as soon as anything polls PID() across a daemon
	// exit, which a follow-pid restart test does. Written at the five
	// assignment sites of launcherPID/daemonPID, nowhere else.
	pidPublished atomic.Int64
	stopPID      int // PID of stop-command process (0 if none)
	exitStatus   ExitStatus
	procHandle   process.ProcessHandle

	// daemonGoneDuringStop records that the daemon died while the
	// stop-command was still running, so the stop-command's completion
	// is what finishes the transition to STOPPED.
	daemonGoneDuringStop bool

	// Timer for start/stop/restart timeouts
	processTimer *time.Timer
	timerPurpose bgTimerPurpose

	// Timeout configuration
	startTimeout time.Duration
	stopTimeout  time.Duration
	restartDelay time.Duration
	// systemd TimeoutAbortSec= / TimeoutStartFailureMode=. Stored for
	// parity with ProcessService; BGProcessService's timeout escalation
	// path is more limited but the fields let the config layer wire
	// without special-casing.
	timeoutAbortSec         time.Duration
	timeoutStartFailureMode TimeoutFailureMode
	// systemd ExitType= — accepted for config-parity with process
	// services. BGProcess polls the pidfile so cgroup-drain semantics
	// are less meaningful, but honouring the setting keeps operator
	// mental model consistent.
	exitType ExitType

	// Progressive restart backoff (OpenRC-compatible, linear additive)
	restartDelayStep    time.Duration
	restartDelayCap     time.Duration
	currentRestartDelay time.Duration

	// systemd-style RestartRandomizedDelaySec: additive jitter drawn
	// from [0, restartRandomizedDelay). 0 disables.
	restartRandomizedDelay time.Duration
	// systemd RestartMaxDelaySec cap on delay + jitter total. 0 = no cap.
	restartMaxDelay time.Duration

	// Restart rate limiting
	restartInterval      time.Duration
	maxRestartCount      int
	restartIntervalTime  time.Time
	restartIntervalCount int
	lastStartTime        time.Time

	// State tracking
	stopIssued       bool
	doingSmoothRecov bool

	// interruptedStopBy is when the stop-timeout armed by an interrupted
	// start runs out (zero: no stop-timeout). It bounds the wait for the
	// pid file of a daemon the launcher forked before giving up.
	interruptedStopBy time.Time

	// Timer that relaunches the daemon when a smooth recovery has to
	// wait out restart-delay. An AfterFunc, not processTimer: the
	// recovery starts from handleDaemonTermination, so monitorDaemon has
	// already returned and nothing would read processTimer.
	smoothRecovTimer *time.Timer

	// recoveryPoll is set while pollForPIDFile waits for the pid file of
	// a daemon relaunched by smooth recovery: the service is STARTED
	// then, not STARTING, and has no pid to track until the file comes.
	recoveryPoll bool

	// lastDaemonPID is the daemon that last died while tracked. A smooth
	// recovery that reads it back from the pid file is reading the file
	// that daemon left behind, not one the new daemon has written.
	lastDaemonPID int

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
)

// signalProcessOnly decides whether a stop signals just the process or its
// whole group.
//
// For a daemon slinit launched, the group is one slinit set up, so
// signalling it is how a forking daemon's workers get stopped with their
// master instead of orphaned. An ADOPTED process is different: we did not
// create its group, and if it is not the group leader the group belongs to
// whoever started it — plausibly an operator's login shell, whose job would
// then be killed by a service stop. So when following, the group is only
// signalled if the adopted process leads it, which means the group is its
// own.
func (s *BGProcessService) signalProcessOnly(pid int) bool {
	if s.Flags.SignalProcessOnly {
		return true
	}
	if s.followPID == "" || pid <= 0 {
		return false
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		// Not queryable: a different session, so it leads its own group.
		return false
	}
	return pgid != pid
}

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

func (s *BGProcessService) SetCommand(cmd []string)     { s.command = cmd }
func (s *BGProcessService) SetArgv0(a string)           { s.argv0 = a }
func (s *BGProcessService) SetStopCommand(cmd []string) { s.stopCommand = cmd }
func (s *BGProcessService) SetWorkingDir(dir string)    { s.workingDir = dir }
func (s *BGProcessService) SetEnvFile(path string)      { s.envFile = path }
func (s *BGProcessService) SetPIDFile(path string)      { s.pidFile = path }
func (s *BGProcessService) GetPIDFile() string          { return s.pidFile }
func (s *BGProcessService) SetFollowPID(path string)    { s.followPID = path }
func (s *BGProcessService) GetFollowPID() string        { return s.followPID }

// pidFileToWatch is the file the daemon's pid is read from: the one an
// adopted process's owner writes, or the one our own launcher writes.
func (s *BGProcessService) pidFileToWatch() string {
	if s.followPID != "" {
		return s.followPID
	}
	return s.pidFile
}
func (s *BGProcessService) SetRunAs(uid, gid uint32) { s.runAsUID = uid; s.runAsGID = gid }
func (s *BGProcessService) SetSupplementaryGroups(gids []uint32) {
	s.supplementaryGIDs = gids
}

// effectiveRunAsUID / GID prefer the dynamic-user transient UID when
// allocated, falling back to the configured run-as values.
func (s *BGProcessService) effectiveRunAsUID() uint32 {
	if uid := s.Record().DynamicUID(); uid != 0 {
		return uid
	}
	return s.runAsUID
}
func (s *BGProcessService) effectiveRunAsGID() uint32 {
	if uid := s.Record().DynamicUID(); uid != 0 {
		return uid
	}
	return s.runAsGID
}
func (s *BGProcessService) SetStartTimeout(d time.Duration)    { s.startTimeout = d }
func (s *BGProcessService) SetStopTimeout(d time.Duration)     { s.stopTimeout = d }
func (s *BGProcessService) SetTimeoutAbortSec(d time.Duration) { s.timeoutAbortSec = d }
func (s *BGProcessService) SetTimeoutStartFailureMode(m TimeoutFailureMode) {
	s.timeoutStartFailureMode = m
}
func (s *BGProcessService) SetExitType(t ExitType)          { s.exitType = t }
func (s *BGProcessService) SetRestartDelay(d time.Duration) { s.restartDelay = d }

// SetRestartBackoff configures progressive (linear additive) restart backoff.
func (s *BGProcessService) SetRestartBackoff(step, cap time.Duration) {
	s.restartDelayStep = step
	s.restartDelayCap = cap
}

// SetRestartRandomizedDelay configures additive jitter on the restart delay.
func (s *BGProcessService) SetRestartRandomizedDelay(d time.Duration) {
	s.restartRandomizedDelay = d
}

// SetRestartMaxDelay caps the (delay + jitter) sum, matching systemd
// RestartMaxDelaySec. 0 disables the cap.
func (s *BGProcessService) SetRestartMaxDelay(d time.Duration) {
	s.restartMaxDelay = d
}

// nextRestartDelay returns the delay to use for the next restart and advances
// the progressive backoff counter. When step <= 0, always returns restartDelay.
// Jitter (restartRandomizedDelay) is applied on top of the base value when set.
func (s *BGProcessService) nextRestartDelay() time.Duration {
	var delay time.Duration
	if s.restartDelayStep <= 0 {
		delay = s.restartDelay
	} else {
		if s.currentRestartDelay < s.restartDelay {
			s.currentRestartDelay = s.restartDelay
		}
		delay = s.currentRestartDelay
		next := delay + s.restartDelayStep
		capDelay := s.restartDelayCap
		if capDelay <= 0 {
			capDelay = 60 * time.Second
		}
		if next > capDelay {
			next = capDelay
		}
		s.currentRestartDelay = next
	}
	total := delay + jitter(s.restartRandomizedDelay)
	if s.restartMaxDelay > 0 && total > s.restartMaxDelay {
		total = s.restartMaxDelay
	}
	return total
}

// BecomingInactive is called when the service won't restart. The
// log-type=pipe pipe stays open (see ProcessService.BecomingInactive).
func (s *BGProcessService) BecomingInactive() {
	s.closeDoneCh()
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
	// See ProcessService.PID() — same reentrancy rationale, and the same
	// reason for an atomic mirror rather than a bare read: callers here
	// do not hold queueMu, and the monitor goroutine writes daemonPID
	// when the daemon exits.
	return int(s.pidPublished.Load())
}

// publishPID keeps the mirror in step with whichever pid PID() should
// report: the daemon once known, the launcher before that.
func (s *BGProcessService) publishPID() {
	if s.daemonPID > 0 {
		s.pidPublished.Store(int64(s.daemonPID))
		return
	}
	s.pidPublished.Store(int64(s.launcherPID))
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
	if len(s.command) == 0 && s.followPID == "" {
		s.services.logger.Error("Service '%s': no command specified", s.serviceName)
		return false
	}

	// systemd GuessMainPID= opt-in: allow bgprocess with no pid-file
	// as long as the service runs in a delegated cgroup — we scan
	// cgroup.procs after the launcher exits and pick the first non-
	// init pid. If neither pid-file nor guess-main-pid is set, we
	// still refuse (a bgprocess without either has no way to track
	// its lifecycle).
	if s.pidFile == "" && s.followPID == "" && !s.Record().GuessMainPID() {
		s.services.logger.Error("Service '%s': no pid-file specified for bgprocess (or set guess-main-pid = yes)", s.serviceName)
		return false
	}

	if err := s.Record().resolveDeferredRunAs(); err != nil {
		s.services.logger.Error("Service '%s': %v", s.serviceName, err)
		return false
	}

	// Dynamic-user allocation (#13).
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
	// before fork/exec. See ProcessService.BringUp.
	if err := s.CheckRequiredPaths(); err != nil {
		s.services.logger.Error("Service '%s': %v", s.serviceName, err)
		return false
	}

	s.lastStartTime = time.Now()
	s.stopIssued = false
	s.exitStatus = ExitStatus{}
	s.daemonPID = 0
	s.publishPID()

	// follow-pid: there is nothing to launch. Adopt the process the pid
	// file names and hand it to the same supervisor a launched daemon
	// gets — the poll already tolerates a reparented process, notices a
	// zombie, and checks /proc start time so a recycled pid is not
	// mistaken for the original.
	//
	// None of the launch machinery below applies: no pipe to create,
	// because the process's stdio belongs to whoever started it and
	// cannot be redirected after the fact, and no exec to configure,
	// because the credentials, cgroup and hardening of a process we did
	// not start are already fixed. The loader refuses the directives that
	// contradict following outright (command, pid-file, a non-bgprocess
	// type); the exec-time ones are documented as not applying rather
	// than enumerated here, because that list lives in pkg/process and a
	// copy of it would drift.
	if s.followPID != "" {
		return s.adoptFollowedPID()
	}

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
		Command:           s.command,
		Argv0:             s.argv0,
		WorkingDir:        s.workingDir,
		Env:               s.buildEnv(),
		TermSignal:        s.termSignal,
		SignalProcessOnly: s.Flags.SignalProcessOnly,
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
	s.publishPID()
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
	// A smooth recovery waiting out restart-delay has nothing left to
	// launch.
	s.cancelSmoothRecovery()

	pid := s.daemonPID
	if pid <= 0 {
		pid = s.launcherPID
	}
	if pid <= 0 && s.recoveryPoll {
		// A smooth recovery's launcher has exited but its daemon has
		// not written the pid file yet. Stopping now would leave that
		// daemon running unsupervised: wait for it as the stop of an
		// interrupted start does, and bring it down.
		s.recoveryPoll = false
		s.cancelTimer()
		s.interruptedStopBy = time.Time{}
		s.pollForForkedDaemon()
		return
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

	s.services.logger.Info("Service '%s': sending %s to process %d",
		s.serviceName, signalName(sig), pid)

	// Signal the daemon's whole process group, not just the daemon: a
	// forking daemon's workers are its children and share its group, and
	// signalling the master alone orphans them. SignalProcessOnly opts
	// out, matching dinit, where kill_pg gates on the same flag.
	err := process.SignalDaemonGroup(pid, sig, s.signalProcessOnly(pid))
	if err != nil {
		s.logStopSignalFailure(err)
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
		Command:           s.stopCommand,
		WorkingDir:        s.workingDir,
		Env:               s.buildEnv(),
		RunAsUID:          s.effectiveRunAsUID(),
		RunAsGID:          s.effectiveRunAsGID(),
		SupplementaryGIDs: s.supplementaryGIDs,
	}
	s.Record().ApplyProcessAttrs(&params)
	// Use the credentials the main command was started with; the
	// service's stop removes them.
	params.ReuseCredentials = true

	pid, exitCh, err := process.StartProcess(params)
	if err != nil {
		s.services.logger.Error("Service '%s': failed to start stop-command: %v",
			s.serviceName, err)
		return false
	}

	s.stopPID = pid
	s.services.logger.Info("Service '%s': stop-command started (pid %d)", s.serviceName, pid)

	// The daemon this stop is for. When the stop-command finishes, only
	// that process is signalled, and only while the service is still
	// STOPPING with it: reading s.daemonPID then instead signalled
	// whatever daemon had been started since, if the old one had died
	// and the service had already been restarted.
	target := s.daemonPID
	if target <= 0 {
		target = s.launcherPID
	}

	// Monitor the stop-command. It takes queueMu for the state it shares
	// with the state machine (stopPID was written here with no lock), and
	// signals outside it, as ProcessService.execStopCommand does.
	go func() {
		// The stop-command's own timeout: the stop-timeout armed by
		// BringDown is read only by monitorDaemon, which returns when
		// the daemon dies — the usual first act of a stop-command — and
		// the stop then waits for this command to finish.
		var timeoutCh <-chan time.Time
		if s.stopTimeout > 0 {
			t := time.NewTimer(s.stopTimeout)
			defer t.Stop()
			timeoutCh = t.C
		}

		var exit process.ChildExit
		select {
		case exit = <-exitCh:
		case <-timeoutCh:
			s.services.logger.Error(
				"Service '%s': stop-command exceeded stop-timeout, killing (pid %d)",
				s.serviceName, pid)
			process.SignalProcess(pid, syscall.SIGKILL, false)
			exit = <-exitCh
		}
		process.KillProcessGroup(exit.PID)

		s.services.queueMu.Lock()
		if s.stopPID == pid {
			s.stopPID = 0
		}
		daemonGone := s.daemonGoneDuringStop
		if daemonGone {
			// The daemon died while this command ran, so its death
			// could not finish the stop: finish it here.
			s.daemonGoneDuringStop = false
			if s.state.Load() == StateStopping {
				s.stopIssued = false
				s.Stopped()
				s.services.processQueuesLocked()
			}
		}
		current := s.daemonPID
		if current <= 0 {
			current = s.launcherPID
		}
		signal := !daemonGone && target > 0 && current == target &&
			s.state.Load() == StateStopping
		s.services.queueMu.Unlock()

		if exit.Exited() && exit.Status.ExitStatus() == 0 {
			s.services.logger.Info("Service '%s': stop-command completed successfully",
				s.serviceName)
			// Stop-command succeeded — now send term signal to daemon process
			if signal {
				// SIGTERM, deliberately, and both branches below agree.
				//
				// This fell back to SIGKILL for a while (e96e761,
				// "bgprocess: change signal for softreboot"), to stop a
				// daemon surviving a soft reboot and holding its port
				// against the next generation — nginx and :80. The
				// cause of that was elsewhere: SoftReboot never swept
				// the old userspace at all, so a process that had been
				// signalled but had not finished dying crossed the
				// re-exec. It sweeps now, and SIGKILL here is no longer
				// buying anything.
				//
				// What it did cost is the point. Reaching this branch
				// means the service's own stop procedure ran and
				// succeeded: `nginx -s quit` returned, so nginx has
				// begun draining connections and closing listeners.
				// SIGKILL on top of that throws the drain away and
				// makes ExecStop decorative, and a daemon that flushes
				// state on exit loses the flush. It applied to every
				// stop of every bgprocess with a stop-command, not only
				// to soft reboots.
				//
				// A daemon that ignores TERM is still dealt with: the
				// stop-timeout (10s by default) escalates to SIGKILL
				// and sweeps the cgroup tree. That ladder is the right
				// place for force, not this line.
				sig := s.termSignal
				if sig == 0 {
					sig = syscall.SIGTERM
				}
				s.services.logger.Info("Service '%s': sending %s to the daemon group (pid %d)",
					s.serviceName, signalName(sig), target)
				process.SignalDaemonGroup(target, sig, s.signalProcessOnly(target))
			}
		} else {
			s.services.logger.Error("Service '%s': stop-command failed (%s)",
				s.serviceName, exitDetail(exit))
			if signal {
				sig := s.termSignal
				if sig == 0 {
					sig = syscall.SIGTERM
				}
				s.services.logger.Info("Service '%s': sending %s to the daemon group (pid %d)",
					s.serviceName, signalName(sig), target)
				process.SignalDaemonGroup(target, sig, s.signalProcessOnly(target))
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

// InterruptStart cancels the start by sending SIGINT to the launcher. With
// a launcher to wait for it returns false, leaving the service STOPPING
// (see stopInterruptedStart), as dinit's issue_start_interrupt does.
func (s *BGProcessService) InterruptStart() bool {
	if s.waitingForDeps {
		return true
	}
	if s.launcherPID > 0 {
		process.SignalProcess(s.launcherPID, syscall.SIGINT, false)
		s.stopInterruptedStart()
		return false
	}
	return true
}

// stopInterruptedStart moves a start whose launcher has just been told to
// give up — by a stop arriving while STARTING, or by the start-timeout —
// to STOPPING with the stop-timeout armed, as dinit's
// issue_start_interrupt does. The launcher's exit then ends the stop
// (handleLauncherExit), and one that ignores the signal is killed by the
// stop-timeout.
func (s *BGProcessService) stopInterruptedStart() {
	if s.stopTimeout > 0 {
		s.armTimer(s.stopTimeout, bgTimerStopTimeout)
		s.interruptedStopBy = time.Now().Add(s.stopTimeout)
	} else {
		s.cancelTimer()
		s.interruptedStopBy = time.Time{}
	}
	s.state.Store(StateStopping)
	// The service has left STARTING: give its start slot back.
	s.dropStartSlot()
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
		timer := s.getTimer()
		select {
		case exit, ok := <-exitCh:
			if !ok {
				return
			}
			s.handleLauncherExit(exit)
			return

		case <-timerC(timer):
			s.handleTimerExpired(timer)

		case <-s.timerUpdateCh:
			continue

		case <-s.doneCh:
			return
		}
	}
}

// handleLauncherExit processes the launcher process termination.
// Runs in the monitorLauncher goroutine; acquires queueMu to serialize
// state mutations with the main scheduling path.
func (s *BGProcessService) handleLauncherExit(exit process.ChildExit) {
	// Kill remaining process group members from the launcher — but only
	// when the launcher failed.
	//
	// A forking service's launcher exits by design, leaving the daemon
	// behind, and that daemon is still in the launcher's process group
	// until it calls setsid() for itself. SIGKILLing the group here
	// therefore races the daemon's own detach and sometimes wins:
	// measured with nginx, the start failed about two runs in five with
	// no nginx process left and an empty pid-file directory. dinit does
	// not kill the group on this path either — kill_pg there is for
	// interrupting a start, for stopping, and for the stop-timeout
	// escalation — so skipping it restores parity as much as it fixes
	// the race.
	//
	// A launcher that exited badly is different: nothing is going to be
	// adopted, so anything still in its group is just litter.
	//
	// SignalProcessOnly opts out of pgroup signals for this service
	// (dinit parity: baseproc-service.cc kill_pg gates on the same flag).
	if !s.Flags.SignalProcessOnly && !exit.ExitedClean() {
		process.KillProcessGroup(exit.PID)
	}

	// Kill entire cgroup tree to clean up orphaned processes
	if s.Flags.KillAllOnStop {
		s.killCgroupTree(syscall.SIGKILL)
	}

	s.services.queueMu.Lock()
	defer s.services.queueMu.Unlock()

	s.launcherPID = 0
	s.publishPID()
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

	if s.state.Load() == StateStopping {
		// The start was interrupted or timed out, and the launcher is
		// gone: the stop is over, and so is the start — whatever the
		// launcher's status, the start has already been settled.
		s.cancelTimer()
		s.stopIssued = false
		if exit.ExitedClean() {
			// It may have forked the daemon before giving up. Look for
			// it as a start would, and adopt it only to bring it down.
			if s.pidFile != "" {
				pid, result, err := process.ReadPIDFile(s.pidFile)
				if result == process.PIDResultOK {
					s.bringDownForkedDaemonLocked(pid)
					return
				}
				if result == process.PIDResultFailed && pidFileStillComing(err) {
					// Written after the launcher's exit, as on the
					// start path (pollForPIDFile).
					s.pollForForkedDaemon()
					return
				}
			} else if s.Record().GuessMainPID() {
				if pid, err := guessMainPIDFromCgroup(s.EffectiveCgroupPath()); err == nil {
					s.bringDownForkedDaemonLocked(pid)
					return
				}
			}
		}
		s.Stopped()
		s.services.processQueuesLocked()
		return
	}

	if exit.ExecErr != nil {
		s.services.logger.Error("Service '%s': launcher exec failed: %v",
			s.serviceName, exit.ExecErr)
		if s.state.Load() == StateStarted {
			s.failSmoothRecoveryLocked()
			return
		}
		s.cancelTimer()
		s.stopReason = ReasonExecFailed
		s.state.Store(StateStopping)
		s.failedToStart(false, true)
		s.services.processQueuesLocked()
		return
	}

	if !exit.ExitedClean() {
		exitCode := -1
		if exit.Exited() {
			exitCode = exit.Status.ExitStatus()
		}
		s.services.logger.Error("Service '%s': launcher exited with code %d",
			s.serviceName, exitCode)
		if s.state.Load() == StateStarted {
			s.failSmoothRecoveryLocked()
			return
		}
		s.cancelTimer()
		s.stopReason = ReasonFailed
		s.failedToStart(false, true)
		s.services.processQueuesLocked()
		return
	}

	// Launcher exited cleanly. Discover the daemon PID: read pid-
	// file when configured, otherwise (guess-main-pid) scan the
	// service's cgroup.procs and pick the first non-init pid.
	var (
		pid    int
		result process.PIDResult
		err    error
	)
	if s.pidFile != "" {
		pid, result, err = process.ReadPIDFile(s.pidFile)
		if result == process.PIDResultFailed && pidFileStillComing(err) {
			// A forking daemon writes its pid file in the child, after
			// the launcher it forked from has already exited — so the
			// file is routinely not there yet at this instant. Measured
			// with nginx: absent in 7 of 10 starts, appearing within a
			// few hundred milliseconds. Reading once and giving up made
			// slinit fail real daemons at random.
			//
			// Poll instead, on a goroutine: this runs under queueMu, so
			// waiting here would freeze every other service for the
			// duration.
			s.pollForPIDFile(s.pidFile, err)
			return
		}
	} else if s.Record().GuessMainPID() {
		pid, err = guessMainPIDFromCgroup(s.EffectiveCgroupPath())
		if err != nil {
			result = process.PIDResultFailed
		} else {
			result = process.PIDResultOK
		}
	}
	if result == process.PIDResultFailed {
		s.failPIDDiscoveryLocked(fmt.Errorf("failed to discover daemon pid: %w", err))
		return
	}

	if result == process.PIDResultTerminated && s.state.Load() == StateStarted {
		if pid == s.lastDaemonPID {
			// The file the dead daemon left: wait for the new one's.
			s.pollForPIDFile(s.pidFile, fmt.Errorf(
				"pid file still names the daemon being replaced (PID %d)", pid))
		} else {
			s.recoveredDaemonDiedLocked(pid)
		}
		return
	}
	if result == process.PIDResultTerminated {
		s.failPIDDiscoveryLocked(fmt.Errorf("daemon (PID %d) already terminated", pid))
		return
	}

	// PIDResultOK - daemon is running
	s.finishPIDDiscoveryLocked(pid)
}

// adoptFollowedPID starts a follow-pid service: read the pid file, attach
// to what it names, and let monitorDaemon supervise it. Caller holds
// queueMu, as for every BringUp.
//
// A pid file that is absent, or that names a process which is not running,
// is NOT a failure here — it is the normal case when slinit is asked to
// follow something that has not been started yet, or whose file is left
// over from a previous run. Both wait for the start-timeout, which is the
// only honest reading: we cannot distinguish "about to appear" from "never
// going to" except by waiting. A launched bgprocess treats the same result
// as fatal, because there the daemon was just forked and a dead pid means
// it died; pollForPIDFile tells the two apart by s.followPID.
func (s *BGProcessService) adoptFollowedPID() bool {
	pid, result, err := process.ReadPIDFile(s.followPID)
	switch result {
	case process.PIDResultOK:
		s.services.logger.Info("Service '%s': following existing process %d from %s",
			s.serviceName, pid, s.followPID)
		s.finishPIDDiscoveryLocked(pid)
	default:
		// Absent, stale or unparseable — wait for the owner to write it.
		s.pollForPIDFile(s.followPID, err)
	}
	return true
}

// finishPIDDiscoveryLocked completes a successful start once the daemon's
// PID is known. Shared by the immediate read and the polling path so the
// two cannot drift apart. Caller holds queueMu.
func (s *BGProcessService) finishPIDDiscoveryLocked(pid int) {
	s.recoveryPoll = false
	s.daemonPID = pid
	s.publishPID()

	// Create utmp entry for the daemon process
	if s.HasUtmp() {
		s.services.utmpCreate(s.inittabID, s.inittabLine, s.Record().UtmpMode(), pid)
	}

	s.cancelTimer()
	s.Started()
	s.services.processQueuesLocked()

	// Start monitoring the daemon process
	go s.monitorDaemon()
}

// failPIDDiscoveryLocked aborts the start when the daemon's PID cannot be
// established. Caller holds queueMu.
func (s *BGProcessService) failPIDDiscoveryLocked(err error) {
	s.recoveryPoll = false
	s.services.logger.Error("Service '%s': %v", s.serviceName, err)
	if s.state.Load() == StateStarted {
		s.failSmoothRecoveryLocked()
		return
	}
	s.cancelTimer()
	s.stopReason = ReasonFailed
	s.failedToStart(false, true)
	s.services.processQueuesLocked()
}

// failSmoothRecoveryLocked ends a smooth recovery that could not bring a
// daemon back: its launcher failed, or its pid file could not be read.
// The service is STARTED, so this is not a failed start — failedToStart
// would leave its hard dependents STARTED on top of nothing. As dinit's
// hard_failure does, stop it and its dependents for good. Caller holds
// queueMu.
func (s *BGProcessService) failSmoothRecoveryLocked() {
	s.recoveryPoll = false
	s.cancelTimer()
	s.stopReason = ReasonFailed
	s.UnrecoverableStop()
	s.services.processQueuesLocked()
}

// recoveredDaemonDiedLocked handles a smooth recovery whose pid file names
// a new daemon that is no longer running: it died as soon as it started.
// That is a daemon death like any other (dinit: goto begin in
// bgproc_service::handle_exit_status). Caller holds queueMu.
func (s *BGProcessService) recoveredDaemonDiedLocked(pid int) {
	s.recoveryPoll = false
	s.cancelTimer()
	s.services.logger.Error("Service '%s': daemon process %d terminated",
		s.serviceName, pid)
	s.exitStatus = ExitStatus{Vanished: true}
	s.lastDaemonPID = pid
	s.daemonDiedLocked()
}

// pidFileStillComing reports whether a failed read looks like a daemon
// that has not finished writing yet — the file is absent, or present and
// empty. Anything else (unparseable content, a permission problem) is a
// real error: waiting on it would only delay the failure by the whole
// start timeout, which is what a misconfigured service used to be spared.
func pidFileStillComing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, process.ErrPIDFileEmpty)
}

// pollForPIDFile waits for a forking daemon to write its pid file, then
// finishes or fails the start.
//
// It owns its own deadline rather than leaning on the start-timeout timer
// already armed: that timer only acts when a PID is known
// (bgTimerStartTimeout reads launcherPID, then daemonPID), and here both
// are zero — the launcher has exited and the daemon is not identified yet.
// Relying on it would leave the service wedged in STARTING forever.
//
// A smooth recovery reaches here too, with the service STARTED: the
// relaunched daemon writes its pid file late just as a first start's
// does, and until it is read the service tracks no process at all.
// recoveryPoll marks that case. Caller holds queueMu.
func (s *BGProcessService) pollForPIDFile(pidFile string, firstErr error) {
	pollState := StateStarting
	if s.state.Load() == StateStarted {
		pollState = StateStarted
		s.recoveryPoll = true
	}
	// polling reports, under queueMu, whether this poll still owns the
	// service: a stop or a failure elsewhere means there is nothing left
	// to finish.
	polling := func() bool {
		return s.state.Load() == pollState && (pollState == StateStarting || s.recoveryPoll)
	}
	deadline := s.startTimeout
	if deadline <= 0 {
		// start-timeout=0 means "no timeout" for the launcher, but a pid
		// file that never appears must not hold a service in STARTING
		// with nothing watching it.
		deadline = defaultStartTimeout
	}

	go func() {
		expiry := time.Now().Add(deadline)
		ticker := time.NewTicker(pidFilePollInterval)
		defer ticker.Stop()

		lastErr := firstErr
		for {
			<-ticker.C

			// Cheap unlocked pre-check: a stop or a failure elsewhere
			// means there is nothing left to finish. Re-checked under
			// the lock before acting on it.
			if s.state.Load() != pollState {
				return
			}

			pid, result, err := process.ReadPIDFile(pidFile)
			if result == process.PIDResultFailed {
				lastErr = err
				if !pidFileStillComing(err) {
					// Present but unparseable: that will not fix itself,
					// so fail now rather than sit out the whole deadline.
					s.services.queueMu.Lock()
					if polling() {
						s.failPIDDiscoveryLocked(fmt.Errorf("failed to discover daemon pid: %w", err))
					}
					s.services.queueMu.Unlock()
					return
				}
				if time.Now().After(expiry) {
					s.services.queueMu.Lock()
					if polling() {
						s.failPIDDiscoveryLocked(fmt.Errorf(
							"failed to discover daemon pid within %s: %w", deadline, lastErr))
					}
					s.services.queueMu.Unlock()
					return
				}
				continue
			}

			s.services.queueMu.Lock()
			if !polling() {
				s.services.queueMu.Unlock()
				return
			}
			// Following, the file is stale or names a process that has
			// not started yet; in a smooth recovery it may still be the
			// one the dead daemon left.
			stale := result == process.PIDResultTerminated &&
				(s.followPID != "" || (pollState == StateStarted && pid == s.lastDaemonPID))
			switch {
			case stale:
				// Keep waiting rather than failing on a leftover file.
				if time.Now().After(expiry) {
					if polling() {
						s.failPIDDiscoveryLocked(fmt.Errorf(
							"pid file %s still names a process that is not running (PID %d) after %s",
							pidFile, pid, deadline))
					}
					s.services.queueMu.Unlock()
					return
				}
				s.services.queueMu.Unlock()
				continue
			case result == process.PIDResultTerminated && pollState == StateStarted:
				s.recoveredDaemonDiedLocked(pid)
			case result == process.PIDResultTerminated:
				s.failPIDDiscoveryLocked(fmt.Errorf("daemon (PID %d) already terminated", pid))
			default:
				s.finishPIDDiscoveryLocked(pid)
			}
			s.services.queueMu.Unlock()
			return
		}
	}()
}

// bringDownForkedDaemonLocked adopts the daemon an interrupted launcher
// forked, only to bring it down: its end, seen by monitorDaemon, ends the
// stop. Caller holds queueMu.
func (s *BGProcessService) bringDownForkedDaemonLocked(pid int) {
	s.daemonPID = pid
	s.publishPID()
	go s.monitorDaemon()
	s.BringDown()
	s.services.processQueuesLocked()
}

// pollForForkedDaemon is pollForPIDFile for a STOPPING service: the
// launcher gave up on an interrupted start but may have forked the daemon
// first, whose pid file is not written yet. Reading it once and calling
// the stop done left that daemon running with nothing supervising it.
// Wait for the file as a start would, but no longer than the stop-timeout
// armed by the interrupt, so the stop cannot hang on a file that never
// comes.
//
// A file that names the daemon which last died is the one it left behind,
// not one the new daemon has written: keep waiting on it as on an absent
// file (pollForPIDFile does the same in a smooth recovery).
func (s *BGProcessService) pollForForkedDaemon() {
	stalePID := s.lastDaemonPID
	deadline := s.startTimeout
	if deadline <= 0 {
		deadline = defaultStartTimeout
	}
	expiry := time.Now().Add(deadline)
	if !s.interruptedStopBy.IsZero() && s.interruptedStopBy.Before(expiry) {
		expiry = s.interruptedStopBy
	}
	pidFile := s.pidFile

	go func() {
		ticker := time.NewTicker(pidFilePollInterval)
		defer ticker.Stop()
		for {
			<-ticker.C
			if s.state.Load() != StateStopping {
				return
			}
			pid, result, err := process.ReadPIDFile(pidFile)
			pending := (result == process.PIDResultFailed && pidFileStillComing(err)) ||
				(result == process.PIDResultTerminated && stalePID > 0 && pid == stalePID)
			if pending && time.Now().Before(expiry) {
				continue
			}

			s.services.queueMu.Lock()
			// Still this stop, and nothing adopted meanwhile.
			if s.state.Load() == StateStopping && s.launcherPID == 0 && s.daemonPID == 0 {
				if result == process.PIDResultOK {
					s.bringDownForkedDaemonLocked(pid)
				} else {
					s.Stopped()
					s.services.processQueuesLocked()
				}
			}
			s.services.queueMu.Unlock()
			return
		}
	}()
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
		timer := s.getTimer()
		select {
		case <-ticker.C:
			if s.daemonPID <= 0 {
				s.handleDaemonTermination()
				return
			}
			err := syscall.Kill(s.daemonPID, 0)
			if err != nil || procIsZombie(s.daemonPID) {
				// Process is gone — or exited and waiting to be reaped
				// by its parent (not us: a daemon is reparented away),
				// which kill(pid, 0) cannot tell from running.
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

		case <-timerC(timer):
			s.handleTimerExpired(timer)

		case <-s.timerUpdateCh:
			continue

		case <-s.doneCh:
			return
		}
	}
}

// handleDaemonTermination handles when the daemon process disappears.
// Runs in the monitorDaemon goroutine; acquires queueMu.
func (s *BGProcessService) handleDaemonTermination() {
	s.services.queueMu.Lock()
	defer s.services.queueMu.Unlock()

	// Log severity follows expectation: if we initiated the stop
	// (state == StateStopping), the daemon dying IS the success
	// case — operators should not see ERROR during a clean shutdown.
	// Anything else is a genuine surprise (crash, OOM, kernel kill).
	state := s.state.Load()
	if state == StateStopping {
		s.services.logger.Info("Service '%s': daemon process %d terminated",
			s.serviceName, s.daemonPID)
	} else {
		s.services.logger.Error("Service '%s': daemon process %d terminated",
			s.serviceName, s.daemonPID)
	}

	// Clear utmp entry
	if s.HasUtmp() {
		s.services.utmpClear(s.inittabID, s.inittabLine)
	}

	// The daemon was never our child, so there is no wait status for it —
	// and exitStatus still holds the LAUNCHER's, which exited 0 to report
	// that it had forked the daemon successfully. Leaving that in place is
	// what stopped `restart = on-failure` from ever firing here: the policy
	// asked whether the exit was signalled (no, it exited) or non-zero (no,
	// it was 0) and concluded nothing had gone wrong. Replace it with what
	// is actually true.
	s.exitStatus = ExitStatus{Vanished: true}

	s.lastDaemonPID = s.daemonPID
	s.daemonPID = 0
	s.publishPID()
	s.cancelTimer()

	switch state {
	case StateStopping:
		// A stop-command still running is commonly still cleaning up
		// after killing the daemon: its exit finishes the stop, as for
		// ProcessService (dinit: stopped() only if stop_pid == -1).
		if s.stopPID > 0 {
			s.daemonGoneDuringStop = true
			s.services.logger.Info(
				"Service '%s': daemon gone, waiting for stop-command (pid %d)",
				s.serviceName, s.stopPID)
			s.services.processQueuesLocked()
			return
		}
		// Waiting for a hard dependent: as for a process service, do
		// not go STOPPED underneath it (see dispatchAfterExitLocked).
		if s.waitingForDeps {
			if s.desired.Load() == StateStarted && !s.pinnedStopped && !s.restartLimitExhausted {
				s.stopReason = ReasonTerminated
				s.Stopped()
			}
			s.services.processQueuesLocked()
			return
		}
		s.stopIssued = false
		s.Stopped()
		s.services.processQueuesLocked()

	case StateStarted:
		s.daemonDiedLocked()
	}
}

// daemonDiedLocked handles the death of a STARTED service's daemon: a
// smooth recovery where allowed, otherwise an unexpected termination.
// Caller must hold queueMu.
func (s *BGProcessService) daemonDiedLocked() {
	if s.smoothRecovery && s.CheckRestart() {
		s.doingSmoothRecov = true
		s.doSmoothRecovery()
	} else {
		s.handleUnexpectedTerminationLocked()
	}
}

// handleUnexpectedTerminationLocked handles when a started daemon dies
// unexpectedly. Caller must hold queueMu.
func (s *BGProcessService) handleUnexpectedTerminationLocked() {
	s.stopReason = ReasonTerminated
	s.forceStop = true

	s.doStop(false)
	s.services.processQueuesLocked()

	// Waiting for a hard dependent: restart through Stopped(), as dinit
	// does (see ProcessService.handleTerminationLocked).
	if s.state.Load() == StateStopping && s.waitingForDeps &&
		s.desired.Load() == StateStarted && !s.pinnedStopped && !s.restartLimitExhausted {
		s.Stopped()
		s.services.processQueuesLocked()
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
			s.handleUnexpectedTerminationLocked()
		} else {
			s.doingSmoothRecov = false
		}
	} else {
		delay := effectiveDelay - elapsed
		var t *time.Timer
		t = time.AfterFunc(delay, func() {
			s.services.queueMu.Lock()
			defer s.services.queueMu.Unlock()
			s.fireSmoothRecoveryLocked(t)
		})
		s.smoothRecovTimer = t
	}
}

// fireSmoothRecoveryLocked runs when the timer armed by doSmoothRecovery
// fires. It acts only if t is still the armed timer and the service is
// still STARTED with no process: a stop or a fresh start in the meantime
// owns the service now. Caller must hold queueMu — t is read under it.
func (s *BGProcessService) fireSmoothRecoveryLocked(t *time.Timer) {
	if s.smoothRecovTimer != t {
		return
	}
	s.smoothRecovTimer = nil
	if !s.doingSmoothRecov || s.state.Load() != StateStarted ||
		s.launcherPID > 0 || s.daemonPID > 0 {
		s.doingSmoothRecov = false
		return
	}
	s.doingSmoothRecov = false
	if !s.self.BringUp() {
		s.handleUnexpectedTerminationLocked()
		return
	}
	s.services.processQueuesLocked()
}

// cancelSmoothRecovery disarms a smooth-recovery relaunch still waiting
// out restart-delay.
func (s *BGProcessService) cancelSmoothRecovery() {
	if s.smoothRecovTimer != nil {
		s.smoothRecovTimer.Stop()
		s.smoothRecovTimer = nil
		s.doingSmoothRecov = false
	}
}

// handleTimerExpired processes a timer expiration.
// Runs in a monitor goroutine; acquires queueMu.
func (s *BGProcessService) handleTimerExpired(timer *time.Timer) {
	s.services.queueMu.Lock()
	defer s.services.queueMu.Unlock()

	// A tick received just before a stop or restart re-armed the timer
	// belongs to the timer it replaced: acting on it would give the new
	// purpose its deadline early (a stop escalated to SIGKILL at once).
	if timer != s.processTimer {
		return
	}

	purpose := s.timerPurpose
	s.timerPurpose = bgTimerNone

	switch purpose {
	case bgTimerStartTimeout:
		pid := s.launcherPID
		if pid <= 0 {
			pid = s.daemonPID
		}
		if pid > 0 {
			if s.state.Load() == StateStarted {
				// A smooth recovery whose launcher overran: the service
				// is STARTED, so the recovery failed, not a start.
				s.services.logger.Error("Service '%s': start timeout exceeded during smooth recovery",
					s.serviceName)
				s.handleUnexpectedTerminationLocked()
				return
			}
			s.services.logger.Error("Service '%s': start timeout exceeded, sending SIGINT",
				s.serviceName)
			process.SignalProcess(pid, syscall.SIGINT, false)
			s.stopReason = ReasonTimedOut
			s.stopInterruptedStart()
			s.failedToStart(false, false)
			s.services.processQueuesLocked()
		}

	case bgTimerStopTimeout:
		pid := s.daemonPID
		if pid <= 0 {
			// Still the launcher: an interrupted start it ignored.
			pid = s.launcherPID
		}
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

// getTimer returns the armed timer, or nil. The monitor hands it to
// handleTimerExpired with its tick.
func (s *BGProcessService) getTimer() *time.Timer {
	s.services.queueMu.RLock()
	defer s.services.queueMu.RUnlock()
	return s.processTimer
}

// readProcStartTime reads field 22 (starttime) from /proc/PID/stat.
// This value is the process start time in clock ticks since boot and is
// unique enough (combined with PID) to detect PID recycling.
// Returns "" on any error.
// procIsZombie reports whether pid has exited but not been reaped yet
// (state Z in /proc/PID/stat).
func procIsZombie(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// pid (comm) state ...: comm may contain ')', so use the last one.
	idx := bytes.LastIndexByte(data, ')')
	return idx >= 0 && idx+2 < len(data) && data[idx+2] == 'Z'
}

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

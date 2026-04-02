package service

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
)

const (
	defaultStopTimeout      = 10 * time.Second
	defaultStartTimeout     = 60 * time.Second
	defaultRestartDelay     = 200 * time.Millisecond
	defaultRestartInterval  = 10 * time.Second
	defaultMaxRestarts      = 3
	defaultFinishTimeout    = 5 * time.Second
	defaultReadyCheckInterval = time.Second
)

// ProcessService manages a long-running process.
type ProcessService struct {
	ServiceRecord

	// Command configuration
	command            []string
	stopCommand        []string
	finishCommand      []string      // runs after process exits (before restart decision)
	readyCheckCommand  []string      // polls to verify service readiness
	readyCheckInterval time.Duration // polling interval (default 1s)
	preStopHook        []string      // runs before SIGTERM in BringDown
	controlCommands    map[string][]string // signal name → custom command
	workingDir         string
	envFile            string
	envDir             string // directory with one file per env var
	chroot             string // chroot before exec
	lockFile           string // exclusive flock path
	newSession         bool   // setsid() before exec
	closeStdin         bool   // close fd 0
	closeStdout        bool   // close fd 1
	closeStderr        bool   // close fd 2

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
	paused           bool // true when service is SIGSTOP'd (pause/continue)

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
	logRotator   *LogRotator // manages rotation, filtering, processing

	// Log rotation/filtering config
	logMaxSize    int64
	logMaxFiles   int
	logRotateTime time.Duration
	logProcessor  []string
	logIncludes   []string
	logExcludes   []string

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

// SetFinishCommand sets the finish command (runs after process exits).
func (s *ProcessService) SetFinishCommand(cmd []string) { s.finishCommand = cmd }

// SetReadyCheckCommand sets the ready-check command and optional interval.
func (s *ProcessService) SetReadyCheckCommand(cmd []string, interval time.Duration) {
	s.readyCheckCommand = cmd
	if interval > 0 {
		s.readyCheckInterval = interval
	} else {
		s.readyCheckInterval = time.Second
	}
}

// SetPreStopHook sets the pre-stop hook command.
func (s *ProcessService) SetPreStopHook(cmd []string) { s.preStopHook = cmd }

// SetEnvDir sets the environment directory path.
func (s *ProcessService) SetEnvDir(dir string) { s.envDir = dir }

// SetControlCommands sets the custom signal handler commands.
func (s *ProcessService) SetControlCommands(cmds map[string][]string) { s.controlCommands = cmds }

// SetChroot sets the chroot directory.
func (s *ProcessService) SetChroot(dir string) { s.chroot = dir }

// SetLockFile sets the exclusive lock file path.
func (s *ProcessService) SetLockFile(path string) { s.lockFile = path }

// SetNewSession enables setsid() for the child process.
func (s *ProcessService) SetNewSession(v bool) { s.newSession = v }

// SetCloseFDs sets which standard file descriptors to close.
func (s *ProcessService) SetCloseFDs(stdin, stdout, stderr bool) {
	s.closeStdin = stdin
	s.closeStdout = stdout
	s.closeStderr = stderr
}

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

// SetLogRotation configures log rotation parameters.
func (s *ProcessService) SetLogRotation(maxSize int64, maxFiles int, rotateTime time.Duration) {
	s.logMaxSize = maxSize
	s.logMaxFiles = maxFiles
	s.logRotateTime = rotateTime
}

// SetLogProcessor sets the log processor command.
func (s *ProcessService) SetLogProcessor(cmd []string) { s.logProcessor = cmd }

// SetLogFilters sets log include/exclude patterns.
func (s *ProcessService) SetLogFilters(includes, excludes []string) {
	s.logIncludes = includes
	s.logExcludes = excludes
}

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
// If a pre-stop-hook is configured, it runs first (synchronously with timeout).
// Then if a stop-command is configured, it is executed. If it fails to
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

	// Run pre-stop-hook before any stop action
	if len(s.preStopHook) > 0 {
		s.execPreStopHook()
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

	// Send termination signal (or run control-command if configured)
	sig := s.termSignal
	if sig == 0 {
		sig = syscall.SIGTERM
	}

	sigName := signalName(sig)
	if cmd, ok := s.controlCommands[sigName]; ok && len(cmd) > 0 {
		// Run custom control command instead of raw signal
		s.execControlCommand(sigName, cmd)
	} else {
		s.services.logger.Info("Service '%s': sending %v to process %d",
			s.serviceName, sig, s.pid)

		err := process.SignalProcess(s.pid, sig, s.Flags.SignalProcessOnly)
		if err != nil {
			s.services.logger.Error("Service '%s': failed to signal process: %v",
				s.serviceName, err)
		}
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
			// Stop-command succeeded — now send term signal to main process
			if s.pid > 0 {
				sig := s.termSignal
				if sig == 0 {
					sig = syscall.SIGTERM
				}
				process.SignalProcess(s.pid, sig, s.Flags.SignalProcessOnly)
			}
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

// buildEnv merges env-file, env-dir, and runtime extraEnv into a single slice.
func (s *ProcessService) buildEnv() []string {
	env := s.Record().BuildEnvWithFile(s.envFile)

	// Merge env-dir variables (runit-style: one file per variable)
	if s.envDir != "" {
		dirEnv, err := process.ReadEnvDir(s.envDir)
		if err != nil {
			s.services.logger.Error("Service '%s': failed to read env-dir '%s': %v",
				s.serviceName, s.envDir, err)
		} else if len(dirEnv) > 0 {
			// Apply env-dir on top of existing env
			for k, v := range dirEnv {
				if v == "" {
					// Empty value = unset: remove from env
					for i := len(env) - 1; i >= 0; i-- {
						if strings.HasPrefix(env[i], k+"=") {
							env = append(env[:i], env[i+1:]...)
							break
						}
					}
				} else {
					// Set or override
					found := false
					entry := k + "=" + v
					for i, e := range env {
						if strings.HasPrefix(e, k+"=") {
							env[i] = entry
							found = true
							break
						}
					}
					if !found {
						env = append(env, entry)
					}
				}
			}
		}
	}
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
		// Use LogRotator if rotation, filtering, or processing is configured
		if s.logMaxSize > 0 || s.logMaxFiles > 0 || s.logRotateTime > 0 ||
			len(s.logProcessor) > 0 || len(s.logIncludes) > 0 || len(s.logExcludes) > 0 {
			if s.logRotator != nil {
				s.logRotator.Close()
			}
			var err error
			s.logRotator, err = NewLogRotator(LogRotatorConfig{
				FilePath:    s.logFile,
				FilePerms:   os.FileMode(s.logFilePerms),
				FileUID:     s.logFileUID,
				FileGID:     s.logFileGID,
				MaxSize:     s.logMaxSize,
				MaxFiles:    s.logMaxFiles,
				RotateTime:  s.logRotateTime,
				Processor:   s.logProcessor,
				Includes:    s.logIncludes,
				Excludes:    s.logExcludes,
				ServiceName: s.serviceName,
				Logger:      s.services.logger,
			})
			if err != nil {
				return fmt.Errorf("failed to create log rotator: %w", err)
			}
			var pipeErr error
			outputPipe, pipeErr = s.logRotator.CreatePipe()
			if pipeErr != nil {
				return fmt.Errorf("failed to create log rotator pipe: %w", pipeErr)
			}
		} else {
			f, err := os.OpenFile(s.logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, os.FileMode(s.logFilePerms))
			if err != nil {
				return fmt.Errorf("failed to open logfile '%s': %w", s.logFile, err)
			}
			if s.logFileUID >= 0 || s.logFileGID >= 0 {
				_ = os.Chown(s.logFile, s.logFileUID, s.logFileGID)
			}
			outputPipe = f
		}
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
		Chroot:            s.chroot,
		NewSession:        s.newSession,
		LockFile:          s.lockFile,
		CloseStdin:        s.closeStdin,
		CloseStdout:       s.closeStdout,
		CloseStderr:       s.closeStderr,
	}
	s.Record().ApplyProcessAttrs(&params)

	pid, exitCh, err := process.StartProcess(params)
	if err != nil {
		if outputPipe != nil && s.logType == LogToBuffer {
			s.logBuf.CloseWriteEnd()
		} else if outputPipe != nil && s.logType == LogToFile {
			if s.logRotator != nil {
				s.logRotator.Close()
				s.logRotator = nil
			} else {
				outputPipe.Close()
			}
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
		if s.logRotator != nil {
			s.logRotator.CloseWriteEnd()
			s.logRotator.StartReader()
		} else {
			outputPipe.Close()
		}
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
	} else if len(s.readyCheckCommand) > 0 {
		// Ready-check-command: poll external command until it succeeds
		s.readyCh = make(chan bool, 1)
		go s.watchReadyCheck()
		go s.monitorProcess(exitCh)

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

	// Run finish-command if configured (before restart decision)
	if len(s.finishCommand) > 0 && exit.ExecErr == nil {
		s.execFinishCommand(exit)
	}

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

// execFinishCommand runs the finish-command after a process exits.
// It passes the exit code and signal number as arguments.
// Runs synchronously with a timeout so it doesn't block forever.
func (s *ProcessService) execFinishCommand(exit process.ChildExit) {
	exitCode := "-1"
	waitStatus := "0"
	if exit.Exited() {
		exitCode = strconv.Itoa(exit.Status.ExitStatus())
	}
	if exit.Signaled() {
		waitStatus = strconv.Itoa(int(exit.Status.Signal()))
	}

	args := make([]string, len(s.finishCommand)-1, len(s.finishCommand)+1)
	copy(args, s.finishCommand[1:])
	args = append(args, exitCode, waitStatus)

	ctx, cancel := context.WithTimeout(context.Background(), defaultFinishTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, s.finishCommand[0], args...)
	cmd.Dir = s.workingDir
	cmd.Env = s.buildEnv()

	s.services.logger.Info("Service '%s': running finish-command", s.serviceName)
	if err := cmd.Run(); err != nil {
		s.services.logger.Error("Service '%s': finish-command failed: %v",
			s.serviceName, err)
	}
}

// watchReadyCheck polls the ready-check-command until it succeeds or times out.
// Sends true on readyCh when the command exits 0, false if the doneCh is closed.
func (s *ProcessService) watchReadyCheck() {
	interval := s.readyCheckInterval
	if interval <= 0 {
		interval = defaultReadyCheckInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.doneCh:
			s.readyCh <- false
			return
		case <-ticker.C:
			cmd := exec.Command(s.readyCheckCommand[0], s.readyCheckCommand[1:]...)
			cmd.Dir = s.workingDir
			cmd.Env = s.buildEnv()
			if err := cmd.Run(); err == nil {
				s.readyCh <- true
				return
			}
		}
	}
}

// execPreStopHook runs the pre-stop-hook before sending stop signal.
// Runs synchronously with a timeout. The hook receives the service PID
// as first argument (like runit's control scripts).
func (s *ProcessService) execPreStopHook() {
	args := make([]string, len(s.preStopHook)-1, len(s.preStopHook))
	copy(args, s.preStopHook[1:])
	args = append(args, strconv.Itoa(s.pid))

	ctx, cancel := context.WithTimeout(context.Background(), defaultFinishTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, s.preStopHook[0], args...)
	cmd.Dir = s.workingDir
	cmd.Env = s.buildEnv()

	s.services.logger.Info("Service '%s': running pre-stop-hook", s.serviceName)
	if err := cmd.Run(); err != nil {
		s.services.logger.Error("Service '%s': pre-stop-hook failed: %v",
			s.serviceName, err)
	}
}

// execControlCommand runs a custom control command for a signal.
// The command receives the service PID as an appended argument.
// Runs synchronously with a timeout.
func (s *ProcessService) execControlCommand(sigName string, command []string) {
	args := make([]string, len(command)-1, len(command))
	copy(args, command[1:])
	args = append(args, strconv.Itoa(s.pid))

	ctx, cancel := context.WithTimeout(context.Background(), defaultFinishTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, command[0], args...)
	cmd.Dir = s.workingDir
	cmd.Env = s.buildEnv()

	s.services.logger.Info("Service '%s': running control-command-%s", s.serviceName, sigName)
	if err := cmd.Run(); err != nil {
		s.services.logger.Error("Service '%s': control-command-%s failed: %v",
			s.serviceName, sigName, err)
	}
}

// signalName returns the uppercase name of a signal (e.g., "TERM", "HUP").
func signalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGTERM:
		return "TERM"
	case syscall.SIGHUP:
		return "HUP"
	case syscall.SIGINT:
		return "INT"
	case syscall.SIGQUIT:
		return "QUIT"
	case syscall.SIGKILL:
		return "KILL"
	case syscall.SIGUSR1:
		return "USR1"
	case syscall.SIGUSR2:
		return "USR2"
	case syscall.SIGALRM:
		return "ALRM"
	case syscall.SIGSTOP:
		return "STOP"
	case syscall.SIGCONT:
		return "CONT"
	default:
		return strconv.Itoa(int(sig))
	}
}

// SendSignalWithControl sends a signal to the service, using control-command if configured.
// Returns true if signal was sent (or control command ran).
func (s *ProcessService) SendSignalWithControl(sig syscall.Signal) bool {
	if s.pid <= 0 {
		return false
	}
	sigName := signalName(sig)
	if cmd, ok := s.controlCommands[sigName]; ok && len(cmd) > 0 {
		s.execControlCommand(sigName, cmd)
		return true
	}
	err := process.SignalProcess(s.pid, sig, s.Flags.SignalProcessOnly)
	return err == nil
}

// Pause suspends the service process with SIGSTOP (or control-command-STOP).
func (s *ProcessService) Pause() bool {
	if s.pid <= 0 || s.paused {
		return false
	}
	if cmd, ok := s.controlCommands["STOP"]; ok && len(cmd) > 0 {
		s.execControlCommand("STOP", cmd)
	} else {
		if err := process.SignalProcess(s.pid, syscall.SIGSTOP, s.Flags.SignalProcessOnly); err != nil {
			s.services.logger.Error("Service '%s': SIGSTOP failed: %v", s.serviceName, err)
			return false
		}
	}
	s.paused = true
	s.services.logger.Info("Service '%s': paused", s.serviceName)
	return true
}

// Continue resumes a paused service process with SIGCONT (or control-command-CONT).
func (s *ProcessService) Continue() bool {
	if s.pid <= 0 || !s.paused {
		return false
	}
	if cmd, ok := s.controlCommands["CONT"]; ok && len(cmd) > 0 {
		s.execControlCommand("CONT", cmd)
	} else {
		if err := process.SignalProcess(s.pid, syscall.SIGCONT, s.Flags.SignalProcessOnly); err != nil {
			s.services.logger.Error("Service '%s': SIGCONT failed: %v", s.serviceName, err)
			return false
		}
	}
	s.paused = false
	s.services.logger.Info("Service '%s': continued", s.serviceName)
	return true
}

// IsPaused returns whether the service is currently paused.
func (s *ProcessService) IsPaused() bool { return s.paused }

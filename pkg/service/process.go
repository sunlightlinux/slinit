package service

import (
	"context"
	"errors"
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

	// Progressive restart backoff (OpenRC-compatible, linear additive)
	restartDelayStep    time.Duration // increment added per successive restart (0 = disabled)
	restartDelayCap     time.Duration // max capped delay (0 = no cap, default 60s when step > 0)
	currentRestartDelay time.Duration // current effective delay, advances on each restart

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
	socketFD  *os.File   // primary listening socket (fd 3, nil if no socket-listen)
	socketFDs []*os.File // additional sockets (fd 4, 5, ... for multiple socket-listen)
	socketOnDemand bool  // start service on first connection (socket-activation = on-demand)
	socketDemandStop chan struct{} // signal to stop on-demand watcher
	socketDemandDone chan struct{} // closed when watcher goroutine exits
	socketDemandLn   net.Listener  // listener owned by watcher; closed to break Accept

	// Readiness notification
	readyNotifyFD  int      // fd number child writes to (-1 if none)
	readyNotifyVar string   // env var name ("" if none)
	readyPipeRead  *os.File // read-end of notification pipe (parent watches)
	readyCh        chan bool // receives true=ready, false=EOF/error

	// Service-level watchdog. Piggybacks on the ready-notification pipe:
	// the first message marks the service ready, subsequent writes act
	// as keepalives that reset the watchdog timer. A miss declares the
	// service unhealthy and triggers Stop(false) — the existing
	// restart-on-failure path takes it from there.
	watchdogTimeout time.Duration
	watchdogStop    chan struct{} // closed to terminate the watcher goroutine
	watchdogDone    chan struct{} // closed when the watcher goroutine returns

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

	// Output/error logger commands (OpenRC OUTPUT_LOGGER / ERROR_LOGGER)
	// When set, stdout (and stderr unless errorLogger is set) is piped
	// to the output-logger command. If errorLogger is set, stderr is
	// piped to it separately.
	outputLogger []string   // command + args for stdout logger
	errorLogger  []string   // command + args for stderr logger (optional)
	loggerCmd    *exec.Cmd  // running output-logger process
	errLoggerCmd *exec.Cmd  // running error-logger process

	// Cron-like periodic task
	cronRunner *CronRunner

	// Continuous health checking (post-STARTED)
	healthChecker *HealthChecker

	// Virtual TTY (screen-like attach/detach)
	vtty           *VirtualTTY
	vttyEnabled    bool
	vttyScrollback int
	vttySockDir    string

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
// SetSocketOnDemand enables on-demand socket activation (lazy start).
func (s *ProcessService) SetSocketOnDemand(v bool) { s.socketOnDemand = v }

func (s *ProcessService) SetCloseFDs(stdin, stdout, stderr bool) {
	s.closeStdin = stdin
	s.closeStdout = stdout
	s.closeStderr = stderr
}

// SetVTTY enables virtual TTY for this service.
func (s *ProcessService) SetVTTY(enabled bool, scrollback int, sockDir string) {
	s.vttyEnabled = enabled
	s.vttyScrollback = scrollback
	s.vttySockDir = sockDir
}

// VTTY returns the virtual TTY instance, or nil if not configured.
func (s *ProcessService) VTTY() *VirtualTTY { return s.vtty }

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

// SetRestartBackoff configures progressive (linear additive) restart backoff.
// step > 0 enables the feature: each successive restart adds `step` to the
// delay, capped at `cap` (or 60s if cap is 0). The backoff resets when the
// service has been running stably for longer than restartInterval.
func (s *ProcessService) SetRestartBackoff(step, cap time.Duration) {
	s.restartDelayStep = step
	s.restartDelayCap = cap
}

// nextRestartDelay returns the delay to use for the next restart and advances
// the progressive backoff counter. When step <= 0, always returns restartDelay.
func (s *ProcessService) nextRestartDelay() time.Duration {
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

// SetOutputLogger sets the output-logger command (OpenRC OUTPUT_LOGGER).
// When configured, stdout (and stderr unless an error-logger is set) is
// piped to this external command.
func (s *ProcessService) SetOutputLogger(cmd []string) { s.outputLogger = cmd }

// SetErrorLogger sets the error-logger command (OpenRC ERROR_LOGGER).
// When configured, stderr is piped to this command separately from stdout.
func (s *ProcessService) SetErrorLogger(cmd []string) { s.errorLogger = cmd }

// SetCronConfig configures the periodic cron task.
func (s *ProcessService) SetCronConfig(cmd []string, interval, delay time.Duration, onError string) {
	s.cronRunner = NewCronRunner(s, cmd, interval, delay, onError, s.services.logger)
}

// SetHealthCheck configures the continuous health checker.
func (s *ProcessService) SetHealthCheck(cmd []string, interval, delay time.Duration,
	maxFailures int, unhealthyCmd []string) {
	onFail := func() {
		// Trigger a restart by sending SIGTERM to the process.
		// Auto-restart policy will handle the restart.
		s.services.logger.Info("Service '%s': health check triggering restart", s.serviceName)
		s.Stop(false)
	}
	s.healthChecker = NewHealthChecker(s, cmd, interval, delay, maxFailures, unhealthyCmd,
		s.services.logger, onFail)
}

// startHealthCheckIfConfigured starts the health checker if configured.
func (s *ProcessService) startHealthCheckIfConfigured() {
	if s.healthChecker != nil {
		s.services.logger.Info("Service '%s': starting health check (interval=%v, max-failures=%d)",
			s.serviceName, s.healthChecker.interval, s.healthChecker.maxFailures)
		s.healthChecker.Start()
	}
}

// stopHealthChecker stops the health checker if active.
func (s *ProcessService) stopHealthChecker() {
	if s.healthChecker != nil {
		s.healthChecker.Stop()
	}
}

// startWatchdogWatcher launches the per-service watchdog goroutine after
// the initial readiness signal. The goroutine reads from the same pipe
// that delivered the readiness byte; every subsequent read is treated
// as a keepalive that resets the deadline. If the deadline expires
// without a read, the service is stopped and the configured restart
// policy takes over.
//
// Caller must hold queueMu.
func (s *ProcessService) startWatchdogWatcher() {
	if s.watchdogTimeout <= 0 || s.readyPipeRead == nil {
		return
	}
	if s.watchdogStop != nil {
		// Already running — defensive; should not normally happen because
		// handleReadyNotification fires exactly once per start cycle.
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	pipe := s.readyPipeRead
	timeout := s.watchdogTimeout
	s.watchdogStop = stop
	s.watchdogDone = done

	s.services.logger.Info("Service '%s': watchdog armed (timeout=%v)",
		s.serviceName, timeout)
	go s.watchdogLoop(pipe, timeout, stop, done)
}

// watchdogLoop blocks on pipe reads with a deadline equal to the
// watchdog timeout. It returns when:
//   - the deadline elapses → watchdog miss → Stop(false)
//   - the pipe is closed by the service → also a miss (the service
//     voluntarily disarmed itself, which a telco-grade init must not
//     allow silently — restart and surface)
//   - stop is closed → BringDown / handleChildExit asked us to quit
//     (the child is gone or being shut down explicitly; the regular
//     paths handle that case)
func (s *ProcessService) watchdogLoop(pipe *os.File, timeout time.Duration,
	stop, done chan struct{},
) {
	defer close(done)

	buf := make([]byte, 128)
	for {
		select {
		case <-stop:
			return
		default:
		}

		if err := pipe.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			// Non-pollable fd. Without deadlines we cannot enforce the
			// watchdog; bail loudly so the operator notices.
			s.services.logger.Error("Service '%s': watchdog disabled: %v",
				s.serviceName, err)
			return
		}

		n, err := pipe.Read(buf)
		// Cancellation racing with the read? Treat as clean shutdown.
		select {
		case <-stop:
			return
		default:
		}

		switch {
		case err == nil && n > 0:
			// Keepalive received; loop resets the deadline.
			continue
		case isDeadlineExceeded(err):
			s.services.logger.Error("Service '%s': watchdog timeout (%v) — restarting",
				s.serviceName, timeout)
			s.fireWatchdogStop()
			return
		default:
			// EOF or other read error. The service either died (in
			// which case handleChildExit will close stop shortly and
			// our select above already covered that) or closed its
			// end of the watchdog pipe while still running. Either
			// way, the watchdog cannot continue — escalate to Stop.
			s.services.logger.Error("Service '%s': watchdog pipe lost (%v) — restarting",
				s.serviceName, err)
			s.fireWatchdogStop()
			return
		}
	}
}

// stopWatchdogWatcher signals the watchdog goroutine to terminate.
// Idempotent. Used from BringDown (caller does not hold queueMu).
func (s *ProcessService) stopWatchdogWatcher() {
	s.stopWatchdogWatcherLocked()
}

// stopWatchdogWatcherLocked is the queueMu-held variant. It sets the
// pipe deadline to "now" so the watcher's blocked Read returns
// immediately, then drops the channel references. We do NOT wait for
// the goroutine to exit: the caller may itself be running under
// queueMu (e.g. handleChildExit), and the watcher's Stop(false) path
// would re-enter that lock. The goroutine is short-lived once the
// pipe is closed elsewhere, so leaking it briefly is harmless.
func (s *ProcessService) stopWatchdogWatcherLocked() {
	if s.watchdogStop == nil {
		return
	}
	select {
	case <-s.watchdogStop:
		// already closed
	default:
		close(s.watchdogStop)
	}
	if s.readyPipeRead != nil {
		// Push the deadline into the past to unblock any in-flight Read.
		_ = s.readyPipeRead.SetReadDeadline(time.Unix(1, 0))
	}
	s.watchdogStop = nil
	s.watchdogDone = nil
}

// isDeadlineExceeded matches both os.ErrDeadlineExceeded and the
// underlying syscall error wrappers Go uses on different kernels.
func isDeadlineExceeded(err error) bool {
	return errors.Is(err, os.ErrDeadlineExceeded)
}

// fireWatchdogStop is the watchdog goroutine's path into the state
// machine. A watchdog miss is treated as a service failure: force-kill
// the still-running child and let the configured restart policy take
// over.
//
// We don't go through Stop(false): when requiredBy is 0 (e.g. soft
// waits-for activation) Stop() clobbers desired=StateStopped, killing
// any chance of restart in Stopped(). doStop(false) is also wrong here
// — it inspects exitStatus to decide on auto-restart, but the child
// hasn't exited yet so the policy check sees a zero/empty status and
// declines to restart, then Release() drops requiredBy to 0 and clears
// desired anyway. Instead, evaluate the restart policy ourselves and
// pass the result as withRestart so doStop preserves desired and skips
// the Release path. Once SIGTERM kills the child, handleChildExit →
// Stopped() sees desired==Started and calls initiateStart().
func (s *ProcessService) fireWatchdogStop() {
	s.services.queueMu.Lock()
	defer s.services.queueMu.Unlock()

	s.stopReason = ReasonTerminated
	s.forceStop = true

	withRestart := false
	switch s.autoRestart {
	case RestartAlways, RestartOnFailure:
		withRestart = s.CheckRestart()
	}

	s.doStop(withRestart)
	s.services.processQueuesLocked()
}

// startCronIfConfigured starts the cron runner if a cron-command is configured.
func (s *ProcessService) startCronIfConfigured() {
	if s.cronRunner != nil {
		s.services.logger.Info("Service '%s': starting cron task (interval=%v)", s.serviceName, s.cronRunner.interval)
		s.cronRunner.Start()
	}
}

// stopCronRunner stops the cron runner if active.
func (s *ProcessService) stopCronRunner() {
	if s.cronRunner != nil {
		s.cronRunner.Stop()
	}
}

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

// SetWatchdogTimeout configures the per-service watchdog. The service
// must keep writing to the ready-notification pipe at least once per
// timeout window or slinit declares it unhealthy and stops it (which
// triggers the configured restart policy). A zero value disables the
// watchdog. The loader rejects watchdog-timeout without ready-notification.
func (s *ProcessService) SetWatchdogTimeout(d time.Duration) {
	s.watchdogTimeout = d
}

// HasWatchdog returns true if a service-level watchdog is configured.
func (s *ProcessService) HasWatchdog() bool {
	return s.watchdogTimeout > 0
}

// WatchdogTimeout returns the configured per-service watchdog timeout
// (zero when disabled).
func (s *ProcessService) WatchdogTimeout() time.Duration {
	return s.watchdogTimeout
}

// openSocket creates and binds listening sockets for socket activation.
// Supports Unix sockets (path), TCP (tcp:host:port), and UDP (udp:host:port).
// Multiple socket-listen directives result in multiple fds (LISTEN_FDS=N).
func (s *ProcessService) openSocket() error {
	paths := s.socketPaths
	if len(paths) == 0 && s.socketPath != "" {
		paths = []string{s.socketPath}
	}
	if len(paths) == 0 || s.socketFD != nil {
		return nil
	}

	for i, path := range paths {
		fd, err := s.openOneSocket(path)
		if err != nil {
			// Clean up already-opened sockets on failure
			s.closeSocket()
			return fmt.Errorf("socket-listen[%d] %q: %w", i, path, err)
		}
		if i == 0 {
			s.socketFD = fd
		} else {
			s.socketFDs = append(s.socketFDs, fd)
		}
	}
	return nil
}

// openOneSocket opens a single listening socket. The path format determines
// the socket type:
//   - "tcp:host:port" or "tcp4:host:port" or "tcp6:host:port" → TCP
//   - "udp:host:port" or "udp4:host:port" or "udp6:host:port" → UDP
//   - anything else → Unix domain socket
func (s *ProcessService) openOneSocket(path string) (*os.File, error) {
	// TCP socket
	if strings.HasPrefix(path, "tcp:") || strings.HasPrefix(path, "tcp4:") || strings.HasPrefix(path, "tcp6:") {
		parts := strings.SplitN(path, ":", 2)
		network := parts[0]
		addr := parts[1]
		ln, err := net.Listen(network, addr)
		if err != nil {
			return nil, fmt.Errorf("tcp listen: %w", err)
		}
		fd, err := ln.(*net.TCPListener).File()
		if err != nil {
			ln.Close()
			return nil, fmt.Errorf("tcp fd: %w", err)
		}
		ln.Close()
		return fd, nil
	}

	// UDP socket
	if strings.HasPrefix(path, "udp:") || strings.HasPrefix(path, "udp4:") || strings.HasPrefix(path, "udp6:") {
		parts := strings.SplitN(path, ":", 2)
		network := parts[0]
		addr := parts[1]
		conn, err := net.ListenPacket(network, addr)
		if err != nil {
			return nil, fmt.Errorf("udp listen: %w", err)
		}
		fd, err := conn.(*net.UDPConn).File()
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("udp fd: %w", err)
		}
		conn.Close()
		return fd, nil
	}

	// Unix domain socket
	info, err := os.Stat(path)
	if err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("file exists and is not a socket: %s", path)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat: %w", err)
	}

	os.Remove(path) // remove stale socket

	addr := &net.UnixAddr{Name: path, Net: "unix"}
	unixListener, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, fmt.Errorf("unix listen: %w", err)
	}
	unixListener.SetUnlinkOnClose(false)

	fd, err := unixListener.File()
	if err != nil {
		unixListener.Close()
		return nil, fmt.Errorf("unix fd: %w", err)
	}
	unixListener.Close()

	// Set permissions/ownership on Unix socket
	if s.socketPerms != 0 {
		if err := os.Chmod(path, os.FileMode(s.socketPerms)); err != nil {
			fd.Close()
			return nil, fmt.Errorf("chmod: %w", err)
		}
	}
	if s.socketUID >= 0 || s.socketGID >= 0 {
		uid, gid := s.socketUID, s.socketGID
		if uid < 0 {
			uid = -1
		}
		if gid < 0 {
			gid = -1
		}
		if err := os.Chown(path, uid, gid); err != nil {
			fd.Close()
			return nil, fmt.Errorf("chown: %w", err)
		}
	}

	return fd, nil
}

// closeSocket closes all activation sockets and removes Unix socket files.
func (s *ProcessService) closeSocket() {
	if s.socketFD != nil {
		s.socketFD.Close()
		s.socketFD = nil
	}
	for _, fd := range s.socketFDs {
		fd.Close()
	}
	s.socketFDs = nil

	// Remove Unix socket files
	paths := s.socketPaths
	if len(paths) == 0 && s.socketPath != "" {
		paths = []string{s.socketPath}
	}
	for _, p := range paths {
		if !strings.Contains(p, ":") {
			os.Remove(p)
		}
	}
}

// startOnDemandWatcher starts a goroutine that watches the primary socket
// for incoming connections. On first activity, it starts the service.
// The watcher uses epoll-like polling (Accept with timeout) to detect connections.
func (s *ProcessService) startOnDemandWatcher() {
	if s.socketFD == nil || !s.socketOnDemand {
		return
	}
	s.socketDemandStop = make(chan struct{})
	s.socketDemandDone = make(chan struct{})

	// Dup the fd on the caller's goroutine so a concurrent closeSocket
	// cannot race the child's read of s.socketFD.
	fdDup, err := syscall.Dup(int(s.socketFD.Fd()))
	if err != nil {
		s.services.logger.Error("Service '%s': on-demand socket dup failed: %v", s.serviceName, err)
		close(s.socketDemandDone)
		return
	}

	f := os.NewFile(uintptr(fdDup), "on-demand-socket")
	ln, err := net.FileListener(f)
	f.Close()
	if err != nil {
		s.services.logger.Error("Service '%s': on-demand listener failed: %v", s.serviceName, err)
		close(s.socketDemandDone)
		return
	}
	s.socketDemandLn = ln

	go func() {
		defer close(s.socketDemandDone)
		defer ln.Close()

		conn, err := ln.Accept()
		if err != nil {
			// Listener closed (stop) or other error — exit.
			return
		}
		// Got a connection! Close it back (the real service will accept it
		// after being started — the connection stays in the socket backlog).
		conn.Close()

		select {
		case <-s.socketDemandStop:
			return
		default:
		}

		s.services.logger.Info("Service '%s': on-demand socket activation triggered", s.serviceName)

		// Start the service via the normal path
		s.services.StartService(s.self)
	}()
}

// stopOnDemandWatcher stops the on-demand socket watcher and waits for
// its goroutine to exit, so the caller can safely close the socket after.
func (s *ProcessService) stopOnDemandWatcher() {
	if s.socketDemandStop != nil {
		close(s.socketDemandStop)
		s.socketDemandStop = nil
		if s.socketDemandLn != nil {
			s.socketDemandLn.Close() // unblocks Accept
			s.socketDemandLn = nil
		}
		if s.socketDemandDone != nil {
			<-s.socketDemandDone
			s.socketDemandDone = nil
		}
	}
}

// BecomingInactive is called when the service won't restart. Cleans up socket.
func (s *ProcessService) BecomingInactive() {
	s.stopOnDemandWatcher()
	s.closeDoneCh()
	s.closeSocket()
	s.CloseOutputPipe()
	s.stopLoggerCommands()
	if s.vtty != nil {
		s.vtty.Close()
		s.vtty = nil
	}
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
func (s *ProcessService) PID() int {
	// pid is written under queueMu.Lock by the scheduler. We can't RLock
	// here because PID() is also called from within queueMu.Lock via
	// notifyListeners → encodeStatus5Into callbacks (RWMutex is not
	// reentrant). Relies on int reads being atomic on supported archs;
	// worst case a reader sees a stale value, never a torn one.
	return s.pid
}

// GetExitStatus returns the exit status of the last process.
func (s *ProcessService) GetExitStatus() ExitStatus { return s.exitStatus }

// killCgroupTree sends a signal to all processes in the service's cgroup.
// Used when kill-all-on-stop is set to ensure the entire process tree is terminated.
func (s *ProcessService) killCgroupTree(sig syscall.Signal) {
	cgPath := s.EffectiveCgroupPath()
	if cgPath == "" {
		return
	}
	if err := process.KillCgroup(cgPath, sig); err != nil {
		s.services.logger.Error("Service '%s': cgroup kill (%v): %v",
			s.serviceName, sig, err)
	}
}

// BringUp starts the service process.
func (s *ProcessService) BringUp() bool {
	if len(s.command) == 0 {
		s.services.logger.Error("Service '%s': no command specified", s.serviceName)
		return false
	}

	// Fail-fast pre-start check: required_files / required_dirs must exist
	// before we even attempt fork/exec. Produces a clear error instead of
	// a cryptic ENOENT crash from the child.
	if err := s.CheckRequiredPaths(); err != nil {
		s.services.logger.Error("Service '%s': %v", s.serviceName, err)
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
	// Stop health checker and cron runner if active
	s.stopHealthChecker()
	s.stopCronRunner()
	s.stopWatchdogWatcher()

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

	// Kill entire cgroup process tree if configured
	if s.Flags.KillAllOnStop {
		s.killCgroupTree(sig)
	}

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
	now := time.Now()

	if s.maxRestartCount > 0 {
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
			// New interval: stable period elapsed, reset progressive backoff
			s.restartIntervalTime = now
			s.restartIntervalCount = 1
			s.currentRestartDelay = s.restartDelay
		}
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
	} else if s.logType == LogToPipe && s.sharedLoggerName != "" {
		// Shared logger: output goes through SharedLogMux → logger's stdin
		mux := s.services.GetSharedLogMux(s.sharedLoggerName)
		if mux != nil {
			pipeW, err := mux.AddProducer(s.serviceName)
			if err != nil {
				s.services.logger.Error("Service '%s': failed to add to shared-logger '%s': %v",
					s.serviceName, s.sharedLoggerName, err)
			} else {
				outputPipe = pipeW
			}
		} else {
			s.services.logger.Error("Service '%s': shared-logger '%s' mux not found",
				s.serviceName, s.sharedLoggerName)
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
			// O_NOFOLLOW + fchown-via-fd close the symlink-swap window
			// where slinit (running as root) would otherwise write to —
			// or chown — an attacker-pointed path.
			f, err := os.OpenFile(s.logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, os.FileMode(s.logFilePerms))
			if err != nil {
				return fmt.Errorf("failed to open logfile '%s': %w", s.logFile, err)
			}
			if s.logFileUID >= 0 || s.logFileGID >= 0 {
				_ = f.Chown(s.logFileUID, s.logFileGID)
			}
			outputPipe = f
		}
	} else if s.logType == LogToCommand && len(s.outputLogger) > 0 {
		// Spawn an external logger command and pipe stdout (+ stderr unless
		// a separate error-logger is configured) to it. This is the
		// OpenRC OUTPUT_LOGGER equivalent.
		var err error
		outputPipe, s.loggerCmd, err = spawnLoggerCommand(s.outputLogger, s.serviceName, "output-logger")
		if err != nil {
			return fmt.Errorf("output-logger: %w", err)
		}
	}

	// Optional separate error-logger: stderr goes to a different command.
	var errorPipe *os.File
	if s.logType == LogToCommand && len(s.errorLogger) > 0 {
		var err error
		errorPipe, s.errLoggerCmd, err = spawnLoggerCommand(s.errorLogger, s.serviceName, "error-logger")
		if err != nil {
			// Clean up the output-logger we already started
			if s.loggerCmd != nil {
				s.loggerCmd.Process.Kill()
				s.loggerCmd.Wait()
				s.loggerCmd = nil
			}
			if outputPipe != nil {
				outputPipe.Close()
				outputPipe = nil
			}
			return fmt.Errorf("error-logger: %w", err)
		}
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
		// This service IS a shared logger — read from the mux pipe
		inputPipe = mux.InputPipe()
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

	// Set up virtual TTY if enabled
	var ptySlave string
	if s.vttyEnabled {
		if s.vtty != nil {
			s.vtty.Close()
		}
		var err error
		s.vtty, ptySlave, err = OpenVirtualTTY(s.serviceName, s.vttyScrollback, s.vttySockDir)
		if err != nil {
			s.services.logger.Error("Service '%s': failed to open vtty: %v", s.serviceName, err)
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
		ErrorPipe:         errorPipe,
		InputPipe:         inputPipe,
		PTYSlave:          ptySlave,
		SocketFD:          s.socketFD,
		ExtraSocketFDs:    s.socketFDs,
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
		if s.logType == LogToCommand {
			if outputPipe != nil {
				outputPipe.Close()
			}
			if errorPipe != nil {
				errorPipe.Close()
			}
			s.stopLoggerCommands()
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
	} else if s.logType == LogToCommand {
		// Close the write-ends in the parent — the child inherited them.
		if outputPipe != nil {
			outputPipe.Close()
		}
		if errorPipe != nil {
			errorPipe.Close()
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
		s.startCronIfConfigured()
		s.startHealthCheckIfConfigured()
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
// Runs in the monitorProcess goroutine; acquires queueMu.
func (s *ProcessService) handleReadyNotification(ready bool) {
	s.services.queueMu.Lock()
	defer s.services.queueMu.Unlock()

	// Nil the channel so we don't select on it again
	s.readyCh = nil

	// When a watchdog is configured we keep the pipe open after the
	// initial readiness signal: subsequent writes act as keepalives.
	// Without a watchdog the pipe is one-shot, mirroring dinit.
	keepPipe := ready && s.HasWatchdog() && s.state.Load() == StateStarting
	if !keepPipe {
		s.closeReadyPipe()
	}

	if s.state.Load() != StateStarting {
		// Not in STARTING state; ignore readiness signal
		return
	}

	if ready {
		// Child signaled readiness
		s.cancelTimer()
		s.services.logger.Info("Service '%s': readiness notification received", s.serviceName)
		s.Started()
		s.startCronIfConfigured()
		s.startHealthCheckIfConfigured()
		if keepPipe {
			s.startWatchdogWatcher()
		}
		s.services.processQueuesLocked()
	} else {
		// EOF without data - child closed pipe without writing
		s.services.logger.Error("Service '%s': readiness pipe closed without notification", s.serviceName)
		s.cancelTimer()
		s.stopReason = ReasonFailed
		s.failedToStart(false, false)
		s.services.processQueuesLocked()
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
// Runs in the monitorProcess goroutine; acquires queueMu.
func (s *ProcessService) handleChildExit(exit process.ChildExit) {
	// Kill any remaining processes in the child's process group
	// (e.g., orphaned sleep, background scripts spawned by the shell).
	// The lead process is already reaped; wait4(-pgid) only targets
	// group members, so this is safe for other managed services.
	process.KillProcessGroup(exit.PID)

	// Kill entire cgroup tree to clean up any orphaned processes
	if s.Flags.KillAllOnStop {
		s.killCgroupTree(syscall.SIGKILL)
	}

	// Clear utmp entry
	if s.HasUtmp() && s.services.OnUtmpClear != nil {
		s.services.OnUtmpClear(s.inittabID, s.inittabLine)
	}

	s.services.queueMu.Lock()
	defer s.services.queueMu.Unlock()

	s.exitStatus = ExitStatus{
		WaitStatus: exit.Status,
		HasStatus:  true,
	}
	if exit.ExecErr != nil {
		s.exitStatus.ExecFailed = true
		s.exitStatus.ExecStage = uint8(exit.ExecErr.Stage)
		s.exitStatus.ExecErrno = extractErrno(exit.ExecErr.Err)
	}

	s.pid = 0
	s.procHandle.Clear()
	s.cancelTimer()
	s.stopWatchdogWatcherLocked()
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
		s.state.Store(StateStopping)
		s.failedToStart(false, true)
		s.services.processQueuesLocked()
		return
	}

	state := s.state.Load()

	switch state {
	case StateStarting:
		// Process died while we thought it was starting
		s.services.logger.Error("Service '%s': process exited during startup (status: %v)",
			s.serviceName, exit.Status)
		s.stopReason = ReasonFailed
		s.failedToStart(false, true)
		s.services.processQueuesLocked()

	case StateStopping:
		// Expected - we asked it to stop
		s.stopIssued = false
		s.Stopped()
		s.services.processQueuesLocked()

	case StateStarted:
		// Unexpected termination. Only log non-clean exits (match dinit
		// did_exit_clean semantics): a clean exit (code 0) from a
		// restart=true service is normal turnover, not an error.
		if exit.Exited() {
			if code := exit.Status.ExitStatus(); code != 0 {
				s.services.logger.Error("Service '%s': process exited with code %d",
					s.serviceName, code)
			}
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
			s.handleUnexpectedTerminationLocked()
		}
	}
}

// handleUnexpectedTerminationLocked handles when a started process dies
// unexpectedly. Caller must hold queueMu.
func (s *ProcessService) handleUnexpectedTerminationLocked() {
	s.stopReason = ReasonTerminated
	s.forceStop = true

	s.doStop(false)
	s.services.processQueuesLocked()

	// If after processing queues we're still stopping and desired is STARTED,
	// the restart will be handled by the state machine
	if s.state.Load() == StateStopping && s.desired.Load() == StateStarted && !s.IsStartPinned() {
		s.initiateStart()
		s.services.processQueuesLocked()
	}
}

// doSmoothRecovery restarts the process without affecting dependents.
func (s *ProcessService) doSmoothRecovery() {
	s.closeReadyPipe()

	effectiveDelay := s.nextRestartDelay()
	if s.restartDelayStep > 0 && effectiveDelay > s.restartDelay {
		s.services.logger.Info("Service '%s': smooth recovery - restarting process (backoff %v)",
			s.serviceName, effectiveDelay)
	} else {
		s.services.logger.Info("Service '%s': smooth recovery - restarting process",
			s.serviceName)
	}

	now := time.Now()
	elapsed := now.Sub(s.lastStartTime)

	if elapsed >= effectiveDelay {
		// Can restart immediately
		if err := s.startProcess(); err != nil {
			s.services.logger.Error("Service '%s': smooth recovery failed: %v",
				s.serviceName, err)
			s.doingSmoothRecov = false
			s.handleUnexpectedTerminationLocked()
		} else {
			s.doingSmoothRecov = false
		}
	} else {
		// Need to delay restart
		delay := effectiveDelay - elapsed
		s.armTimer(delay, timerRestartDelay)
	}
}

// handleTimerExpired processes a timer expiration.
// Runs in the monitorProcess goroutine; acquires queueMu.
func (s *ProcessService) handleTimerExpired() {
	s.services.queueMu.Lock()
	defer s.services.queueMu.Unlock()

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
		// Kill entire cgroup tree on SIGKILL escalation
		if s.Flags.KillAllOnStop {
			s.killCgroupTree(syscall.SIGKILL)
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
				s.handleUnexpectedTerminationLocked()
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
	s.services.queueMu.RLock()
	defer s.services.queueMu.RUnlock()
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

// spawnLoggerCommand starts an external command that reads from a pipe on its
// stdin. Returns the write-end of the pipe (to be used as the child's stdout
// or stderr) and the running *exec.Cmd. The caller must close the pipe after
// passing it to StartProcess.
func spawnLoggerCommand(cmdArgs []string, svcName, label string) (*os.File, *exec.Cmd, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("pipe: %w", err)
	}

	cmd := exec.CommandContext(context.Background(), cmdArgs[0], cmdArgs[1:]...)
	cmd.Stdin = r
	// Logger's own stdout/stderr go to /dev/null to avoid feedback loops.
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		r.Close()
		w.Close()
		return nil, nil, fmt.Errorf("start %s for %s: %w", label, svcName, err)
	}

	// Close the read-end in the parent — the child inherited it via fork.
	r.Close()

	// Reap the logger process in the background so it doesn't zombie.
	go cmd.Wait()

	return w, cmd, nil
}

// stopLoggerCommands kills any running output/error logger processes and
// cleans up references. Called when the service stops or becomes inactive.
func (s *ProcessService) stopLoggerCommands() {
	if s.loggerCmd != nil && s.loggerCmd.Process != nil {
		s.loggerCmd.Process.Kill()
		s.loggerCmd = nil
	}
	if s.errLoggerCmd != nil && s.errLoggerCmd.Process != nil {
		s.errLoggerCmd.Process.Kill()
		s.errLoggerCmd = nil
	}
}

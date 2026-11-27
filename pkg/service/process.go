package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"golang.org/x/sys/unix"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// The defaults a service gets for a setting its description leaves out.
// Exported so the config parser can start every description from them:
// a setting that is absent then means the default, and one given as 0
// means 0, on first load and on reload alike.
const (
	DefaultStopTimeout     = defaultStopTimeout
	DefaultStartTimeout    = defaultStartTimeout
	DefaultRestartDelay    = defaultRestartDelay
	DefaultRestartInterval = defaultRestartInterval
	DefaultMaxRestarts     = defaultMaxRestarts
)

const (
	defaultStopTimeout     = 10 * time.Second
	defaultStartTimeout    = 60 * time.Second
	defaultRestartDelay    = 200 * time.Millisecond
	defaultRestartInterval = 10 * time.Second
	defaultMaxRestarts     = 3
	defaultFinishTimeout   = 5 * time.Second
	// 100ms is short enough that sockets binding in microseconds don't
	// pay a visible boot-time cost, and long enough that a slower
	// readiness (systemd unit, real database open) doesn't turn into
	// a busy-loop. Combined with the immediate first check inside
	// watchReadyCheck the fast path costs one command exec only.
	// NOTE: this is not the effective default for the directive.
	// SetReadyCheckCommand always assigns readyCheckInterval — the
	// given value, or time.Second when none was given — so the guard
	// in watchReadyCheck that falls back to this constant is only
	// reached if the field was never set at all. The documented and
	// actual default for ready-check-interval is therefore 1s, and
	// slinit-service(5) said 100ms until this was noticed.
	//
	// Left as it is rather than wired up: making the constant
	// effective would poll ten times as often for every service with
	// a ready-check, which is a behaviour change nobody asked for.
	// Change both halves together if that is ever wanted.
	defaultReadyCheckInterval = 100 * time.Millisecond
)

// ProcessService manages a long-running process.
type ProcessService struct {
	ServiceRecord

	// Command configuration
	command            []string
	argv0              string // override argv[0] presented to the exec'd binary (runit chpst -b)
	stopCommand        []string
	finishCommand      []string            // runs after process exits (before restart decision)
	preStartCommand    []string            // runs before fork+exec; non-zero exit fails the start
	postStartCommand   []string            // runs after Started(); non-zero exit only logs
	readyCheckCommand  []string            // polls to verify service readiness
	readyCheckInterval time.Duration       // polling interval (default 1s)
	preStopHook        []string            // runs before SIGTERM in BringDown
	controlCommands    map[string][]string // signal name → custom command
	workingDir         string
	envFile            string
	envDir             string // directory with one file per env var
	// envGenerator: path to an executable that emits `KEY=VALUE\n` lines
	// on stdout at service-start time. Merged after env-file and env-dir
	// so it wins conflicts. A failure is logged and its output ignored;
	// the start goes on.
	envGenerator string
	chroot       string // chroot before exec
	lockFile     string // exclusive flock path
	newSession   bool   // setsid() before exec
	closeStdin   bool   // close fd 0
	closeStdout  bool   // close fd 1
	closeStderr  bool   // close fd 2

	// startEnv is the environment computed for the current start —
	// env-file, env-dir, setenv variables and the env-generator's
	// output. The main process gets it, and, when an env-generator is
	// configured, so do the hooks and every ready-check poll of that
	// start: buildEnv ran the generator again for each of them. startEnvFresh marks an environment
	// computed ahead of the fork (for pre-start-command) that the fork
	// has not used yet. envMu guards both: the post-start-command and
	// ready-check goroutines read startEnv.
	envMu         sync.Mutex
	startEnv      []string
	startEnvFresh bool

	// The post-start-command run of the current start: postStartCancel
	// ends it, postStartDone closes when it has. postStartMu guards both.
	postStartMu     sync.Mutex
	postStartCancel context.CancelFunc
	postStartDone   chan struct{}

	// stateHook is the hook a transition is waiting on — pre-start
	// (with the start's environment), pre-stop-hook, a stop's
	// control-command or finish-command — run off queueMu (statehook.go).
	// Set and cleared under queueMu; atomic so the shutdown teardown can
	// cancel it without the lock.
	stateHook atomic.Pointer[stateHook]
	// pendingExit is a main-process exit that arrived while a stop's
	// pre-stop-hook or control-command was running. It is handled once
	// that hook has finished, as it was when the hook held the lock.
	pendingExit *process.ChildExit
	// queuedHooks are state hooks asked for while another ran, started
	// one at a time as each completes (see startStateHookLocked). Not
	// expected to be used.
	queuedHooks []func()

	// Credentials
	runAsUID          uint32
	runAsGID          uint32
	supplementaryGIDs []uint32

	// Process state
	pid     int
	stopPID int // PID of stop-command process (0 if none)
	// mainGoneDuringStop records that the main process exited while the
	// stop-command was still running, so the stop-command's own
	// completion is what finishes the transition to STOPPED.
	mainGoneDuringStop bool
	exitStatus         ExitStatus
	procHandle         process.ProcessHandle

	// pidPublished mirrors pid for readers that cannot take queueMu —
	// PID(), and through it the log rotator's GetPID callback and the
	// status encoders. Written at pid's two assignment sites only; see
	// PID() for why the bare int read it replaced was a data race.
	pidPublished atomic.Int64

	// hookTimeout bounds every hook: pre-start-command,
	// post-start-command, finish-command, pre-stop-hook,
	// control-command-* and the env-generator. Zero means
	// defaultFinishTimeout.
	//
	// It is a per-service knob and not a constant because five seconds
	// is too short for a hook that legitimately waits on something —
	// a mount appearing, a socket accepting — and such a hook was
	// killed and, for pre-start-command, failed the start outright.
	// It stays a bounded wait rather than becoming optional because the
	// service's own transition waits for the hook. (Until the hooks
	// moved off the scheduling lock, statehook.go, it was also how long
	// one service's hook could hold up every other service: measured, a
	// service whose pre-start-command slept held up an unrelated
	// service's start for the remainder of this timeout.)
	hookTimeout time.Duration

	// start-delay: wait before each attempt's fork/exec. See
	// startdelay.go for why this is a timer and not a sleeping hook.
	startDelay     time.Duration
	startDelayT    *time.Timer
	startDelayDone bool

	// start-when-idle: wait for the other services to finish starting.
	startWhenIdle bool
	idleT         *time.Timer // the wait in progress, nil otherwise

	// Timer for start/stop/restart timeouts
	processTimer *time.Timer
	timerPurpose timerPurpose

	// pendingStopSignal is a per-invocation override consumed by
	// pickStopSignal. Set by fireWatchdogStop to route the watchdog
	// signal through the standard doStop path; cleared after use so
	// the next stop cycle reads normal config.
	pendingStopSignal syscall.Signal

	// Timeout configuration
	startTimeout time.Duration
	stopTimeout  time.Duration
	restartDelay time.Duration
	// systemd TimeoutStartFailureMode= — signal delivered when
	// start-timeout expires. Default TimeoutFailureTerminate maps to
	// the legacy SIGINT behaviour.
	timeoutStartFailureMode TimeoutFailureMode
	// systemd TimeoutAbortSec= — when non-zero, stop-timeout fires
	// SIGABRT first + arms another timer of this length before
	// escalating to SIGKILL. Zero preserves the legacy immediate
	// SIGKILL path.
	timeoutAbortSec time.Duration
	// systemd ExitType= — with ExitTypeCgroup the service is considered
	// stopped only after the delegated cgroup drains. Requires an
	// EffectiveCgroupPath (delegate-cgroup or explicit cgroup =); with
	// no cgroup this silently degrades to ExitTypeMain semantics.
	exitType ExitType

	// Progressive restart backoff (OpenRC-compatible, linear additive)
	restartDelayStep    time.Duration // increment added per successive restart (0 = disabled)
	restartDelayCap     time.Duration // max capped delay (0 = no cap, default 60s when step > 0)
	currentRestartDelay time.Duration // current effective delay, advances on each restart

	// systemd-style RestartRandomizedDelaySec: jitter drawn from
	// [0, restartRandomizedDelay) added to the effective restart delay
	// to spread out reconnect storms across a fleet. 0 disables jitter.
	restartRandomizedDelay time.Duration
	// systemd RestartMaxDelaySec: cap on the (delay + jitter) sum.
	// Distinct from restartDelayCap which only caps the progressive
	// backoff itself; this is the honest final ceiling. 0 = no cap.
	restartMaxDelay time.Duration

	// Restart rate limiting
	restartInterval      time.Duration
	maxRestartCount      int
	restartIntervalTime  time.Time
	restartIntervalCount int
	lastStartTime        time.Time

	// Timer that fires initiateStart() after a restart-delay window on
	// the non-smooth-recovery path. Uses time.AfterFunc directly (not
	// the shared processTimer) because monitorProcess exits as soon as
	// handleChildExit returns, so the shared timer's monitor-loop
	// listener isn't alive to receive it. Held so we can Stop() it if
	// the operator cancels the restart before it fires.
	restartDelayTimer *time.Timer

	// Timer that relaunches the process when a smooth recovery has to
	// wait out restart-delay. Same reason as restartDelayTimer for not
	// using processTimer: the recovery starts from handleChildExit, so
	// monitorProcess has already returned and nothing would read it.
	smoothRecovTimer *time.Timer

	// State tracking
	stopIssued       bool
	doingSmoothRecov bool
	paused           bool // true when service is SIGSTOP'd (pause/continue)

	// Socket activation
	socketFD       *os.File   // primary listening socket (fd 3, nil if no socket-listen)
	socketFDs      []*os.File // additional sockets (fd 4, 5, ... for multiple socket-listen)
	socketOnDemand bool       // launch the process on the first client (socket-activation = on-demand)
	demandWake     *os.File   // write end of the watcher's wake pipe; closing it disarms
	demandGen      uint64     // bumped on every arm/disarm; a stale watcher's activation is a no-op

	// Recent on-demand launches, for the trigger limit (ondemand.go),
	// and whether the last launch's process exited cleanly (code 0).
	demandTriggers  []time.Time
	demandLastClean bool

	// Readiness notification
	readyNotifyFD  int       // fd number child writes to (-1 if none)
	readyNotifyVar string    // env var name ("" if none)
	readyPipeRead  *os.File  // read-end of notification pipe (parent watches)
	readyCh        chan bool // receives true=ready, false=EOF/error

	// sd_notify readiness (ready-notification = sd-notify): the service
	// gets $NOTIFY_SOCKET instead of the pipe's write end, which slinit
	// keeps (sdPipeW) and writes a line to for READY=1, and for each
	// WATCHDOG=1 after it, so the pipe machinery above (readiness,
	// start timeout, watchdog) serves sd_notify unchanged. sdMu guards
	// sdPipeW and sdReady: the notify listener writes from its own
	// goroutine, without queueMu.
	readySD bool
	sdMu    sync.Mutex
	sdPipeW *os.File
	sdReady bool

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
	logMinFiles   int // svlogd Nmin: floor for ENOSPC drain
	logRotateTime time.Duration
	logProcessor  []string
	logIncludes   []string
	logExcludes   []string
	// s6-log-style regex selection chain. Populated by SetLogSelect;
	// mutually exclusive with the include/exclude pair at load time.
	logSelect            []string
	logRateLimitInterval time.Duration
	logRateLimitBurst    int
	logLevelMax          int
	syslogIdent          string
	syslogLevel          int
	noLevelPrefix        bool
	logExtraFields       map[string]string
	logSanitizeChar      byte
	logSanitizeExtra     []byte
	logMaxLineLength     int
	logTimestampMode     string
	logLinePrefix        string
	logReadBufferSize    int
	logForwardUDP        string
	logForwardFormat     string
	logForwardFacility   int
	logForwardTag        string
	// syslogForwarderOnly holds a SyslogForwarder created for the
	// standalone log-forward-udp path (no rotator). Closed at
	// service stop so we don't leak the UDP socket across restarts.
	syslogForwarderOnly *SyslogForwarder
	// s6-log-style priority alert channel. When alertFile is non-
	// empty, lines whose syslog level is <= alertLevel are ALSO
	// written to alertFile (in addition to the main log). -1 disables.
	alertFile  string
	alertLevel int

	// stderrLogFile sends the service's stderr to its own file instead
	// of merging it into logfile. Rotation and filtering follow the
	// logfile-* settings: one knob buys the separate stream, and
	// duplicating fifteen logfile-* directives per stream would buy
	// very little more.
	stderrLogFile string
	stderrRotator *LogRotator

	// stderr rotation overrides. Zero means "use the stdout stream's
	// value", which is what stderr-logfile did on its own: it gave
	// stderr a file but left it sharing one rotation policy, because
	// rotatorConfigFor varied only the path. A service that is quiet on
	// stdout and noisy on stderr could not rotate them differently.
	stderrMaxSize    int64
	stderrMaxFiles   int
	stderrRotateTime time.Duration

	// Output/error logger commands (OpenRC OUTPUT_LOGGER / ERROR_LOGGER)
	// When set, stdout (and stderr unless errorLogger is set) is piped
	// to the output-logger command. If errorLogger is set, stderr is
	// piped to it separately.
	outputLogger []string // command + args for stdout logger
	errorLogger  []string // command + args for stderr logger (optional)
	// Each logger runs under a supervisor that restarts it when it
	// exits: slinit holds the pipe read end, so the service cannot be
	// killed by SIGPIPE when its log consumer goes away. See
	// loggersupervise.go.
	loggerSup    *loggerSupervisor // output-logger
	errLoggerSup *loggerSupervisor // error-logger

	// Cron-like periodic task
	cronSlot

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
	// timerAbortTimeout: SIGABRT phase inserted between stop-timeout
	// (SIGTERM) and the SIGKILL escalation. Wraps timeout-abort-sec.
	timerAbortTimeout
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
		logLevelMax:     -1,
		syslogLevel:     -1,
		alertLevel:      -1,
	}
	svc.ServiceRecord = *NewServiceRecord(svc, set, name, TypeProcess)
	return svc
}

// SetCommand sets the startup command.
func (s *ProcessService) SetCommand(cmd []string) { s.command = cmd }

// SetArgv0 overrides the argv[0] presented to the exec'd binary.
// Empty means "use command[0]" (default). Mirrors runit's chpst -b.
func (s *ProcessService) SetArgv0(a string) { s.argv0 = a }

// effectiveRunAsUID returns the dynamic-user UID when allocated,
// otherwise the configured runAsUID. Called by startProcess so the
// switch between static run-as and dynamic-user is transparent to
// the rest of the exec path.
func (s *ProcessService) effectiveRunAsUID() uint32 {
	if uid := s.Record().DynamicUID(); uid != 0 {
		return uid
	}
	return s.runAsUID
}

// effectiveRunAsGID mirrors effectiveRunAsUID for the GID. Dynamic
// users reuse the UID as GID (matching systemd: one transient UID is
// also a transient GID with the same value).
func (s *ProcessService) effectiveRunAsGID() uint32 {
	if uid := s.Record().DynamicUID(); uid != 0 {
		return uid
	}
	return s.runAsGID
}

// drainFDStore hands the previously stashed fds to the next BringUp.
// Returns nil when fd-store is disabled — the safe default.
func (s *ProcessService) drainFDStore() []process.FDStoreEntry {
	if store := s.Record().FDStore(); store != nil {
		return store.Drain()
	}
	return nil
}

// notifySocketPath returns the per-service $NOTIFY_SOCKET path, or ""
// when fd-store / sd_notify is not configured for this service.
func (s *ProcessService) notifySocketPath() string {
	if l := s.Record().notifySock; l != nil {
		return l.Path()
	}
	return ""
}

// SetStopCommand sets the stop command.
func (s *ProcessService) SetStopCommand(cmd []string) { s.stopCommand = cmd }

// SetFinishCommand sets the finish command (runs after process exits).
func (s *ProcessService) SetFinishCommand(cmd []string) { s.finishCommand = cmd }

// SetPreStartCommand records a command to run before the main process
// is forked, which waits for it. A non-zero exit fails the start (systemd's
// ExecStartPre= semantics).
func (s *ProcessService) SetPreStartCommand(cmd []string) { s.preStartCommand = cmd }

// SetPostStartCommand records a command to run asynchronously after the
// service has reached STARTED. A non-zero exit is logged but does not
// fail the service (systemd's ExecStartPost= semantics).
func (s *ProcessService) SetPostStartCommand(cmd []string) { s.postStartCommand = cmd }

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

// openLogFile opens a service's log-file for appending, creating it with
// perm, never through a symlink. O_NONBLOCK so a FIFO at the path with no
// reader fails the start with ENXIO instead of blocking PID 1 in open(2)
// with the service set's lock held; the service gets the fd back in
// blocking mode, as a plain open would have given it.
func openLogFile(path string, perm os.FileMode) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_APPEND|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, uint32(perm.Perm()))
	if err == nil {
		if err = unix.SetNonblock(fd, false); err != nil {
			unix.Close(fd)
		}
	}
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}

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

// VTTYSockDir returns the directory the vtty attach socket is created in.
func (s *ProcessService) VTTYSockDir() string { return s.vttySockDir }

// SetWorkingDir sets the working directory.
func (s *ProcessService) SetWorkingDir(dir string) { s.workingDir = dir }

// SetEnvFile sets the environment file path.
func (s *ProcessService) SetEnvFile(path string) { s.envFile = path }

// SetEnvGenerator points at an executable that emits KEY=VALUE lines
// on stdout at start-time. Empty disables. A non-zero exit fails the
// start — the operator asked for a strict env source.
func (s *ProcessService) SetEnvGenerator(path string) { s.envGenerator = path }

// SetRunAs sets the UID and GID to run the process as.
func (s *ProcessService) SetRunAs(uid, gid uint32) {
	s.runAsUID = uid
	s.runAsGID = gid
}

// SetSupplementaryGroups sets the child's supplementary group set.
// Applied together with SetRunAs — see loader.applySupplementaryGroups.
func (s *ProcessService) SetSupplementaryGroups(gids []uint32) {
	s.supplementaryGIDs = gids
}

// SetStartTimeout sets the start timeout.
func (s *ProcessService) SetStartTimeout(d time.Duration) { s.startTimeout = d }

// SetStopTimeout sets the stop timeout.
func (s *ProcessService) SetStopTimeout(d time.Duration) { s.stopTimeout = d }

// SetTimeoutStartFailureMode picks the signal delivered when the
// start-timeout expires. Default (terminate) preserves the legacy
// SIGINT-then-escalation behaviour.
func (s *ProcessService) SetTimeoutStartFailureMode(m TimeoutFailureMode) {
	s.timeoutStartFailureMode = m
}

// SetTimeoutAbortSec configures the SIGABRT phase before SIGKILL
// during a stop-timeout escalation. Zero preserves the legacy
// immediate-SIGKILL path.
func (s *ProcessService) SetTimeoutAbortSec(d time.Duration) { s.timeoutAbortSec = d }

// SetExitType picks whether service-stopped is decided by the main
// pid alone (ExitTypeMain, default) or by the delegated cgroup
// becoming empty (ExitTypeCgroup).
func (s *ProcessService) SetExitType(t ExitType) { s.exitType = t }

// pickStopSignal chooses the primary signal delivered by doStop. The
// picker order matches systemd's precedence: a watchdog-triggered stop
// uses watchdog-signal (default SIGABRT), a restart-driven stop uses
// restart-kill-signal (falls back to term-signal), everything else
// uses term-signal.
func (s *ProcessService) pickStopSignal() syscall.Signal {
	// watchdog-signal wins when set — Stop() sets a per-invocation
	// pendingStopSignal that we consume here.
	if s.pendingStopSignal != 0 {
		sig := s.pendingStopSignal
		s.pendingStopSignal = 0
		return sig
	}
	// A restart the operator asked for (slinitctl restart) counts too;
	// it is the case restart-kill-signal exists for. InAutoRestart alone
	// is set only after the process has exited by itself, when there is
	// nothing left to signal, so the directive never took effect.
	if s.Record().InAutoRestart() || s.Record().IsRestarting() {
		if sig := s.Record().RestartKillSignal(); sig != 0 {
			return sig
		}
	}
	sig := s.termSignal
	if sig == 0 {
		sig = syscall.SIGTERM
	}
	return sig
}

// killsToGroup reports whether the service's stop escalation should
// signal the whole cgroup vs. the main pid only. Merges two config
// axes: the historical options=kill-all-on-stop flag and the newer
// kill-mode = control-group|mixed enum. mixed uses the group only at
// escalation; process/none do not touch the group; control-group
// always does. If either config axis is set to a group-touching mode
// we take it.
func (s *ProcessService) killsToGroup() bool {
	if s.Flags.KillAllOnStop {
		return true
	}
	switch s.Record().KillMode() {
	case KillModeControlGroup, KillModeMixed:
		return true
	}
	return false
}

// stopTimeoutSignal picks the signal delivered when the stop-timeout
// expires. Mirrors systemd's TimeoutStopFailureMode=:
//   - terminate (default): SIGKILL (or SIGABRT-then-KILL if
//     timeoutAbortSec is set — legacy path preserved).
//   - abort: SIGABRT immediately (skip the timeoutAbortSec branch).
//   - kill: SIGKILL immediately, bypass any SIGABRT phase.
//
// The escalation SIGKILL after abort is still finalKillSignal()
// per FinalKillSignal=.
func (s *ProcessService) stopTimeoutSignal() (syscall.Signal, string) {
	switch s.Record().TimeoutStopFailureMode() {
	case TimeoutFailureAbort:
		return syscall.SIGABRT, "SIGABRT (stop-failure-mode=abort)"
	case TimeoutFailureKill:
		return s.Record().FinalKillSignal(), "SIGKILL (stop-failure-mode=kill)"
	}
	return 0, ""
}

// startTimeoutSignal returns the signal + human label for the
// configured timeout-start-failure-mode.
func (s *ProcessService) startTimeoutSignal() (syscall.Signal, string) {
	switch s.timeoutStartFailureMode {
	case TimeoutFailureAbort:
		return syscall.SIGABRT, "SIGABRT (start-failure-mode=abort)"
	case TimeoutFailureKill:
		return syscall.SIGKILL, "SIGKILL (start-failure-mode=kill)"
	}
	return syscall.SIGINT, "SIGINT (start-failure-mode=terminate)"
}

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

// SetRestartRandomizedDelay configures jitter added to the restart delay
// (systemd RestartRandomizedDelaySec). 0 disables jitter.
func (s *ProcessService) SetRestartRandomizedDelay(d time.Duration) {
	s.restartRandomizedDelay = d
}

// SetRestartMaxDelay configures the systemd RestartMaxDelaySec cap:
// hard upper bound on the sum of restart-delay (+ backoff step) plus
// restart-randomized-delay jitter. Zero means no cap.
func (s *ProcessService) SetRestartMaxDelay(d time.Duration) {
	s.restartMaxDelay = d
}

// ScheduleRestartWithBackoff implements the Service interface hook that
// Stopped() calls before it would otherwise invoke initiateStart() on the
// willRestart path. If the process's last start was less than the next
// effective restart-delay ago, arm restartDelayTimer for the remaining
// duration, set pendingInitiateStart, and return true so Stopped()
// skips its own initiateStart call. When the timer fires the handler
// clears the flag and calls Record().initiateStart() itself, so the
// operator sees the configured delay (and progressive backoff) between
// crash and re-launch.
//
// When restart-delay is 0 or the delay already elapsed (e.g. a service
// that ran a long time before crashing), returns false immediately —
// Stopped() proceeds as before with no observable delay.
//
// nextRestartDelay() is called only on the "actually needs to wait"
// path so the backoff counter advances in lockstep with real restarts.
func (s *ProcessService) ScheduleRestartWithBackoff() bool {
	// smooth-recovery has its own dedicated backoff path in
	// doSmoothRecovery; don't double-handle it here.
	if s.doingSmoothRecov {
		return false
	}
	// No delay configured (zero-value restart-delay + no step): let
	// Stopped() restart immediately, matching pre-fix behaviour for
	// services that never opted into backoff.
	if s.restartDelay <= 0 && s.restartDelayStep <= 0 {
		return false
	}
	effectiveDelay := s.nextRestartDelay()
	if effectiveDelay <= 0 {
		return false
	}
	elapsed := time.Since(s.lastStartTime)
	if elapsed >= effectiveDelay {
		// The gap between last start and now is already larger than
		// the required delay (e.g. a stable service that ran an hour
		// before crashing). Restart immediately.
		return false
	}
	remaining := effectiveDelay - elapsed
	if s.restartDelayStep > 0 && effectiveDelay > s.restartDelay {
		s.services.logger.Info("Service '%s': restart delayed %v (backoff %v)",
			s.serviceName, remaining, effectiveDelay)
	} else {
		s.services.logger.Info("Service '%s': restart delayed %v",
			s.serviceName, remaining)
	}
	// Own goroutine via AfterFunc — monitorProcess dies after
	// handleChildExit returns, so a shared processTimer would fire
	// into an empty select. AfterFunc gives us a private callback.
	if s.restartDelayTimer != nil {
		s.restartDelayTimer.Stop()
	}
	s.restartDelayTimer = time.AfterFunc(remaining, s.fireDelayedInitiateStart)
	return true
}

// CancelPendingRestart disarms the restart-delay timer and reports
// whether one was armed. See the Service interface for why the state
// machine needs to be able to call a deferred restart off.
func (s *ProcessService) CancelPendingRestart() bool {
	if s.restartDelayTimer == nil {
		return false
	}
	s.restartDelayTimer.Stop()
	s.restartDelayTimer = nil
	return true
}

// fireDelayedInitiateStart is the AfterFunc callback armed by
// ScheduleRestartWithBackoff. Re-checks the same gate that Stopped()
// applied — desired might have flipped to Stopped while the timer
// was pending (operator ran `slinitctl stop`), in which case the
// restart is abandoned.
func (s *ProcessService) fireDelayedInitiateStart() {
	s.services.queueMu.Lock()
	defer s.services.queueMu.Unlock()
	if s.state.Load() != StateStopped {
		// Something else moved the service on while the timer was
		// pending; whoever did owns its accounting now.
		s.restartDelayTimer = nil
		return
	}
	if s.desired.Load() != StateStarted || s.pinnedStopped {
		// Giving up the restart. Stopped() skipped the inactive
		// transition because it expected this timer to restart the
		// service, so settle it here instead — otherwise the service
		// stays counted active while sitting in STOPPED forever.
		s.Record().abandonPendingRestart()
		s.services.processQueuesLocked()
		return
	}
	s.restartDelayTimer = nil
	s.Record().initiateStart()
	s.services.processQueuesLocked()
}

// nextRestartDelay returns the delay to use for the next restart and advances
// the progressive backoff counter. When step <= 0, always returns restartDelay.
// Jitter (restartRandomizedDelay) is applied on top of the base value when set.
func (s *ProcessService) nextRestartDelay() time.Duration {
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

// SetLogMinFiles sets the ENOSPC drain floor (svlogd Nmin). When
// disk-full is encountered, LogRotator deletes rotated files down to
// this count and retries the write. 0 disables the recovery path.
func (s *ProcessService) SetLogMinFiles(n int) {
	s.logMinFiles = n
}

// SetLogProcessor sets the log processor command.
func (s *ProcessService) SetLogProcessor(cmd []string) { s.logProcessor = cmd }

// SetStderrLogFile routes stderr to its own file. Rotation follows
// logfile's rules unless SetStderrLogRotation overrides them.
func (s *ProcessService) SetStderrLogFile(path string) { s.stderrLogFile = path }

// SetStderrLogRotation overrides rotation for the stderr file only. A
// zero in any position keeps the stdout stream's value for that one
// setting, so a service can change just the size and inherit the rest.
func (s *ProcessService) SetStderrLogRotation(maxSize int64, maxFiles int, rotateTime time.Duration) {
	s.stderrMaxSize = maxSize
	s.stderrMaxFiles = maxFiles
	s.stderrRotateTime = rotateTime
}

// rotatorConfigFor builds the log-pipeline settings shared by the
// service's streams. Extracted when stderr gained a file of its own:
// the alternative was a second copy of thirty fields that had to be
// kept in step by hand.
//
// The caller adds what belongs to one stream only — the UDP forwarder,
// the output-logger sink and the alert file stay on stdout, so a
// service with both files does not get its remote syslog or its alert
// lines duplicated.
//
// ServiceName stays the bare service name for both streams. It is what
// emitJournalLogLine records, so qualifying it per stream would have
// renamed the unit in the journal for every service with a rotator and
// broken `slinit-journalctl -u <svc>`.
func (s *ProcessService) rotatorConfigFor(path string) LogRotatorConfig {
	return s.rotatorConfig(path, 0, 0, 0)
}

// rotatorConfigForStderr is rotatorConfigFor with the stderr overrides
// applied. Kept as a separate entry point rather than a flag on the
// first so the stdout path is literally unchanged.
func (s *ProcessService) rotatorConfigForStderr(path string) LogRotatorConfig {
	return s.rotatorConfig(path, s.stderrMaxSize, s.stderrMaxFiles, s.stderrRotateTime)
}

// rotatorConfig builds the pipeline settings. A non-zero override
// replaces the stream-shared value for that one field; zero inherits.
func (s *ProcessService) rotatorConfig(path string, maxSize int64, maxFiles int,
	rotateTime time.Duration) LogRotatorConfig {
	if maxSize == 0 {
		maxSize = s.logMaxSize
	}
	if maxFiles == 0 {
		maxFiles = s.logMaxFiles
	}
	if rotateTime == 0 {
		rotateTime = s.logRotateTime
	}
	return LogRotatorConfig{
		GetPID:         s.PID,
		FilePath:       path,
		FilePerms:      os.FileMode(s.logFilePerms),
		FileUID:        s.logFileUID,
		FileGID:        s.logFileGID,
		MaxSize:        maxSize,
		MaxFiles:       maxFiles,
		MinFiles:       s.logMinFiles,
		RotateTime:     rotateTime,
		Processor:      s.logProcessor,
		Includes:       s.logIncludes,
		Excludes:       s.logExcludes,
		Select:         s.logSelect,
		RateInterval:   s.logRateLimitInterval,
		RateBurst:      s.logRateLimitBurst,
		LogLevelMax:    s.logLevelMax,
		SyslogIdent:    s.syslogIdent,
		DefaultLevel:   s.syslogLevel,
		NoLevelPrefix:  s.noLevelPrefix,
		ExtraFields:    s.logExtraFields,
		SanitizeChar:   s.logSanitizeChar,
		SanitizeExtra:  s.logSanitizeExtra,
		MaxLineLength:  s.logMaxLineLength,
		TimestampMode:  s.logTimestampMode,
		LinePrefix:     s.logLinePrefix,
		ReadBufferSize: s.logReadBufferSize,
		ServiceName:    s.serviceName,
		Logger:         s.services.logger,

		// log-processor: the service's run-as, working-dir and environment.
		PrepareProcessor: s.prepareHookCmd,
	}
}

// SetOutputLogger sets the output-logger command (OpenRC OUTPUT_LOGGER).
// When configured, stdout (and stderr unless an error-logger is set) is
// piped to this external command.
func (s *ProcessService) SetOutputLogger(cmd []string) { s.outputLogger = cmd }

// SetErrorLogger sets the error-logger command (OpenRC ERROR_LOGGER).
// When configured, stderr is piped to this command separately from stdout.
func (s *ProcessService) SetErrorLogger(cmd []string) { s.errorLogger = cmd }

// SetCronConfig configures the periodic cron task in interval mode.
func (s *ProcessService) SetCronConfig(cmd []string, interval, delay time.Duration, onError string) {
	s.setCronRunner(NewCronRunner(s, cmd, interval, delay, onError, s.services.logger))
}

// SetCronCalendar configures the periodic cron task in calendar mode.
// When calendar is non-nil it takes precedence over any interval
// scheduling that may have been set earlier.
func (s *ProcessService) SetCronCalendar(
	cmd []string, calendar *CalendarSpec,
	randomizedDelay time.Duration, persistent bool, onError string,
) {
	s.setCronRunner(NewCalendarCronRunner(
		s, cmd, calendar, randomizedDelay, persistent, onError, s.services.logger))
}

// SetHealthCheck configures the continuous health checker.
//
// An empty cmd removes the health check. A checker already running is
// replaced, not left behind: on a reload of a started service the old
// one used to keep running, unreachable, after the service stopped, and
// the new one never started. It is stopped here and, when the service
// is running, the new one takes over at once.
func (s *ProcessService) SetHealthCheck(cmd []string, interval, delay time.Duration,
	maxFailures int, unhealthyCmd []string) {
	wasRunning := false
	if old := s.healthChecker; old != nil {
		wasRunning = old.running()
		old.Stop()
		s.healthChecker = nil
	}
	if len(cmd) == 0 {
		return
	}
	onFail := func() {
		// Not inline: the checker's Stop() waits for this goroutine
		// and is called with queueMu held (BringDown), so taking the
		// lock here would deadlock against it.
		go s.fireHealthCheckStop(s.PID())
	}
	s.healthChecker = NewHealthChecker(s, cmd, interval, delay, maxFailures, unhealthyCmd,
		s.services.logger, onFail)
	if wasRunning {
		s.healthChecker.Start()
	}
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
			s.services.NoteWatchdogMiss()
			s.fireWatchdogStop(stop)
			return
		default:
			// EOF or other read error. The service either died (in
			// which case handleChildExit will close stop shortly and
			// our select above already covered that) or closed its
			// end of the watchdog pipe while still running. Either
			// way, the watchdog cannot continue — escalate to Stop.
			s.services.logger.Error("Service '%s': watchdog pipe lost (%v) — restarting",
				s.serviceName, err)
			s.services.NoteWatchdogMiss()
			s.fireWatchdogStop(stop)
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
//
// stop identifies the watcher that fired. One cancelled while this
// waited for queueMu — its child exited, or the service was stopped and
// maybe started again — has nothing left to stop.
func (s *ProcessService) fireWatchdogStop(stop chan struct{}) {
	s.services.queueMu.Lock()
	defer s.services.queueMu.Unlock()

	if s.watchdogStop != stop {
		return
	}

	sig := s.Record().WatchdogSignal()
	if sig == 0 {
		sig = syscall.SIGABRT
	}

	// On-demand: the process is not the service. Kill just the process;
	// its exit puts the service back to listening, as any exit does.
	if s.onDemandListening() {
		if s.pid > 0 {
			s.services.logger.Error("Service '%s': watchdog expired, sending %s to process %d",
				s.serviceName, signalName(sig), s.pid)
			process.SignalProcess(s.pid, sig, s.Flags.SignalProcessOnly)
		}
		return
	}

	s.stopReason = ReasonTerminated
	s.forceStop = true

	// systemd WatchdogSignal= — route through the standard stop path
	// but with the operator-picked signal (default SIGABRT — that's
	// what the systemd docs specify). pickStopSignal consumes and
	// clears this per invocation.
	s.pendingStopSignal = sig

	withRestart := false
	switch s.autoRestartThisRun() {
	case RestartAlways, RestartOnFailure, RestartOnSuccess:
		withRestart = s.checkRestartOnce()
	}

	s.doStop(withRestart)
	s.services.processQueuesLocked()
}

// fireHealthCheckStop acts on a health check that reached its failure
// threshold: the process is stopped as having failed, and the restart
// policy decides what follows — the same path as a watchdog expiry. It
// used to call Stop(false), which only dropped the explicit activation:
// a service a started dependent still required kept running unhealthy,
// and one that did stop had its desired state set to STOPPED, so
// restart = yes never brought it back.
//
// pid is the process the checker judged; if the service has moved on
// since (stopped, restarted), the verdict no longer applies.
func (s *ProcessService) fireHealthCheckStop(pid int) {
	s.services.queueMu.Lock()
	defer s.services.queueMu.Unlock()

	if pid <= 0 || s.pid != pid || s.state.Load() != StateStarted {
		return
	}
	s.services.logger.Error("Service '%s': unhealthy, stopping process %d", s.serviceName, pid)

	s.stopReason = ReasonTerminated
	s.forceStop = true

	withRestart := false
	switch s.autoRestartThisRun() {
	case RestartAlways, RestartOnFailure, RestartOnSuccess:
		withRestart = s.checkRestartOnce()
	}

	s.doStop(withRestart)
	s.services.processQueuesLocked()
}

// startCronIfConfigured starts the cron runner if a cron-command is configured.
func (s *ProcessService) startCronIfConfigured() {
	if s.cronRunner != nil {
		s.services.logger.Info("Service '%s': starting cron task (%s)", s.serviceName, s.cronRunner.Schedule(time.Now()))
		s.cronRunner.Start()
	}
}

// stopCronRunner stops the cron runner if active.
func (s *ProcessService) stopCronRunner() {
	s.stopCron()
}

// SetLogFilters sets log include/exclude patterns.
func (s *ProcessService) SetLogFilters(includes, excludes []string) {
	s.logIncludes = includes
	s.logExcludes = excludes
}

// SetLogSelect sets the s6-log-style regex selection chain. Each token
// is `+regex` or `-regex`; the last-matched verdict wins per line; no
// match at end of chain → include. Mutually exclusive with the
// include/exclude pair — the loader validates before calling.
func (s *ProcessService) SetLogSelect(tokens []string) {
	s.logSelect = tokens
}

// SetAlertLog configures the s6-log-style priority alert channel.
// path=="" disables it (level is ignored). When path is non-empty,
// level is 0..7 (syslog) and lines with priority <= level are copied
// to path in addition to the main log write. The loader normalises
// the level via resolveAlertLevel before calling this.
func (s *ProcessService) SetAlertLog(path string, level int) {
	s.alertFile = path
	s.alertLevel = level
}

// SetLogRateLimit configures the log pipeline's token-bucket limiter.
// interval=0 or burst=0 disables it.
func (s *ProcessService) SetLogRateLimit(interval time.Duration, burst int) {
	s.logRateLimitInterval = interval
	s.logRateLimitBurst = burst
}

// SetSyslogMeta sets the journal metadata of the service's output lines:
// the identifier they are shown under (syslog-identifier, "" for the
// service name), the level of a line that names none (syslog-level, -1
// for info), whether a "<N>" prefix gives a line's level and is left out
// of its text (syslog-level-prefix), and extra journal fields
// (log-extra-fields).
func (s *ProcessService) SetSyslogMeta(ident string, level int, levelPrefix bool, extra map[string]string) {
	s.syslogIdent = ident
	s.syslogLevel = level
	s.noLevelPrefix = !levelPrefix
	s.logExtraFields = extra
}

// SetLogLevelMax sets the maximum syslog severity (0..7) to keep.
// -1 disables the filter.
func (s *ProcessService) SetLogLevelMax(level int) {
	s.logLevelMax = level
}

// SetLogSanitize configures svlogd -r/-R style byte replacement.
// char == 0 disables sanitization; extra lists bytes to replace in
// addition to the default control-char set.
func (s *ProcessService) SetLogSanitize(char byte, extra []byte) {
	s.logSanitizeChar = char
	s.logSanitizeExtra = extra
}

// SetLogMaxLineLength caps log line length. Lines longer than n bytes
// are truncated and marked with '+', svlogd(8) -l style. n == 0
// disables the cap.
func (s *ProcessService) SetLogMaxLineLength(n int) {
	s.logMaxLineLength = n
}

// SetLogTimestamp configures a per-line timestamp prefix mode
// (svlogd -t/-tt/-ttt). Empty disables. Valid: tai64n, human, iso8601.
func (s *ProcessService) SetLogTimestamp(mode string) {
	s.logTimestampMode = mode
}

// SetLogLinePrefix configures a static per-line prefix (svlogd p<...>).
// Empty disables. A trailing space is appended at LogRotator load time
// if the operator omitted it.
func (s *ProcessService) SetLogLinePrefix(prefix string) {
	s.logLinePrefix = prefix
}

// SetLogReadBufferSize sets the per-Read chunk size for LogRotator's
// pipe reader (svlogd -b). 0 selects the built-in default (4096).
// The parser bounds explicit values to [512..1MB] before they arrive.
func (s *ProcessService) SetLogReadBufferSize(n int) {
	s.logReadBufferSize = n
}

// SetLogForward configures the UDP syslog forwarder (svlogd u/U).
// dest is host:port; empty disables. facility is the numeric syslog
// facility (0..23). format is "rfc3164" or "rfc5424". tag defaults
// to the service name when empty.
func (s *ProcessService) SetLogForward(dest, format string, facility int, tag string) {
	s.logForwardUDP = dest
	s.logForwardFormat = format
	s.logForwardFacility = facility
	s.logForwardTag = tag
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
	return s.readyNotifyFD >= 0 || s.readyNotifyVar != "" || s.readySD
}

// SetReadyNotificationSD selects sd_notify readiness: the service is
// started when it sends READY=1 on $NOTIFY_SOCKET (systemd's
// Type=notify), and WATCHDOG=1 keeps a watchdog-timeout fed.
func (s *ProcessService) SetReadyNotificationSD(on bool) { s.readySD = on }

// sdNotify turns sd_notify READY=1 and WATCHDOG=1 into lines on the
// readiness pipe. READY=1 is the first line, the readiness signal; a
// WATCHDOG=1 is a keepalive only once the service is ready, since a line
// before it would read as readiness. The write is bounded: a pipe the
// watchdog does not drain must not hold up the listener.
func (s *ProcessService) sdNotify(ready, watchdog bool) {
	s.sdMu.Lock()
	defer s.sdMu.Unlock()
	if s.sdPipeW == nil {
		return
	}
	if ready && !s.sdReady {
		s.sdReady = true
	} else if !(watchdog && s.sdReady) {
		return
	}
	_ = s.sdPipeW.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	_, _ = s.sdPipeW.Write([]byte{'\n'})
}

// setSDPipe installs (or, with nil, closes) the write end slinit keeps
// for sd_notify readiness.
func (s *ProcessService) setSDPipe(w *os.File) {
	s.sdMu.Lock()
	defer s.sdMu.Unlock()
	if s.sdPipeW != nil {
		s.sdPipeW.Close()
	}
	s.sdPipeW, s.sdReady = w, false
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

// listenConfig returns the ListenConfig used for inet listeners. When
// socket-reuseport is set it installs SO_REUSEPORT on the socket before
// bind — which is the only moment the option can be set, so this has to go
// through ListenConfig.Control rather than being applied to a finished
// listener.
//
// SO_REUSEPORT is what lets several services hold the same host:port: the
// kernel hashes each incoming connection to one of the bound sockets. The
// intended shape is N instances of one template (web@1 … web@4), each its
// own service with its own supervised process, sharing a port. A worker
// that dies takes only its own socket out of the set, so the port keeps
// being served by its siblings while slinit restarts it.
//
// Linux requires every socket sharing a port this way to have been created
// by the same effective UID. slinit opens all of them itself, so that
// holds regardless of each service's run-as.
func (s *ProcessService) listenConfig() *net.ListenConfig {
	if !s.SocketReusePort() {
		return &net.ListenConfig{}
	}
	return &net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var setErr error
			if err := c.Control(func(fd uintptr) {
				// SO_REUSEPORT is not in the stdlib syscall package on
				// Linux, hence x/sys/unix.
				setErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET,
					unix.SO_REUSEPORT, 1)
			}); err != nil {
				return err
			}
			return setErr
		},
	}
}

// openOneSocket opens a single listening socket. The path format determines
// the socket type:
//   - "tcp:host:port" or "tcp4:host:port" or "tcp6:host:port" → TCP
//   - "udp:host:port" or "udp4:host:port" or "udp6:host:port" → UDP
//   - anything else → Unix domain socket
//
// socket-reuseport applies to the tcp/udp forms only. On a Unix socket the
// option is accepted by the kernel and does nothing, so applying it there
// would only suggest a guarantee that is not being made.
func (s *ProcessService) openOneSocket(path string) (*os.File, error) {
	// TCP socket
	if strings.HasPrefix(path, "tcp:") || strings.HasPrefix(path, "tcp4:") || strings.HasPrefix(path, "tcp6:") {
		parts := strings.SplitN(path, ":", 2)
		network := parts[0]
		addr := parts[1]
		ln, err := s.listenConfig().Listen(context.Background(), network, addr)
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
		conn, err := s.listenConfig().ListenPacket(context.Background(), network, addr)
		if err != nil {
			return nil, fmt.Errorf("udp listen: %w", err)
		}
		fd, err := conn.(*net.UDPConn).File()
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("udp fd: %w", err)
		}
		conn.Close()
		// File() returned a dup, so it and the closed conn name the same
		// socket: joining on either applies to the membership the child
		// inherits. Joining after bind is also the only valid order —
		// IP_ADD_MEMBERSHIP on an unbound socket has no port to deliver to.
		if grp := s.SocketMulticastGroup(); grp != "" {
			if err := joinMulticastGroup(int(fd.Fd()), network, grp); err != nil {
				fd.Close()
				return nil, err
			}
		}
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
	if s.socketPerms != 0 || s.socketUID >= 0 || s.socketGID >= 0 {
		uid, gid := s.socketUID, s.socketGID
		if uid < 0 {
			uid = -1
		}
		if gid < 0 {
			gid = -1
		}
		if err := setSocketFileAttrs(path, s.socketPerms, uid, gid); err != nil {
			fd.Close()
			return nil, err
		}
	}

	return fd, nil
}

// setSocketFileAttrs applies socket-permissions and socket-uid/gid to the
// socket file just bound at path, without following a symlink.
//
// By path, a chmod/chown between bind and here followed whatever stood at
// path by then, and the socket's directory is often one the service user
// can write (its runtime-directory). Replacing the socket with a symlink
// in that window handed root's chmod/chown any file on the system. The
// name is pinned with O_PATH|O_NOFOLLOW and checked to be a socket; mode
// and owner then go through that descriptor. A socket cannot be opened
// for real, so the mode goes through fchmodat2(AT_EMPTY_PATH) where the
// kernel has it (6.6+) and /proc/self/fd otherwise, as systemd does.
func setSocketFileAttrs(path string, perms, uid, gid int) error {
	pfd, err := unix.Open(path, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("socket %s: %w", path, err)
	}
	defer unix.Close(pfd)
	var st unix.Stat_t
	if err := unix.Fstat(pfd, &st); err != nil {
		return fmt.Errorf("socket %s: %w", path, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return fmt.Errorf("socket %s: replaced by a non-socket after bind; "+
			"refusing to chmod/chown it", path)
	}
	if perms != 0 {
		err := unix.Fchmodat(pfd, "", uint32(perms), unix.AT_EMPTY_PATH)
		if err == unix.EOPNOTSUPP || err == unix.ENOSYS || err == unix.EINVAL {
			err = unix.Chmod("/proc/self/fd/"+strconv.Itoa(pfd), uint32(perms))
		}
		if err != nil {
			return fmt.Errorf("chmod: %w", err)
		}
	}
	if uid != -1 || gid != -1 {
		if err := unix.Fchownat(pfd, "", uid, gid, unix.AT_EMPTY_PATH); err != nil {
			return fmt.Errorf("chown: %w", err)
		}
	}
	return nil
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

// BecomingInactive is called when the service won't restart. Cleans up socket.
func (s *ProcessService) BecomingInactive() {
	s.disarmOnDemand()
	s.closeDoneCh()
	s.closeSocket()
	// The log-type=pipe pipe is NOT closed here: it lives as long as the
	// record, as in dinit, so a consumer keeps reading across the
	// producer's restarts instead of seeing EOF and exiting. It is closed
	// when the service is unloaded (or handed to a placeholder).
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
	// Read from the atomic mirror, not from s.pid.
	//
	// s.pid is written under queueMu.Lock by the scheduler, and PID()
	// cannot take that lock: it is also called from inside the locked
	// region via notifyListeners → encodeStatus5Into, and the RWMutex
	// is not reentrant. This used to read s.pid bare, on the argument
	// that an int read is atomic on supported architectures — true of
	// the hardware, but still a data race, and the race detector says
	// so. It went unreported only because no test had a goroutine
	// calling PID() while a start was writing it; the log rotator does
	// exactly that through its GetPID callback, which the multiwriter
	// test finally exercised.
	//
	// The mirror is written at both of s.pid's two assignments and
	// nowhere else. Keep it that way, or make s.pid itself the atomic
	// and convert its ~30 in-lock readers.
	return int(s.pidPublished.Load())
}

// GetExitStatus returns the exit status of the last process.
func (s *ProcessService) GetExitStatus() ExitStatus { return s.exitStatus }

// cgroupDrainTimeout caps how long exit-type=cgroup will wait for the
// delegated cgroup to become unpopulated before proceeding anyway. Kept
// generous — the operator asked for cgroup drain, so we honour it even
// when it's slow; but a permanently-stuck worker must not deadlock the
// service manager.
const cgroupDrainTimeout = 30 * time.Second

// cgroupDrainPollInterval is the poll cadence for cgroup.events. Fast
// enough for interactive stop, coarse enough that a 30s wait is at most
// 150 file reads.
const cgroupDrainPollInterval = 200 * time.Millisecond

// readCgroupPopulated reads cgroup.events and returns whether the
// "populated 1" line is set. A read error (cgroup gone) is reported as
// unpopulated — the drain is over either way.
func readCgroupPopulated(cgPath string) (bool, error) {
	data, err := os.ReadFile(cgPath + "/cgroup.events")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "populated" {
			return fields[1] == "1", nil
		}
	}
	return false, nil
}

// waitCgroupThenDispatch polls cgroup.events until the delegated cgroup
// drains (or the drain timeout fires), then resumes the state-machine
// transition on queueMu. Runs in its own goroutine.
func (s *ProcessService) waitCgroupThenDispatch(cgPath string, state ServiceState, exit process.ChildExit) {
	deadline := time.Now().Add(cgroupDrainTimeout)
	for {
		populated, err := readCgroupPopulated(cgPath)
		if err != nil || !populated {
			break
		}
		if time.Now().After(deadline) {
			s.services.queueMu.Lock()
			s.services.logger.Error(
				"Service '%s': cgroup drain timed out after %s, proceeding with stop-transition",
				s.serviceName, cgroupDrainTimeout)
			s.services.queueMu.Unlock()
			break
		}
		time.Sleep(cgroupDrainPollInterval)
	}
	s.services.queueMu.Lock()
	defer s.services.queueMu.Unlock()
	s.dispatchAfterExitLocked(state, exit)
}

// killCgroupTree sends a signal to all processes in the service's cgroup.
// Used when kill-all-on-stop is set to ensure the entire process tree is terminated.
func (s *ProcessService) killCgroupTree(sig syscall.Signal) {
	cgPath := s.OwnCgroupPath()
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

	// The previous run's state hook (a finish-command, say) is still
	// running: a new run must not begin under it (statehook.go).
	if s.bringUpBlockedLocked() {
		return true
	}

	// A stop that was still waiting for its dependents, and so had not
	// yet touched the process, has been cancelled by this start: the
	// process it would have stopped is still running. Forking another
	// orphaned that one (it ran on, unsupervised, and its exit was
	// taken for the new run's). It is the service's process still;
	// the start is over.
	if s.pid > 0 {
		if s.stopIssued || s.stopPID > 0 {
			// Being stopped after all: not expected, since a stop
			// that has begun is not cancelled. Wait for the exit,
			// which looks at the start again (dispatchAfterExitLocked).
			s.services.logger.Error("Service '%s': start while its process %d is being stopped; it waits for the exit",
				s.serviceName, s.pid)
			s.waitingForDeps = true
			s.dropStartSlot()
			return true
		}
		s.services.logger.Info("Service '%s': stop cancelled, keeping process %d",
			s.serviceName, s.pid)
		s.Started()
		return true
	}

	if err := s.Record().resolveDeferredRunAs(); err != nil {
		s.services.logger.Error("Service '%s': %v", s.serviceName, err)
		return false
	}

	// Dynamic-user: allocate a transient UID/GID from the pool. This
	// must happen before any UID-dependent setup (ServiceDirs chown,
	// credentials chown) so they all see the same effective identity.
	if err := s.Record().allocateDynamicUID(); err != nil {
		s.services.logger.Error("Service '%s': dynamic-user: %v", s.serviceName, err)
		return false
	}

	// fd-store setup: open $NOTIFY_SOCKET when the feature is enabled
	// so the child can sd_notify FDSTORE=1 us back. The socket lives
	// at /run/slinit/notify/<svc>.sock, owned by the run-as user.
	if s.Record().FDStoreMax() > 0 || s.readySD {
		path, err := s.Record().setupNotifySocket(s.effectiveRunAsUID(), s.effectiveRunAsGID())
		if err != nil {
			s.services.logger.Error("Service '%s': $NOTIFY_SOCKET setup: %v", s.serviceName, err)
			return false
		}
		_ = path // exported into ExecParams below
	}

	// Evaluate systemd-style start preconditions. assert-* failure aborts
	// the start; condition-* failure short-circuits to STARTED with no
	// process so dependents see a satisfied dep.
	//
	// An environment kept for a fork that never came (an earlier start
	// that failed before it) is not this start's.
	s.discardStartEnv()
	// An exec-condition runs a command, for up to execConditionTimeout:
	// the predicates are then evaluated off the lock, as a state hook
	// (execConditionDoneLocked carries the start on).
	if s.hasExecCondition() {
		s.startExecConditionLocked()
		return true
	}
	switch outcome, reason := s.CheckPredicates(); outcome {
	case PredFailed:
		s.services.logger.Error("Service '%s': %s", s.serviceName, reason)
		return false
	case PredSkip:
		s.services.logger.Info("Service '%s': skipped (%s)", s.serviceName, reason)
		s.markSkippedStartReason(reason)
		return true
	}
	return s.bringUpCheckedLocked()
}

// startExecConditionLocked evaluates the start's predicates, one of
// them an exec-condition, off the lock: the service stays STARTING
// meanwhile, and a stop ends the evaluation, killing the command. An
// exec-condition runs with the start's environment, so with an
// env-generator that is computed first, once, and the pre-start-command
// and the fork reuse it. A service-started predicate asks about the
// service set and is answered under the lock, by the completion; the
// verdict is the one CheckPredicates gives (combinePredicates). Caller
// holds queueMu.
func (s *ProcessService) startExecConditionLocked() {
	preds := s.predicates
	res := make([]predResult, len(preds))
	envGen := s.envGenerator != ""
	// Reads the service's settings when the command is prepared, off
	// the lock: a reload refuses to change them while a state hook
	// runs (RunningStateHook).
	prep := prepareExecCondition(s)
	s.startStateHookLocked(hookExecCondition,
		func(ctx context.Context) func() error {
			return func() error {
				if envGen {
					s.prepareStartEnvCtx(ctx)
				}
				for i, p := range preds {
					if ctx.Err() != nil {
						break
					}
					if p.Kind != PredServiceStarted {
						res[i].ok, res[i].why = p.evaluateCtx(ctx, prep)
					}
				}
				return ctx.Err()
			}
		},
		func(_ error, cancelled bool) {
			s.execConditionDoneLocked(preds, res, cancelled)
		})
}

// execConditionDoneLocked carries a start on once its predicates have
// been evaluated (startExecConditionLocked): as BringUp did when it
// evaluated them itself. Caller holds queueMu.
func (s *ProcessService) execConditionDoneLocked(preds []Predicate, res []predResult, cancelled bool) {
	switch s.state.Load() {
	case StateStarting:
	case StateStopping:
		// A stop came while the predicates were evaluated, and ended
		// that: the start ends here, and the stop, which waited for
		// the hook, goes on.
		s.discardStartEnv()
		s.services.AddTransitionQueue(s)
		return
	default:
		s.discardStartEnv()
		return
	}
	if cancelled {
		// Cancelled with no stop asked for: the shutdown's final kill.
		s.services.logger.Error("Service '%s': exec-condition ended", s.serviceName)
		s.discardStartEnv()
		s.state.Store(StateStopping)
		s.failedToStart(false, true)
		return
	}
	for i, p := range preds {
		if p.Kind == PredServiceStarted {
			res[i].ok, res[i].why = s.checkServiceStarted(p)
		}
	}
	switch outcome, reason := combinePredicates(preds, res); outcome {
	case PredFailed:
		s.services.logger.Error("Service '%s': %s", s.serviceName, reason)
		s.state.Store(StateStopping)
		s.failedToStart(false, true)
		return
	case PredSkip:
		s.services.logger.Info("Service '%s': skipped (%s)", s.serviceName, reason)
		s.markSkippedStartReason(reason)
		return
	}
	if !s.bringUpCheckedLocked() {
		// As callBringUp does for a BringUp that fails.
		s.state.Store(StateStopping)
		s.failedToStart(false, true)
	}
}

// bringUpCheckedLocked is the rest of BringUp once the start's
// predicates have passed: required paths, the activation socket, the
// pre-start-command and the launch. False means the start failed.
// Caller holds queueMu.
func (s *ProcessService) bringUpCheckedLocked() bool {
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

	// socket-activation = on-demand: the sockets are open and accepting,
	// which is all a dependent needs, so the service is STARTED now and
	// the process waits for the first client (see ondemand.go).
	if s.socketOnDemand && s.socketFD != nil {
		s.demandTriggers = nil
		s.demandLastClean = false
		if err := s.armOnDemand(); err != nil {
			s.services.logger.Error("Service '%s': socket activation: %v", s.serviceName, err)
			return false
		}
		s.services.logger.Info("Service '%s': listening, process starts on the first client",
			s.serviceName)
		// The first client may come much later: the launch then
		// computes its environment.
		s.discardStartEnv()
		s.Started()
		return true
	}

	// systemd-style ExecStartPre=: the fork waits for it, and a
	// non-zero exit fails the start. Runs after sandbox prep / required
	// paths but before the main fork, so a failed pre-hook never leaves
	// a half-started process behind.
	//
	// It runs off the lock, as a state hook (statehook.go), with the
	// service STARTING; preStartDoneLocked carries the start on. So
	// does the env-generator: the environment of this start is computed
	// there too, so the pre-start-command sees what the main process
	// will get and startProcess uses it rather than running the
	// generator under the lock. An exec-condition may have computed it
	// already (startExecConditionLocked).
	if len(s.preStartCommand) > 0 || s.envGenerator != "" {
		argv := s.preStartCommand
		cred := s.hookCredential(s.privHooks.PreStart)
		s.startStateHookLocked(hookPreStart,
			func(ctx context.Context) func() error {
				return func() error {
					s.ensureStartEnvCtx(ctx)
					if len(argv) == 0 || ctx.Err() != nil {
						return ctx.Err()
					}
					return s.runHookCommandCtx(ctx, argv, "pre-start-command", cred)
				}
			},
			s.preStartDoneLocked)
		return true
	}
	return s.launchLocked()
}

// preStartDoneLocked carries a start on once its pre-start hook
// (statehook.go) has run. Caller holds queueMu.
func (s *ProcessService) preStartDoneLocked(err error, cancelled bool) {
	switch s.state.Load() {
	case StateStarting:
		switch {
		case err == nil && !cancelled:
			if s.launchLocked() {
				return
			}
		case err != nil && !cancelled:
			s.services.logger.Error("Service '%s': pre-start-command failed: %v",
				s.serviceName, err)
		default:
			// Cancelled with no stop asked for: the shutdown's
			// final kill.
			s.services.logger.Error("Service '%s': pre-start-command ended: %v",
				s.serviceName, err)
		}
		s.discardStartEnv()
		s.state.Store(StateStopping)
		s.failedToStart(false, true)
	case StateStopping:
		// A stop came while the hook ran, and ended it: the start ends
		// here, and the stop, which waited for the hook, goes on.
		s.discardStartEnv()
		s.services.AddTransitionQueue(s)
	default:
		s.discardStartEnv()
	}
}

// launchLocked is the rest of a start once its pre-start-command has
// run: start-when-idle and start-delay, the fork, the post-start-command.
// False means the fork failed. Caller holds queueMu.
func (s *ProcessService) launchLocked() bool {
	// launchLater is the launch from a timer's callback, where a failure
	// can no longer be BringUp's return value.
	launchLater := func() {
		if err := s.startProcess(); err != nil {
			s.services.logger.Error("Service '%s': failed to start: %v",
				s.serviceName, err)
			s.failedToStart(false, true)
			return
		}
		s.launchPostStartCommand()
	}

	// start-when-idle, then start-delay: hold off the fork/exec without
	// holding the scheduling lock. The service stays STARTING until the
	// timer fires, so a stop arriving meanwhile still finds it actionable.
	if s.idleWaitTimer(func() {
		if !s.startDelayTimer(launchLater) {
			launchLater()
		}
	}) {
		return true
	}
	if s.startDelayTimer(launchLater) {
		return true
	}

	if err := s.startProcess(); err != nil {
		s.services.logger.Error("Service '%s': failed to start: %v", s.serviceName, err)
		return false
	}

	// Note: startProcess() calls Started() immediately (no readiness protocol),
	// so no start timeout is needed here. If a readiness protocol is added later,
	// the start timeout should be armed BEFORE startProcess() and cancelled
	// inside startProcess() when readiness is confirmed.

	s.launchPostStartCommand()
	return true
}

// launchPostStartCommand runs the systemd-style ExecStartPost= hook:
// asynchronous, exit code only logged. Runs in a goroutine so a slow
// hook doesn't block the scheduling loop. Called after a successful
// startProcess on both the immediate and the start-delay path.
//
// The run belongs to this start. A previous start's run still going is
// ended (its process group killed) and waited for first, and BringDown
// ends it too. Before, nothing ended it short of hook-timeout: it ran on
// after the service had stopped, and a quick stop and start had two
// runs overlapping.
func (s *ProcessService) launchPostStartCommand() {
	if len(s.postStartCommand) == 0 {
		return
	}
	s.cancelPostStart(true)
	// Read here, under the caller's lock, not in the goroutine: a
	// reload may replace the command while this run is under way.
	argv := s.postStartCommand
	cred := s.hookCredential(s.privHooks.PostStart)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.postStartMu.Lock()
	s.postStartCancel, s.postStartDone = cancel, done
	s.postStartMu.Unlock()
	go func() {
		defer close(done)
		defer cancel()
		err := s.runHookCommandCtx(ctx, argv, "post-start-command", cred)
		switch {
		case err == nil:
		case ctx.Err() != nil:
			s.services.logger.Info("Service '%s': post-start-command ended: the service is stopping or restarting",
				s.serviceName)
		default:
			s.services.logger.Error("Service '%s': post-start-command failed: %v",
				s.serviceName, err)
		}
	}()
}

// cancelPostStart ends a post-start-command still running, killing its
// process group, and with wait set waits until it has gone.
func (s *ProcessService) cancelPostStart(wait bool) {
	s.postStartMu.Lock()
	cancel, done := s.postStartCancel, s.postStartDone
	s.postStartCancel, s.postStartDone = nil, nil
	s.postStartMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if wait && done != nil {
		<-done
	}
}

// runHookCommand executes a one-shot hook (pre-start-command,
// post-start-command) using the same working-dir / env / timeout as
// finish-command. Synchronous; returns the exec.Cmd error.
//
// cred is hookCredential's: the service's run-as user and groups, as
// systemd runs ExecStartPre= and ExecStartPost= as User=, or nil for
// root (a "+" command, or no run-as). Until 4.0.3 both hooks always ran
// as root.
func (s *ProcessService) runHookCommand(cmd []string, label string, cred *syscall.Credential) error {
	return s.runHookCommandCtx(context.Background(), cmd, label, cred)
}

// runHookCommandCtx is runHookCommand ended early by parent.
func (s *ProcessService) runHookCommandCtx(parent context.Context, cmd []string, label string, cred *syscall.Credential) error {
	if len(cmd) == 0 {
		return nil
	}
	timeout := s.effectiveHookTimeout()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	c := newHookCmd(ctx, cmd)
	c.SysProcAttr.Credential = cred // newHookCmd made SysProcAttr
	c.Dir = s.workingDir
	c.Env = s.hookCmdEnv()
	s.services.logger.Info("Service '%s': running %s", s.serviceName, label)
	return annotateHookTimeout(process.RunAdhoc(c), ctx, timeout)
}

// effectiveHookTimeout returns the configured hook-timeout, or the
// built-in default when unset.
func (s *ProcessService) effectiveHookTimeout() time.Duration {
	if s.hookTimeout > 0 {
		return s.hookTimeout
	}
	return defaultFinishTimeout
}

// SetHookTimeout sets the bound for the hooks. Values <= 0
// select the default.
func (s *ProcessService) SetHookTimeout(d time.Duration) { s.hookTimeout = d }

// annotateHookTimeout turns the kill into an explanation. A hook that
// outran its deadline was reported as "signal: killed", which names
// the mechanism and hides the cause: nothing told the operator that
// slinit's own timeout did it, or what the timeout was, so the only
// way to find out was to read the source.
func annotateHookTimeout(err error, ctx context.Context, timeout time.Duration) error {
	if err == nil {
		return nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("timed out after %v (hook-timeout): %w", timeout, err)
	}
	return err
}

// BringDown stops the service process.
// If a pre-stop-hook is configured, it runs first, off the lock and bounded
// by hook-timeout; the service stays STOPPING and the hook's completion
// goes on with the rest (issueStopLocked). Then if a stop-command is
// configured, it is executed. If it fails to start, we fall back to
// sending the termination signal directly.
func (s *ProcessService) BringDown() {
	// A start-delay still counting down has nothing left to launch, nor
	// has a start-when-idle wait, nor a smooth recovery waiting out
	// restart-delay.
	s.cancelStartDelay()
	s.cancelIdleWait()
	s.cancelSmoothRecovery()
	// Nor does a socket watcher still waiting for its first client.
	s.disarmOnDemand()
	// A post-start-command still running ends with the service.
	s.cancelPostStart(false)
	// Stop health checker and cron runner if active
	s.stopHealthChecker()
	s.stopCronRunner()
	s.stopWatchdogWatcher()

	// Close readiness pipe if still open (no longer waiting for readiness)
	s.closeReadyPipe()

	// A state hook is running off the lock (statehook.go). Its
	// completion carries the stop on: a pre-stop-hook or
	// control-command goes on to the stop signal, a finish-command to
	// the exit's dispatch, which finds the service STOPPING. A pre-start
	// has nothing left to do once the start is called off, so it is
	// ended now.
	if h := s.stateHook.Load(); h != nil {
		if h.kind.endsWithStop() {
			h.cancel()
		}
		return
	}

	if s.pid <= 0 {
		// Process already dead
		s.cancelTimer()
		s.Stopped()
		return
	}

	if s.stopPID > 0 || s.stopIssued {
		return
	}

	// Run pre-stop-hook before any stop action. It runs off the lock;
	// its completion issues the stop (issueStopLocked), or, if the
	// process has exited meanwhile, handles that exit.
	if len(s.preStopHook) > 0 {
		pid := s.pid
		s.startStateHookLocked(hookPreStop, s.preparePreStopHook, func(error, bool) {
			if exit, ok := s.takePendingExit(pid); ok {
				s.afterExitLocked(exit)
				return
			}
			if s.state.Load() != StateStopping || s.pid <= 0 {
				// Nothing else moves a service out of STOPPING
				// while the hook runs; should it, let the
				// transition look again.
				if s.state.Load() == StateStopping {
					s.services.AddTransitionQueue(s)
				}
				return
			}
			s.issueStopLocked()
		})
		return
	}
	s.issueStopLocked()
}

// issueStopLocked is the stop proper, once any pre-stop-hook has run:
// the stop-command, or the stop signal (or the control-command that
// stands in for it), then the stop-timeout. Caller holds queueMu.
func (s *ProcessService) issueStopLocked() {
	// Try stop-command first (like dinit's process_service::bring_down)
	if len(s.stopCommand) > 0 {
		if s.execStopCommand() {
			s.stopIssued = true
			s.resumeForStop()
			if s.stopTimeout > 0 {
				s.armTimer(s.stopTimeout, timerStopTimeout)
			}
			return
		}
		// stop-command failed to start; fall through to signal
	}

	// systemd KillMode=none — skip the primary signal entirely. The
	// service is expected to terminate on its own or via a
	// control-command triggered elsewhere. We still arm the stop
	// timeout so an unresponsive service escalates to
	// final-kill-signal (unless survive-final-kill-signal is set).
	if s.Record().KillMode() == KillModeNone {
		s.services.logger.Info("Service '%s': kill-mode=none, skipping primary signal",
			s.serviceName)
		s.stopIssued = true
		if s.stopTimeout > 0 {
			s.armTimer(s.stopTimeout, timerStopTimeout)
		}
		return
	}

	// Send termination signal (or run control-command if configured).
	// The picker honours watchdog-signal (via pendingStopSignal) and
	// restart-kill-signal in addition to term-signal.
	sig := s.pickStopSignal()

	sigName := signalName(sig)
	if cmd, ok := s.controlCommands[sigName]; ok && len(cmd) > 0 {
		pid := s.pid
		// Run custom control command instead of raw signal, off the
		// lock. What follows the signal follows the command's
		// completion, as it followed the command before.
		s.startStateHookLocked(hookStopControl,
			func(ctx context.Context) func() error {
				return s.prepareControlCommandCtx(ctx, sigName, cmd)
			},
			func(error, bool) {
				s.stopSignalSentLocked(sig)
				if exit, ok := s.takePendingExit(pid); ok {
					s.afterExitLocked(exit)
					return
				}
				if s.stopTimeout > 0 {
					s.armTimer(s.stopTimeout, timerStopTimeout)
				}
			})
		return
	}

	s.services.logger.Info("Service '%s': sending %s to process %d",
		s.serviceName, signalName(sig), s.pid)

	err := process.SignalProcess(s.pid, sig, s.Flags.SignalProcessOnly)
	if err != nil {
		s.logStopSignalFailure(err)
	}
	s.stopSignalSentLocked(sig)

	// Arm stop timeout for SIGKILL escalation
	if s.stopTimeout > 0 {
		s.armTimer(s.stopTimeout, timerStopTimeout)
	}
}

// stopSignalSentLocked is what follows the stop signal, or the
// control-command sent in its place. Caller holds queueMu.
func (s *ProcessService) stopSignalSentLocked(sig syscall.Signal) {
	s.resumeForStop()

	s.stopIssued = true

	// Kill entire cgroup process tree if configured. kill-mode=mixed
	// only touches the group at escalation time, so during the
	// primary-signal phase we only widen if kill-all-on-stop or
	// kill-mode=control-group is set.
	if s.Flags.KillAllOnStop || s.Record().KillMode() == KillModeControlGroup {
		s.killCgroupTree(sig)
	}
}

// resumeForStop sends SIGCONT to a paused service during its stop. A
// SIGSTOPped process keeps the stop signal pending until it is continued,
// so only the stop-timeout's SIGKILL would end it, and with stop-timeout
// = 0 nothing would; a stop-command cannot get an answer from it either.
// systemd continues every process it sends a stop signal to for the same
// reason (kill_and_sigcont). The pending signal is delivered as the
// process wakes. Called with queueMu held.
func (s *ProcessService) resumeForStop() {
	if !s.paused {
		return
	}
	s.paused = false
	if err := process.SignalProcess(s.pid, syscall.SIGCONT, s.Flags.SignalProcessOnly); err != nil &&
		!errors.Is(err, syscall.ESRCH) {
		s.services.logger.Error("Service '%s': SIGCONT on stop failed: %v", s.serviceName, err)
	}
}

// logStopSignalFailure reports a failure to deliver a stop signal. ESRCH —
// "no such process" — means the process, or its whole group, is already
// gone: a service that exits the moment a stop begins (a pipe consumer whose
// writers have just stopped, say) or one reaped a hair before we signalled
// it. During a stop that is the outcome we wanted, not a failure, so it is
// noted at info rather than logged as an error. Any other failure is a real
// error. (KillProcessGroup already treats an empty group's ESRCH as benign.)
func (s *ServiceRecord) logStopSignalFailure(err error) {
	if errors.Is(err, syscall.ESRCH) {
		s.services.logger.Info("Service '%s': already exited before the stop signal",
			s.serviceName)
		return
	}
	s.services.logger.Error("Service '%s': failed to signal process: %v",
		s.serviceName, err)
}

// execStopCommand starts the stop-command process. Returns true if it was
// launched successfully. The stop-command runs independently; when it exits,
// the monitoring goroutine receives the event via stopExitCh and then signals
// the main process if it is still alive.
func (s *ProcessService) execStopCommand() bool {
	params := process.ExecParams{
		Command:           s.stopCommand,
		WorkingDir:        s.workingDir,
		Env:               s.hookEnv(),
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
		s.services.logger.Error("Service '%s': failed to start stop-command: %v",
			s.serviceName, err)
		return false
	}

	s.stopPID = pid
	s.services.logger.Info("Service '%s': stop-command started (pid %d)", s.serviceName, pid)

	// Monitor stop-command in a goroutine.
	//
	// It takes queueMu for the same reason every other writer of this
	// state does: stopPID and pid are read by the state machine, and
	// this goroutine used to write stopPID with no lock at all. The
	// signal is sent outside the lock, since SignalProcess can block
	// and nothing in the state machine should wait on it.
	go func() {
		// The stop-command's own timeout. It cannot lean on the
		// stop-timeout armed by the caller: processTimer is read only
		// by monitorProcess, which returns when the main process exits,
		// and the usual shape of a stop-command is to kill that process
		// first. So past that moment the caller's timer has no reader
		// and a wedged stop-command would hold the service in STOPPING
		// for good — measured at 20s with stop-timeout = 4 before this
		// existed.
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
			s.services.queueMu.Lock()
			pid := s.stopPID
			s.services.queueMu.Unlock()
			if pid > 0 {
				s.services.logger.Error(
					"Service '%s': stop-command exceeded stop-timeout, killing (pid %d)",
					s.serviceName, pid)
				process.SignalProcess(pid, syscall.SIGKILL, false)
			}
			// Still wait for the exit so the transition below runs
			// exactly once, from one place.
			exit = <-exitCh
		}
		process.KillProcessGroup(exit.PID)

		s.services.queueMu.Lock()
		s.stopPID = 0
		mainGone := s.mainGoneDuringStop
		mainPID := s.pid
		if mainGone {
			// The main process already exited while this command was
			// running, so its exit could not complete the stop. Do it
			// here instead — this is the transition dinit 4da302f /
			// 798e037 guard for.
			s.mainGoneDuringStop = false
			s.stopIssued = false
			s.Stopped()
			s.services.processQueuesLocked()
		}
		s.services.queueMu.Unlock()

		if exit.Exited() && exit.Status.ExitStatus() == 0 {
			s.services.logger.Info("Service '%s': stop-command completed successfully",
				s.serviceName)
		} else {
			s.services.logger.Error("Service '%s': stop-command failed (%s)",
				s.serviceName, exitDetail(exit))
		}
		// Signal the main process if it is still there. Skipped when it
		// has already gone: the pid would either be stale or, worse,
		// reused by something unrelated.
		//
		// The message about it is inside the branch on purpose. It used
		// to be part of the failure line above, which then announced
		// "sending signal" whether or not one was sent — and after the
		// stop-command transition started waiting for the main process
		// to be gone, the common case became exactly the one where no
		// signal follows.
		if !mainGone && mainPID > 0 {
			sig := s.termSignal
			if sig == 0 {
				sig = syscall.SIGTERM
			}
			s.services.logger.Info("Service '%s': sending %s to the main process (pid %d)",
				s.serviceName, signalName(sig), mainPID)
			process.SignalProcess(mainPID, sig, s.Flags.SignalProcessOnly)
		}
	}()

	return true
}

// CanInterruptStart returns true if the starting process can be interrupted.
func (s *ProcessService) CanInterruptStart() bool {
	if s.waitingForDeps || s.waitingToLaunch() || s.stateHookRunning() {
		return true
	}
	// Can interrupt if process is running (we'll send SIGINT)
	return s.pid > 0
}

// waitingToLaunch reports whether a start-delay or start-when-idle timer
// is still holding the fork/exec back. There is then no process, and a stop
// has only the timer to cancel; refusing it because there is no process to
// signal let the timer launch one just to stop it.
func (s *ProcessService) waitingToLaunch() bool {
	return s.startDelayT != nil || s.idleT != nil
}

// InterruptStart cancels the start by sending SIGINT to the process. With
// a process to wait for it returns false, leaving the service STOPPING
// (see stopInterruptedStart), as dinit's issue_start_interrupt does.
func (s *ProcessService) InterruptStart() bool {
	if s.waitingForDeps {
		return true
	}
	if s.waitingToLaunch() {
		s.cancelStartDelay()
		s.cancelIdleWait()
		return true
	}
	// A state hook is running: a pre-start-command, ended here (its
	// process group killed), or the finish-command of a process that
	// died while starting, left to finish. There is no process; the
	// service goes STOPPING and the hook's completion finishes the stop.
	if h := s.stateHook.Load(); h != nil {
		if h.kind.endsWithStop() {
			h.cancel()
		}
		return true
	}

	if s.pid > 0 {
		s.services.logger.Info("Service '%s': interrupting start (SIGINT to %d)",
			s.serviceName, s.pid)
		process.SignalProcess(s.pid, syscall.SIGINT, s.Flags.SignalProcessOnly)
		// Can't immediately transition; need to wait for process to die
		s.stopInterruptedStart()
		return false
	}

	return true
}

// stopInterruptedStart moves a start whose process has just been told to
// give up — by a stop arriving while STARTING, or by the start-timeout —
// to STOPPING with the stop-timeout armed, as dinit's
// issue_start_interrupt does. The process's exit then ends the stop like
// any other (handleChildExit), and a process that ignores the signal is
// escalated by the stop-timeout like any other. Left in STARTING with no
// timer, a process ignoring SIGINT held the service there for good, and
// its exit was taken for a failed start: a cancelled start was reported
// as a failure, a timed-out one three times.
func (s *ProcessService) stopInterruptedStart() {
	if s.stopTimeout > 0 {
		s.armTimer(s.stopTimeout, timerStopTimeout)
	} else {
		s.cancelTimer()
	}
	s.state.Store(StateStopping)
	// The service has left STARTING: give its start slot back.
	s.dropStartSlot()
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

// prepareStartEnv computes the environment for a start that is about to
// run its pre-start-command, and keeps it for the fork.
func (s *ProcessService) prepareStartEnv() {
	s.prepareStartEnvCtx(context.Background())
}

// prepareStartEnvCtx is prepareStartEnv with an env-generator run that
// ctx can end early.
func (s *ProcessService) prepareStartEnvCtx(ctx context.Context) {
	env := s.buildEnvCtx(ctx)
	s.envMu.Lock()
	s.startEnv, s.startEnvFresh = env, true
	s.envMu.Unlock()
}

// launchEnv returns the environment for startProcess's fork: the one
// prepareStartEnv computed for this start if the fork has not used it
// yet, otherwise a new one (a restart, a smooth recovery). Either way it
// becomes the environment the hooks of this run see.
func (s *ProcessService) launchEnv() []string {
	s.envMu.Lock()
	if s.startEnvFresh {
		s.startEnvFresh = false
		env := s.startEnv
		s.envMu.Unlock()
		return env
	}
	s.envMu.Unlock()
	env := s.buildEnv()
	s.envMu.Lock()
	s.startEnv = env
	s.envMu.Unlock()
	return env
}

// withWatchdogUsec adds $WATCHDOG_USEC for an sd_notify service with a
// watchdog-timeout: sd_watchdog_enabled() reads it to learn that a
// watchdog is armed and how often to send WATCHDOG=1 (systemd sets it
// from WatchdogSec=). The pipe-based readiness modes have no such
// variable, so it is only for sd-notify. A copy, so the cached start
// environment stays as it was.
func (s *ProcessService) withWatchdogUsec(env []string) []string {
	if !s.readySD || s.watchdogTimeout <= 0 {
		return env
	}
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if !strings.HasPrefix(e, "WATCHDOG_USEC=") {
			out = append(out, e)
		}
	}
	return append(out, "WATCHDOG_USEC="+strconv.FormatInt(s.watchdogTimeout.Microseconds(), 10))
}

// ensureStartEnv is prepareStartEnv unless this start has computed its
// environment already (for an exec-condition) and the fork has not used
// it yet.
func (s *ProcessService) ensureStartEnv() {
	s.ensureStartEnvCtx(context.Background())
}

// ensureStartEnvCtx is ensureStartEnv with an env-generator run that
// ctx can end early.
func (s *ProcessService) ensureStartEnvCtx(ctx context.Context) {
	s.envMu.Lock()
	fresh := s.startEnvFresh
	s.envMu.Unlock()
	if !fresh {
		s.prepareStartEnvCtx(ctx)
	}
}

// hasExecCondition reports whether an exec-condition is among the
// service's predicates.
func (s *ProcessService) hasExecCondition() bool {
	for _, p := range s.predicates {
		if p.Kind == PredExecCondition {
			return true
		}
	}
	return false
}

// discardStartEnv drops the environment prepareStartEnv kept for a fork
// that is not going to happen (pre-start-command failed), so a later
// start does not take it for its own.
func (s *ProcessService) discardStartEnv() {
	s.envMu.Lock()
	s.startEnvFresh = false
	s.envMu.Unlock()
}

// hookEnv returns the environment for a command run on behalf of the
// current start: post-start-command, ready-check-command,
// finish-command, pre-stop-hook, stop-command, control-command-*.
//
// With an env-generator it is the start's environment, so the generator
// runs once per start — the man page's "at start time" — rather than
// once per hook and per ready-check poll, under the scheduling lock for
// most of them. Only a service never started computes one here.
//
// Without one it is computed afresh, as it always was: nothing costly
// runs, and a variable set with slinitctl setenv reaches the next hook
// of the running instance. Freezing it for every service took that
// away.
func (s *ProcessService) hookEnv() []string {
	if s.envGenerator == "" {
		return s.buildEnv()
	}
	s.envMu.Lock()
	env := s.startEnv
	s.envMu.Unlock()
	if env != nil {
		return env
	}
	env = s.buildEnv()
	s.envMu.Lock()
	if s.startEnv == nil {
		s.startEnv = env
	}
	s.envMu.Unlock()
	return env
}

// hookCmdEnv is hookEnv as a command's whole environment: slinit's own
// variables (filtered as the main process's are) and then the service's.
// Setting cmd.Env to hookEnv alone dropped PATH, HOME and the rest of
// what a hook inherited when Env was nil, as soon as the service had a
// variable of its own.
func (s *ProcessService) hookCmdEnv() []string {
	return s.Record().withInheritedEnv(s.hookEnv())
}

// buildEnv merges env-file, env-dir, and runtime extraEnv into a single slice.
func (s *ProcessService) buildEnv() []string {
	return s.buildEnvCtx(context.Background())
}

// buildEnvCtx is buildEnv with an env-generator run that ctx can end
// early.
func (s *ProcessService) buildEnvCtx(ctx context.Context) []string {
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

	// env-generator (systemd EnvironmentGenerator sibling): run the
	// helper and merge its stdout as KEY=VALUE lines. Merged AFTER
	// env-file/env-dir so it can override them per systemd semantics.
	// Errors are logged but don't fail the start here (startProcess
	// separately gates on a strict-mode invocation if we ever add it);
	// keeping this a soft failure prevents a misconfigured generator
	// from turning into a system-wide boot outage.
	if s.envGenerator != "" {
		genEnv, err := s.runEnvGenerator(ctx)
		if err != nil {
			s.services.logger.Error("Service '%s': env-generator '%s' failed: %v",
				s.serviceName, s.envGenerator, err)
		} else {
			for k, v := range genEnv {
				entry := k + "=" + v
				replaced := false
				for i, e := range env {
					if strings.HasPrefix(e, k+"=") {
						env[i] = entry
						replaced = true
						break
					}
				}
				if !replaced {
					env = append(env, entry)
				}
			}
		}
	}
	return env
}

// envGeneratorMaxOutput caps what PID 1 keeps of an env-generator's
// stdout. Output beyond it fails the generator rather than being cut at
// an arbitrary byte, which could leave half a value in the environment.
const envGeneratorMaxOutput = 64 << 10

// runEnvGenerator invokes the service's env-generator and parses its
// stdout as KEY=VALUE lines. Blank lines and lines beginning with '#'
// are skipped so the generator can be a hand-written shell script with
// comments. Non-zero exit yields an error.
//
// It is bounded like the other hooks: hook-timeout (or parent ending),
// then SIGKILL to its process group. Before, a generator that hung
// froze every start and stop in the system for as long as it hung — it
// ran under the scheduling lock, as it still does for an exec-condition
// (BringUp) — and its output was read into an unbounded buffer inside
// PID 1. It runs as the service's run-as user, as the service's other
// commands do.
func (s *ProcessService) runEnvGenerator(parent context.Context) (map[string]string, error) {
	timeout := s.effectiveHookTimeout()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := newHookCmd(ctx, []string{s.envGenerator})
	s.applyRunAs(cmd)
	out := &cappedBuffer{max: envGeneratorMaxOutput}
	cmd.Stdout = out
	if err := annotateHookTimeout(process.RunAdhoc(cmd), ctx, timeout); err != nil {
		return nil, err
	}
	if out.overflow {
		return nil, fmt.Errorf("output exceeds %d bytes", envGeneratorMaxOutput)
	}
	return parseEnvGeneratorOutput(out.buf.Bytes()), nil
}

// parseEnvGeneratorOutput turns KEY=VALUE lines into a map, skipping
// blank lines, comments and lines without a name before the '='.
func parseEnvGeneratorOutput(out []byte) map[string]string {
	kv := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		kv[line[:i]] = line[i+1:]
	}
	return kv
}

// cappedBuffer keeps the first max bytes written to it and notes that
// more arrived. It goes on accepting (and dropping) the rest, so the
// writer is never blocked on a full pipe.
type cappedBuffer struct {
	buf      bytes.Buffer
	max      int
	overflow bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room < len(p) {
		c.overflow = true
		if room > 0 {
			c.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}

// startProcess forks and execs the service process.
func (s *ProcessService) startProcess() error {
	// Crash-shell freeze: catches the smooth-recovery restart path
	// too (doSmoothRecovery → startProcess directly, bypassing
	// callBringUp). Without this check, tty svc with
	// smooth-recovery=yes respawns bash into /dev/console while
	// pkg/shutdown.spawnCrashShell is trying to hold the tty for
	// sulogin. Return a sentinel error so the caller logs it and
	// gives up cleanly.
	if s.services.IsCrashPaused() {
		return fmt.Errorf("service '%s': start suppressed (crash-shell freeze)", s.serviceName)
	}
	s.lastStartTime = time.Now()
	s.startDelayDone = false
	s.stopIssued = false
	s.exitStatus = ExitStatus{}
	// An exit parked for a stop hook belongs to the run before this one.
	s.pendingExit = nil

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
	} else if (s.logType == LogToFile && s.logFile != "") || s.logType == LogToJournal {
		// Use LogRotator if rotation, filtering, processing, rate
		// limit, or severity filter is configured, and always for the
		// journal, which only the rotator feeds.
		if s.logType == LogToJournal || s.logMaxSize > 0 || s.logMaxFiles > 0 || s.logRotateTime > 0 ||
			len(s.logProcessor) > 0 || len(s.logIncludes) > 0 || len(s.logExcludes) > 0 ||
			len(s.logSelect) > 0 ||
			(s.logRateLimitInterval > 0 && s.logRateLimitBurst > 0) ||
			s.logLevelMax >= 0 ||
			s.syslogIdent != "" || s.syslogLevel >= 0 || s.noLevelPrefix || len(s.logExtraFields) > 0 ||
			s.logSanitizeChar != 0 || len(s.logSanitizeExtra) > 0 ||
			s.logMaxLineLength > 0 ||
			s.logTimestampMode != "" || s.logLinePrefix != "" ||
			s.logForwardUDP != "" ||
			len(s.outputLogger) > 0 ||
			s.stderrLogFile != "" ||
			s.alertFile != "" {
			// A restart replaces the previous instance's logger along
			// with its rotator. Stop the old logger first: until now it
			// was overwritten below and leaked. Stopping it closes the
			// pipe's read end, so a rotator write blocked on a logger
			// that reads nothing fails with EPIPE, and Close() below —
			// under queueMu — does not wait on that logger.
			if s.loggerSup != nil {
				s.loggerSup.Stop()
				s.loggerSup = nil
			}
			if s.logRotator != nil {
				s.logRotator.Close()
			}
			// Build the UDP forwarder before the rotator so we can
			// bail out cleanly if the destination doesn't resolve.
			// output-logger beside a logfile: the service writes to
			// the rotator, and the rotator copies each accepted line
			// into the logger's stdin. Both destinations are honoured
			// instead of one silently winning, and because slinit —
			// not the service — writes to the logger, a logger that
			// exits cannot kill the service with SIGPIPE. One that
			// stops reading holds up the logfile and, once the pipe
			// fills, the service, as a full pipe would: lines are
			// never dropped for a logger that is merely slow.
			var loggerSink io.Writer
			if len(s.outputLogger) > 0 {
				w, sup, lerr := startLoggerSupervisor(
					s.outputLogger, s.serviceName, "output-logger",
					s.services.logger, s.hasRunningProcess)
				if lerr != nil {
					return fmt.Errorf("output-logger: %w", lerr)
				}
				s.loggerSup = sup
				loggerSink = w
			}

			var forwarder *SyslogForwarder
			if s.logForwardUDP != "" {
				fw, ferr := NewSyslogForwarder(
					s.logForwardUDP, s.logForwardFormat, s.logForwardFacility,
					s.logForwardTag, s.serviceName, s.services.logger)
				if ferr != nil {
					s.services.logger.Error("Service '%s': syslog-forward disabled (%v)",
						s.serviceName, ferr)
				} else {
					forwarder = fw
				}
			}
			cfg := s.rotatorConfigFor(s.logFile)
			cfg.Forwarder = forwarder
			cfg.Sink = loggerSink
			cfg.AlertFilePath = s.alertFile
			cfg.AlertLevel = s.alertLevel
			var err error
			s.logRotator, err = NewLogRotator(cfg)
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
			f, err := openLogFile(s.logFile, os.FileMode(s.logFilePerms))
			if err != nil {
				return fmt.Errorf("failed to open logfile '%s': %w", s.logFile, err)
			}
			if s.logFileUID >= 0 || s.logFileGID >= 0 {
				_ = f.Chown(s.logFileUID, s.logFileGID)
			}
			outputPipe = f
		}
	} else if s.logForwardUDP != "" {
		// Standalone syslog-forward: log-forward-udp is set but no
		// log-type / log-file / rotator-triggering directive
		// activated the LogToFile+LogRotator path above. Capture
		// stdout+stderr through a fresh pipe and ship each line
		// via UDP without touching disk. finit / svlogd u/U parity:
		// a service that only wants remote syslog shouldn't need
		// to declare a local log file too.
		fw, ferr := NewSyslogForwarder(
			s.logForwardUDP, s.logForwardFormat, s.logForwardFacility,
			s.logForwardTag, s.serviceName, s.services.logger)
		if ferr != nil {
			s.services.logger.Error("Service '%s': syslog-forward disabled (%v)",
				s.serviceName, ferr)
		} else {
			pr, pw, perr := os.Pipe()
			if perr != nil {
				fw.Close()
				s.services.logger.Error("Service '%s': syslog-forward pipe: %v",
					s.serviceName, perr)
			} else {
				outputPipe = pw
				s.syslogForwarderOnly = fw
				go s.forwardOnlyLoop(pr, fw)
			}
		}
	} else if s.logType == LogToCommand && len(s.outputLogger) > 0 {
		// Spawn an external logger command and pipe stdout (+ stderr unless
		// a separate error-logger is configured) to it. This is the
		// OpenRC OUTPUT_LOGGER equivalent.
		//
		// Only BecomingInactive stopped the previous start's logger, so
		// on an auto-restart it was overwritten here and left running,
		// one more logger supervisor and process per restart. It is
		// retired rather than killed, so that it still gets the last
		// lines the previous instance wrote.
		if s.loggerSup != nil {
			s.loggerSup.retire(loggerRetireGrace)
			s.loggerSup = nil
		}
		var err error
		outputPipe, s.loggerSup, err = startLoggerSupervisor(
			s.outputLogger, s.serviceName, "output-logger", s.services.logger,
			s.hasRunningProcess)
		if err != nil {
			return fmt.Errorf("output-logger: %w", err)
		}
	}

	// Optional separate stderr file: stderr gets its own rotator, so
	// the two streams land in two files instead of interleaving in one.
	var errorPipe *os.File
	if (s.logType == LogToFile || s.logType == LogToJournal) && s.stderrLogFile != "" {
		if s.stderrRotator != nil {
			s.stderrRotator.Close()
		}
		var rerr error
		s.stderrRotator, rerr = NewLogRotator(s.rotatorConfigForStderr(s.stderrLogFile))
		if rerr != nil {
			return fmt.Errorf("stderr-logfile: %w", rerr)
		}
		errorPipe, rerr = s.stderrRotator.CreatePipe()
		if rerr != nil {
			return fmt.Errorf("stderr-logfile pipe: %w", rerr)
		}
	}

	// Optional separate error-logger: stderr goes to a different command.
	if s.logType == LogToCommand && len(s.errorLogger) > 0 {
		if s.errLoggerSup != nil { // as for the output-logger above
			s.errLoggerSup.retire(loggerRetireGrace)
			s.errLoggerSup = nil
		}
		var err error
		errorPipe, s.errLoggerSup, err = startLoggerSupervisor(
			s.errorLogger, s.serviceName, "error-logger", s.services.logger,
			s.hasRunningProcess)
		if err != nil {
			// Clean up the output-logger we already started
			if s.loggerSup != nil {
				s.loggerSup.Stop()
				s.loggerSup = nil
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
			} else if outputPipe != nil && (s.logType == LogToFile || s.logType == LogToJournal) {
				outputPipe.Close()
			} else if s.syslogForwarderOnly != nil && outputPipe != nil {
				outputPipe.Close()
				s.syslogForwarderOnly.Close()
				s.syslogForwarderOnly = nil
			}
			return err
		}
		s.readyPipeRead = pr
		if s.readySD {
			// The service gets $NOTIFY_SOCKET, not the pipe; slinit writes
			// to it on READY=1 (sdNotify).
			s.setSDPipe(pw)
		} else {
			notifyPipeWrite = pw
		}
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
		Argv0:             s.argv0,
		WorkingDir:        s.workingDir,
		Env:               s.withWatchdogUsec(s.launchEnv()),
		TermSignal:        s.termSignal,
		OnConsole:         s.Flags.RunsOnConsole || s.Flags.StartsOnConsole,
		UnmaskSigint:      s.Flags.UnmaskIntr,
		SignalProcessOnly: s.Flags.SignalProcessOnly,
		RunAsUID:          s.effectiveRunAsUID(),
		RunAsGID:          s.effectiveRunAsGID(),
		SupplementaryGIDs: s.supplementaryGIDs,
		OutputPipe:        outputPipe,
		ErrorPipe:         errorPipe,
		InputPipe:         inputPipe,
		PTYSlave:          ptySlave,
		SocketFD:          s.socketFD,
		ExtraSocketFDs:    s.socketFDs,
		ControlSocketFD:   csClientFD,
		NotifyPipe:        notifyPipeWrite,
		StoredFDs:         s.drainFDStore(),
		NotifySocketPath:  s.notifySocketPath(),
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

	// Notify anyone holding the console (e.g. the boot debugger's
	// raw-mode reader) that a service is about to inherit /dev/console.
	// Must fire BEFORE StartProcess: once the child forks, our termios
	// state is what it captures — restoring after the fact would be
	// clobbered by the child's own "save original" step (bash --login
	// reads termios once at startup and reuses it between commands).
	if params.OnConsole && s.services.OnConsoleAcquire != nil {
		s.services.OnConsoleAcquire()
	}

	pid, exitCh, err := process.StartProcess(params)
	// The drained fd-store entries now belong to the child (or are lost
	// with a failed start, as the drain already decided): close our
	// copies, or they stay open until a GC runs their finalizers.
	for _, e := range params.StoredFDs {
		if e.File != nil {
			e.File.Close()
		}
	}
	if err != nil {
		// Record it before unwinding: the error names the stage and the
		// errno, and without this the service was left with an empty
		// status and a stop reason still saying "normal".
		s.exitStatus = noteStartExecFailure(s.Record(), err)
		if outputPipe != nil && s.logType == LogToBuffer {
			s.logBuf.CloseWriteEnd()
		} else if outputPipe != nil && (s.logType == LogToFile || s.logType == LogToJournal) {
			if s.logRotator != nil {
				s.logRotator.Close()
				s.logRotator = nil
			} else {
				outputPipe.Close()
			}
		} else if outputPipe != nil && s.logType == LogToPipe && s.sharedLoggerName != "" {
			outputPipe.Close() // the mux's pipe for this start
		}
		if s.stderrRotator != nil {
			s.stderrRotator.Close()
			s.stderrRotator = nil
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
		if s.syslogForwarderOnly != nil {
			// StartProcess failed after forwarder + pipe were
			// created — release the UDP socket + close pipe
			// write end so the reader goroutine exits.
			if outputPipe != nil {
				outputPipe.Close()
			}
			s.syslogForwarderOnly.Close()
			s.syslogForwarderOnly = nil
		}
		if notifyPipeWrite != nil {
			notifyPipeWrite.Close()
			s.readyPipeRead.Close()
			s.readyPipeRead = nil
		} else if s.readySD {
			// sd-notify kept the write end itself (setSDPipe): both ends
			// go, or every failed start (an on-demand launch of a missing
			// binary, per connection) leaks a pipe until a GC.
			s.closeReadyPipe()
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
	} else if s.logType == LogToFile || s.logType == LogToJournal {
		// outputPipe is nil with stderr-logfile and no logfile; stderr
		// still has its rotator to start.
		if outputPipe != nil {
			if s.logRotator != nil {
				s.logRotator.CloseWriteEnd()
				s.logRotator.StartReader()
			} else {
				outputPipe.Close()
			}
		}
		if s.stderrRotator != nil {
			s.stderrRotator.CloseWriteEnd()
			s.stderrRotator.StartReader()
		}
	} else if s.logType == LogToCommand {
		// Close the write-ends in the parent — the child inherited them.
		if outputPipe != nil {
			outputPipe.Close()
		}
		if errorPipe != nil {
			errorPipe.Close()
		}
	} else if s.syslogForwarderOnly != nil && outputPipe != nil {
		// Standalone log-forward-udp path: close parent's copy of
		// the pipe write end. The child inherited its own fd via
		// dup2 inside the runner; the reader goroutine (started in
		// startProcess above) sees EOF once every child copy is
		// closed, exiting the loop and letting the forwarder go.
		outputPipe.Close()
	} else if s.logType == LogToPipe && s.sharedLoggerName != "" && outputPipe != nil {
		// A shared-logger producer gets a new pipe from the mux on
		// every start, so the parent keeps nothing: holding the write
		// end kept the mux's reader from ever seeing EOF, and left one
		// fd per start open until a GC ran its finalizer.
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
	s.pidPublished.Store(int64(pid))
	s.procHandle = process.ProcessHandle{PID: pid, ExitCh: exitCh}
	s.paused = false

	// Create utmp entry if inittab-id or inittab-line is configured
	if s.HasUtmp() {
		s.services.utmpCreate(s.inittabID, s.inittabLine, s.Record().UtmpMode(), pid)
	}

	// Start monitoring goroutine
	s.closeDoneCh()
	s.doneCh = make(chan struct{})
	s.timerUpdateCh = make(chan struct{}, 1)

	// If readiness notification is configured, start a reader goroutine
	// on the notification pipe and defer Started() until data arrives.
	if s.readyPipeRead != nil {
		s.readyCh = make(chan bool, 1)
		go s.watchReadyPipe(s.readyPipeRead, s.readyCh)
		go s.monitorProcess(exitCh)

		// Arm start timeout while waiting for readiness
		if s.startTimeout > 0 {
			s.armTimer(s.startTimeout, timerStartTimeout)
		}
	} else if len(s.readyCheckCommand) > 0 {
		// Ready-check-command: poll external command until it succeeds
		s.readyCh = make(chan bool, 1)
		go s.watchReadyCheck(s.readyCh, s.doneCh, s.startTimeout)
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
//
// The pipe and channel are passed in, not read from s: closeReadyPipe and
// handleReadyNotification reset those fields under queueMu while this
// goroutine runs without it. A closed pipe makes Read return, which
// reports false.
// watchReadyPipe waits for the s6-style readiness signal: bytes written
// to the notification pipe up to and including a newline.
//
// The newline is the signal, not merely the first byte, which is what
// s6 itself requires — s6-linux-init's own reader loops
// `read()` until `memchr(buf, '\n', r)` succeeds and warns when the
// pipe closes first. dinit took any byte as readiness until it adopted
// the same rule in 5d5ea44; slinit had the same gap, and it mattered:
// anything a service wrote to that fd marked it started, so a progress
// line or a stray diagnostic was indistinguishable from "ready".
//
// The loop is load-bearing, not polish. A service may split its signal
// across writes — "rea" then "dy\n" — and a single Read would see the
// first chunk, find no newline and wrongly give up. Measured: with one
// read the split form fails, with the loop it succeeds.
//
// Returns false on EOF with no newline seen, which is the service
// exiting or closing the fd without ever signalling, and on a read
// error. Both are logged, because the old code discarded the error and
// a failed pipe was indistinguishable from a service that simply never
// reported ready.
func (s *ProcessService) watchReadyPipe(pipe *os.File, ch chan bool) {
	buf := make([]byte, 128)
	for {
		n, err := pipe.Read(buf)
		if bytes.IndexByte(buf[:n], '\n') >= 0 {
			ch <- true
			return
		}
		if err != nil {
			// A deadline pushed into the past is how stopping a service
			// unblocks this goroutine on purpose, so it is not a fault
			// and must not be reported as one.
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				// Info, not Error: a process that exits without
				// signalling is already reported as a start failure by
				// the exit path, and a second Error line for the same
				// event is noise. This adds the detail that line lacks.
				if errors.Is(err, io.EOF) {
					s.services.logger.Info(
						"Service '%s': readiness pipe closed with no newline — "+
							"the service never signalled ready",
						s.serviceName)
				} else {
					s.services.logger.Error(
						"Service '%s': readiness pipe not readable: %v",
						s.serviceName, err)
				}
			}
			ch <- false
			return
		}
	}
}

// monitorProcess runs in a goroutine, waiting for the process to exit,
// readiness notification, or for timers to fire.
func (s *ProcessService) monitorProcess(exitCh <-chan process.ChildExit) {
	for {
		timer := s.getTimer()
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

		case <-timerC(timer):
			s.handleTimerExpired(timer)

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

	// After the readiness signal the pipe stays open and is still read
	// (see keepReadingReadyPipe); otherwise it is done with.
	keepPipe := ready &&
		(s.state.Load() == StateStarting || s.state.Load() == StateStarted)
	if !keepPipe {
		s.closeReadyPipe()
	}

	if s.state.Load() != StateStarting {
		// State is not STARTING but readyCh still fired. The one
		// legitimate context is smooth recovery: doSmoothRecovery
		// keeps state at StateStarted, calls startProcess which arms
		// a fresh readyCh + start-timeout timer, and the new child
		// re-signals readiness. A ready=true here changes no state,
		// but settles the new process's timers (below). A ready=false —
		// child crashed before notifying, or closed the pipe without
		// writing — means the recovered process is unhealthy and
		// must be treated as an unexpected termination, else the
		// service stays wedged at STARTED with no live PID while
		// dependents believe it is up. Mirrors dinit's fix 991fceeb
		// (2026-08-30, "Better handling of smooth recovery failure":
		// treat smooth-recovery failure universally as termination
		// rather than start failure).
		if s.onDemandListening() {
			// An on-demand launch: the service has been STARTED since
			// its sockets opened. Readiness only gates the extras; a
			// failed launch is handled when the process exits.
			if ready {
				s.cancelTimer()
				s.startCronIfConfigured()
				s.startHealthCheckIfConfigured()
				if keepPipe {
					s.keepReadingReadyPipe()
				}
			}
			return
		}
		if !ready && s.state.Load() == StateStarted {
			s.services.logger.Error(
				"Service '%s': readiness pipe closed during smooth recovery — treating as unexpected termination",
				s.serviceName)
			s.cancelTimer()
			s.handleRecoveryFailureLocked()
			return
		}
		if ready && s.state.Load() == StateStarted {
			// The recovered process is ready. The service is already
			// STARTED, but what startProcess armed for this process
			// still has to be settled: the start-timeout, which would
			// otherwise fire on the healthy process, and the watchdog,
			// which handleChildExit stopped with the old one.
			s.cancelTimer()
			if keepPipe {
				s.keepReadingReadyPipe()
			}
			return
		}
		// STOPPING/STOPPED — nothing meaningful to do.
		return
	}

	if ready {
		// Child signaled readiness
		s.cancelTimer()
		s.services.logger.Info("Service '%s': readiness notification received", s.serviceName)
		s.warnIfReloadSignalUnhandled()
		s.Started()
		s.startCronIfConfigured()
		s.startHealthCheckIfConfigured()
		if keepPipe {
			s.keepReadingReadyPipe()
		}
		s.services.processQueuesLocked()
	} else {
		// EOF without data - child closed pipe without writing
		s.services.logger.Error("Service '%s': readiness pipe closed without notification", s.serviceName)
		s.cancelTimer()
		s.stopReason = ReasonFailed
		s.failedToStart(false, false)
		// The process may still be running — it closed the fd without
		// exiting. Bring it down; its exit then ends the stop. As in
		// dinit (ready_notify_watcher::fd_event).
		s.BringDown()
		s.services.processQueuesLocked()
	}
}

// keepReadingReadyPipe keeps reading the readiness pipe once the service
// is ready: the watchdog does when one is configured, since later writes
// are its keepalives; otherwise a goroutine drains the pipe to EOF.
// Closing the read end instead killed a service that wrote to the fd a
// second time with SIGPIPE. dinit keeps reading until EOF too
// (ready_notify_watcher). The pipe is closed when the process exits or
// is stopped (closeReadyPipe), which also ends the drain.
//
// Caller must hold queueMu.
func (s *ProcessService) keepReadingReadyPipe() {
	if s.readyPipeRead == nil {
		return // ready-check-command: there is no pipe
	}
	if s.HasWatchdog() {
		s.startWatchdogWatcher()
		return
	}
	go func(pipe *os.File) {
		buf := make([]byte, 128)
		for {
			if _, err := pipe.Read(buf); err != nil {
				return
			}
		}
	}(s.readyPipeRead)
}

// closeReadyPipe closes the read-end of the notification pipe if open,
// and the write end slinit keeps for sd_notify.
func (s *ProcessService) closeReadyPipe() {
	if s.readyPipeRead != nil {
		s.readyPipeRead.Close()
		s.readyPipeRead = nil
	}
	// Whatever readySD says now: a reload may have changed it while the
	// pipe of the old setting was open.
	s.setSDPipe(nil)
}

// handleChildExit processes a child process termination.
// Runs in the monitorProcess goroutine; acquires queueMu.
func (s *ProcessService) handleChildExit(exit process.ChildExit) {
	// Kill any remaining processes in the child's process group
	// (e.g., orphaned sleep, background scripts spawned by the shell).
	// The lead process is already reaped; wait4(-pgid) only targets
	// group members, so this is safe for other managed services.
	// SignalProcessOnly opts out of every pgroup signal for this service
	// (dinit parity: baseproc-service.cc kill_pg gates on the same flag).
	// A reload rewrites the flags and the inittab id/line under queueMu;
	// read them there, then do the killing without it.
	s.services.queueMu.Lock()
	flags := s.Flags
	inittabID, inittabLine := s.inittabID, s.inittabLine
	s.services.queueMu.Unlock()

	if !flags.SignalProcessOnly {
		process.KillProcessGroup(exit.PID)
	}

	// Kill entire cgroup tree to clean up any orphaned processes
	if flags.KillAllOnStop {
		s.killCgroupTree(syscall.SIGKILL)
	}

	// Clear utmp entry
	if inittabID != "" || inittabLine != "" {
		s.services.utmpClear(inittabID, inittabLine)
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
	s.pidPublished.Store(0)
	s.procHandle.Clear()
	// The pause belonged to the process that just exited.
	s.paused = false
	// Cancelled unconditionally. Keeping the stop-timeout armed here
	// would be pointless: processTimer is only ever read by
	// monitorProcess, which returns with the process, so once the main
	// process is gone nothing reads that channel. A stop-command still
	// running past this point is bounded by its own timeout instead —
	// see execStopCommand.
	s.cancelTimer()
	s.stopWatchdogWatcherLocked()
	s.closeReadyPipe()

	// systemd RemoveIPC= — sweep POSIX shm + SysV IPC owned by the
	// service's UID. Skipped for root (UID 0) to avoid nuking shared
	// system state. Runs on the queueMu goroutine — the sweep is
	// bounded (walks a small procfs table) so we don't defer it.
	if s.Record().RemoveIPCEnabled() {
		uid := s.effectiveRunAsUID()
		if uid != 0 {
			removeIPCForUID(uid)
		}
	}

	// A pre-stop-hook or a stop's control-command is still running: the
	// exit is handled when it has finished, so the finish-command still
	// follows the pre-stop-hook and the stop is not declared over under
	// a hook that is still at work. Only the exit of the process that
	// stop is for: no other hook's completion would take it, and one
	// left parked was taken later, by another run's stop, as that run's
	// exit.
	if h := s.stateHook.Load(); h != nil {
		if h.kind.parksExit() && exit.PID == h.pid {
			e := exit
			s.pendingExit = &e
			return
		}
		// Not expected: no other hook runs while there is a process
		// (a new run does not begin under one). Its completion
		// carries the transition on; dispatching this exit too would
		// have it handled twice.
		s.services.logger.Error("Service '%s': process %d exited during its %s; exit not handled",
			s.serviceName, exit.PID, h.kind)
		return
	}
	s.afterExitLocked(exit)
}

// afterExitLocked runs the finish-command for a main-process exit, if
// one is configured, and then hands the exit to the state machine
// (dispatchExitLocked). The finish-command runs off the lock and the
// service stays where it is meanwhile; a stop that arrives in the
// meantime waits for it (BringDown), and the exit is dispatched against
// the state the service is in once it is done. So it still runs before
// the restart decision, and before a restart, as systemd's
// ExecStopPost= does. Caller holds queueMu.
func (s *ProcessService) afterExitLocked(exit process.ChildExit) {
	if len(s.finishCommand) > 0 && exit.ExecErr == nil {
		s.startStateHookLocked(hookFinish,
			func(ctx context.Context) func() error {
				return s.prepareFinishCommand(ctx, exit)
			},
			func(error, bool) { s.dispatchExitLocked(exit) })
		return
	}
	s.dispatchExitLocked(exit)
}

// dispatchExitLocked is the state-machine half of a main-process exit,
// after its finish-command. Caller holds queueMu.
func (s *ProcessService) dispatchExitLocked(exit process.ChildExit) {
	if exit.ExecErr != nil {
		// Process failed during exec/setup
		s.services.logger.Error("Service '%s': exec failed: %v",
			s.serviceName, exit.ExecErr)
		if s.onDemandListening() {
			// The service is up — its sockets are — and only this
			// launch failed. Wait for the next client instead.
			s.rearmOnDemandLocked()
			s.services.processQueuesLocked()
			return
		}
		s.stopReason = ReasonExecFailed
		s.state.Store(StateStopping)
		s.failedToStart(false, true)
		s.services.processQueuesLocked()
		return
	}

	state := s.state.Load()

	// systemd ExitType=cgroup: main pid gone but the delegated cgroup
	// may still hold workers spawned by fork-exec. Defer the
	// stopped-transition until the cgroup drains. StateStarting keeps
	// the immediate-failure path — a start-time failure is not
	// "waiting for a healthy workload to finish".
	if s.exitType == ExitTypeCgroup && state != StateStarting {
		if cgPath := s.OwnCgroupPath(); cgPath != "" {
			if populated, _ := readCgroupPopulated(cgPath); populated {
				s.services.logger.Info(
					"Service '%s': main pid exited, waiting for cgroup drain (exit-type=cgroup)",
					s.serviceName)
				go s.waitCgroupThenDispatch(cgPath, state, exit)
				return
			}
		}
	}

	s.dispatchAfterExitLocked(state, exit)
}

// dispatchAfterExitLocked runs the state-machine tail of handleChildExit
// after any exit-type=cgroup drain wait. Caller must hold queueMu.
func (s *ProcessService) dispatchAfterExitLocked(state ServiceState, exit process.ChildExit) {
	switch state {
	case StateStarting:
		if s.waitingForDeps {
			// The start has not launched anything yet: this is the
			// process of an earlier run, whose stop a start
			// cancelled (BringUp keeps such a process), exiting
			// before the start got to it. The start goes on, and
			// launches a new one.
			s.services.logger.Info("Service '%s': process of the previous run exited (status: %v)",
				s.serviceName, exit.Status)
			s.services.AddTransitionQueue(s)
			s.services.processQueuesLocked()
			return
		}
		if s.waitingForStartSlot || s.waitingForConsole {
			// The same, with the deps up and the start queued for a
			// start slot or the console: the grant reaches BringUp,
			// which finds no process and launches one.
			s.services.logger.Info("Service '%s': process of the previous run exited (status: %v)",
				s.serviceName, exit.Status)
			return
		}
		// Process died while we thought it was starting
		s.services.logger.Error("Service '%s': process exited during startup (status: %v)",
			s.serviceName, exit.Status)
		s.stopReason = ReasonFailed
		s.failedToStart(false, true)
		s.services.processQueuesLocked()

	case StateStopping:
		// Expected — we asked it to stop. But the main process exiting
		// is not the same event as the stop having finished: a
		// stop-command is commonly written to kill the daemon first and
		// then clean up after it, so it is still running at this point.
		//
		// Declaring Stopped here let dependents and shutdown proceed as
		// though the cleanup had completed. Measured before this guard:
		// a stop-command that killed the process and then slept five
		// seconds reported STOPPED for the whole five, with the cleanup
		// marker still absent. Same shape as dinit 4da302f / 798e037,
		// which guard on stop_pid for the same reason, and the other
		// half of the orphaned-daemon problem --fast caused in v2.3.9.
		//
		// The stop-command's own monitor finishes the transition. The
		// stop-timeout armed alongside it still governs, so a wedged
		// stop-command cannot hold the service in STOPPING forever.
		if s.stopPID > 0 {
			s.mainGoneDuringStop = true
			s.services.logger.Info(
				"Service '%s': main process gone, waiting for stop-command (pid %d)",
				s.serviceName, s.stopPID)
			s.services.processQueuesLocked()
			return
		}
		// Still waiting for a hard dependent to stop: going STOPPED
		// now would put this service down underneath it. Restart now
		// if that is where the stop was heading — the dependents need
		// not be waited for — and otherwise wait; BringDown, finding
		// no process, finishes the stop once they are down. dinit's
		// process_service::handle_exit_status does the same.
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
		// Unexpected termination. Only log non-clean exits (match dinit
		// did_exit_clean semantics): a clean exit (code 0) from a
		// restart=true service is normal turnover, not an error.
		if exit.Exited() {
			if code := exit.Status.ExitStatus(); code != 0 {
				s.services.logger.Error("Service '%s': process exited with code %d",
					s.serviceName, code)
			}
		} else if exit.Signaled() {
			s.services.logger.Error("Service '%s': process killed by %s",
				s.serviceName, signalName(exit.Status.Signal()))
		}

		if s.socketOnDemand {
			// On-demand: the process ending is not the service ending.
			// Its sockets stay open; the next client launches it again.
			s.demandLastClean = exit.Exited() && exit.Status.ExitStatus() == 0
			s.rearmOnDemandLocked()
			s.services.processQueuesLocked()
			return
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

// handleRecoveryFailureLocked stops a service whose smooth recovery failed. That
// is a failure whatever the exit status left by the process it was replacing:
// reading that one could report a recovery that never ran as a clean stop.
// Caller must hold queueMu.
func (s *ProcessService) handleRecoveryFailureLocked() {
	s.handleTerminationLocked(ReasonFailed)
}

// handleUnexpectedTerminationLocked handles when a started process dies
// unexpectedly. Caller must hold queueMu.
func (s *ProcessService) handleUnexpectedTerminationLocked() {
	s.handleTerminationLocked(ReasonTerminated)
}

func (s *ProcessService) handleTerminationLocked(reason StoppedReason) {
	s.services.ClearOperatorStop()
	s.stopReason = reason
	s.forceStop = true

	s.doStop(false)
	s.services.processQueuesLocked()

	// Still STOPPING: waiting for a hard dependent to stop. A restart
	// need not wait for it — dinit's handle_unexpected_termination calls
	// stopped() here, which "will restart us". Going through Stopped()
	// rather than initiateStart() clears forceStop (else the relaunch is
	// stopped again at once), counts the restart and applies
	// restart-delay. Only when Stopped() will restart: otherwise it
	// would go STOPPED under a running dependent, and BringDown finishes
	// the stop once the dependents are down.
	if s.state.Load() == StateStopping && s.waitingForDeps &&
		s.desired.Load() == StateStarted && !s.pinnedStopped && !s.restartLimitExhausted {
		s.Stopped()
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
		s.smoothRelaunchLocked()
	} else {
		// Need to delay restart
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
func (s *ProcessService) fireSmoothRecoveryLocked(t *time.Timer) {
	if s.smoothRecovTimer != t {
		return
	}
	s.smoothRecovTimer = nil
	if !s.doingSmoothRecov || s.state.Load() != StateStarted || s.pid > 0 {
		s.doingSmoothRecov = false
		return
	}
	s.smoothRelaunchLocked()
	s.services.processQueuesLocked()
}

// smoothRelaunchLocked launches the process again for a smooth recovery.
// With an env-generator, the new run's environment is computed first,
// off the lock, as for a start (BringUp): startProcess would otherwise
// run the generator under it. The service stays STARTED meanwhile; a
// stop ends the wait, killing the generator. Caller holds queueMu.
func (s *ProcessService) smoothRelaunchLocked() {
	if s.envGenerator == "" {
		s.smoothRelaunchNowLocked()
		return
	}
	s.startStateHookLocked(hookEnvGen,
		func(ctx context.Context) func() error {
			return func() error {
				s.prepareStartEnvCtx(ctx)
				return ctx.Err()
			}
		},
		func(_ error, cancelled bool) {
			switch {
			case s.state.Load() == StateStopping:
				// Stopped meanwhile; the stop waited for us.
				s.discardStartEnv()
				s.doingSmoothRecov = false
				s.services.AddTransitionQueue(s)
			case cancelled || !s.doingSmoothRecov || s.state.Load() != StateStarted || s.pid > 0:
				s.discardStartEnv()
				s.doingSmoothRecov = false
			default:
				s.smoothRelaunchNowLocked()
			}
		})
}

// smoothRelaunchNowLocked is the fork of a smooth recovery. Caller holds
// queueMu.
func (s *ProcessService) smoothRelaunchNowLocked() {
	err := s.startProcess()
	s.doingSmoothRecov = false
	if err != nil {
		s.services.logger.Error("Service '%s': smooth recovery failed: %v",
			s.serviceName, err)
		s.handleRecoveryFailureLocked()
	}
}

// cancelSmoothRecovery disarms a smooth-recovery relaunch still waiting
// out restart-delay.
func (s *ProcessService) cancelSmoothRecovery() {
	if s.smoothRecovTimer != nil {
		s.smoothRecovTimer.Stop()
		s.smoothRecovTimer = nil
		s.doingSmoothRecov = false
	}
}

// handleTimerExpired processes a timer expiration.
// Runs in the monitorProcess goroutine; acquires queueMu.
func (s *ProcessService) handleTimerExpired(timer *time.Timer) {
	s.services.queueMu.Lock()
	defer s.services.queueMu.Unlock()

	// A tick received just before a stop or restart re-armed the timer
	// belongs to the timer it replaced: acting on it would give the new
	// purpose its deadline early (a stop escalated to SIGKILL at once).
	if timer != s.processTimer {
		return
	}

	purpose := s.timerPurpose
	s.timerPurpose = timerNone

	switch purpose {
	case timerStartTimeout:
		// Start timeout expired. Signal choice is systemd-configurable
		// via timeout-start-failure-mode (terminate/abort/kill); default
		// terminate = SIGINT to preserve the historical slinit behaviour.
		if s.pid > 0 {
			if s.state.Load() == StateStarted && !s.onDemandListening() {
				// A smooth recovery whose new process never signalled
				// ready. The service is STARTED, dependents and all, so
				// this is the recovery failing, not a start: as in
				// dinit (process_service::start_timed_out).
				s.services.logger.Error("Service '%s': start timeout exceeded during smooth recovery",
					s.serviceName)
				s.handleRecoveryFailureLocked()
				return
			}
			sig, label := s.startTimeoutSignal()
			s.services.logger.Error("Service '%s': start timeout exceeded, sending %s",
				s.serviceName, label)
			process.SignalProcess(s.pid, sig, s.Flags.SignalProcessOnly)
			if s.onDemandListening() {
				// The service stays up; the exit re-arms the sockets.
				return
			}
			s.stopReason = ReasonTimedOut
			// STOPPING, and wait for the process to go (dinit's
			// start_timed_out).
			s.stopInterruptedStart()
			s.failedToStart(false, false)
			s.services.processQueuesLocked()
		}

	case timerStopTimeout:
		// systemd TimeoutStopFailureMode= — if the operator picked
		// abort or kill, honour that instead of the historical
		// SIGABRT-then-SIGKILL cascade. Zero-value (terminate) falls
		// through to the legacy path.
		if sig, label := s.stopTimeoutSignal(); sig != 0 {
			if s.pid > 0 {
				s.services.logger.Error("Service '%s': stop timeout exceeded, sending %s",
					s.serviceName, label)
				process.SignalProcess(s.pid, sig, s.Flags.SignalProcessOnly)
			}
			if s.killsToGroup() {
				s.killCgroupTree(sig)
			}
			// For abort mode arm a follow-on timer so we still escalate
			// to the final-kill-signal. For kill mode we already sent
			// the final signal; no further escalation.
			if s.Record().TimeoutStopFailureMode() == TimeoutFailureAbort {
				s.armTimer(s.timeoutAbortSec, timerAbortTimeout)
			}
			return
		}
		// Stop timeout expired. If timeout-abort-sec is configured,
		// insert a SIGABRT phase so the child can dump core / print
		// a stack trace before SIGKILL — matches systemd
		// TimeoutAbortSec=. Without it we go straight to SIGKILL.
		if s.timeoutAbortSec > 0 && s.pid > 0 {
			s.services.logger.Error("Service '%s': stop timeout exceeded, sending SIGABRT (timeout-abort-sec=%v)",
				s.serviceName, s.timeoutAbortSec)
			process.SignalProcess(s.pid, syscall.SIGABRT, s.Flags.SignalProcessOnly)
			s.armTimer(s.timeoutAbortSec, timerAbortTimeout)
			return
		}
		if s.Record().SurviveFinalKillSignal() {
			// systemd SurviveFinalKillSignal=yes: no SIGKILL, no
			// cgroup sweep. The operator has assumed responsibility
			// for terminating the service by other means.
			s.services.logger.Info("Service '%s': stop timeout — survive-final-kill-signal set, not escalating",
				s.serviceName)
			return
		}
		finalSig := s.Record().FinalKillSignal()
		if s.pid > 0 {
			s.services.logger.Error("Service '%s': stop timeout exceeded, sending %s",
				s.serviceName, signalName(finalSig))
			process.SignalProcess(s.pid, finalSig, false) // Always kill group
		}
		// Kill entire cgroup tree on final-signal escalation, per
		// kill-mode + kill-all-on-stop option.
		if s.killsToGroup() {
			s.killCgroupTree(finalSig)
		}
		// Also kill stop-command if still running
		if s.stopPID > 0 {
			s.services.logger.Error("Service '%s': killing stop-command (pid %d)",
				s.serviceName, s.stopPID)
			process.SignalProcess(s.stopPID, syscall.SIGKILL, false)
		}

	case timerAbortTimeout:
		// SIGABRT window elapsed without the child exiting. Escalate
		// to the final-kill-signal with the same fan-out as the
		// direct-SIGKILL path, unless survive-final-kill-signal is set.
		if s.Record().SurviveFinalKillSignal() {
			s.services.logger.Info("Service '%s': abort timeout — survive-final-kill-signal set, not escalating",
				s.serviceName)
			return
		}
		finalSig := s.Record().FinalKillSignal()
		if s.pid > 0 {
			s.services.logger.Error("Service '%s': abort timeout exceeded, sending %s",
				s.serviceName, signalName(finalSig))
			process.SignalProcess(s.pid, finalSig, false)
		}
		if s.killsToGroup() {
			s.killCgroupTree(finalSig)
		}
		if s.stopPID > 0 {
			process.SignalProcess(s.stopPID, syscall.SIGKILL, false)
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

// getTimer returns the armed timer, or nil. The monitor hands it to
// handleTimerExpired with its tick.
func (s *ProcessService) getTimer() *time.Timer {
	s.services.queueMu.RLock()
	defer s.services.queueMu.RUnlock()
	return s.processTimer
}

// timerC is t's channel, or nil, which never delivers, for no timer.
func timerC(t *time.Timer) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}

// applyRunAs gives an auxiliary command (finish-command, pre-stop-hook,
// ready-check-command, control-command-*) the service's run-as user,
// groups included, as the main command and stop-command get. They ran
// as root, while systemd runs ExecStopPost=, ExecStop=, ExecReload= and
// ExecCondition= as User=.
func (s *ProcessService) applyRunAs(cmd *exec.Cmd) {
	uid, gid := s.effectiveRunAsUID(), s.effectiveRunAsGID()
	if uid == 0 && gid == 0 {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{
		Uid:    uid,
		Gid:    gid,
		Groups: s.supplementaryGIDs,
	}
}

// execFinishCommand runs the finish-command after a process exits.
// It passes the exit code and signal number as arguments.
// Runs synchronously with a timeout so it doesn't block forever; the
// exit handler runs it off the lock instead (prepareFinishCommand).
func (s *ProcessService) execFinishCommand(exit process.ChildExit) {
	_ = s.prepareFinishCommand(context.Background(), exit)()
}

// prepareFinishCommand builds the finish-command for exit and returns
// the function that runs it, bounded by hook-timeout and ended early by
// parent; failures are logged, and the error is returned for the
// caller's information only. Everything is read from the service here,
// so a caller holding queueMu can let it go before the run.
func (s *ProcessService) prepareFinishCommand(parent context.Context, exit process.ChildExit) func() error {
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

	timeout := s.effectiveHookTimeout()
	ctx, cancel := context.WithTimeout(parent, timeout)

	cmd := newHookCmd(ctx, append([]string{s.finishCommand[0]}, args...))
	cmd.Dir = s.workingDir
	cmd.Env = s.hookCmdEnv()
	if !s.privHooks.Finish {
		s.applyRunAs(cmd)
	}

	return func() error {
		defer cancel()
		s.services.logger.Info("Service '%s': running finish-command", s.serviceName)
		err := annotateHookTimeout(process.RunAdhoc(cmd), ctx, timeout)
		if err != nil && !isECHILDErr(err) {
			// RunAdhoc registers the child with the exit router, so the
			// PID-1 reaper claiming the zombie first no longer turns a
			// clean run into "waitid: no child processes". The ECHILD
			// check stays as a fallback for the case where the router has
			// no status either: the finish-command still ran, and a
			// spurious failure line on every shutdown of a runit-style
			// service is the worse outcome.
			s.services.logger.Error("Service '%s': finish-command failed: %v",
				s.serviceName, err)
			return err
		}
		return nil
	}
}

// watchReadyCheck polls the ready-check-command until it succeeds or times out.
// Sends true on readyCh when the command exits 0, false if the doneCh is closed.
// Runs the check ONCE immediately before entering the polling loop so a
// service that is already ready pays a single exec latency instead of one
// full interval — matters for sockets that bind in microseconds (dbus,
// most listen(2)-then-fork daemons).
//
// ch and done are passed in rather than read from s, for the reason given
// at watchReadyPipe.
//
// startTimeout is the start-timeout armed with the watch (read by the
// caller, which holds the lock a reload changes it under). An attempt
// may run until the start would time out; zero leaves only the end of
// the watch to end it.
func (s *ProcessService) watchReadyCheck(ch chan bool, done chan struct{}, startTimeout time.Duration) {
	interval := s.readyCheckInterval
	if interval <= 0 {
		interval = defaultReadyCheckInterval
	}

	// A check under way ends with the watch: when the start times out,
	// the process exits or the service is stopped, done closes and the
	// check's process group is killed. Before, a check that hung kept
	// running after the service had failed or stopped, and each restart
	// added another. The deadline is the start-timeout's own, so an
	// attempt is never cut short of it: hook-timeout bounded each attempt
	// for a while, and a check that legitimately takes longer than five
	// seconds (pg_isready -t 10) never passed.
	ctx, cancel := context.WithCancel(context.Background())
	if startTimeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), startTimeout)
	}
	defer cancel()
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()

	// Immediate first check. The doneCh race is handled by the loop
	// below — if we return here on success, all good; if the command
	// fails, fall through to the polling loop which will also serve
	// any pending doneCh signal.
	if s.runReadyCheck(ctx) {
		ch <- true
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			ch <- false
			return
		case <-ticker.C:
			if s.runReadyCheck(ctx) {
				ch <- true
				return
			}
		}
	}
}

// readyCheckAttemptTimeout bounds one ready-check attempt. Atomic, so a
// test can change it while a watch from an earlier test still runs.
var readyCheckAttemptTimeout atomicDuration

func init() { readyCheckAttemptTimeout.Store(30 * time.Second) }

// runReadyCheckOnce executes the configured ready-check-command exactly
// once and reports whether it exited 0.
func (s *ProcessService) runReadyCheckOnce() bool {
	return s.runReadyCheck(context.Background())
}

// runReadyCheck runs one ready-check attempt, ended by ctx (the end of
// the watch or the start-timeout's deadline) with its process group
// killed, and by readyCheckAttemptTimeout. The immediate first check and
// the polling loop share it. hook-timeout does not bound it: a slow check
// is still a check.
func (s *ProcessService) runReadyCheck(ctx context.Context) bool {
	// Each attempt also has a bound of its own, well above hook-timeout,
	// so one attempt that hangs (a stalled connect) is retried instead of
	// holding the whole watch: with no start-timeout it would otherwise
	// never end, and with one it would use it all up.
	ctx, cancel := context.WithTimeout(ctx, readyCheckAttemptTimeout.Load())
	defer cancel()
	cmd := newHookCmd(ctx, s.readyCheckCommand)
	cmd.Dir = s.workingDir
	cmd.Env = s.hookCmdEnv()
	s.applyRunAs(cmd)
	return process.RunAdhoc(cmd) == nil
}

// execPreStopHook runs the pre-stop-hook synchronously, with a timeout.
// The hook receives the service PID as its last argument (like runit's
// control scripts). BringDown runs it off the lock instead
// (preparePreStopHook).
func (s *ProcessService) execPreStopHook() {
	_ = s.preparePreStopHook(context.Background())()
}

// preparePreStopHook builds the pre-stop-hook, with the service's pid
// as its last argument, and returns the function that runs it, bounded
// by hook-timeout and ended early by parent. As prepareControlCommand,
// it reads everything from the service here, so the run needs no lock.
func (s *ProcessService) preparePreStopHook(parent context.Context) func() error {
	args := make([]string, len(s.preStopHook)-1, len(s.preStopHook))
	copy(args, s.preStopHook[1:])
	args = append(args, strconv.Itoa(s.pid))

	timeout := s.effectiveHookTimeout()
	ctx, cancel := context.WithTimeout(parent, timeout)

	cmd := newHookCmd(ctx, append([]string{s.preStopHook[0]}, args...))
	cmd.Dir = s.workingDir
	cmd.Env = s.hookCmdEnv()
	if !s.privHooks.PreStop {
		s.applyRunAs(cmd)
	}

	return func() error {
		defer cancel()
		s.services.logger.Info("Service '%s': running pre-stop-hook", s.serviceName)
		err := annotateHookTimeout(process.RunAdhoc(cmd), ctx, timeout)
		if err != nil {
			s.services.logger.Error("Service '%s': pre-stop-hook failed: %v",
				s.serviceName, err)
		}
		return err
	}
}

// execControlCommand runs a custom control command for a signal.
// The command receives the service PID as an appended argument.
// Runs synchronously with a timeout.
func (s *ProcessService) execControlCommand(sigName string, command []string) {
	s.prepareControlCommand(sigName, command)()
}

// prepareControlCommand builds the control command for a signal and returns
// a function that runs it; the caller must call it. Everything the command
// needs from the service, the pid above all, is read here, so a caller
// holding queueMu can let the lock go before running the command, which
// may take up to the hook timeout.
func (s *ProcessService) prepareControlCommand(sigName string, command []string) func() {
	run := s.prepareControlCommandCtx(context.Background(), sigName, command)
	return func() { _ = run() }
}

// prepareControlCommandCtx is prepareControlCommand with a run that
// parent can end early, and that returns the command's error (already
// logged).
func (s *ProcessService) prepareControlCommandCtx(parent context.Context, sigName string, command []string) func() error {
	args := make([]string, len(command)-1, len(command))
	copy(args, command[1:])
	args = append(args, strconv.Itoa(s.pid))

	timeout := s.effectiveHookTimeout()
	ctx, cancel := context.WithTimeout(parent, timeout)

	cmd := newHookCmd(ctx, append([]string{command[0]}, args...))
	cmd.Dir = s.workingDir
	cmd.Env = s.hookCmdEnv()
	if !s.privHooks.Control[sigName] {
		s.applyRunAs(cmd)
	}

	return func() error {
		defer cancel()
		s.services.logger.Info("Service '%s': running control-command-%s", s.serviceName, sigName)
		err := annotateHookTimeout(process.RunAdhoc(cmd), ctx, timeout)
		if err != nil {
			s.services.logger.Error("Service '%s': control-command-%s failed: %v",
				s.serviceName, sigName, err)
		}
		return err
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

// signalWithControl sends sig to the service's process, or, when a
// control-command-<SIG> is configured, returns that command for the caller
// to run. Called with queueMu held (ServiceSet.SignalService): the pid is
// read and signalled under the lock that clears it on exit, and the
// command, which can run for the hook timeout, is run after the lock is
// let go. hasPID is false when there is no process.
func (s *ProcessService) signalWithControl(sig syscall.Signal) (run func(), hasPID bool, err error) {
	if s.pid <= 0 {
		return nil, false, nil
	}
	sigName := signalName(sig)
	if cmd, ok := s.controlCommands[sigName]; ok && len(cmd) > 0 {
		return s.prepareControlCommand(sigName, cmd), true, nil
	}
	return nil, true, process.SignalProcess(s.pid, sig, s.Flags.SignalProcessOnly)
}

// pause suspends the service process with SIGSTOP, or returns
// control-command-STOP for the caller to run. Called with queueMu held
// (ServiceSet.PauseService), with the same split as signalWithControl.
func (s *ProcessService) pause() (run func(), ok bool) {
	if s.pid <= 0 || s.paused {
		return nil, false
	}
	if cmd, ok := s.controlCommands["STOP"]; ok && len(cmd) > 0 {
		run = s.prepareControlCommand("STOP", cmd)
	} else {
		if err := process.SignalProcess(s.pid, syscall.SIGSTOP, s.Flags.SignalProcessOnly); err != nil {
			s.services.logger.Error("Service '%s': SIGSTOP failed: %v", s.serviceName, err)
			return nil, false
		}
	}
	s.paused = true
	s.services.logger.Info("Service '%s': paused", s.serviceName)
	return run, true
}

// resume resumes a paused service process with SIGCONT, or returns
// control-command-CONT for the caller to run. Called with queueMu held
// (ServiceSet.ContinueService).
func (s *ProcessService) resume() (run func(), ok bool) {
	if s.pid <= 0 || !s.paused {
		return nil, false
	}
	if cmd, ok := s.controlCommands["CONT"]; ok && len(cmd) > 0 {
		run = s.prepareControlCommand("CONT", cmd)
	} else {
		if err := process.SignalProcess(s.pid, syscall.SIGCONT, s.Flags.SignalProcessOnly); err != nil {
			s.services.logger.Error("Service '%s': SIGCONT failed: %v", s.serviceName, err)
			return nil, false
		}
	}
	s.paused = false
	s.services.logger.Info("Service '%s': continued", s.serviceName)
	return run, true
}

// IsPaused returns whether the service is currently paused. Called with
// queueMu held.
func (s *ProcessService) IsPaused() bool { return s.paused }

// stopLoggerCommands kills any running output/error logger processes and
// cleans up references. Called when the service stops or becomes inactive.
func (s *ProcessService) stopLoggerCommands() {
	if s.loggerSup != nil {
		s.loggerSup.Stop()
		s.loggerSup = nil
	}
	if s.stderrRotator != nil {
		s.stderrRotator.Close()
		s.stderrRotator = nil
	}
	if s.errLoggerSup != nil {
		s.errLoggerSup.Stop()
		s.errLoggerSup = nil
	}
	// Standalone syslog-forward (no rotator) cleanup — closes the
	// UDP socket the forwarder cached. Nil-safe.
	if s.syslogForwarderOnly != nil {
		s.syslogForwarderOnly.Close()
		s.syslogForwarderOnly = nil
	}
}

// forwardOnlyLoop reads pr line-by-line and ships each line via
// SyslogForwarder. Used by the standalone log-forward-udp path
// (no LogRotator). Exits when pr closes (child stdout+stderr FDs
// closed after fork+exec, plus service exit closes them too).
// stderr is captured in the same pipe as stdout because slinit's
// exec sets both to outputPipe.
func (s *ProcessService) forwardOnlyLoop(pr *os.File, fw *SyslogForwarder) {
	defer pr.Close()
	// 64 KiB is ample for syslog over UDP (RFC 3164 max 1024 bytes,
	// RFC 5424 typically 8 KB); a longer line goes in 64 KiB pieces.
	// bufio.Scanner, used here before, stopped at such a line instead,
	// and the deferred Close then left the service writing to a pipe
	// with no reader.
	readLines(pr, 64*1024, func(line []byte) bool {
		fw.Send(line)
		return true
	})
}

// warnIfReloadSignalUnhandled says so when a service declares a
// reload-signal that its process neither catches nor blocks.
//
// Readiness is the moment to ask: the process has finished setting
// itself up, so its signal dispositions are the ones it means to run
// with. If the reload signal is not among them, `slinitctl reload` will
// not reload this service — it will kill it, because the default action
// for most signals is to terminate. The service works perfectly until
// the first reload, which is exactly the kind of fault that is cheap to
// mention now and expensive to find later.
//
// systemd v262 made this a startup failure for Type=notify-reload.
// slinit warns instead: refusing would stop services that run fine
// today from starting at all, and that is a behaviour change an
// existing setup could be relying on. The check is the same either way.
//
// Silence on error is deliberate — an unreadable or unexpected status
// file means "could not tell", and a warning nobody can act on is
// worse than none.
func (s *ProcessService) warnIfReloadSignalUnhandled() {
	if s.reloadSignal == 0 || s.pid <= 0 {
		return
	}
	disp, err := process.ReloadSignalDisposition(s.pid, s.reloadSignal)
	if err != nil || disp == process.SignalHandled {
		return
	}
	// ServiceLogger carries Error and Info and nothing between them.
	// Info is the honest level: nothing has failed, the service is
	// running. The wording has to carry the weight instead.
	//
	// Two different faults, and saying the wrong one would be worse
	// than saying nothing: an ignored signal is discarded, so a reload
	// is a no-op, while a defaulted one terminates the service.
	switch disp {
	case process.SignalIgnored:
		s.services.logger.Info(
			"Service '%s': WARNING: reload-signal is %s but pid %d ignores it — "+
				"a reload would do nothing at all",
			s.serviceName, signalName(s.reloadSignal), s.pid)
	default:
		s.services.logger.Info(
			"Service '%s': WARNING: reload-signal is %s but pid %d neither catches, "+
				"blocks nor ignores it — a reload would terminate this service "+
				"instead of reloading it",
			s.serviceName, signalName(s.reloadSignal), s.pid)
	}
}

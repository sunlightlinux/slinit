// slinit is a service manager and init system inspired by dinit, written in Go.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/config"
	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/eventloop"
	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/platform"
	"github.com/sunlightlinux/slinit/pkg/process"
	"github.com/sunlightlinux/slinit/pkg/service"
	"github.com/sunlightlinux/slinit/pkg/shutdown"
	"github.com/sunlightlinux/slinit/pkg/snapshot"
	"github.com/sunlightlinux/slinit/pkg/utmp"
	"github.com/sunlightlinux/slinit/pkg/watchdog"
	"golang.org/x/sys/unix"
)

// version is injected at build time via:
//   go build -ldflags "-X main.version=v1.10.10" ./cmd/slinit
// Local builds without ldflags report "dev".
var version = "dev"

const (
	defaultSystemServiceDir = "/etc/slinit.d"
	defaultUserServiceDir   = ".config/slinit.d"
	defaultBootService      = "boot"
	defaultSystemSocket     = "/run/slinit.socket"
	defaultUserSocket       = ".slinitctl"
)

// stringSlice implements flag.Value for repeated -t/--service flags.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	bootStartTime := time.Now()

	// SysV init compatibility: when invoked as halt/poweroff/reboot (via
	// /sbin/halt → slinit symlinks), act as a thin CLI that requests the
	// shutdown via the control socket. Must run before flag.Parse so the
	// compat shim isn't confused by slinit's own flag set.
	if handleSysVCompat() {
		return
	}

	// Parse command-line flags
	var (
		serviceDirs    string
		socketPath     string
		systemMode     bool
		userMode       bool
		containerMode  bool
		bootServices   stringSlice
		showVersion    bool
		logLevel       string
		consoleLevel   string
		quietMode      bool
		autoRecovery   bool
		envFile         string
		readyFD         int
		logFile         string
		cgroupPath      string
		cpuAffinityStr  string
		restoreSnapPath string
	)

	flag.StringVar(&serviceDirs, "services-dir", "", "service description directory (comma-separated for multiple)")
	flag.StringVar(&serviceDirs, "d", "", "service description directory (comma-separated for multiple)")
	flag.StringVar(&socketPath, "socket-path", "", "control socket path")
	flag.StringVar(&socketPath, "p", "", "control socket path")
	flag.BoolVar(&systemMode, "system", false, "run as system service manager")
	flag.BoolVar(&systemMode, "s", false, "run as system service manager")
	flag.BoolVar(&systemMode, "m", false, "run as system manager (even if not PID 1)")
	flag.BoolVar(&systemMode, "system-mgr", false, "run as system manager (even if not PID 1)")
	flag.BoolVar(&userMode, "user", false, "run as user service manager")
	flag.BoolVar(&userMode, "u", false, "run as user service manager")
	flag.BoolVar(&containerMode, "o", false, "run in container mode (for Docker/LXC/Podman)")
	flag.BoolVar(&containerMode, "container", false, "run in container mode (for Docker/LXC/Podman)")
	flag.Var(&bootServices, "t", "service to start at boot (can be specified multiple times)")
	flag.Var(&bootServices, "service", "service to start at boot (can be specified multiple times)")
	flag.BoolVar(&showVersion, "version", false, "show version and exit")
	flag.StringVar(&logLevel, "log-level", "info", "log level (debug, info, notice, warn, error)")
	flag.StringVar(&consoleLevel, "console-level", "", "minimum level for console output (overrides log-level for console)")
	flag.BoolVar(&quietMode, "q", false, "suppress all but error output (equivalent to --console-level error)")
	flag.BoolVar(&quietMode, "quiet", false, "suppress all but error output (equivalent to --console-level error)")
	flag.BoolVar(&autoRecovery, "r", false, "auto-run recovery service on boot failure")
	flag.BoolVar(&autoRecovery, "auto-recovery", false, "auto-run recovery service on boot failure")
	flag.StringVar(&envFile, "e", "", "environment file to load at startup")
	flag.StringVar(&envFile, "env-file", "", "environment file to load at startup")
	flag.IntVar(&readyFD, "F", -1, "file descriptor to notify when boot service is ready")
	flag.IntVar(&readyFD, "ready-fd", -1, "file descriptor to notify when boot service is ready")
	flag.StringVar(&logFile, "l", "", "log to file instead of console")
	flag.StringVar(&logFile, "log-file", "", "log to file instead of console")
	flag.StringVar(&cgroupPath, "b", "", "default cgroup base path for services")
	flag.StringVar(&cgroupPath, "cgroup-path", "", "default cgroup base path for services")
	flag.StringVar(&cpuAffinityStr, "cpu-affinity", "", "default CPU affinity for daemon and services (e.g. 0-3)")
	flag.StringVar(&cpuAffinityStr, "a", "", "default CPU affinity for daemon and services (e.g. 0-3)")
	flag.StringVar(&restoreSnapPath, "restore-from-snapshot", "",
		"replay operator intent (activations, pins, triggers, global env) from a snapshot file written by a prior slinit instance")

	var catchAllLog string
	var noCatchAll bool
	flag.StringVar(&catchAllLog, "catch-all-log", "", "catch-all log file path (default: /run/slinit/catch-all.log)")
	flag.BoolVar(&noCatchAll, "B", false, "disable catch-all logger")
	flag.BoolVar(&noCatchAll, "no-catch-all", false, "disable catch-all logger")

	var shutdownGrace string
	flag.StringVar(&shutdownGrace, "shutdown-grace", "3s", "SIGTERM→SIGKILL grace period during shutdown (e.g. 3s, 5000ms)")

	var bootBanner string
	var initUmask string
	var consoleDup bool
	var devtmpfsPath string
	var runMode string
	var kcmdlineDest string
	flag.StringVar(&bootBanner, "banner", "slinit booting...", "boot banner (empty to disable)")
	flag.StringVar(&initUmask, "umask", "0022", "initial umask (octal)")
	flag.BoolVar(&consoleDup, "1", false, "duplicate log output to /dev/console (when using --log-file)")
	flag.BoolVar(&consoleDup, "console-dup", false, "duplicate log output to /dev/console (when using --log-file)")
	flag.StringVar(&devtmpfsPath, "devtmpfs-path", "/dev", "mount devtmpfs at this path (empty disables the mount)")
	flag.StringVar(&runMode, "run-mode", "mount", "how to stage /run at boot (mount|remount|keep)")
	flag.StringVar(&kcmdlineDest, "kcmdline-dest", "/run/slinit/kcmdline", "snapshot /proc/cmdline to this path (empty disables)")

	var timestampFormat string
	flag.StringVar(&timestampFormat, "timestamp-format", "wallclock", "log timestamp format (wallclock|iso|tai64n|none)")

	var noWall bool
	flag.BoolVar(&noWall, "no-wall", false, "disable wall broadcasts at shutdown")

	var bootRlimits string
	flag.StringVar(&bootRlimits, "rlimits", "",
		"global resource limits applied to slinit and inherited by every service "+
			"(comma-separated name=soft[:hard], e.g. 'nofile=65536,core=0,stack=8388608:unlimited')")

	var parallelStartLimit int
	var parallelSlowThreshold string
	var sysOverride string
	var confDir string
	flag.IntVar(&parallelStartLimit, "parallel-start-limit", 0, "max concurrent service starts (0=unlimited)")
	flag.StringVar(&parallelSlowThreshold, "parallel-start-slow-threshold", "10s", "time before a starting service is considered slow")

	var watchdogDevice string
	var watchdogTimeoutStr string
	var watchdogIntervalStr string
	var noWatchdog bool
	flag.StringVar(&watchdogDevice, "watchdog-device", "", "hardware watchdog character device (auto-detected when empty)")
	flag.StringVar(&watchdogTimeoutStr, "watchdog-timeout", "60s", "kernel-side watchdog timeout (e.g. 30s, 2m)")
	flag.StringVar(&watchdogIntervalStr, "watchdog-interval", "", "watchdog ping interval (default: timeout/3)")
	flag.BoolVar(&noWatchdog, "no-watchdog", false, "disable hardware watchdog feeder even when running as PID 1")
	flag.StringVar(&sysOverride, "sys", "", "override platform detection (docker, lxc, podman, wsl, xen0, xenu, none)")
	flag.StringVar(&sysOverride, "S", "", "override platform detection (short for --sys)")
	flag.StringVar(&confDir, "conf-dir", "", "override conf.d overlay directories (comma-separated; 'none' disables overlays)")

	flag.Parse()

	if showVersion {
		detected := platform.Detect()
		if detected == platform.None {
			fmt.Printf("slinit version %s (platform: bare-metal)\n", version)
		} else {
			fmt.Printf("slinit version %s (platform: %s)\n", version, detected)
		}
		os.Exit(0)
	}

	// Determine mode
	isPID1 := os.Getpid() == 1

	// Safety net: if slinit panics, catch it and perform emergency cleanup.
	// PID 1: kill all processes + force reboot. Container: exit(111).
	defer shutdown.CrashRecovery(isPID1, containerMode)

	// Catch-all logger: capture stdout/stderr through a pipe so that early
	// boot messages, child process output, and panics are preserved to a
	// persistent log file while still being visible on the console.
	// Inspired by s6-linux-init's catch-all logger (s6-svscan-log).
	// Enabled by default for PID 1 and container mode; use -B to disable.
	//
	// When running as PID 1 we must stage /run *before* opening the log
	// file — otherwise InitPID1 will later mount a fresh tmpfs over /run
	// and hide whatever file we opened on the initramfs's /run. StageRun
	// is idempotent, so InitPID1's own call below is a no-op.
	var cal *logging.CatchAllLogger
	if (isPID1 || containerMode) && !noCatchAll {
		if isPID1 {
			if rm, err := shutdown.ParseRunMode(runMode); err == nil {
				shutdown.SetRunMode(rm)
			}
			shutdown.StageRun(logging.New(logging.LevelError))

			// Defensive against an in-place exec from a prior slinit
			// (soft-reboot path): the previous instance dup2'd fd 1/2
			// to its catch-all pipe write end, which inherits across
			// exec but loses its reader (the prior drain goroutine is
			// gone). StartCatchAll's Dup(1) would then save that dead
			// pipe end as "console" — drain goroutine writes a couple
			// of bytes, kernel buffer fills, EPIPE, every subsequent
			// log line is silently dropped.
			//
			// Open /dev/console fresh and bind it to fd 1/2 here.
			// On a clean kernel boot this is a no-op (fd 1/2 are
			// already /dev/console); on soft-reboot it restores the
			// invariant that StartCatchAll expects.
			if cf, err := os.OpenFile("/dev/console", os.O_RDWR, 0); err == nil {
				syscall.Dup2(int(cf.Fd()), 1)
				syscall.Dup2(int(cf.Fd()), 2)
				cf.Close()
			}
		}
		var err error
		cal, err = logging.StartCatchAll(catchAllLog)
		if err != nil {
			fmt.Fprintf(os.Stderr, "slinit: catch-all logger: %v (continuing without)\n", err)
			cal = nil
		} else {
			defer cal.Stop()
		}
	}

	// SysV init compatibility: "init 0" → poweroff, "init 6" → reboot,
	// "init N" (N in 1..5) → start runlevel-N. OpenRC-style named
	// runlevels (single, nonetwork, default, boot, sysinit) also
	// dispatch to the corresponding runlevel-<name> service. Slinit
	// has no native runlevel concept — these are pure aliases, so the
	// admin must define runlevel-N / runlevel-<name> services for the
	// dispatch to have anything to do.
	if !isPID1 {
		args := flag.Args()
		if len(args) > 0 {
			switch args[0] {
			case "0":
				sendShutdownAndExit(socketPath, systemMode, service.ShutdownPoweroff)
			case "6":
				sendShutdownAndExit(socketPath, systemMode, service.ShutdownReboot)
			case "1", "2", "3", "4", "5":
				startServiceAndExit(socketPath, systemMode, "runlevel-"+args[0])
			case "single", "nonetwork", "default", "boot", "sysinit":
				startServiceAndExit(socketPath, systemMode, "runlevel-"+args[0])
			}
		}
	}

	// Positional args are treated as service names.
	// When running as PID 1 on Linux, the kernel passes unrecognized cmdline
	// args to init, so we only accept known service names ("single") and
	// ignore everything else — matching dinit behavior.
	// However, if -m (--system-mgr) or -o (--container) was explicitly given,
	// the user controls the command line, so accept all args (dinit's
	// process_sys_args logic: filtering only when PID1+root without -m/-o).
	processSysArgs := isPID1 && !systemMode && !containerMode
	if processSysArgs {
		for _, arg := range flag.Args() {
			if arg == "single" {
				bootServices = append(bootServices, arg)
			}
			// Other kernel parameters (e.g. "nopti", "auto") are ignored
		}
	} else {
		for _, arg := range flag.Args() {
			if len(arg) > 0 && arg[0] == '-' {
				fmt.Fprintf(os.Stderr, "slinit: unrecognized option: %s\n", arg)
				os.Exit(1)
			}
			bootServices = append(bootServices, arg)
		}
	}

	// Default to "boot" if no services specified
	if len(bootServices) == 0 {
		bootServices = []string{defaultBootService}
	}

	if isPID1 {
		systemMode = true
	}
	if containerMode {
		systemMode = true
	}
	if !systemMode && !userMode {
		// Default to user mode if not PID 1
		userMode = true
	}

	// Set up logger
	mainLogLevel := parseLogLevel(logLevel)
	consLevel := mainLogLevel
	if consoleLevel != "" {
		consLevel = parseLogLevel(consoleLevel)
	}
	if quietMode {
		consLevel = logging.LevelError
	}
	logger := logging.New(consLevel)
	logger.SetMainLevel(mainLogLevel)
	if tf, err := logging.ParseTimestampFormat(timestampFormat); err == nil {
		logging.SetTimestampFormat(tf)
	} else {
		fmt.Fprintf(os.Stderr, "slinit: %v (using default wallclock)\n", err)
	}

	// Redirect log output to file (--log-file/-l)
	if logFile != "" {
		lf, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0640)
		if err != nil {
			fmt.Fprintf(os.Stderr, "slinit: cannot open log file '%s': %v\n", logFile, err)
			os.Exit(1)
		}
		defer lf.Close()
		logger.SetOutput(lf)
	} else if systemMode {
		// In system mode without --log-file, use syslog as the main log
		// facility (like dinit's /dev/log connection).
		if err := logger.SetSyslog(); err != nil {
			// Syslog may not be available yet (e.g. read-only rootfs);
			// this is not fatal — we'll keep logging to console.
			logger.Debug("syslog not available: %v", err)
		} else {
			defer logger.CloseSyslog()
		}
	}

	// Console duplicate: tee log output to /dev/console even when
	// --log-file redirects the primary output to a file.
	// Inspired by s6-linux-init-maker's -1 flag.
	if consoleDup {
		cons, err := os.OpenFile("/dev/console", os.O_WRONLY, 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "slinit: --console-dup: %v (ignored)\n", err)
		} else {
			defer cons.Close()
			logger.SetConsoleDup(cons)
		}
	}

	// Apply shutdown grace period.
	if grace, err := time.ParseDuration(shutdownGrace); err == nil {
		shutdown.SetKillGracePeriod(grace)
		logger.Debug("Shutdown grace period: %v", grace)
	} else {
		logger.Error("Invalid --shutdown-grace %q: %v (using default %v)",
			shutdownGrace, err, shutdown.DefaultKillGracePeriod)
	}

	// Apply boot housekeeping settings before InitPID1.
	shutdown.SetBootBanner(bootBanner)
	if mask, err := strconv.ParseUint(initUmask, 8, 32); err == nil {
		shutdown.SetInitUmask(uint32(mask))
	} else {
		logger.Error("Invalid --umask %q: %v (using default 0022)", initUmask, err)
	}
	shutdown.SetDevtmpfsPath(devtmpfsPath)
	if rm, err := shutdown.ParseRunMode(runMode); err == nil {
		shutdown.SetRunMode(rm)
	} else {
		logger.Error("Invalid --run-mode %q: %v (using default mount)", runMode, err)
	}
	shutdown.SetKcmdlineDest(kcmdlineDest)

	// Wall broadcasts at shutdown (enabled by default, disable with --no-wall).
	shutdown.SetWallEnabled(!noWall)

	// Global rlimits: parse the --rlimits flag now so failures surface
	// immediately. The values are applied after InitPID1/InitContainer so
	// they take effect before any service starts — child processes inherit
	// them automatically on fork.
	var parsedRlimits []shutdown.BootRlimit
	if bootRlimits != "" {
		var err error
		parsedRlimits, err = shutdown.ParseBootRlimits(bootRlimits)
		if err != nil {
			fmt.Fprintf(os.Stderr, "slinit: --rlimits: %v\n", err)
			os.Exit(1)
		}
	}

	if containerMode {
		logger.Notice("slinit starting in container mode (PID %d)", os.Getpid())
		if err := shutdown.InitContainer(logger); err != nil {
			logger.Error("Container initialization warning: %v", err)
		}
	} else if isPID1 {
		logger.Notice("slinit starting as PID 1 (init system mode)")
		if err := shutdown.InitPID1(logger); err != nil {
			logger.Error("PID 1 initialization warning: %v", err)
		}
		// InitPID1's setupConsole Dup2s fd 1/2 to /dev/console, which
		// breaks the catch-all redirect set up earlier. Re-attach so
		// every subsequent log line goes through the pipe — this keeps
		// timestamps strictly monotonic in print order. Without this,
		// messages logged before InitPID1 (e.g. "starting as PID 1")
		// sit buffered in the pipe and flush AFTER later direct-to-
		// console writes, producing visibly out-of-order timestamps.
		if cal != nil {
			if err := cal.ReattachStdoutErr(); err != nil {
				logger.Error("Failed to re-attach catch-all: %v", err)
			}
		}
	} else if systemMode {
		logger.Notice("slinit starting in system mode")
	} else {
		logger.Info("slinit starting in user mode")
	}

	// Apply global rlimits to slinit itself. Every child forked from here
	// inherits these, so they act as a system-wide default that per-service
	// rlimit-* settings can further tighten.
	if len(parsedRlimits) > 0 {
		n := shutdown.ApplyBootRlimits(parsedRlimits, logger)
		logger.Info("Applied %d/%d global rlimits", n, len(parsedRlimits))
	}

	// Hardware watchdog feeder: only meaningful when we're system manager
	// (PID 1 or container PID 1). The feeder programs the kernel timer
	// and pings at a sub-timeout cadence; if slinit hangs the kernel
	// resets the box. Auto-enable when running as PID 1 with a watchdog
	// device present; disable explicitly with --no-watchdog or by
	// pointing --watchdog-device at a non-existent path.
	wd := startWatchdog(isPID1, containerMode, noWatchdog,
		watchdogDevice, watchdogTimeoutStr, watchdogIntervalStr, logger)

	// Determine service directories
	dirs := resolveServiceDirs(serviceDirs, systemMode)
	logger.Info("Service directories: %v", dirs)

	// Determine socket path
	sock := resolveSocketPath(socketPath, systemMode)
	logger.Debug("Control socket: %s", sock)

	// Create service set
	serviceSet := service.NewServiceSet(logger)

	// Wire UTMP callbacks (keeps service pkg cgo-free)
	serviceSet.OnUtmpCreate = func(id, line string, pid int) {
		utmp.CreateEntry(id, line, pid)
	}
	serviceSet.OnUtmpClear = func(id, line string) {
		utmp.ClearEntry(id, line)
	}
	serviceSet.OnRWReady = func() {
		if utmp.LogBoot() {
			logger.Info("Boot time logged to utmp/wtmp")
		}
	}

	// Load daemon-level environment file (--env-file/-e)
	if envFile != "" {
		if fileEnv, err := process.ReadEnvFile(envFile); err == nil {
			env := make([]string, 0, len(fileEnv))
			for k, v := range fileEnv {
				env = append(env, k+"="+v)
			}
			serviceSet.SetGlobalEnv(env)
			logger.Info("Loaded %d variables from env-file '%s'", len(fileEnv), envFile)
		} else {
			logger.Error("Failed to read env-file '%s': %v", envFile, err)
		}
	}

	// Set default cgroup base path (--cgroup-path/-b)
	if cgroupPath != "" {
		serviceSet.SetDefaultCgroupPath(cgroupPath)
		logger.Info("Default cgroup path: %s", cgroupPath)
	}

	// Discover slinit-runner so services that configure mlockall(2) or
	// set_mempolicy(2) can have those syscalls applied via the helper
	// before exec'ing the real command. The discovery is best-effort:
	// if we don't find the binary, those settings silently degrade and
	// the operator gets a startup warning when services try to use them.
	if runner := findSlinitRunner(); runner != "" {
		serviceSet.SetRunnerPath(runner)
		logger.Debug("slinit-runner discovered at %s", runner)
	}

	// Set global CPU affinity (--cpu-affinity/-a)
	if cpuAffinityStr != "" {
		cpus, err := config.ParseCPUAffinity(cpuAffinityStr)
		if err != nil {
			logger.Error("Invalid --cpu-affinity %q: %v", cpuAffinityStr, err)
		} else {
			// Apply to slinit daemon itself
			var cpuSet unix.CPUSet
			for _, c := range cpus {
				cpuSet.Set(int(c))
			}
			if err := unix.SchedSetaffinity(0, &cpuSet); err != nil {
				logger.Error("Failed to set daemon CPU affinity: %v", err)
			} else {
				logger.Info("Daemon CPU affinity set to: %s", cpuAffinityStr)
			}
			// Set as default for all child services
			serviceSet.SetDefaultCPUAffinity(cpus)
		}
	}

	// Set ready notification fd (--ready-fd/-F)
	// dinit writes the control socket path to this fd; we do the same.
	if readyFD >= 0 {
		// Validate the fd is actually open (like dinit's fcntl check)
		if readyFD == 0 {
			logger.Error("ready-fd cannot be stdin (fd 0)")
		} else if _, err := unix.FcntlInt(uintptr(readyFD), unix.F_GETFD, 0); err != nil {
			logger.Error("ready-fd %d is not open: %v", readyFD, err)
		} else {
			// Set FD_CLOEXEC to prevent leaking to child processes
			if readyFD > 2 {
				unix.CloseOnExec(readyFD)
			}
			serviceSet.SetReadyFD(readyFD)
			readySock := sock // capture resolved socket path
			serviceSet.OnBootReady = func() {
				f := os.NewFile(uintptr(readyFD), "ready-fd")
				// Write socket path + null terminator (dinit compat)
				if _, err := f.Write(append([]byte(readySock), 0)); err != nil {
					logger.Error("Failed to write to ready-fd %d: %v", readyFD, err)
				}
				f.Close()
				logger.Info("Readiness notification sent on fd %d (socket: %s)", readyFD, readySock)
			}
		}
	}

	// Configure parallel start limiter
	if parallelStartLimit > 0 {
		slowThresh, err := time.ParseDuration(parallelSlowThreshold)
		if err != nil {
			logger.Error("Invalid --parallel-start-slow-threshold: %v, using 10s", err)
			slowThresh = 10 * time.Second
		}
		serviceSet.SetStartLimiter(parallelStartLimit, slowThresh)
		logger.Info("Parallel start limit: %d (slow threshold: %v)", parallelStartLimit, slowThresh)
	}

	// Record boot timing (use first service as the boot timing target)
	serviceSet.SetBootStartTime(bootStartTime)
	serviceSet.SetBootServiceName(bootServices[0])
	if uptime, err := readKernelUptime(); err == nil {
		serviceSet.SetKernelUptime(uptime)
	}

	// Detect or override platform for keyword-based service filtering
	var detectedPlatform platform.Type
	if sysOverride != "" {
		if sysOverride == "none" {
			detectedPlatform = platform.None
		} else if platform.IsValid(sysOverride) {
			detectedPlatform = platform.Type(strings.ToLower(sysOverride))
			logger.Info("Platform override: %s", detectedPlatform)
		} else {
			logger.Error("Invalid --sys value %q (valid: docker, lxc, podman, wsl, xen0, xenu, openvz, vserver, systemd-nspawn, uml, rkt, none)", sysOverride)
			os.Exit(1)
		}
	} else {
		detectedPlatform = platform.Detect()
		if detectedPlatform != platform.None {
			logger.Info("Detected platform: %s", detectedPlatform)
		}
	}

	// Create and configure the loader
	loader := config.NewDirLoader(serviceSet, dirs)
	loader.SetPlatform(detectedPlatform)

	// Configure conf.d overlay directories.
	// Default (--conf-dir not passed) keeps built-in /etc/slinit.conf.d.
	// --conf-dir=none disables overlays; otherwise comma-separated list.
	if confDir != "" {
		if confDir == "none" {
			loader.SetOverlayDirs(nil)
		} else {
			parts := strings.Split(confDir, ",")
			cleaned := parts[:0]
			for _, p := range parts {
				if p = strings.TrimSpace(p); p != "" {
					cleaned = append(cleaned, p)
				}
			}
			loader.SetOverlayDirs(cleaned)
		}
	}

	// Enable init.d fallback (auto-detect SysV init scripts)
	var initDDirs []string
	for _, d := range config.DefaultInitDDirs {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			initDDirs = append(initDDirs, d)
		}
	}
	if len(initDDirs) > 0 {
		loader.SetInitDDirs(initDDirs)
	}

	serviceSet.SetLoader(loader)

	// Load and start boot services (-t svc1 -t svc2 ... or positional args)
	startedAny := false
	for _, svcName := range bootServices {
		svc, err := serviceSet.LoadService(svcName)
		if err != nil {
			logger.Error("Failed to load service '%s': %v", svcName, err)
			continue
		}
		serviceSet.StartService(svc)
		// "activation requested", not "STARTED": StartService only
		// schedules the start; the service may still be STARTING (or
		// blocked on a trigger / dependency / waits-for) when this
		// returns. The actual STARTED transition is logged by
		// ServiceLogger.ServiceStarted from the state machine.
		logger.Info("Boot service '%s' activation requested", svcName)
		startedAny = true
	}

	if !startedAny {
		if containerMode {
			logger.Error("No boot services could be loaded, exiting (container mode)")
			closeWatchdog(wd, logger)
			os.Exit(1)
		}
		if isPID1 {
			logger.Error("No service files found in %v", dirs)
			logger.Error("Create at least '%s' in one of the service directories", bootServices[0])
			logger.Error("Rebooting in 10 seconds...")
			time.Sleep(10 * time.Second)
			closeWatchdog(wd, logger)
			shutdown.Execute(service.ShutdownReboot, logger)
		}
		closeWatchdog(wd, logger)
		os.Exit(1)
	}

	// Replay operator intent from a prior slinit instance if requested.
	// Boot services are already activated by this point; snapshot adds
	// additional intent (manual activations, pins, triggers, global env)
	// that the boot graph alone cannot reconstruct.
	if restoreSnapPath != "" {
		applySnapshot(restoreSnapPath, serviceSet, logger)
	}

	// Start control socket server
	ctx := context.Background()
	ctrlServer := control.NewServer(serviceSet, sock, logger)
	if err := ctrlServer.Start(ctx); err != nil {
		logger.Error("Failed to start control socket: %v", err)
		// Non-fatal: continue without control socket
	} else {
		defer ctrlServer.Stop()
	}

	// Wire pass-cs-fd: when a service creates a socketpair, the server end
	// becomes a control connection handled by the control server.
	serviceSet.OnPassCSFD = func(conn net.Conn) {
		ctrlServer.HandlePassCSFD(conn)
	}

	// Boot loop: runs the event loop, handles boot failures with recovery
	for {
		loop := eventloop.New(serviceSet, logger)

		if containerMode {
			loop.SetContainerMode(true)
			loop.SetPID1Mode(true) // enable boot failure detection
		} else if isPID1 {
			loop.SetPID1Mode(true)
		}

		ctrlServer.ShutdownFunc = func(st service.ShutdownType) {
			loop.InitiateShutdown(st)
		}
		ctrlServer.WallFunc = func(st service.ShutdownType, delay time.Duration, cancelled bool) {
			if cancelled {
				shutdown.WallShutdownCancelled(st, logger)
				return
			}
			shutdown.WallShutdownNotice(st, delay, logger)
		}
		loop.OnReopenSocket = func() {
			if err := ctrlServer.Reopen(); err != nil {
				logger.Error("Failed to reopen control socket: %v", err)
			}
		}

		// Capture operator-visible intent for soft-reboot. The new
		// slinit binary picks this up via --restore-from-snapshot
		// (appended to argv inside SoftReboot below).
		loop.OnPreShutdown = func(st service.ShutdownType) {
			if st != service.ShutdownSoftReboot {
				return
			}
			snap := snapshot.Capture(serviceSet)
			if err := snapshot.Write(snapshot.SoftRebootPath, snap); err != nil {
				logger.Error("Soft-reboot snapshot write failed: %v", err)
				return
			}
			logger.Info("Soft-reboot snapshot saved to %s (%d service intents, %d global env vars)",
				snapshot.SoftRebootPath, len(snap.Services), len(snap.GlobalEnv))
		}

		// /etc/slinit/shutdown.allow access control: only engage when
		// running as PID 1 (the only situation where CAD-style signal
		// escalation from local users is a concern). Containers and
		// user-mode slinit don't need this — their shutdown paths are
		// already gated by the runtime or by filesystem permissions.
		if isPID1 && !containerMode {
			allowPath := shutdown.FindShutdownAllow(shutdown.DefaultShutdownAllowPaths)
			if allowPath != "" {
				logger.Notice("Shutdown access control enabled via %s", allowPath)
				loop.SignalShutdownGate = func(sigName string) bool {
					allowed, _ := shutdown.CheckShutdownAllow(allowPath, logger)
					return allowed
				}
			}
		}

		if err := loop.Run(ctx); err != nil {
			if err == context.Canceled {
				logger.Info("Event loop cancelled")
			} else {
				logger.Error("Event loop error: %v", err)
			}
		}

		shutdownType := loop.GetShutdownType()

		// Container mode: write results and exit with appropriate code.
		// Inspired by s6-linux-init's container-results directory.
		if containerMode {
			exitCode := 0
			if shutdownType != service.ShutdownNone {
				// Normal shutdown — collect exit code from boot service.
				exitCode = containerExitCode(serviceSet, bootServices)
				logger.Info("Container shutdown complete (exit code %d, type %s)",
					exitCode, shutdownType)
			} else {
				// Boot failure — no explicit shutdown was requested.
				exitCode = 1
				if ec := containerExitCode(serviceSet, bootServices); ec != 0 {
					exitCode = ec
				}
				shutdownType = service.ShutdownPoweroff
				logger.Error("Boot failure detected (container mode, exit code %d)", exitCode)
			}
			if err := shutdown.WriteContainerResults(exitCode, shutdownType); err != nil {
				logger.Debug("Failed to write container results: %v", err)
			}
			closeWatchdog(wd, logger)
			os.Exit(exitCode)
		}

		// Normal shutdown (non-PID1 or explicit shutdown requested)
		if !isPID1 {
			break
		}
		if shutdownType != service.ShutdownNone {
			closeWatchdog(wd, logger)
			handlePID1Shutdown(shutdownType, logger)
			// handlePID1Shutdown does not return
		}

		// Boot failure detected: all services stopped without shutdown
		logger.Error("Boot failure detected")
		syscall.Sync() // minimize data loss

		if autoRecovery {
			logger.Notice("Auto-recovery enabled, attempting to start 'recovery' service")
			if tryStartService("recovery", serviceSet, loader, logger) {
				serviceSet.ResetBootTiming()
				continue // re-enter boot loop
			}
			logger.Error("Failed to start recovery service, rebooting")
			closeWatchdog(wd, logger)
			shutdown.Execute(service.ShutdownReboot, logger)
		}

		// Interactive prompt (no -r flag)
		action := confirmRestartBoot(logger)
		switch action {
		case 'r':
			logger.Notice("User chose reboot")
			closeWatchdog(wd, logger)
			shutdown.Execute(service.ShutdownReboot, logger)
		case 'e':
			logger.Notice("User chose recovery")
			if tryStartService("recovery", serviceSet, loader, logger) {
				serviceSet.ResetBootTiming()
				continue
			}
			logger.Error("Failed to start recovery service, rebooting")
			closeWatchdog(wd, logger)
			shutdown.Execute(service.ShutdownReboot, logger)
		case 's':
			logger.Notice("User chose restart boot sequence")
			if tryStartServices(bootServices, serviceSet, loader, logger) {
				serviceSet.ResetBootTiming()
				continue
			}
			logger.Error("Failed to restart boot services, rebooting")
			closeWatchdog(wd, logger)
			shutdown.Execute(service.ShutdownReboot, logger)
		case 'p':
			logger.Notice("User chose poweroff")
			closeWatchdog(wd, logger)
			shutdown.Execute(service.ShutdownPoweroff, logger)
		default:
			logger.Error("Invalid choice, rebooting")
			closeWatchdog(wd, logger)
			shutdown.Execute(service.ShutdownReboot, logger)
		}
	}

	closeWatchdog(wd, logger)
	logger.Info("slinit shutdown complete")
}

// handlePID1Shutdown performs the appropriate system action after all services
// have stopped when running as PID 1. Called only for explicit shutdowns
// (shutdownType != ShutdownNone). This function does not return.
func handlePID1Shutdown(shutdownType service.ShutdownType, logger *logging.Logger) {
	switch shutdownType {
	case service.ShutdownSoftReboot:
		logger.Notice("Performing soft reboot")
		if err := shutdown.SoftReboot(logger); err != nil {
			logger.Error("Soft reboot failed: %v, falling back to hard reboot", err)
			shutdown.Execute(service.ShutdownReboot, logger)
		}
		// SoftReboot calls exec, should not reach here
		shutdown.InfiniteHold()

	case service.ShutdownHalt, service.ShutdownPoweroff, service.ShutdownReboot, service.ShutdownKexec:
		shutdown.Execute(shutdownType, logger)

	case service.ShutdownRemain:
		logger.Notice("Shutdown type is REMAIN, staying up with no services")
		shutdown.InfiniteHold()

	default:
		logger.Error("Unknown shutdown type: %s, halting", shutdownType)
		shutdown.Execute(service.ShutdownHalt, logger)
	}
}

func parseLogLevel(s string) logging.Level {
	switch strings.ToLower(s) {
	case "debug":
		return logging.LevelDebug
	case "info":
		return logging.LevelInfo
	case "notice":
		return logging.LevelNotice
	case "warn", "warning":
		return logging.LevelWarn
	case "error":
		return logging.LevelError
	default:
		return logging.LevelInfo
	}
}

func resolveServiceDirs(flagValue string, systemMode bool) []string {
	if flagValue != "" {
		return strings.Split(flagValue, ",")
	}

	if systemMode {
		return []string{
			"/etc/slinit.d",
			"/run/slinit.d",
			"/usr/local/lib/slinit.d",
			"/lib/slinit.d",
		}
	}

	// User mode: multiple dirs like dinit
	home, err := os.UserHomeDir()
	if err != nil {
		return []string{defaultUserServiceDir}
	}
	dirs := []string{}
	// Prefer $XDG_CONFIG_HOME/slinit.d if set
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		dirs = append(dirs, xdg+"/slinit.d")
	} else {
		dirs = append(dirs, home+"/.config/slinit.d")
	}
	dirs = append(dirs,
		"/etc/slinit.d/user",
		"/usr/lib/slinit.d/user",
		"/usr/local/lib/slinit.d/user",
	)
	return dirs
}

// readKernelUptime reads /proc/uptime and returns the system uptime duration.
// This represents the time from kernel boot to when slinit started.
func readKernelUptime() (time.Duration, error) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return 0, fmt.Errorf("unexpected /proc/uptime format")
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(secs * float64(time.Second)), nil
}

func resolveSocketPath(flagValue string, systemMode bool) string {
	if flagValue != "" {
		return flagValue
	}

	if systemMode {
		return defaultSystemSocket
	}

	// User mode: prefer $XDG_RUNTIME_DIR/slinitctl (like dinit),
	// fall back to $HOME/.slinitctl
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return xdg + "/slinitctl"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return defaultUserSocket
	}
	return home + "/" + defaultUserSocket
}

// sendShutdownAndExit connects to the running slinit instance via the control
// socket and sends a shutdown command. Used for SysV init compatibility
// (e.g. "init 0" for poweroff, "init 6" for reboot).
func sendShutdownAndExit(socketFlag string, systemFlag bool, shutType service.ShutdownType) {
	// When invoked as "init 0/6", we're typically root targeting the system instance
	sock := resolveSocketPath(socketFlag, systemFlag || os.Getuid() == 0)

	conn, err := net.Dial("unix", sock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit: cannot connect to %s: %v\n", sock, err)
		os.Exit(1)
	}
	defer conn.Close()

	payload := []byte{uint8(shutType)}
	if err := control.WritePacket(conn, control.CmdShutdown, payload); err != nil {
		fmt.Fprintf(os.Stderr, "slinit: shutdown request failed: %v\n", err)
		os.Exit(1)
	}

	rply, _, err := control.ReadPacket(conn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit: reading shutdown reply: %v\n", err)
		os.Exit(1)
	}

	if rply != control.RplyACK {
		fmt.Fprintf(os.Stderr, "slinit: shutdown not acknowledged (reply: %d)\n", rply)
		os.Exit(1)
	}

	os.Exit(0)
}

// startServiceAndExit connects to the running slinit instance and asks
// it to start the named service. Used for SysV-style `init N` dispatch
// (N in 1..5) which we translate into "start the runlevel-N service".
//
// The dance mirrors slinitctl's cmdStart: load the service (→ handle),
// then issue CmdStartService. A missing service is reported as an
// actionable error so the operator knows they need to define one.
func startServiceAndExit(socketFlag string, systemFlag bool, name string) {
	sock := resolveSocketPath(socketFlag, systemFlag || os.Getuid() == 0)

	conn, err := net.Dial("unix", sock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit: cannot connect to %s: %v\n", sock, err)
		os.Exit(1)
	}
	defer conn.Close()

	// Load — yields a handle if the service exists.
	if err := control.WritePacket(conn, control.CmdLoadService, control.EncodeServiceName(name)); err != nil {
		fmt.Fprintf(os.Stderr, "slinit: load %s: %v\n", name, err)
		os.Exit(1)
	}
	rply, payload, err := control.ReadPacket(conn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit: reading load reply: %v\n", err)
		os.Exit(1)
	}
	if rply != control.RplyServiceRecord {
		fmt.Fprintf(os.Stderr,
			"slinit: cannot start %s (reply: %d) — define a %q service to use this runlevel alias\n",
			name, rply, name)
		os.Exit(1)
	}
	if len(payload) < 5 {
		fmt.Fprintf(os.Stderr, "slinit: truncated load reply (%d bytes)\n", len(payload))
		os.Exit(1)
	}
	handle := binary.LittleEndian.Uint32(payload[1:5])

	// Start — flags=0 (no pin, not stop). EncodeHandle pads the 1-byte
	// flag field to match the wire format used by slinitctl.
	startPayload := make([]byte, 5)
	binary.LittleEndian.PutUint32(startPayload[0:4], handle)
	startPayload[4] = 0
	if err := control.WritePacket(conn, control.CmdStartService, startPayload); err != nil {
		fmt.Fprintf(os.Stderr, "slinit: start %s: %v\n", name, err)
		os.Exit(1)
	}
	rply, _, err = control.ReadPacket(conn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit: reading start reply: %v\n", err)
		os.Exit(1)
	}
	switch rply {
	case control.RplyACK, control.RplyAlreadySS:
		os.Exit(0)
	case control.RplyShuttingDown:
		fmt.Fprintln(os.Stderr, "slinit: system is shutting down")
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "slinit: start %s not acknowledged (reply: %d)\n", name, rply)
		os.Exit(1)
	}
}

// tryStartServices attempts to load and start all named services. Returns true
// if at least one service was successfully started.
func tryStartServices(names []string, serviceSet *service.ServiceSet, loader *config.DirLoader, logger *logging.Logger) bool {
	ok := false
	for _, name := range names {
		if tryStartService(name, serviceSet, loader, logger) {
			ok = true
		}
	}
	return ok
}

// applySnapshot reads a snapshot from path, ensures every service it
// names is loaded (so Restore can resolve it), then applies the
// operator intent. A missing snapshot file is logged and ignored —
// fresh boots without a prior soft-reboot are normal.
func applySnapshot(path string, serviceSet *service.ServiceSet, logger *logging.Logger) {
	snap, err := snapshot.Read(path)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Info("No snapshot at %s — proceeding with fresh boot", path)
			return
		}
		logger.Error("Snapshot %s: %v — proceeding without restore", path, err)
		return
	}

	// Pre-load any service that the snapshot names but the boot graph
	// hasn't pulled in yet. Errors are non-fatal: restore.go logs and
	// skips unresolved entries, which is the right behaviour when a
	// service file was removed between the snapshot and this boot.
	for _, e := range snap.Services {
		if e.Name == "" {
			continue
		}
		if _, err := serviceSet.LoadService(e.Name); err != nil {
			logger.Warn("Snapshot references service %q which failed to load: %v", e.Name, err)
		}
	}

	if _, err := snapshot.Restore(serviceSet, snap, logger); err != nil {
		logger.Error("Snapshot restore failed: %v", err)
		return
	}

	// Snapshot consumed. Remove it so a later restart of slinit
	// (e.g. an operator manually re-running it for diagnostics)
	// does not silently replay stale intent. The file is on tmpfs
	// for the soft-reboot path, so this is just hygiene; for an
	// operator-supplied path it is the only thing that prevents
	// accidental double-restore.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		logger.Warn("Failed to remove consumed snapshot %s: %v", path, err)
	}
}

// tryStartService attempts to load and start a named service. Returns true on success.
// Used by the boot failure recovery loop to restart "boot" or "recovery" services.
func tryStartService(name string, serviceSet *service.ServiceSet, loader *config.DirLoader, logger *logging.Logger) bool {
	svc, err := serviceSet.LoadService(name)
	if err != nil {
		logger.Error("Failed to load service '%s': %v", name, err)
		return false
	}
	serviceSet.StartService(svc)
	logger.Info("Service '%s' activation requested for recovery", name)
	return true
}

// confirmRestartBoot displays an interactive prompt on /dev/console for the user
// to choose an action after a boot failure. Returns one of: 'r' (reboot),
// 'e' (recovery), 's' (restart boot), 'p' (poweroff).
// Falls back to 'r' (reboot) if the console cannot be opened.
func confirmRestartBoot(logger *logging.Logger) byte {
	f, err := os.OpenFile("/dev/console", os.O_RDWR, 0)
	if err != nil {
		logger.Error("Cannot open /dev/console for recovery prompt: %v", err)
		return 'r'
	}
	defer f.Close()

	fd := int(f.Fd())

	// Save current terminal settings
	oldTermios, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		logger.Error("Cannot get terminal settings: %v", err)
		return 'r'
	}

	// Set raw mode: disable canonical mode and echo
	rawTermios := *oldTermios
	rawTermios.Lflag &^= unix.ICANON | unix.ECHO
	rawTermios.Cc[unix.VMIN] = 1
	rawTermios.Cc[unix.VTIME] = 0

	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &rawTermios); err != nil {
		logger.Error("Cannot set raw terminal mode: %v", err)
		return 'r'
	}

	// Restore terminal on return
	defer unix.IoctlSetTermios(fd, unix.TCSETS, oldTermios)

	msg := "\n\nAll services have stopped with no shutdown issued; boot failure?\n" +
		"Choose: (r)eboot, r(e)covery, re(s)tart boot sequence, (p)ower off? "
	f.WriteString(msg)

	// Read a single byte
	buf := make([]byte, 1)
	for {
		n, err := f.Read(buf)
		if err != nil || n == 0 {
			logger.Error("Failed to read from console: %v", err)
			return 'r'
		}
		ch := buf[0]
		if ch == 'r' || ch == 'e' || ch == 's' || ch == 'p' {
			f.WriteString(string(ch) + "\n")
			return ch
		}
		// Ignore invalid keys, re-prompt
	}
}

// startWatchdog opens the hardware watchdog, programs the kernel timeout,
// and starts a background goroutine that pings it on a ticker. Returns
// nil when the feeder is disabled, when the device is missing, or when
// programming fails — in those cases slinit continues without a feeder
// and logs a warning so the operator can spot the misconfiguration.
//
// Auto-enable rule: PID 1 or container PID 1, --no-watchdog not set,
// and a watchdog device available. User-mode invocations never arm the
// hardware watchdog (the kernel only lets one process own the device,
// and PID 1 should always be that process on a real init system).
func startWatchdog(isPID1, containerMode, noWatchdog bool,
	device, timeoutStr, intervalStr string,
	logger *logging.Logger,
) *watchdog.Feeder {
	if noWatchdog {
		return nil
	}
	if !isPID1 && !containerMode {
		return nil
	}

	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		logger.Error("Invalid --watchdog-timeout %q: %v (watchdog disabled)", timeoutStr, err)
		return nil
	}
	var interval time.Duration
	if intervalStr != "" {
		interval, err = time.ParseDuration(intervalStr)
		if err != nil {
			logger.Error("Invalid --watchdog-interval %q: %v (watchdog disabled)", intervalStr, err)
			return nil
		}
	}

	wd, err := watchdog.Open(watchdog.Config{
		Device:   device,
		Timeout:  timeout,
		Interval: interval,
	})
	if err != nil {
		// Most common cause is "no device" on bare-metal kernels without
		// a watchdog driver, or in containers. Surface as Notice not
		// Error — running without a watchdog is degraded but expected
		// in many environments.
		logger.Notice("Hardware watchdog unavailable: %v (continuing without)", err)
		return nil
	}
	logger.Notice("Hardware watchdog armed: device=%s timeout=%s interval=%s",
		wd.Device(), wd.Timeout(), wd.Interval())

	go func() {
		if err := wd.Run(context.Background()); err != nil {
			logger.Error("Watchdog feeder stopped: %v", err)
		}
	}()
	return wd
}

// findSlinitRunner locates the slinit-runner exec helper. Order:
//   1. Same directory as the running slinit binary (typical PID 1
//      install where everything lives in /sbin or /usr/sbin).
//   2. PATH lookup for "slinit-runner".
//   3. Hard-coded /usr/sbin and /sbin fallbacks.
// Returns "" when none are present — services that configure mlockall
// or NUMA will then log a warning and start without those settings.
func findSlinitRunner() string {
	const name = "slinit-runner"
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	for _, p := range []string{"/usr/sbin/" + name, "/sbin/" + name, "/usr/local/sbin/" + name} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// closeWatchdog disarms the kernel watchdog before any shutdown / reboot
// path. Idempotent: safe to call from every exit point even if the
// feeder was never opened or has already been closed.
func closeWatchdog(wd *watchdog.Feeder, logger *logging.Logger) {
	if wd == nil {
		return
	}
	if err := wd.Close(); err != nil {
		logger.Error("Watchdog close: %v", err)
	}
}

// containerExitCode extracts the exit code from the first boot service
// that has a non-zero exit status. Returns 0 if all services exited cleanly.
func containerExitCode(ss *service.ServiceSet, bootNames []string) int {
	for _, name := range bootNames {
		svc := ss.FindService(name, false)
		if svc == nil {
			continue
		}
		es := svc.GetExitStatus()
		if es.Exited() && es.ExitCode() != 0 {
			return es.ExitCode()
		}
		if es.Signaled() {
			// Convention: 128 + signal number
			return 128 + int(es.Signal())
		}
	}
	return 0
}

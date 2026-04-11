// slinit is a service manager and init system inspired by dinit, written in Go.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
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
	"github.com/sunlightlinux/slinit/pkg/utmp"
	"golang.org/x/sys/unix"
)

const (
	version = "0.1.0"

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
		envFile        string
		readyFD        int
		logFile        string
		cgroupPath     string
		cpuAffinityStr string
	)

	flag.StringVar(&serviceDirs, "services-dir", "", "service description directory (comma-separated for multiple)")
	flag.StringVar(&socketPath, "socket-path", "", "control socket path")
	flag.BoolVar(&systemMode, "system", false, "run as system service manager")
	flag.BoolVar(&systemMode, "m", false, "run as system manager (even if not PID 1)")
	flag.BoolVar(&systemMode, "system-mgr", false, "run as system manager (even if not PID 1)")
	flag.BoolVar(&userMode, "user", false, "run as user service manager")
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
	flag.StringVar(&bootBanner, "banner", "slinit booting...", "boot banner (empty to disable)")
	flag.StringVar(&initUmask, "umask", "0022", "initial umask (octal)")
	flag.BoolVar(&consoleDup, "1", false, "duplicate log output to /dev/console (when using --log-file)")
	flag.BoolVar(&consoleDup, "console-dup", false, "duplicate log output to /dev/console (when using --log-file)")

	var parallelStartLimit int
	var parallelSlowThreshold string
	var sysOverride string
	var confDir string
	flag.IntVar(&parallelStartLimit, "parallel-start-limit", 0, "max concurrent service starts (0=unlimited)")
	flag.StringVar(&parallelSlowThreshold, "parallel-start-slow-threshold", "10s", "time before a starting service is considered slow")
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
	if (isPID1 || containerMode) && !noCatchAll {
		cal, err := logging.StartCatchAll(catchAllLog)
		if err != nil {
			fmt.Fprintf(os.Stderr, "slinit: catch-all logger: %v (continuing without)\n", err)
		} else {
			defer cal.Stop()
		}
	}

	// SysV init compatibility: "init 0" → poweroff, "init 6" → reboot
	// When not PID 1, numeric arguments trigger shutdown via control socket.
	if !isPID1 {
		args := flag.Args()
		if len(args) > 0 {
			switch args[0] {
			case "0":
				sendShutdownAndExit(socketPath, systemMode, service.ShutdownPoweroff)
			case "6":
				sendShutdownAndExit(socketPath, systemMode, service.ShutdownReboot)
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
	} else if systemMode {
		logger.Notice("slinit starting in system mode")
	} else {
		logger.Info("slinit starting in user mode")
	}

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
		logger.Info("Boot service '%s' started", svcName)
		startedAny = true
	}

	if !startedAny {
		if containerMode {
			logger.Error("No boot services could be loaded, exiting (container mode)")
			os.Exit(1)
		}
		if isPID1 {
			logger.Error("No service files found in %v", dirs)
			logger.Error("Create at least '%s' in one of the service directories", bootServices[0])
			logger.Error("Rebooting in 10 seconds...")
			time.Sleep(10 * time.Second)
			shutdown.Execute(service.ShutdownReboot, logger)
		}
		os.Exit(1)
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
		loop.OnReopenSocket = func() {
			if err := ctrlServer.Reopen(); err != nil {
				logger.Error("Failed to reopen control socket: %v", err)
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
			os.Exit(exitCode)
		}

		// Normal shutdown (non-PID1 or explicit shutdown requested)
		if !isPID1 {
			break
		}
		if shutdownType != service.ShutdownNone {
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
			shutdown.Execute(service.ShutdownReboot, logger)
		}

		// Interactive prompt (no -r flag)
		action := confirmRestartBoot(logger)
		switch action {
		case 'r':
			logger.Notice("User chose reboot")
			shutdown.Execute(service.ShutdownReboot, logger)
		case 'e':
			logger.Notice("User chose recovery")
			if tryStartService("recovery", serviceSet, loader, logger) {
				serviceSet.ResetBootTiming()
				continue
			}
			logger.Error("Failed to start recovery service, rebooting")
			shutdown.Execute(service.ShutdownReboot, logger)
		case 's':
			logger.Notice("User chose restart boot sequence")
			if tryStartServices(bootServices, serviceSet, loader, logger) {
				serviceSet.ResetBootTiming()
				continue
			}
			logger.Error("Failed to restart boot services, rebooting")
			shutdown.Execute(service.ShutdownReboot, logger)
		case 'p':
			logger.Notice("User chose poweroff")
			shutdown.Execute(service.ShutdownPoweroff, logger)
		default:
			logger.Error("Invalid choice, rebooting")
			shutdown.Execute(service.ShutdownReboot, logger)
		}
	}

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

// tryStartService attempts to load and start a named service. Returns true on success.
// Used by the boot failure recovery loop to restart "boot" or "recovery" services.
func tryStartService(name string, serviceSet *service.ServiceSet, loader *config.DirLoader, logger *logging.Logger) bool {
	svc, err := serviceSet.LoadService(name)
	if err != nil {
		logger.Error("Failed to load service '%s': %v", name, err)
		return false
	}
	serviceSet.StartService(svc)
	logger.Info("Service '%s' started for recovery", name)
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

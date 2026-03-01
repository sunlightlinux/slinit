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

func main() {
	bootStartTime := time.Now()

	// Parse command-line flags
	var (
		serviceDirs   string
		socketPath    string
		systemMode    bool
		userMode      bool
		containerMode bool
		bootService   string
		showVersion   bool
		logLevel      string
		autoRecovery  bool
	)

	flag.StringVar(&serviceDirs, "services-dir", "", "service description directory (comma-separated for multiple)")
	flag.StringVar(&socketPath, "socket-path", "", "control socket path")
	flag.BoolVar(&systemMode, "system", false, "run as system service manager")
	flag.BoolVar(&userMode, "user", false, "run as user service manager")
	flag.BoolVar(&containerMode, "o", false, "run in container mode (for Docker/LXC/Podman)")
	flag.BoolVar(&containerMode, "container", false, "run in container mode (for Docker/LXC/Podman)")
	flag.StringVar(&bootService, "boot-service", defaultBootService, "name of the boot service to start")
	flag.BoolVar(&showVersion, "version", false, "show version and exit")
	flag.StringVar(&logLevel, "log-level", "info", "log level (debug, info, notice, warn, error)")
	flag.BoolVar(&autoRecovery, "r", false, "auto-run recovery service on boot failure")
	flag.BoolVar(&autoRecovery, "auto-recovery", false, "auto-run recovery service on boot failure")

	flag.Parse()

	if showVersion {
		fmt.Printf("slinit version %s\n", version)
		os.Exit(0)
	}

	// Determine mode
	isPID1 := os.Getpid() == 1

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
	level := parseLogLevel(logLevel)
	logger := logging.New(level)

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

	// Record boot timing
	serviceSet.SetBootStartTime(bootStartTime)
	serviceSet.SetBootServiceName(bootService)
	if uptime, err := readKernelUptime(); err == nil {
		serviceSet.SetKernelUptime(uptime)
	}

	// Create and configure the loader
	loader := config.NewDirLoader(serviceSet, dirs)
	serviceSet.SetLoader(loader)

	// Load and start the boot service
	bootSvc, err := serviceSet.LoadService(bootService)
	if err != nil {
		logger.Error("Failed to load boot service '%s': %v", bootService, err)
		if containerMode {
			logger.Error("Boot service not found, exiting (container mode)")
			os.Exit(1)
		}
		if isPID1 {
			logger.Error("No service files found in %v", dirs)
			logger.Error("Create at least '%s' in one of the service directories", bootService)
			logger.Error("Rebooting in 10 seconds...")
			time.Sleep(10 * time.Second)
			shutdown.Execute(service.ShutdownReboot, logger)
		}
		os.Exit(1)
	}

	serviceSet.StartService(bootSvc)
	logger.Info("Boot service '%s' started", bootService)

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

		if err := loop.Run(ctx); err != nil {
			if err == context.Canceled {
				logger.Info("Event loop cancelled")
			} else {
				logger.Error("Event loop error: %v", err)
			}
		}

		shutdownType := loop.GetShutdownType()

		// Container mode: exit with appropriate code
		if containerMode {
			if shutdownType != service.ShutdownNone {
				logger.Info("Container shutdown complete")
				os.Exit(0)
			}
			// Boot failure in container mode
			logger.Error("Boot failure detected (container mode)")
			os.Exit(1)
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
				continue
			}
			logger.Error("Failed to start recovery service, rebooting")
			shutdown.Execute(service.ShutdownReboot, logger)
		case 's':
			logger.Notice("User chose restart boot sequence")
			if tryStartService(bootService, serviceSet, loader, logger) {
				continue
			}
			logger.Error("Failed to restart boot service, rebooting")
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
	return []string{
		home + "/.config/slinit.d",
		"/etc/slinit.d/user",
		"/usr/lib/slinit.d/user",
	}
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

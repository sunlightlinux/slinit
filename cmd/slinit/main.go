// slinit is a service manager and init system inspired by dinit, written in Go.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sunlightlinux/slinit/pkg/config"
	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/eventloop"
	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
	"github.com/sunlightlinux/slinit/pkg/shutdown"
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
		bootService   string
		showVersion   bool
		logLevel      string
	)

	flag.StringVar(&serviceDirs, "services-dir", "", "service description directory (comma-separated for multiple)")
	flag.StringVar(&socketPath, "socket-path", "", "control socket path")
	flag.BoolVar(&systemMode, "system", false, "run as system service manager")
	flag.BoolVar(&userMode, "user", false, "run as user service manager")
	flag.StringVar(&bootService, "boot-service", defaultBootService, "name of the boot service to start")
	flag.BoolVar(&showVersion, "version", false, "show version and exit")
	flag.StringVar(&logLevel, "log-level", "info", "log level (debug, info, notice, warn, error)")

	flag.Parse()

	if showVersion {
		fmt.Printf("slinit version %s\n", version)
		os.Exit(0)
	}

	// Determine mode
	isPID1 := os.Getpid() == 1
	if isPID1 {
		systemMode = true
	}
	if !systemMode && !userMode {
		// Default to user mode if not PID 1
		userMode = true
	}

	// Set up logger
	level := parseLogLevel(logLevel)
	logger := logging.New(level)

	if isPID1 {
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
		if isPID1 {
			logger.Error("Cannot proceed without boot service in init mode")
			// In PID 1 mode, we can't just exit
			select {}
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

	// Run the event loop
	loop := eventloop.New(serviceSet, logger)

	// Enable PID 1 mode on event loop (boot failure detection, orphan reaping)
	if isPID1 {
		loop.SetPID1Mode(true)
	}

	// Wire shutdown from control protocol to event loop
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

	// Handle post-loop shutdown actions
	shutdownType := loop.GetShutdownType()

	if isPID1 {
		handlePID1Shutdown(shutdownType, logger)
		// handlePID1Shutdown does not return
	}

	logger.Info("slinit shutdown complete")
}

// handlePID1Shutdown performs the appropriate system action after all services
// have stopped when running as PID 1. This function does not return.
func handlePID1Shutdown(shutdownType service.ShutdownType, logger *logging.Logger) {
	switch shutdownType {
	case service.ShutdownNone:
		// Boot failure: services stopped without explicit shutdown
		logger.Error("Boot failure detected, attempting reboot")
		shutdown.Execute(service.ShutdownReboot, logger)

	case service.ShutdownSoftReboot:
		logger.Notice("Performing soft reboot")
		if err := shutdown.SoftReboot(logger); err != nil {
			logger.Error("Soft reboot failed: %v, falling back to hard reboot", err)
			shutdown.Execute(service.ShutdownReboot, logger)
		}
		// SoftReboot calls exec, should not reach here
		shutdown.InfiniteHold()

	case service.ShutdownHalt, service.ShutdownPoweroff, service.ShutdownReboot:
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
		return []string{defaultSystemServiceDir}
	}

	// User mode: ~/.config/slinit.d
	home, err := os.UserHomeDir()
	if err != nil {
		return []string{defaultUserServiceDir}
	}
	return []string{home + "/" + defaultUserServiceDir}
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

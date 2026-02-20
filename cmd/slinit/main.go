// slinit is a service manager and init system inspired by dinit, written in Go.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/config"
	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/eventloop"
	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
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

	logger.Info("slinit shutdown complete")
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

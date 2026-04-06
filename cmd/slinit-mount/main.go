// slinit-mount — autofs lazy mount daemon for slinit.
// Sets up autofs mount points so that filesystems are mounted on-demand
// when accessed, and optionally unmounted after an idle timeout.
//
// Usage:
//
//	slinit-mount [options]
//	slinit-mount -d /etc/slinit.d/mount.d --foreground
package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/sunlightlinux/slinit/pkg/autofs"
	"golang.org/x/sys/unix"
)

const (
	defaultSystemMountDir = "/etc/slinit.d/mount.d"
	defaultExpireInterval = 60 // seconds between expiry sweeps
)

type daemonConfig struct {
	mountDirs      []string
	foreground     bool
	verbose        bool
	expireInterval int // seconds
}

func main() {
	cfg := parseArgs()

	if len(cfg.mountDirs) == 0 {
		cfg.mountDirs = []string{defaultSystemMountDir}
	}

	// Set up logging
	logger := log.New(os.Stderr, "slinit-mount: ", log.LstdFlags)

	// Load mount units
	units, err := autofs.LoadMountUnits(cfg.mountDirs)
	if err != nil {
		fatal("load mount units: %v", err)
	}

	if len(units) == 0 {
		logger.Println("no mount units found, exiting")
		os.Exit(0)
	}

	logger.Printf("loaded %d mount unit(s)", len(units))

	// Create mount handler
	handler := autofs.NewMountHandler(logger)

	// Set up autofs mounts and register pipe fds
	type mountInfo struct {
		am   *autofs.AutofsMount
		unit *autofs.MountUnit
	}

	fdMap := make(map[int]*mountInfo) // pipe fd → mount info
	var activeMounts []*autofs.AutofsMount

	for _, unit := range units {
		am, err := autofs.Setup(unit)
		if err != nil {
			logger.Printf("WARNING: failed to set up autofs for %s (%s): %v",
				unit.Name, unit.Where, err)
			continue
		}
		fdMap[am.PipeFD()] = &mountInfo{am: am, unit: unit}
		activeMounts = append(activeMounts, am)
		logger.Printf("autofs mounted: %s → %s (%s)", unit.Name, unit.Where, unit.Type)
	}

	if len(activeMounts) == 0 {
		fatal("no autofs mounts could be established")
	}

	// Create epoll instance
	epfd, err := unix.EpollCreate1(unix.EPOLL_CLOEXEC)
	if err != nil {
		fatal("epoll_create1: %v", err)
	}
	defer unix.Close(epfd)

	// Register pipe fds with epoll
	for fd := range fdMap {
		event := unix.EpollEvent{
			Events: unix.EPOLLIN,
			Fd:     int32(fd),
		}
		if err := unix.EpollCtl(epfd, unix.EPOLL_CTL_ADD, fd, &event); err != nil {
			fatal("epoll_ctl add fd %d: %v", fd, err)
		}
	}

	// Create timerfd for periodic expiry sweeps
	timerFD, err := unix.TimerfdCreate(unix.CLOCK_MONOTONIC, unix.TFD_CLOEXEC|unix.TFD_NONBLOCK)
	if err != nil {
		fatal("timerfd_create: %v", err)
	}
	defer unix.Close(timerFD)

	interval := cfg.expireInterval
	if interval <= 0 {
		interval = defaultExpireInterval
	}
	timerSpec := unix.ItimerSpec{
		Interval: unix.NsecToTimespec(int64(interval) * 1e9),
		Value:    unix.NsecToTimespec(int64(interval) * 1e9),
	}
	if err := unix.TimerfdSettime(timerFD, 0, &timerSpec, nil); err != nil {
		fatal("timerfd_settime: %v", err)
	}

	timerEvent := unix.EpollEvent{
		Events: unix.EPOLLIN,
		Fd:     int32(timerFD),
	}
	if err := unix.EpollCtl(epfd, unix.EPOLL_CTL_ADD, timerFD, &timerEvent); err != nil {
		fatal("epoll_ctl add timerfd: %v", err)
	}

	// Signal handling
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	// Main event loop
	logger.Println("daemon ready, entering event loop")
	buf := make([]byte, autofs.V5PacketSize)
	events := make([]unix.EpollEvent, len(fdMap)+2)

	running := true
	for running {
		n, err := unix.EpollWait(epfd, events, -1)
		if err != nil {
			if err == unix.EINTR {
				// Check for signals
				select {
				case sig := <-sigCh:
					if sig == syscall.SIGHUP {
						logger.Println("SIGHUP received, reloading config...")
						// TODO: implement config reload
						continue
					}
					logger.Printf("signal %v received, shutting down", sig)
					running = false
					continue
				default:
					continue
				}
			}
			fatal("epoll_wait: %v", err)
		}

		for i := 0; i < n; i++ {
			fd := int(events[i].Fd)

			if fd == timerFD {
				// Timer expired — drain timerfd and run expiry sweep
				var tbuf [8]byte
				unix.Read(timerFD, tbuf[:])

				for _, am := range activeMounts {
					expired, err := am.ExpireMulti()
					if err != nil {
						logger.Printf("expire sweep on %s: %v", am.Mountpoint(), err)
					}
					if expired > 0 && cfg.verbose {
						logger.Printf("expired %d entries on %s", expired, am.Mountpoint())
					}
				}
				continue
			}

			mi, ok := fdMap[fd]
			if !ok {
				continue
			}

			// Read autofs packet from pipe
			nread, err := unix.Read(fd, buf)
			if err != nil {
				if err == unix.EINTR {
					continue
				}
				logger.Printf("read pipe fd %d: %v", fd, err)
				continue
			}

			if nread < autofs.V5PacketSize {
				logger.Printf("short read from pipe fd %d: %d bytes", fd, nread)
				continue
			}

			pkt, err := autofs.ParseV5Packet(buf[:nread])
			if err != nil {
				logger.Printf("parse packet: %v", err)
				continue
			}

			if err := handler.HandlePacket(mi.am, pkt); err != nil {
				logger.Printf("handle packet: %v", err)
			}
		}

		// Non-blocking signal check
		select {
		case sig := <-sigCh:
			if sig == syscall.SIGHUP {
				logger.Println("SIGHUP received, reloading config...")
				continue
			}
			logger.Printf("signal %v received, shutting down", sig)
			running = false
		default:
		}
	}

	// Graceful shutdown: close all autofs mounts
	logger.Println("shutting down, unmounting all autofs entries...")
	for _, am := range activeMounts {
		if err := am.Close(); err != nil {
			logger.Printf("close %s: %v", am.Mountpoint(), err)
		}
	}
	logger.Println("shutdown complete")
}

func parseArgs() daemonConfig {
	cfg := daemonConfig{
		expireInterval: defaultExpireInterval,
	}

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-d", "--mount-dir":
			if i+1 >= len(args) {
				fatal("--mount-dir requires an argument")
			}
			i++
			cfg.mountDirs = append(cfg.mountDirs, args[i])
		case "-f", "--foreground":
			cfg.foreground = true
		case "-v", "--verbose":
			cfg.verbose = true
		case "--expire-interval":
			if i+1 >= len(args) {
				fatal("--expire-interval requires an argument")
			}
			i++
			fmt.Sscanf(args[i], "%d", &cfg.expireInterval)
		case "-h", "--help":
			printUsage()
			os.Exit(0)
		default:
			fatal("unknown option: %s", args[i])
		}
	}

	return cfg
}

func printUsage() {
	exe := filepath.Base(os.Args[0])
	fmt.Fprintf(os.Stderr, `Usage: %s [options]

Autofs lazy mount daemon for slinit. Sets up on-demand mount points
that are automatically mounted when accessed and unmounted after idle timeout.

Options:
  -d, --mount-dir DIR      Mount unit directory (default: %s)
                            Can be specified multiple times
  -f, --foreground         Run in foreground (don't daemonize)
  -v, --verbose            Verbose logging
      --expire-interval N  Seconds between expiry sweeps (default: %d)
  -h, --help               Show this help

Mount unit files (*.mount) use key=value format:
  what = /dev/sda1          Source device or path
  where = /mnt/data         Mount point (required, absolute)
  type = ext4               Filesystem type (required)
  options = rw,noatime      Mount options
  timeout = 300             Idle timeout in seconds (0 = never unmount)
  autofs-type = indirect    "indirect" (default) or "direct"
  directory-mode = 0755     Permissions for auto-created directories
  after: network-online     slinit service dependency

`, exe, defaultSystemMountDir, defaultExpireInterval)
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "slinit-mount: "+format+"\n", args...)
	os.Exit(1)
}

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
	"slices"
	"sync"
	"syscall"
	"time"

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
	expireInterval int    // seconds
	socketPath     string // -p override of the control socket
}

type mountInfo struct {
	am   *autofs.AutofsMount
	unit *autofs.MountUnit
}

// daemon is the state owned by the main event loop. Only the waiter
// goroutines of deferred units run elsewhere, and they touch nothing but
// ready (under readyMu) and readyFD.
type daemon struct {
	cfg          *daemonConfig
	logger       *log.Logger
	epfd         int
	fdMap        map[int]*mountInfo // pipe fd → mount info
	activeMounts []*autofs.AutofsMount
	pending      map[string]*pendingUnit // key → unit waiting for after:
	readyMu      sync.Mutex
	ready        []*pendingUnit // units whose after: services all started
	readyFD      int            // eventfd that wakes the loop for ready; -1 if none
	dial         dialFunc
	retry        time.Duration
}

// mountUnitKey returns the identity key for a mount unit (its Where path).
func mountUnitKey(mu *autofs.MountUnit) string {
	return mu.Where
}

// mountUnitChanged returns true if the unit config differs in a way that
// requires tearing down and re-establishing the autofs mount.
func mountUnitChanged(old, new *autofs.MountUnit) bool {
	return old.What != new.What ||
		old.Type != new.Type ||
		old.Options != new.Options ||
		old.Timeout != new.Timeout ||
		old.AutofsType != new.AutofsType ||
		old.DirMode != new.DirMode ||
		!slices.Equal(old.After, new.After)
}

// addUnit sets mu up now, or defers it until its after: services are
// STARTED.
func (d *daemon) addUnit(mu *autofs.MountUnit, how string) {
	if len(mu.After) > 0 {
		d.deferUnit(mu)
		return
	}
	d.setupUnit(mu, how)
}

// setupUnit establishes the autofs mount for mu and registers its pipe
// with epoll. how is "" or a tag for the log line (e.g. "reload").
func (d *daemon) setupUnit(mu *autofs.MountUnit, how string) bool {
	prefix := ""
	if how != "" {
		prefix = how + ": "
	}
	am, err := autofs.Setup(mu)
	if err != nil {
		d.logger.Printf("WARNING: %sfailed to set up autofs for %s (%s): %v",
			prefix, mu.Name, mu.Where, err)
		return false
	}
	fd := am.PipeFD()
	event := unix.EpollEvent{
		Events: unix.EPOLLIN,
		Fd:     int32(fd),
	}
	if err := unix.EpollCtl(d.epfd, unix.EPOLL_CTL_ADD, fd, &event); err != nil {
		d.logger.Printf("%sepoll_ctl add fd %d: %v", prefix, fd, err)
		am.Close()
		return false
	}
	d.fdMap[fd] = &mountInfo{am: am, unit: mu}
	d.activeMounts = append(d.activeMounts, am)
	if how != "" {
		d.logger.Printf("autofs mounted (%s): %s → %s (%s)", how, mu.Name, mu.Where, mu.Type)
	} else {
		d.logger.Printf("autofs mounted: %s → %s (%s)", mu.Name, mu.Where, mu.Type)
	}
	return true
}

// idle reports that nothing is mounted and nothing is waiting to be.
func (d *daemon) idle() bool {
	return len(d.fdMap) == 0 && len(d.pending) == 0
}

// reloadConfig re-reads mount unit files and reconciles the running state:
// - new units → setup autofs + register with epoll (or wait for after:)
// - removed units → tear down + deregister from epoll (or stop waiting)
// - changed units → tear down old + setup new
// - unchanged → keep as-is, set up or still waiting
func (d *daemon) reloadConfig() {
	logger := d.logger
	logger.Println("reloading mount unit configuration...")

	newUnits, err := autofs.LoadMountUnits(d.cfg.mountDirs)
	if err != nil {
		logger.Printf("reload failed: load mount units: %v", err)
		return
	}

	// Index new units by Where path
	newByKey := make(map[string]*autofs.MountUnit, len(newUnits))
	for _, u := range newUnits {
		newByKey[mountUnitKey(u)] = u
	}

	// Units still waiting for after: are dropped if gone or changed.
	for key, pu := range d.pending {
		nu, exists := newByKey[key]
		if !exists || mountUnitChanged(pu.unit, nu) {
			logger.Printf("dropping pending mount unit: %s (%s)", pu.unit.Name, pu.unit.Where)
			d.cancelPending(key)
		}
	}

	// Index current mounts by Where path (fd → key mapping for removal)
	oldByKey := make(map[string]int) // key → pipe fd
	for fd, mi := range d.fdMap {
		oldByKey[mountUnitKey(mi.unit)] = fd
	}

	// Phase 1: remove units that are gone or changed
	removedMounts := make(map[*autofs.AutofsMount]bool)
	for key, fd := range oldByKey {
		nu, exists := newByKey[key]
		if !exists || mountUnitChanged(d.fdMap[fd].unit, nu) {
			mi := d.fdMap[fd]
			removedMounts[mi.am] = true
			logger.Printf("removing autofs mount: %s (%s)", mi.unit.Name, mi.unit.Where)
			unix.EpollCtl(d.epfd, unix.EPOLL_CTL_DEL, fd, nil)
			if err := mi.am.Close(); err != nil {
				logger.Printf("close %s: %v", mi.unit.Where, err)
			}
			delete(d.fdMap, fd)
			delete(oldByKey, key)
		}
	}

	// Rebuild activeMounts slice (remove closed entries)
	if len(removedMounts) > 0 {
		var kept []*autofs.AutofsMount
		for _, am := range d.activeMounts {
			if !removedMounts[am] {
				kept = append(kept, am)
			}
		}
		d.activeMounts = kept
	}

	// Phase 2: add new units and re-add changed units
	var added, deferred int
	for _, nu := range newUnits {
		key := mountUnitKey(nu)
		if _, stillActive := oldByKey[key]; stillActive {
			continue // unchanged, already running
		}
		if _, stillPending := d.pending[key]; stillPending {
			continue // unchanged, still waiting
		}
		if len(nu.After) > 0 {
			d.deferUnit(nu)
			deferred++
			continue
		}
		if d.setupUnit(nu, "reload") {
			added++
		}
	}

	logger.Printf("reload complete: %d removed/changed, %d added, %d deferred, %d total active, %d pending",
		len(removedMounts), added, deferred, len(d.activeMounts), len(d.pending))
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

	// Create epoll instance
	epfd, err := unix.EpollCreate1(unix.EPOLL_CLOEXEC)
	if err != nil {
		fatal("epoll_create1: %v", err)
	}
	defer unix.Close(epfd)

	// eventfd through which waiter goroutines wake the loop when a
	// deferred unit's after: services have all started
	readyFD, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		fatal("eventfd: %v", err)
	}
	defer unix.Close(readyFD)
	readyEvent := unix.EpollEvent{
		Events: unix.EPOLLIN,
		Fd:     int32(readyFD),
	}
	if err := unix.EpollCtl(epfd, unix.EPOLL_CTL_ADD, readyFD, &readyEvent); err != nil {
		fatal("epoll_ctl add eventfd: %v", err)
	}

	d := &daemon{
		cfg:     &cfg,
		logger:  logger,
		epfd:    epfd,
		fdMap:   make(map[int]*mountInfo),
		pending: make(map[string]*pendingUnit),
		readyFD: readyFD,
		dial:    dialControl(resolveSocketPath(cfg.socketPath)),
		retry:   controlRetryInterval,
	}

	// Set up autofs mounts (or start waiting) and register pipe fds
	for _, unit := range units {
		d.addUnit(unit, "")
	}

	if d.idle() {
		fatal("no autofs mounts could be established")
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
	events := make([]unix.EpollEvent, len(d.fdMap)+3)

	running := true
	for running {
		n, err := unix.EpollWait(epfd, events, -1)
		if err != nil {
			if err == unix.EINTR {
				// Check for signals
				select {
				case sig := <-sigCh:
					if sig == syscall.SIGHUP {
						d.reloadConfig()
						// Resize events slice in case mount count changed
						if cap(events) < len(d.fdMap)+3 {
							events = make([]unix.EpollEvent, len(d.fdMap)+3)
						}
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

				for _, am := range d.activeMounts {
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

			if fd == readyFD {
				// Deferred unit(s) ready — drain eventfd and set them up
				var ebuf [8]byte
				unix.Read(readyFD, ebuf[:])
				d.takeReady()
				continue
			}

			mi, ok := d.fdMap[fd]
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
				d.reloadConfig()
			} else {
				logger.Printf("signal %v received, shutting down", sig)
				running = false
			}
		default:
		}
		if cap(events) < len(d.fdMap)+3 {
			events = make([]unix.EpollEvent, len(d.fdMap)+3)
		}
	}

	// Graceful shutdown: stop waiters, close all autofs mounts
	logger.Println("shutting down, unmounting all autofs entries...")
	for key := range d.pending {
		d.cancelPending(key)
	}
	for _, am := range d.activeMounts {
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
		case "-p", "--socket-path":
			if i+1 >= len(args) {
				fatal("--socket-path requires an argument")
			}
			i++
			cfg.socketPath = args[i]
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
  -p, --socket-path PATH   slinit control socket, for after: (default:
                            /run/slinit.socket as root, else the user socket)
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
  after: network-online     Set up only once these slinit services are started

`, exe, defaultSystemMountDir, defaultExpireInterval)
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "slinit-mount: "+format+"\n", args...)
	os.Exit(1)
}

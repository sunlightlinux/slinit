// slinit-shutdown: standalone shutdown utility for slinit.
//
// Can be invoked as:
//
//	slinit-shutdown [-r|-h|-p|-s|-k] [--system] [--use-passed-cfd]
//	slinit-reboot      (symlink — defaults to reboot)
//	slinit-halt        (symlink — defaults to halt)
//	slinit-soft-reboot (symlink — defaults to soft-reboot)
//
// When invoked without --system, it connects to the slinit daemon via
// the control socket and issues a shutdown command. With --system, it
// performs the shutdown sequence directly (kill all, umount, sync, reboot).
package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
	"github.com/sunlightlinux/slinit/pkg/shutdown"
	"github.com/sunlightlinux/slinit/pkg/utmp"
)

const (
	defaultSystemSocket = "/run/slinit.socket"
)

func main() {
	var (
		showHelp    bool
		sysShutdown bool
		useCFD      bool
		wtmpOnly    bool
		noWtmp      bool
		noSync      bool
		noWall      bool
	)

	shutdownType := defaultShutdownType()

	// Detect invocation name for symlink-based defaults
	execName := filepath.Base(os.Args[0])
	switch {
	case strings.HasSuffix(execName, "reboot") && !strings.HasSuffix(execName, "soft-reboot"):
		shutdownType = service.ShutdownReboot
	case strings.HasSuffix(execName, "soft-reboot"):
		shutdownType = service.ShutdownSoftReboot
	case strings.HasSuffix(execName, "halt"):
		shutdownType = service.ShutdownHalt
	}

	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch {
		case arg == "--help":
			showHelp = true
		case arg == "--system":
			sysShutdown = true
		// systemd(1) reboot/halt/poweroff surface — long-form aliases.
		case arg == "--halt":
			shutdownType = service.ShutdownHalt
		case arg == "--reboot":
			shutdownType = service.ShutdownReboot
		case arg == "--poweroff":
			// systemd: `--poweroff` overrides argv[0] except when the
			// binary is `reboot`, where it is ignored. Match that quirk.
			if !strings.HasSuffix(execName, "reboot") ||
				strings.HasSuffix(execName, "soft-reboot") {
				shutdownType = service.ShutdownPoweroff
			}
		case arg == "-r":
			shutdownType = service.ShutdownReboot
		case arg == "-h":
			// systemd: `-h` is short for --halt only for the poweroff
			// binary; for others it's already a shutdown type toggle.
			shutdownType = service.ShutdownHalt
		case arg == "-p":
			shutdownType = service.ShutdownPoweroff
		case arg == "-s":
			shutdownType = service.ShutdownSoftReboot
		case arg == "-k":
			shutdownType = service.ShutdownKexec
		// systemd(1) reboot(8) options that bypass the init contact
		// (or gate the utmp/wall/sync side effects).
		case arg == "-f", arg == "--force":
			sysShutdown = true
		case arg == "-w", arg == "--wtmp-only":
			wtmpOnly = true
		case arg == "-d", arg == "--no-wtmp":
			noWtmp = true
		case arg == "-n", arg == "--no-sync":
			noSync = true
		case arg == "--no-wall":
			noWall = true
		case arg == "--use-passed-cfd":
			useCFD = true
		case strings.HasPrefix(arg, "--grace="):
			if d, err := time.ParseDuration(arg[len("--grace="):]); err == nil {
				shutdown.SetKillGracePeriod(d)
			} else {
				fmt.Fprintf(os.Stderr, "Invalid --grace value: %s\n", arg[len("--grace="):])
				os.Exit(1)
			}
		default:
			fmt.Fprintf(os.Stderr, "Unrecognized option: %s\n", arg)
			os.Exit(1)
		}
	}

	if showHelp {
		printUsage(execName)
		os.Exit(0)
	}

	// -w / --wtmp-only writes the shutdown record and exits without
	// touching the init system or the reboot syscall. Matches systemd's
	// contract exactly.
	if wtmpOnly {
		utmp.LogShutdown()
		os.Exit(0)
	}

	// Propagate the systemd-style gating knobs to pkg/shutdown so they
	// apply to the direct path (--system / -f). They're no-ops on the
	// daemon path — the daemon owns its own wtmp/sync/wall policy.
	if noSync {
		shutdown.SetSyncEnabled(false)
	}
	if noWtmp {
		shutdown.SetWtmpEnabled(false)
	}
	if noWall {
		shutdown.SetWallEnabled(false)
	}

	if sysShutdown {
		doSystemShutdown(shutdownType)
		// Should not return
		os.Exit(1)
	}

	// Connect to daemon and issue shutdown command
	conn, err := connectToDaemon(useCFD)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to slinit daemon: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	if err := checkProtocolVersion(conn); err != nil {
		fmt.Fprintf(os.Stderr, "Protocol error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Issuing shutdown command...")

	payload := []byte{uint8(shutdownType)}
	if err := control.WritePacket(conn, control.CmdShutdown, payload); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to send shutdown command: %v\n", err)
		os.Exit(1)
	}

	rply, _, err := control.ReadPacket(conn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read reply: %v\n", err)
		os.Exit(1)
	}

	if rply != control.RplyACK {
		fmt.Fprintf(os.Stderr, "Shutdown command failed (reply: %d)\n", rply)
		os.Exit(1)
	}

	// Wait indefinitely — the system should shut down around us
	select {}
}

func defaultShutdownType() service.ShutdownType {
	return service.ShutdownPoweroff
}

func printUsage(execName string) {
	fmt.Fprintf(os.Stderr, `%s: shut down the system
  --help             show this help
  -r, --reboot       reboot the machine
  -h, --halt         halt the machine
  -p, --poweroff     power down the machine (default)
  -s                 soft-reboot (restart slinit with same arguments)
  -k                 execute kernel loaded via kexec
  -f, --force        bypass the init daemon (direct kernel reboot;
                     equivalent to --system, filesystems may not be
                     properly unmounted)
  -n, --no-sync      skip filesystem sync before rebooting
  -d, --no-wtmp      do not write a shutdown entry to utmp/wtmp
  -w, --wtmp-only    write only the shutdown entry to utmp/wtmp,
                     do not actually reboot
  --no-wall          do not broadcast the shutdown wall message
  --use-passed-cfd   use the socket fd from SLINIT_CS_FD env var
  --system           alias for --force (perform shutdown immediately,
                     not for normal use)
  --grace=DURATION   override the SIGTERM→SIGKILL grace period
`, execName)
}

func doSystemShutdown(shutdownType service.ShutdownType) {
	logger := logging.New(logging.LevelInfo)
	logger.SetOutput(openConsole())

	if shutdownType == service.ShutdownSoftReboot {
		// For soft reboot the daemon handles the re-exec; when called
		// with --system we just do the cleanup part and exit 0 so the
		// parent (slinit) can re-exec itself.
		shutdown.KillAllProcesses(logger)
		logger.Info("Syncing filesystems...")
		syncFilesystems()
		os.Exit(0)
	}

	shutdown.Execute(shutdownType, logger)
}

func openConsole() *os.File {
	f, err := os.OpenFile("/dev/console", os.O_WRONLY, 0)
	if err != nil {
		return os.Stderr
	}
	return f
}

func syncFilesystems() {
	syscall.Sync()
}

func connectToDaemon(useCFD bool) (net.Conn, error) {
	if useCFD {
		return connectPassedFD()
	}
	return net.Dial("unix", defaultSystemSocket)
}

func connectPassedFD() (net.Conn, error) {
	fdStr := os.Getenv("SLINIT_CS_FD")
	if fdStr == "" {
		return nil, fmt.Errorf("SLINIT_CS_FD environment variable not set")
	}
	fd, err := strconv.Atoi(fdStr)
	if err != nil {
		return nil, fmt.Errorf("invalid SLINIT_CS_FD value: %s", fdStr)
	}
	f := os.NewFile(uintptr(fd), "slinit-cs-fd")
	if f == nil {
		return nil, fmt.Errorf("invalid file descriptor: %d", fd)
	}
	conn, err := net.FileConn(f)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("FileConn failed: %w", err)
	}
	return conn, nil
}

func checkProtocolVersion(conn net.Conn) error {
	if err := control.WritePacket(conn, control.CmdQueryVersion, nil); err != nil {
		return err
	}
	rply, payload, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyCPVersion {
		return fmt.Errorf("unexpected reply to version query: %d", rply)
	}
	if len(payload) >= 2 {
		serverVer := uint16(payload[0]) | uint16(payload[1])<<8
		if serverVer < control.MinCompatVersion {
			return fmt.Errorf("server protocol version %d too old (need >= %d)", serverVer, control.MinCompatVersion)
		}
	}
	return nil
}

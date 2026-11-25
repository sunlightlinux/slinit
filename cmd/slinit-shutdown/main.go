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

// version is set at build time with -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	// --version, alone: checked before anything else runs, so no other
	// argument changes meaning. The value is stamped by the package
	// build (-X main.version=...).
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Printf("slinit-shutdown version %s\n", version)
		return
	}
	execName := filepath.Base(os.Args[0])
	o, err := parseArgs(execName, os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if o.grace != 0 {
		shutdown.SetKillGracePeriod(o.grace)
	}

	if o.showHelp {
		printUsage(execName)
		os.Exit(0)
	}

	// -w / --wtmp-only writes the shutdown record and exits without
	// touching the init system or the reboot syscall. Matches systemd's
	// contract exactly.
	if o.wtmpOnly {
		utmp.LogShutdown()
		os.Exit(0)
	}

	// -i / --interactive: require the operator to type the local
	// short hostname before proceeding. Applies before any shutdown
	// path (force / system / daemon) so the confirmation is universal.
	if o.interactive {
		verb := shutdownVerb(o.shutdownType)
		if err := shutdown.ConfirmHostname(verb); err != nil {
			fmt.Fprintf(os.Stderr, "slinit-shutdown: %v\n", err)
			os.Exit(1)
		}
	}

	// Propagate the systemd-style gating knobs to pkg/shutdown so they
	// apply to the direct path (--system / -f). They're no-ops on the
	// daemon path — the daemon owns its own wtmp/sync/wall policy.
	if o.noSync {
		shutdown.SetSyncEnabled(false)
	}
	if o.noWtmp {
		shutdown.SetWtmpEnabled(false)
	}
	if o.noWall {
		shutdown.SetWallEnabled(false)
	}

	// shutdown(8): a later time or -c goes to the daemon's scheduler,
	// which walls the countdown and refuses logins in the last five
	// minutes. "now" (or no time) keeps the path below.
	if execName == "shutdown" {
		delay, message, err := parseOperands(o.operands, time.Now())
		if err != nil {
			fmt.Fprintf(os.Stderr, "shutdown: %v\n", err)
			os.Exit(1)
		}
		if o.warnOnly && !o.cancel {
			if err := sendWarning(o.useCFD, shutdown.ShutdownNoticeText(o.shutdownType, delay, message)); err != nil {
				fmt.Fprintf(os.Stderr, "shutdown: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if o.cancel || delay > 0 {
			if o.forceMode || o.sysShutdown {
				fmt.Fprintln(os.Stderr, "shutdown: -f/--system cannot be combined with a scheduled time or -c")
				os.Exit(1)
			}
			if err := scheduleOrCancel(o.useCFD, o.shutdownType, delay, message, o.cancel); err != nil {
				fmt.Fprintf(os.Stderr, "shutdown: %v\n", err)
				os.Exit(1)
			}
			return
		}
	}

	if o.forceMode {
		doForceShutdown(o.shutdownType)
		os.Exit(1)
	}

	if o.sysShutdown {
		doSystemShutdown(o.shutdownType)
		// Should not return
		os.Exit(1)
	}

	// Connect to daemon and issue shutdown command
	conn, err := connectToDaemon(o.useCFD)
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

	payload := []byte{uint8(o.shutdownType)}
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

// options is the parsed command line.
type options struct {
	showHelp    bool
	sysShutdown bool
	forceMode   bool
	useCFD      bool
	wtmpOnly    bool
	noWtmp      bool
	noSync      bool
	noWall      bool
	interactive bool
	grace       time.Duration
	// shutdown(8): -c, -k (warn only), and the operands (a time, then
	// a message).
	cancel       bool
	warnOnly     bool
	operands     []string
	shutdownType service.ShutdownType
}

// parseArgs reads the command line (without argv[0]) for the name the
// binary was invoked as, which sets the default action and, for
// shutdown, the shutdown(8) options.
func parseArgs(execName string, args []string) (options, error) {
	o := options{shutdownType: defaultShutdownType()}

	// Detect invocation name for symlink-based defaults
	switch {
	case strings.HasSuffix(execName, "reboot") && !strings.HasSuffix(execName, "soft-reboot"):
		o.shutdownType = service.ShutdownReboot
	case strings.HasSuffix(execName, "soft-reboot"):
		o.shutdownType = service.ShutdownSoftReboot
	case strings.HasSuffix(execName, "halt"):
		o.shutdownType = service.ShutdownHalt
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help":
			o.showHelp = true
		case arg == "--system":
			o.sysShutdown = true
		// systemd(1) reboot/halt/poweroff surface — long-form aliases.
		case arg == "--halt":
			o.shutdownType = service.ShutdownHalt
		case arg == "--reboot":
			o.shutdownType = service.ShutdownReboot
		case arg == "--poweroff":
			// systemd: `--poweroff` overrides argv[0] except when the
			// binary is `reboot`, where it is ignored. Match that quirk.
			if !strings.HasSuffix(execName, "reboot") ||
				strings.HasSuffix(execName, "soft-reboot") {
				o.shutdownType = service.ShutdownPoweroff
			}
		case arg == "-r":
			o.shutdownType = service.ShutdownReboot
		case execName == "shutdown" && arg == "-h":
			o.shutdownType = shutdownDashH(o.shutdownType)
		case arg == "-h":
			// systemd: `-h` is short for --halt only for the poweroff
			// binary; for others it's already a shutdown type toggle.
			o.shutdownType = service.ShutdownHalt
		case arg == "-p":
			o.shutdownType = service.ShutdownPoweroff
		case arg == "-s":
			o.shutdownType = service.ShutdownSoftReboot
		case execName == "shutdown" && arg == "-k":
			// shutdown(8), sysvinit and systemd alike: send the
			// warning only, never shut down.
			o.warnOnly = true
		case arg == "-k":
			o.shutdownType = service.ShutdownKexec
		// systemd(1) reboot(8) options that bypass the init contact
		// (or gate the utmp/wall/sync side effects). -f/--force triggers
		// the *minimal* path (sync + reboot syscall); --system is the
		// legacy alias for the *full* cleanup path (kill(-1) + umount +
		// sync + reboot). Kept separate to match systemd's contract:
		// `reboot -f` is documented as "does not contact the init system"
		// and "filesystems are not properly unmounted before shutdown".
		case arg == "-f", arg == "--force":
			o.forceMode = true
		case arg == "-w", arg == "--wtmp-only":
			o.wtmpOnly = true
		case arg == "-d", arg == "--no-wtmp":
			o.noWtmp = true
		case arg == "-n", arg == "--no-sync":
			o.noSync = true
		case arg == "--no-wall":
			o.noWall = true
		case arg == "-i", arg == "--interactive":
			// s6-linux-init-hpr -i: prompt for the local short hostname
			// before shutting the box down. Cheap safeguard against the
			// classic SSH footgun of rebooting the wrong host.
			o.interactive = true
		case arg == "--use-passed-cfd":
			o.useCFD = true
		case strings.HasPrefix(arg, "--grace="):
			d, err := time.ParseDuration(arg[len("--grace="):])
			if err != nil {
				return o, fmt.Errorf("Invalid --grace value: %s", arg[len("--grace="):])
			}
			o.grace = d
		case execName == "shutdown" && arg == "-P":
			o.shutdownType = service.ShutdownPoweroff
		case execName == "shutdown" && arg == "-H":
			o.shutdownType = service.ShutdownHalt
		case execName == "shutdown" && arg == "-c":
			o.cancel = true
		case execName == "shutdown" && !strings.HasPrefix(arg, "-"):
			// shutdown(8)'s operands: a time, then an optional wall
			// message. Everything from here on is operands.
			o.operands = args[i:]
			i = len(args)
		default:
			return o, fmt.Errorf("Unrecognized option: %s", arg)
		}
	}
	return o, nil
}

func defaultShutdownType() service.ShutdownType {
	return service.ShutdownPoweroff
}

// shutdownVerb maps the internal ShutdownType to a short verb used in
// the -i confirmation prompt so the operator sees which action they
// are about to authorise.
func shutdownVerb(st service.ShutdownType) string {
	switch st {
	case service.ShutdownReboot:
		return "reboot"
	case service.ShutdownHalt:
		return "halt"
	case service.ShutdownPoweroff:
		return "poweroff"
	case service.ShutdownSoftReboot:
		return "soft-reboot"
	case service.ShutdownKexec:
		return "kexec"
	default:
		return "shut down"
	}
}

func printUsage(execName string) {
	fmt.Fprintf(os.Stderr, `%s: shut down the system
  --help             show this help
  -r, --reboot       reboot the machine
  -h, --halt         halt the machine
  -p, --poweroff     power down the machine (default)
  -s                 soft-reboot (restart slinit with same arguments)
  -k                 execute kernel loaded via kexec
  -f, --force        minimal shutdown path: sync + reboot syscall,
                     do not stop services or unmount filesystems
                     (matches systemd reboot -f contract)
  -n, --no-sync      skip filesystem sync before rebooting
  -d, --no-wtmp      do not write a shutdown entry to utmp/wtmp
  -w, --wtmp-only    write only the shutdown entry to utmp/wtmp,
                     do not actually reboot
  --no-wall          do not broadcast the shutdown wall message
  -i, --interactive  prompt for the short hostname before proceeding
                     (anti-footgun for SSH sessions)
  --use-passed-cfd   use the socket fd from SLINIT_CS_FD env var
  --system           full cleanup path: kill(-1), umount, sync, reboot
                     (used by init itself, not for normal user reboots)
  --grace=DURATION   override the SIGTERM→SIGKILL grace period
`, execName)
	if execName == "shutdown" {
		fmt.Fprint(os.Stderr, `
shutdown [OPTIONS] [TIME [MESSAGE...]]
  TIME               now (the default), +N or N minutes, or HH:MM; a later
                     time is scheduled, warned about on every terminal, and
                     refuses new logins in its last five minutes
  MESSAGE            text added to the warnings
  -c                 cancel a scheduled shutdown
  -k                 only send the warning; do not shut down
  -P, -H             power off, halt
`)
	}
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

// doForceShutdown runs the minimal reboot -f path: no service teardown,
// no umount — just sync and the reboot syscall. Matches systemd's
// documented `reboot -f` contract.
func doForceShutdown(shutdownType service.ShutdownType) {
	logger := logging.New(logging.LevelInfo)
	logger.SetOutput(openConsole())
	shutdown.ExecuteForce(shutdownType, logger)
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

// parseOperands reads shutdown(8)'s operands: an optional time (see
// control.ParseShutdownTime, shared with slinitctl shutdown), then the
// words of a wall message. With no operands the time is now.
func parseOperands(operands []string, now time.Time) (time.Duration, string, error) {
	if len(operands) == 0 {
		return 0, "", nil
	}
	delay, err := control.ParseShutdownTime(operands[0], now)
	if err != nil {
		return 0, "", err
	}
	return delay, strings.Join(operands[1:], " "), nil
}

// scheduleOrCancel asks the daemon to schedule a shutdown, or with
// cancel to cancel the pending one.
func scheduleOrCancel(useCFD bool, st service.ShutdownType, delay time.Duration, message string, cancel bool) error {
	conn, err := connectToDaemon(useCFD)
	if err != nil {
		return fmt.Errorf("cannot connect to slinit: %w", err)
	}
	defer conn.Close()
	if err := checkProtocolVersion(conn); err != nil {
		return err
	}
	cmd, payload := control.CmdCancelShutdown, []byte(nil)
	if !cancel {
		cmd = control.CmdScheduleShutdown
		if payload, err = control.EncodeScheduleShutdown(st, delay, message); err != nil {
			return err
		}
	}
	if err := control.WritePacket(conn, cmd, payload); err != nil {
		return err
	}
	rply, _, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}
	switch {
	case cancel && rply == control.RplyACK:
		fmt.Println("Scheduled shutdown cancelled.")
	case cancel && rply == control.RplyNAK:
		fmt.Println("No shutdown is scheduled.")
	case !cancel && rply == control.RplyACK:
		fmt.Printf("Shutdown (%s) scheduled for %s; use 'shutdown -c' to cancel.\n",
			shutdownVerb(st), time.Now().Add(delay).Format("Mon 2006-01-02 15:04"))
	default:
		return fmt.Errorf("slinit refused the request (reply %d)", rply)
	}
	return nil
}

// sendWarning is shutdown -k: slinit walls text to every logged-in user
// and nothing is scheduled.
func sendWarning(useCFD bool, text string) error {
	conn, err := connectToDaemon(useCFD)
	if err != nil {
		return fmt.Errorf("cannot connect to slinit: %w", err)
	}
	defer conn.Close()
	if err := checkProtocolVersion(conn); err != nil {
		return err
	}
	msg := []byte(text)
	if len(msg) > 0xFFFF {
		msg = msg[:0xFFFF]
	}
	payload := append([]byte{byte(len(msg)), byte(len(msg) >> 8)}, msg...)
	if err := control.WritePacket(conn, control.CmdWallNotice, payload); err != nil {
		return err
	}
	rply, _, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyACK {
		return fmt.Errorf("slinit refused the warning (reply %d)", rply)
	}
	fmt.Println("Shutdown warning sent; nothing is scheduled.")
	return nil
}

// shutdownDashH is -h under the name shutdown: power off, unless a halt was
// already asked for, as in shutdown(8). The other names keep -h as halt.
func shutdownDashH(current service.ShutdownType) service.ShutdownType {
	if current == service.ShutdownHalt {
		return current
	}
	return service.ShutdownPoweroff
}

// slinitctl is the control CLI for the slinit service manager.
// It communicates with a running slinit instance via a Unix domain socket.
package main

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/platform"
	"github.com/sunlightlinux/slinit/pkg/service"
	"github.com/sunlightlinux/slinit/pkg/shutdown"
)

const (
	defaultSystemSocket = "/run/slinit.socket"
	defaultUserSocket   = ".slinitctl"
)

// version is injected at build time via:
//
//	go build -ldflags "-X main.version=v1.10.10" ./cmd/slinitctl
//
// Local builds without ldflags report "dev".
var version = "dev"

// quiet suppresses informational output (set by --quiet/-q).
var quiet bool

// peerCPVersion captures the server's declared protocol version after
// versionHandshake. Individual command paths (e.g. cmdEnable) consult
// it to pick a newer wire variant when both sides support it — for
// v7 that means CmdEnableServiceV7, which returns the target's status
// on the same round-trip and avoids a follow-up SERVICESTATUS query.
var peerCPVersion uint16

// waitTimeout is the reply timeout in seconds set by -w / --wait. 0
// disables the CLI-side cap (server-side timeouts still apply). This
// is a package-level so command functions don't have to plumb it
// through their signatures — matching the existing pattern for quiet.
var waitTimeout time.Duration

func main() {
	args := os.Args[1:]

	// Parse global flags
	var (
		socketPath  string
		systemMode  bool
		userMode    bool
		noWait      bool
		pinFlag     bool
		forceFlag   bool
		ignoreUnst  bool
		offlineMode bool
		servicesDir string
		fromSvc     string
		useCFD      bool
		quietMode   bool
		waitSecs    int // sv -w SEC: per-invocation reply timeout, 0 = no CLI-side cap
	)
	for len(args) > 0 {
		switch {
		case args[0] == "--socket-path" || args[0] == "-p":
			if len(args) < 2 {
				fatal("--socket-path requires an argument")
			}
			socketPath = args[1]
			args = args[2:]
		case strings.HasPrefix(args[0], "--socket-path="):
			socketPath = strings.TrimPrefix(args[0], "--socket-path=")
			args = args[1:]
		case args[0] == "--system" || args[0] == "-s":
			systemMode = true
			args = args[1:]
		case args[0] == "--user" || args[0] == "-u":
			userMode = true
			args = args[1:]
		case args[0] == "--no-wait":
			noWait = true
			args = args[1:]
		case args[0] == "-w" || args[0] == "--wait":
			if len(args) < 2 {
				fatal("-w requires an argument (seconds)")
			}
			n, err := strconv.Atoi(args[1])
			if err != nil || n < 0 {
				fatal("-w: must be a non-negative integer (got %q)", args[1])
			}
			waitSecs = n
			args = args[2:]
		case strings.HasPrefix(args[0], "-w=") || strings.HasPrefix(args[0], "--wait="):
			val := strings.TrimPrefix(strings.TrimPrefix(args[0], "--wait="), "-w=")
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 {
				fatal("-w: must be a non-negative integer (got %q)", val)
			}
			waitSecs = n
			args = args[1:]
		case args[0] == "--pin":
			pinFlag = true
			args = args[1:]
		case args[0] == "--force" || args[0] == "-f":
			forceFlag = true
			args = args[1:]
		case args[0] == "--ignore-unstarted":
			ignoreUnst = true
			args = args[1:]
		case args[0] == "--offline" || args[0] == "-o":
			offlineMode = true
			args = args[1:]
		case args[0] == "--services-dir" || args[0] == "-d":
			if len(args) < 2 {
				fatal("--services-dir requires an argument")
			}
			servicesDir = args[1]
			args = args[2:]
		case strings.HasPrefix(args[0], "--services-dir="):
			servicesDir = strings.TrimPrefix(args[0], "--services-dir=")
			args = args[1:]
		case args[0] == "--from":
			if len(args) < 2 {
				fatal("--from requires an argument")
			}
			fromSvc = args[1]
			args = args[2:]
		case strings.HasPrefix(args[0], "--from="):
			fromSvc = strings.TrimPrefix(args[0], "--from=")
			args = args[1:]
		case args[0] == "--use-passed-cfd":
			useCFD = true
			args = args[1:]
		case args[0] == "--quiet" || args[0] == "-q":
			quietMode = true
			args = args[1:]
		case args[0] == "--help" || args[0] == "-h":
			printUsage()
			os.Exit(0)
		case args[0] == "--version":
			fmt.Printf("slinitctl version %s\n", version)
			os.Exit(0)
		default:
			goto doneFlags
		}
	}
doneFlags:

	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}

	command := args[0]
	cmdArgs := args[1:]

	// Commands that don't need a daemon connection
	if command == "platform" {
		cmdPlatform()
		return
	}
	if command == "completion" {
		shell := "bash"
		if len(cmdArgs) > 0 {
			shell = cmdArgs[0]
		}
		cmdCompletion(shell)
		return
	}
	if command == "is-newer-than" || command == "is-older-than" {
		if len(cmdArgs) != 2 {
			fatal("Usage: slinitctl %s <file-a> <file-b>", command)
		}
		cmdFileCompare(command, cmdArgs[0], cmdArgs[1])
		return
	}

	// Offline mode: enable/disable without connecting to daemon
	if offlineMode {
		svcDir := servicesDir
		if svcDir == "" {
			if systemMode {
				svcDir = "/etc/slinit.d"
			} else {
				if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
					svcDir = xdg + "/slinit.d"
				} else {
					home, err := os.UserHomeDir()
					if err != nil {
						fatal("Cannot determine home directory: %v", err)
					}
					svcDir = home + "/.config/slinit.d"
				}
			}
		}
		// Dinit-parity (upstream 044b950): in offline mode --services-dir
		// is consumed locally (waits-for.d symlink writes), so a relative
		// path must be resolved against THIS process's cwd before we hand
		// it to offlineEnable / offlineDisable.
		if abs, err := filepath.Abs(svcDir); err == nil {
			svcDir = abs
		}
		switch command {
		case "enable":
			if len(cmdArgs) < 1 {
				fatal("Service name required")
			}
			err := offlineEnable(svcDir, fromSvc, cmdArgs[0])
			if err != nil {
				fatal("Error: %v", err)
			}
		case "disable":
			if len(cmdArgs) < 1 {
				fatal("Service name required")
			}
			err := offlineDisable(svcDir, fromSvc, cmdArgs[0])
			if err != nil {
				fatal("Error: %v", err)
			}
		default:
			fatal("Offline mode only supports enable/disable commands")
		}
		return
	}

	sockPath := resolveSocketPath(socketPath, systemMode, userMode)

	var conn net.Conn
	var err error
	if useCFD {
		conn, err = connectPassedFD()
	} else {
		conn, err = connectSocket(sockPath)
	}
	if err != nil {
		if useCFD {
			fatal("Failed to connect via passed fd: %v", err)
		}
		fatal("Failed to connect to slinit at %s: %v", sockPath, err)
	}
	defer conn.Close()

	// Protocol version handshake
	if err := versionHandshake(conn); err != nil {
		fatal("%v", err)
	}

	// Set package-level quiet flag
	quiet = quietMode || noWait
	waitTimeout = time.Duration(waitSecs) * time.Second

	switch command {
	case "list", "ls":
		err = cmdList(conn)
	case "run":
		err = cmdRun(conn, cmdArgs)
	case "start":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdStart(conn, name, pinFlag, noWait)
		})
	case "wake":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdWake(conn, name)
		})
	case "stop":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdStop(conn, name, pinFlag, forceFlag, ignoreUnst, noWait)
		})
	case "release":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdRelease(conn, name)
		})
	case "restart":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdRestart(conn, name, pinFlag, forceFlag, ignoreUnst, noWait)
		})
	case "status":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdStatus(conn, name)
		})
	case "is-started":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdIsStarted(conn, name)
		})
	case "is-failed":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdIsFailed(conn, name)
		})
	case "reset-failed":
		err = cmdResetFailedDispatch(conn, cmdArgs)
	case "shutdown":
		err = cmdShutdownDispatch(conn, cmdArgs)
	case "trigger":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdTrigger(conn, name)
		})
	case "untrigger":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdUntrigger(conn, name)
		})
	case "signal":
		if len(cmdArgs) >= 1 && (cmdArgs[0] == "--list" || cmdArgs[0] == "-l") {
			printSignalList()
			return
		}
		if len(cmdArgs) < 2 {
			fatal("Usage: slinitctl signal [-l|--list] <signal> <service>")
		}
		err = cmdSignal(conn, cmdArgs[1], cmdArgs[0])
	case "pause":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdPause(conn, name)
		})
	case "continue", "cont":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdContinue(conn, name)
		})
	case "once":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdOnce(conn, name)
		})
	case "boot-time", "analyze":
		err = cmdBootTime(conn)
	case "reload":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdReload(conn, name)
		})
	case "reload-all":
		err = cmdReloadAll(conn)
	case "activate-profile":
		if len(cmdArgs) < 1 {
			// Empty means "deactivate filtering" — allow no arg to be
			// treated as such by requiring an explicit "-" sentinel.
			err = fmt.Errorf("usage: activate-profile <name> (or '-' to deactivate)")
		} else {
			target := cmdArgs[0]
			if target == "-" {
				target = ""
			}
			err = cmdActivateProfile(conn, target)
		}
	case "active-profile":
		err = cmdActiveProfile(conn)
	case "list-profiles":
		err = cmdListProfiles(conn)
	case "reload-signal":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdReloadSignal(conn, name)
		})
	case "unload":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdUnload(conn, name)
		})
	case "catlog":
		clearFlag := false
		svcName := ""
		for _, arg := range cmdArgs {
			if arg == "--clear" {
				clearFlag = true
			} else {
				svcName = arg
			}
		}
		if svcName == "" {
			fatal("Usage: slinitctl catlog [--clear] <service>")
		}
		err = cmdCatLog(conn, svcName, clearFlag)
	case "setenv":
		if len(cmdArgs) < 2 {
			fatal("Usage: slinitctl setenv <service> KEY=VALUE")
		}
		err = cmdSetEnv(conn, cmdArgs[0], cmdArgs[1])
	case "unsetenv":
		if len(cmdArgs) < 2 {
			fatal("Usage: slinitctl unsetenv <service> KEY")
		}
		err = cmdUnsetEnv(conn, cmdArgs[0], cmdArgs[1])
	case "getallenv":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdGetAllEnv(conn, name)
		})
	case "reset-env":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdResetEnv(conn, name)
		})
	case "setenv-global":
		if len(cmdArgs) < 1 {
			fatal("Usage: slinitctl setenv-global KEY=VALUE")
		}
		err = cmdSetEnvGlobal(conn, cmdArgs[0])
	case "unsetenv-global":
		if len(cmdArgs) < 1 {
			fatal("Usage: slinitctl unsetenv-global KEY")
		}
		err = cmdUnsetEnvGlobal(conn, cmdArgs[0])
	case "getallenv-global":
		err = cmdGetAllEnvGlobal(conn)
	case "add-dep":
		if len(cmdArgs) < 3 {
			fatal("Usage: slinitctl add-dep <from> <dep-type> <to>")
		}
		err = cmdAddDep(conn, cmdArgs[0], cmdArgs[1], cmdArgs[2])
	case "rm-dep":
		if len(cmdArgs) < 3 {
			fatal("Usage: slinitctl rm-dep <from> <dep-type> <to>")
		}
		err = cmdRmDep(conn, cmdArgs[0], cmdArgs[1], cmdArgs[2])
	case "unpin":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdUnpin(conn, name)
		})
	case "enable":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdEnable(conn, name, fromSvc)
		})
	case "disable":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdDisable(conn, name, fromSvc)
		})
	case "query-name":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdQueryServiceName(conn, name)
		})
	case "service-dirs":
		err = cmdQueryServiceDscDir(conn)
	case "query-load-mech", "load-mech":
		err = cmdQueryLoadMech(conn)
	case "dependents":
		if len(cmdArgs) < 1 {
			fmt.Fprintf(os.Stderr, "usage: slinitctl dependents <service>\n")
			os.Exit(1)
		}
		err = cmdDependents(conn, cmdArgs[0])
	case "list5":
		err = cmdListServices5(conn)
	case "status5":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdServiceStatus5(conn, name)
		})
	case "graph":
		err = cmdGraph(conn)
	case "attach":
		if len(cmdArgs) < 1 {
			fatal("Usage: slinitctl attach <service>")
		}
		// attach doesn't use the control protocol — connects directly to vtty socket
		if conn != nil {
			conn.Close()
		}
		err = cmdAttach(cmdArgs[0], socketPath, systemMode)
	case "action":
		if len(cmdArgs) < 2 {
			fatal("Usage: slinitctl action <service> <action-name>")
		}
		err = cmdRunAction(conn, cmdArgs[0], cmdArgs[1])
	case "list-actions":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdListActions(conn, name)
		})
	default:
		fatal("Unknown command: %s", command)
	}

	if err != nil {
		fatal("Error: %v", err)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: slinitctl [options] <command> [args...]

Options:
  --socket-path, -p PATH   Control socket path
  --system, -s             Connect to system service manager
  --user, -u               Connect to user service manager
  --no-wait                Do not wait for command completion
  -w, --wait SEC           Fail after SEC seconds if the daemon does not
                           reply (0 = no cap; server-side timeouts still
                           apply). Mirrors sv -w SEC.
  --pin                    Pin service in started/stopped state (start/stop)
  --force, -f              Force stop even with dependents (stop/restart)
  --ignore-unstarted       Exit 0 if service already stopped (stop/restart)
  --offline, -o            Offline mode (enable/disable without daemon)
  --services-dir, -d DIR   Service directory (offline mode)
  --from <service>         Source service for enable/disable
  --use-passed-cfd         Use fd from SLINIT_CS_FD env var
  --quiet, -q              Suppress informational output
  --help, -h               Show this help
  --version                Show version

Commands:
  list                     List all loaded services
  start <service>          Start a service (marks active)
  wake <service>           Start without marking active
  stop <service>           Stop a service
  release <service>        Remove active mark (stop if unrequired)
  restart <service>        Restart a service (stop + start)
  status <service>         Show detailed service status
  is-started <service>     Exit 0 if started, 1 otherwise
  is-failed <service>      Exit 0 if failed, 1 otherwise
  shutdown [type] [time]   Shutdown: type=halt|poweroff|reboot|kexec|softreboot
                           time=now|+N (min)|HH:MM (default: poweroff now)
  shutdown -c              Cancel scheduled shutdown
  shutdown --status        Show pending shutdown info
  trigger <service>        Trigger a triggered service
  untrigger <service>      Reset trigger state
  signal [-l] <sig> <svc>  Send signal to service process (-l to list)
  pause <service>          Pause (SIGSTOP) a running service
  continue <service>       Continue (SIGCONT) a paused service
  once <service>           Start service but don't restart on exit
  action <svc> <action>    Run a custom extra-command action
  list-actions <service>   List available extra-command actions
  reload <service>         Reload service configuration from disk
  reload-all               Reload every loaded service from disk (skips transitional)
  reload-signal <service>  Send service's configured reload-signal to its process
  unload <service>         Unload a stopped service from memory
  boot-time                Show boot timing analysis
  catlog [--clear] <svc>   Show buffered service output
  setenv <svc> KEY=VALUE   Set environment variable for service
  unsetenv <svc> KEY       Remove environment variable
  getallenv <svc>          List all runtime environment variables
  reset-env <svc>          Clear all runtime setenv mutations on <svc>
  setenv-global KEY=VALUE  Set global environment variable
  unsetenv-global KEY      Remove global environment variable
  getallenv-global         List all global environment variables
  add-dep <from> <type> <to>  Add runtime dependency
  rm-dep <from> <type> <to>   Remove runtime dependency
  unpin <service>          Remove start/stop pins from a service
  enable <service>         Enable service (add waits-for to boot + start)
  disable <service>        Disable service (remove waits-for from boot + stop)
  graph                    Export dependency graph in DOT format (Graphviz)
  dependents <service>     List services that depend on a service
  query-name <service>     Query the canonical name of a service handle
  service-dirs             List configured service directories
  load-mech                Query loader mechanism info
  list5                    List services (protocol v5, detailed)
  status5 <service>        Show service status (protocol v5, detailed)
  attach <service>         Attach to service virtual terminal
  platform                 Detect and display virtualization/container platform
  completion [shell]       Output shell completion script (bash|zsh|fish)
`)
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "slinitctl: "+format+"\n", args...)
	os.Exit(1)
}

// info prints an informational message unless quiet mode is active.
func info(format string, args ...interface{}) {
	if !quiet {
		fmt.Printf(format, args...)
	}
}

func requireServiceArg(args []string, fn func(string) error) error {
	if len(args) < 1 {
		fatal("Service name required")
	}
	return fn(args[0])
}

func resolveSocketPath(flagValue string, systemMode, userMode bool) string {
	if flagValue != "" {
		return flagValue
	}
	if systemMode {
		return defaultSystemSocket
	}
	if !userMode && os.Getuid() == 0 {
		// Auto-detect: root → system
		return defaultSystemSocket
	}
	// User mode: prefer $XDG_RUNTIME_DIR/slinitctl, fall back to $HOME/.slinitctl
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return xdg + "/slinitctl"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return defaultUserSocket
	}
	return home + "/" + defaultUserSocket
}

func connectSocket(path string) (net.Conn, error) {
	return net.Dial("unix", path)
}

// readReply reads packets from the connection, skipping any unsolicited
// info/event packets (InfoServiceEvent, InfoServiceEvent5, InfoEnvEvent)
// that may arrive due to auto-subscription via allocHandle. Returns the
// first non-info packet.
//
// When waitTimeout > 0 (sv -w SEC), a read deadline is installed so a
// stuck daemon can't hang the CLI forever — a hit deadline surfaces
// as a wrapped net.Error{Timeout:true} for the caller to distinguish.
// The deadline is cleared before returning so subsequent reads on the
// same conn are not accidentally capped.
func readReply(conn net.Conn) (uint8, []byte, error) {
	if waitTimeout > 0 {
		_ = conn.SetReadDeadline(time.Now().Add(waitTimeout))
		defer conn.SetReadDeadline(time.Time{})
	}
	for {
		rply, payload, err := control.ReadPacket(conn)
		if err != nil {
			return 0, nil, err
		}
		switch rply {
		case control.InfoServiceEvent, control.InfoServiceEvent5, control.InfoEnvEvent:
			// Skip unsolicited push notifications
			continue
		default:
			return rply, payload, nil
		}
	}
}

// isStderrTTY reports whether stderr is attached to a terminal.
// Uses TCGETS ioctl — succeeds only on real terminals.
func isStderrTTY() bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, uintptr(os.Stderr.Fd()),
		uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&t)), 0, 0, 0)
	return errno == 0
}

// progressGrace is the initial delay before any progress dots are printed.
// Commands that complete faster than this produce no visual noise.
var progressGrace = 2 * time.Second

// progressTick is the interval between progress dots.
var progressTick = 500 * time.Millisecond

// readReplyWithProgress behaves like readReply but, if stderr is a TTY and
// --quiet is not set, prints a "waiting" line followed by dots while the
// server is still computing a reply. Silent in non-TTY (scripts, logs).
func readReplyWithProgress(conn net.Conn, action string) (uint8, []byte, error) {
	if quiet || !isStderrTTY() {
		return readReply(conn)
	}

	type result struct {
		rply    uint8
		payload []byte
		err     error
	}
	done := make(chan result, 1)
	go func() {
		rply, payload, err := readReply(conn)
		done <- result{rply, payload, err}
	}()

	grace := time.NewTimer(progressGrace)
	defer grace.Stop()

	// Wait for either the reply or the grace period.
	select {
	case r := <-done:
		return r.rply, r.payload, r.err
	case <-grace.C:
	}

	// Still waiting — start printing dots.
	fmt.Fprintf(os.Stderr, "%s", action)
	ticker := time.NewTicker(progressTick)
	defer ticker.Stop()
	for {
		select {
		case r := <-done:
			fmt.Fprintln(os.Stderr)
			return r.rply, r.payload, r.err
		case <-ticker.C:
			fmt.Fprint(os.Stderr, ".")
		}
	}
}

// versionHandshake performs a two-way protocol version check with the server.
// Server sends: min_compat_version(2) + actual_version(2).
// Client checks bidirectional compatibility.
func versionHandshake(conn net.Conn) error {
	if err := control.WritePacket(conn, control.CmdQueryVersion, nil); err != nil {
		return fmt.Errorf("version handshake write: %w", err)
	}

	rply, payload, err := control.ReadPacket(conn)
	if err != nil {
		return fmt.Errorf("version handshake read: %w", err)
	}
	if rply != control.RplyCPVersion {
		return fmt.Errorf("unexpected version reply: %d", rply)
	}

	if len(payload) >= 4 {
		// New format: min_compat(2) + actual(2)
		serverMin := binary.LittleEndian.Uint16(payload[0:])
		serverActual := binary.LittleEndian.Uint16(payload[2:])

		// Check: server's actual version must be >= our min compat
		if serverActual < control.MinCompatVersion {
			return fmt.Errorf("server protocol version %d is too old (need >= %d)", serverActual, control.MinCompatVersion)
		}
		// Check: our actual version must be >= server's min compat
		if control.CPVersion < serverMin {
			return fmt.Errorf("client protocol version %d is too old for server (server needs >= %d)", control.CPVersion, serverMin)
		}
		peerCPVersion = serverActual
	} else if len(payload) >= 2 {
		// Legacy format: just version(2) — v1 server
		serverVer := binary.LittleEndian.Uint16(payload)
		if serverVer < control.MinCompatVersion {
			return fmt.Errorf("server protocol version %d is too old (need >= %d)", serverVer, control.MinCompatVersion)
		}
		peerCPVersion = serverVer
	} else {
		return fmt.Errorf("invalid version reply payload (len=%d)", len(payload))
	}
	return nil
}

// connectPassedFD creates a net.Conn from a file descriptor passed via
// the SLINIT_CS_FD environment variable.
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

// encodeStartStopFlags encodes handle + optional flags byte.
// Bit 0 = pin, Bit 1 = force (relevant only for stop).
func encodeStartStopFlags(handle uint32, pin bool, force bool) []byte {
	var flags uint8
	if pin {
		flags |= 0x01
	}
	if force {
		flags |= 0x02
	}
	if flags == 0 {
		return control.EncodeHandle(handle)
	}
	buf := make([]byte, 5)
	binary.LittleEndian.PutUint32(buf, handle)
	buf[4] = flags
	return buf
}

// stripServiceArg returns the base name without the @argument part.
// For "svc@arg" returns "svc"; for "svc" returns "svc".
func stripServiceArg(name string) string {
	if idx := strings.IndexByte(name, '@'); idx >= 0 {
		return name[:idx]
	}
	return name
}

// offlineEnable creates a waits-for.d symlink (offline mode).
func offlineEnable(svcDir, from, to string) error {
	if from == "" {
		from = "boot"
	}
	// Strip @arg from "from" for directory lookup
	fromBase := stripServiceArg(from)
	waitsDir := svcDir + "/" + fromBase + "/waits-for.d"
	if err := os.MkdirAll(waitsDir, 0755); err != nil {
		return fmt.Errorf("creating waits-for.d: %w", err)
	}
	link := waitsDir + "/" + to
	// Check if link already exists
	if _, err := os.Lstat(link); err == nil {
		info("Service '%s' is already enabled (from '%s').\n", to, from)
		return nil
	}
	// Create relative symlink pointing to the service file (use base name for target)
	target := "../../" + stripServiceArg(to)
	if err := os.Symlink(target, link); err != nil {
		return fmt.Errorf("creating symlink: %w", err)
	}
	info("Service '%s' enabled (from '%s').\n", to, from)
	return nil
}

// offlineDisable removes a waits-for.d symlink (offline mode).
func offlineDisable(svcDir, from, to string) error {
	if from == "" {
		from = "boot"
	}
	// Strip @arg from "from" for directory lookup
	fromBase := stripServiceArg(from)
	link := svcDir + "/" + fromBase + "/waits-for.d/" + to
	if err := os.Remove(link); err != nil {
		if os.IsNotExist(err) {
			info("Service '%s' is not enabled (from '%s').\n", to, from)
			return nil
		}
		return fmt.Errorf("removing symlink: %w", err)
	}
	info("Service '%s' disabled (from '%s').\n", to, from)
	return nil
}

// loadServiceHandle sends LoadService and returns the handle.
// warnIfDescriptionChanged queries the service's load-time mod timestamp
// via protocol v6 and compares it with the current file on disk. If the file
// has been modified since it was loaded, a warning is printed to stderr.
func warnIfDescriptionChanged(conn net.Conn, handle uint32, name string) {
	// Query status6 (includes load mod time)
	if err := control.WritePacket(conn, control.CmdServiceStatus6, control.EncodeHandle(handle)); err != nil {
		return
	}
	rply, payload, err := readReply(conn)
	if err != nil || rply != control.RplyServiceStatus {
		return
	}
	status6, err := control.DecodeServiceStatus6(payload)
	if err != nil || status6.LoadModTime == 0 {
		return
	}

	// Query service description directories to find the file on disk
	if err := control.WritePacket(conn, control.CmdQueryServiceDscDir, nil); err != nil {
		return
	}
	rply, payload, err = readReply(conn)
	if err != nil || rply != control.RplyServiceDscDir || len(payload) < 2 {
		return
	}
	count := int(binary.LittleEndian.Uint16(payload))
	off := 2
	// Strip @arg for template lookup
	baseName := name
	if idx := strings.IndexByte(name, '@'); idx >= 0 {
		baseName = name[:idx]
	}
	searchNames := []string{name}
	if baseName != name {
		searchNames = append(searchNames, baseName)
	}
	for i := 0; i < count; i++ {
		if len(payload) < off+2 {
			return
		}
		dirLen := int(binary.LittleEndian.Uint16(payload[off:]))
		off += 2
		if len(payload) < off+dirLen {
			return
		}
		dir := string(payload[off : off+dirLen])
		off += dirLen
		for _, sn := range searchNames {
			fi, err := os.Stat(filepath.Join(dir, sn))
			if err != nil {
				continue
			}
			if fi.ModTime().Unix() != status6.LoadModTime {
				fmt.Fprintf(os.Stderr, "Warning: service description for '%s' has changed since it was loaded. Consider 'slinitctl reload %s'.\n", name, name)
			}
			return
		}
	}
}

// queryLoadModTime asks the daemon for the mtime the service description
// file had at load time (protocol v6). Returns 0 when the daemon predates
// v6 or the query fails — callers treat 0 as "no stamp available".
func queryLoadModTime(conn net.Conn, handle uint32) int64 {
	if err := control.WritePacket(conn, control.CmdServiceStatus6, control.EncodeHandle(handle)); err != nil {
		return 0
	}
	rply, payload, err := readReply(conn)
	if err != nil || rply != control.RplyServiceStatus {
		return 0
	}
	s6, err := control.DecodeServiceStatus6(payload)
	if err != nil {
		return 0
	}
	return s6.LoadModTime
}

// resolveServiceDescFile queries the daemon's configured service description
// dirs and returns the first path that resolves to an on-disk file for the
// given service name (falling back to the base name for `svc@arg` templates).
// The second return value reports whether the file's current mtime differs
// from loadModTime (0 disables the check — used when the v6 path was
// unavailable). Returns ok=false when no candidate file exists.
func resolveServiceDescFile(conn net.Conn, name string, loadModTime int64) (path string, modified, ok bool) {
	if err := control.WritePacket(conn, control.CmdQueryServiceDscDir, nil); err != nil {
		return "", false, false
	}
	rply, payload, err := readReply(conn)
	if err != nil || rply != control.RplyServiceDscDir || len(payload) < 2 {
		return "", false, false
	}
	count := int(binary.LittleEndian.Uint16(payload))
	off := 2
	baseName := name
	if idx := strings.IndexByte(name, '@'); idx >= 0 {
		baseName = name[:idx]
	}
	searchNames := []string{name}
	if baseName != name {
		searchNames = append(searchNames, baseName)
	}
	for i := 0; i < count; i++ {
		if len(payload) < off+2 {
			return "", false, false
		}
		dirLen := int(binary.LittleEndian.Uint16(payload[off:]))
		off += 2
		if len(payload) < off+dirLen {
			return "", false, false
		}
		dir := string(payload[off : off+dirLen])
		off += dirLen
		for _, sn := range searchNames {
			candidate := filepath.Join(dir, sn)
			fi, err := os.Stat(candidate)
			if err != nil {
				continue
			}
			mod := loadModTime != 0 && fi.ModTime().Unix() != loadModTime
			return candidate, mod, true
		}
	}
	return "", false, false
}

func loadServiceHandle(conn net.Conn, name string) (uint32, error) {
	nameData := control.EncodeServiceName(name)
	if err := control.WritePacket(conn, control.CmdLoadService, nameData); err != nil {
		return 0, fmt.Errorf("write error: %w", err)
	}

	rply, payload, err := control.ReadPacket(conn)
	if err != nil {
		return 0, fmt.Errorf("read error: %w", err)
	}

	switch rply {
	case control.RplyServiceRecord:
		if len(payload) < 6 {
			return 0, fmt.Errorf("invalid service record reply")
		}
		handle := binary.LittleEndian.Uint32(payload[1:5])
		return handle, nil
	case control.RplyNoService:
		return 0, fmt.Errorf("service '%s' not found", name)
	case control.RplyServiceDescErr:
		return 0, fmt.Errorf("service '%s' has a description error", name)
	case control.RplyServiceLoadErr2:
		return 0, fmt.Errorf("service '%s' could not be loaded", name)
	case control.RplyServiceLoadErr:
		return 0, fmt.Errorf("service '%s' load error", name)
	default:
		return 0, fmt.Errorf("unexpected reply: %d", rply)
	}
}

func cmdList(conn net.Conn) error {
	if err := control.WritePacket(conn, control.CmdListServices, nil); err != nil {
		return err
	}

	for {
		rply, payload, err := control.ReadPacket(conn)
		if err != nil {
			return err
		}

		if rply == control.RplyListDone {
			break
		}

		if rply != control.RplySvcInfo {
			return fmt.Errorf("unexpected reply: %d", rply)
		}

		entry, _, err := control.DecodeSvcInfo(payload)
		if err != nil {
			return err
		}

		indicator := formatIndicator(entry)
		suffix := formatSuffix(entry)

		fmt.Printf("[%s] %s%s\n", indicator, entry.Name, suffix)
	}
	return nil
}

// formatIndicator renders the dinit-style 8-char service state indicator.
//
// Layout: 3 chars (started zone) + 2 chars (arrow zone) + 3 chars (stopped zone)
//
// Examples:
//
//	[+]       started, marked active
//	{+}       started, dependency only
//	     {-}  stopped, dependency only
//	[ ]<<     starting, marked active
//	{ }<<     starting, dependency only
//	   >>{ }  stopping, dependency only
//	   <<{ }  starting, but will stop after
//	{ }>>     stopping, but will restart
func formatIndicator(e control.SvcInfoEntry) string {
	active := e.Flags&control.StatusFlagMarkedActive != 0
	open, close := byte('{'), byte('}')
	if active {
		open, close = '[', ']'
	}

	var buf [8]byte
	for i := range buf {
		buf[i] = ' '
	}

	switch e.State {
	case service.StateStarted:
		// Symbol at left (started) position
		buf[0] = open
		buf[1] = '+'
		buf[2] = close

	case service.StateStopped:
		// Symbol at right (stopped) position
		buf[5] = open
		buf[6] = '-'
		buf[7] = close

	case service.StateStarting:
		// Arrow pointing left (<<)
		buf[3] = '<'
		buf[4] = '<'
		if e.TargetState == service.StateStarted {
			// Target bracket at left (started) position
			buf[0] = open
			buf[1] = ' '
			buf[2] = close
		} else {
			// Starting but will stop: target bracket at right (stopped) position
			buf[5] = open
			buf[6] = ' '
			buf[7] = close
		}

	case service.StateStopping:
		// Arrow pointing right (>>)
		buf[3] = '>'
		buf[4] = '>'
		if e.TargetState == service.StateStopped {
			// Target bracket at right (stopped) position
			buf[5] = open
			buf[6] = ' '
			buf[7] = close
		} else {
			// Stopping but will restart: target bracket at left (started) position
			buf[0] = open
			buf[1] = ' '
			buf[2] = close
		}
	}

	return string(buf[:])
}

// formatSuffix returns extra info like (pid: N) or (has console).
func formatSuffix(e control.SvcInfoEntry) string {
	hasPID := e.PID > 0
	hasCon := e.Flags&control.StatusFlagHasConsole != 0
	if !hasPID && !hasCon {
		return ""
	}
	var b strings.Builder
	b.WriteString(" (")
	if hasPID {
		b.WriteString("pid: ")
		b.WriteString(strconv.FormatInt(int64(e.PID), 10))
		if hasCon {
			b.WriteString(", ")
		}
	}
	if hasCon {
		b.WriteString("has console")
	}
	b.WriteByte(')')
	return b.String()
}

func cmdStart(conn net.Conn, name string, pin bool, noWait bool) error {
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}

	warnIfDescriptionChanged(conn, handle, name)

	payload := encodeStartStopFlags(handle, pin, false)
	if err := control.WritePacket(conn, control.CmdStartService, payload); err != nil {
		return err
	}

	rply, _, err := readReplyWithProgress(conn, fmt.Sprintf("starting %s", name))
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyACK:
		info("Service '%s' started.\n", name)
	case control.RplyAlreadySS:
		info("Service '%s' is already started.\n", name)
	case control.RplyPinnedStopped:
		return fmt.Errorf("service '%s' is pinned stopped", name)
	case control.RplyManualRefused:
		return fmt.Errorf("service '%s' refuses manual start (refuse-manual-start = yes)", name)
	case control.RplyShuttingDown:
		return fmt.Errorf("system is shutting down")
	default:
		return fmt.Errorf("unexpected reply: %d", rply)
	}
	return nil
}

func cmdWake(conn net.Conn, name string) error {
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}

	if err := control.WritePacket(conn, control.CmdWakeService, control.EncodeHandle(handle)); err != nil {
		return err
	}

	rply, _, err := readReply(conn)
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyACK:
		info("Service '%s' woken.\n", name)
	case control.RplyAlreadySS:
		info("Service '%s' is already started.\n", name)
	case control.RplyNAK:
		return fmt.Errorf("service '%s' has no active dependents, cannot wake", name)
	case control.RplyShuttingDown:
		return fmt.Errorf("system is shutting down")
	default:
		return fmt.Errorf("unexpected reply: %d", rply)
	}
	return nil
}

func cmdRelease(conn net.Conn, name string) error {
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}

	if err := control.WritePacket(conn, control.CmdReleaseService, control.EncodeHandle(handle)); err != nil {
		return err
	}

	rply, _, err := readReply(conn)
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyACK:
		info("Service '%s' released.\n", name)
	case control.RplyAlreadySS:
		info("Service '%s' is already stopped.\n", name)
	default:
		return fmt.Errorf("unexpected reply: %d", rply)
	}
	return nil
}

func cmdStop(conn net.Conn, name string, pin bool, force bool, ignoreUnstarted bool, noWait bool) error {
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}

	payload := encodeStartStopFlags(handle, pin, force)
	if err := control.WritePacket(conn, control.CmdStopService, payload); err != nil {
		return err
	}

	rply, _, err := readReplyWithProgress(conn, fmt.Sprintf("stopping %s", name))
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyACK:
		info("Service '%s' stopped.\n", name)
	case control.RplyAlreadySS:
		info("Service '%s' is already stopped.\n", name)
	case control.RplyPinnedStarted:
		return fmt.Errorf("service '%s' is pinned started", name)
	case control.RplyManualRefused:
		return fmt.Errorf("service '%s' refuses manual stop (refuse-manual-stop = yes) — use --force to override", name)
	default:
		return fmt.Errorf("unexpected reply: %d", rply)
	}
	return nil
}

func cmdRestart(conn net.Conn, name string, pin bool, force bool, ignoreUnstarted bool, noWait bool) error {
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}

	warnIfDescriptionChanged(conn, handle, name)

	// Stop first
	stopPayload := encodeStartStopFlags(handle, false, force)
	if err := control.WritePacket(conn, control.CmdStopService, stopPayload); err != nil {
		return err
	}
	rply, _, err := readReplyWithProgress(conn, fmt.Sprintf("stopping %s", name))
	if err != nil {
		return err
	}
	if rply != control.RplyACK && rply != control.RplyAlreadySS {
		if ignoreUnstarted && rply == control.RplyAlreadySS {
			// already stopped, proceed
		} else {
			return fmt.Errorf("stop failed: reply %d", rply)
		}
	}

	// Then start
	startPayload := encodeStartStopFlags(handle, pin, false)
	if err := control.WritePacket(conn, control.CmdStartService, startPayload); err != nil {
		return err
	}
	rply, _, err = readReplyWithProgress(conn, fmt.Sprintf("starting %s", name))
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyACK:
		info("Service '%s' restarted.\n", name)
	case control.RplyShuttingDown:
		return fmt.Errorf("system is shutting down")
	default:
		return fmt.Errorf("start failed: reply %d", rply)
	}
	return nil
}

func cmdStatus(conn net.Conn, name string) error {
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}

	if err := control.WritePacket(conn, control.CmdServiceStatus, control.EncodeHandle(handle)); err != nil {
		return err
	}

	rply, payload, err := readReply(conn)
	if err != nil {
		return err
	}

	if rply != control.RplyServiceStatus {
		return fmt.Errorf("unexpected reply: %d", rply)
	}

	status, err := control.DecodeServiceStatus(payload)
	if err != nil {
		return err
	}

	// Fetch load-time mod stamp separately so we can render
	// "(modified since loaded)" without giving up the PID/Type/Exit
	// columns that only ride the v1 status wire. dinit-parity a94ef73.
	loadModTime := queryLoadModTime(conn, handle)

	fmt.Printf("Service: %s\n", name)
	// Show the description file path + modification marker, dinit-parity
	// e099aa4 + a94ef73. Skip on error so init.d/synthesized services
	// don't print a bogus "File:" line.
	if sdfPath, modified, ok := resolveServiceDescFile(conn, name, loadModTime); ok {
		if modified {
			fmt.Printf("  File:    %s (modified since loaded)\n", sdfPath)
		} else {
			fmt.Printf("  File:    %s\n", sdfPath)
		}
	}
	if desc, err := fetchDescription(conn, handle); err == nil && desc != "" {
		fmt.Printf("  Description: %s\n", desc)
	}
	if author, version, usage, err := fetchMetadata(conn, handle); err == nil {
		if author != "" {
			fmt.Printf("  Author:  %s\n", author)
		}
		if version != "" {
			fmt.Printf("  Version: %s\n", version)
		}
		if usage != "" {
			fmt.Printf("  Usage:   %s\n", usage)
		}
	}
	fmt.Printf("  State:   %s\n", formatState(status.State))
	fmt.Printf("  Target:  %s\n", formatTarget(status.TargetState))
	fmt.Printf("  Type:    %s\n", status.SvcType)
	if status.Flags&control.StatusFlagHasPID != 0 {
		fmt.Printf("  PID:     %d\n", status.PID)
	}
	if status.ExitStatus != 0 {
		fmt.Printf("  Exit:    %d\n", status.ExitStatus)
	}

	// Bundle rendering: when the service is an s6-rc-style bundle the
	// members list is non-empty, so we fetch each member's state and
	// print a small tabular section. Non-bundle services get an empty
	// list back and this block is silent.
	if members, err := fetchBundleMembers(conn, handle); err == nil && len(members) > 0 {
		fmt.Println("  Bundle members:")
		for _, m := range members {
			memberState := "?"
			if s, err := getServiceStatus(conn, m); err == nil {
				memberState = formatState(s.State)
			}
			fmt.Printf("    - %s (%s)\n", m, memberState)
		}
	}
	return nil
}

// fetchDescription queries the human-readable description for a service handle.
// Returns empty string if the server does not support the command or the
// service has no description set.
func fetchDescription(conn net.Conn, handle uint32) (string, error) {
	if err := control.WritePacket(conn, control.CmdQueryDescription, control.EncodeHandle(handle)); err != nil {
		return "", err
	}
	rply, payload, err := readReply(conn)
	if err != nil {
		return "", err
	}
	if rply != control.RplyDescription {
		return "", fmt.Errorf("unexpected reply: %d", rply)
	}
	desc, _, err := control.DecodeServiceName(payload)
	return desc, err
}

// fetchBundleMembers queries the s6-rc-style member list for a bundle
// service handle. Empty list is a legitimate reply for non-bundle
// services, so callers use it as a "should I render a Members section?"
// gate rather than as an error signal.
func fetchBundleMembers(conn net.Conn, handle uint32) ([]string, error) {
	if err := control.WritePacket(conn, control.CmdQueryBundleMembers, control.EncodeHandle(handle)); err != nil {
		return nil, err
	}
	rply, payload, err := readReply(conn)
	if err != nil {
		return nil, err
	}
	if rply != control.RplyBundleMembers {
		return nil, fmt.Errorf("unexpected reply: %d", rply)
	}
	members, _, err := control.DecodeStringList(payload)
	return members, err
}

// fetchMetadata queries author/version/usage strings for a service handle.
// Returns empty strings (no error) when the server does not support the
// command, or when the service has no metadata set.
func fetchMetadata(conn net.Conn, handle uint32) (author, version, usage string, err error) {
	if err := control.WritePacket(conn, control.CmdQueryMetadata, control.EncodeHandle(handle)); err != nil {
		return "", "", "", err
	}
	rply, payload, err := readReply(conn)
	if err != nil {
		return "", "", "", err
	}
	if rply != control.RplyMetadata {
		return "", "", "", fmt.Errorf("unexpected reply: %d", rply)
	}
	return control.DecodeMetadata(payload)
}

// getServiceStatus fetches the status for a service via the control protocol.
func getServiceStatus(conn net.Conn, name string) (control.ServiceStatusInfo, error) {
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return control.ServiceStatusInfo{}, err
	}

	if err := control.WritePacket(conn, control.CmdServiceStatus, control.EncodeHandle(handle)); err != nil {
		return control.ServiceStatusInfo{}, err
	}

	rply, payload, err := readReply(conn)
	if err != nil {
		return control.ServiceStatusInfo{}, err
	}

	if rply != control.RplyServiceStatus {
		return control.ServiceStatusInfo{}, fmt.Errorf("unexpected reply: %d", rply)
	}

	return control.DecodeServiceStatus(payload)
}

func cmdIsStarted(conn net.Conn, name string) error {
	status, err := getServiceStatus(conn, name)
	if err != nil {
		return err
	}

	fmt.Println(formatState(status.State))

	if status.State != service.StateStarted {
		os.Exit(1)
	}
	return nil
}

func cmdIsFailed(conn net.Conn, name string) error {
	status, err := getServiceStatus(conn, name)
	if err != nil {
		return err
	}

	failed := status.Flags&control.StatusFlagStartFailed != 0 ||
		(status.State == service.StateStopped && status.ExitStatus != 0)

	if failed {
		fmt.Println("FAILED")
	} else {
		fmt.Println(formatState(status.State))
	}

	if !failed {
		os.Exit(1)
	}
	return nil
}

// cmdRun spawns a transient one-shot service (systemd-run analogue).
// Usage: slinitctl run [--unit NAME] [--type process|scripted] [--wait]
//                     [--description STR] -- COMMAND [ARGS...]
//
// Writes a service description into /run/slinit.d/<name> — that path is
// tmpfs on any real system, so the file evaporates at the next boot
// without polluting /etc/slinit.d. The daemon picks it up via the
// existing load-on-demand path (CmdLoadService reads every default
// service dir), so no new protocol command is needed.
func cmdRun(conn net.Conn, args []string) error {
	var (
		unitName    string
		description string
		svcType     = "process"
		waitStarted bool
	)
	// Simple front-loaded flag parser. The `--` sentinel separates
	// slinitctl's own flags from the command to execute; anything after
	// it is the argv passed to the transient service.
	i := 0
	for ; i < len(args); i++ {
		switch {
		case args[i] == "--":
			i++
			goto commandStart
		case args[i] == "--wait":
			waitStarted = true
		case strings.HasPrefix(args[i], "--unit="):
			unitName = strings.TrimPrefix(args[i], "--unit=")
		case args[i] == "--unit":
			i++
			if i >= len(args) {
				return fmt.Errorf("run: --unit requires a name")
			}
			unitName = args[i]
		case strings.HasPrefix(args[i], "--description="):
			description = strings.TrimPrefix(args[i], "--description=")
		case args[i] == "--description":
			i++
			if i >= len(args) {
				return fmt.Errorf("run: --description requires a value")
			}
			description = args[i]
		case strings.HasPrefix(args[i], "--type="):
			svcType = strings.TrimPrefix(args[i], "--type=")
		case args[i] == "--type":
			i++
			if i >= len(args) {
				return fmt.Errorf("run: --type requires a value")
			}
			svcType = args[i]
		default:
			goto commandStart
		}
	}
commandStart:
	cmdParts := args[i:]
	if len(cmdParts) == 0 {
		return fmt.Errorf("run: no command given (expected: slinitctl run [flags] -- CMD [ARGS...])")
	}
	switch svcType {
	case "process", "scripted":
		// Accepted types for a transient one-shot. bgprocess needs a
		// pidfile which doesn't fit the "one shot from CLI" model.
	default:
		return fmt.Errorf("run: --type must be process or scripted, got %q", svcType)
	}
	if unitName == "" {
		var randbuf [4]byte
		if _, err := rand.Read(randbuf[:]); err != nil {
			return fmt.Errorf("run: cannot generate unit name: %w", err)
		}
		unitName = fmt.Sprintf("run-%x", randbuf)
	}
	if strings.ContainsAny(unitName, "/\x00") {
		return fmt.Errorf("run: unit name must not contain '/' or NUL")
	}

	if err := os.MkdirAll("/run/slinit.d", 0755); err != nil {
		return fmt.Errorf("run: mkdir /run/slinit.d: %w", err)
	}
	path := "/run/slinit.d/" + unitName
	// Quote command tokens minimally: replace embedded double quotes and
	// wrap each token. This matches how svc files typically write
	// commands with arguments — dinit-style tokenisation splits on
	// whitespace but respects double-quoted spans.
	var body strings.Builder
	body.WriteString("type = " + svcType + "\n")
	if description != "" {
		body.WriteString("description = " + description + "\n")
	}
	body.WriteString("command =")
	for _, p := range cmdParts {
		body.WriteString(" \"")
		body.WriteString(strings.ReplaceAll(p, `"`, `\"`))
		body.WriteByte('"')
	}
	body.WriteByte('\n')
	body.WriteString("restart = false\n")
	if err := os.WriteFile(path, []byte(body.String()), 0644); err != nil {
		return fmt.Errorf("run: write %s: %w", path, err)
	}

	if err := cmdStart(conn, unitName, false, false); err != nil {
		return fmt.Errorf("run: start '%s': %w", unitName, err)
	}
	if !waitStarted {
		info("Transient unit '%s' started (%s).\n", unitName, path)
		return nil
	}
	// Wait for the service to settle: STARTED (process still running)
	// or STOPPED (scripted svc that finished). Bounded at 60s so a
	// hung service doesn't wedge the CLI forever.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		st, err := getServiceStatus(conn, unitName)
		if err != nil {
			return err
		}
		switch st.State {
		case service.StateStarted:
			info("Transient unit '%s' STARTED.\n", unitName)
			return nil
		case service.StateStopped:
			if st.Flags&control.StatusFlagStartFailed != 0 {
				return fmt.Errorf("transient unit '%s' failed to start", unitName)
			}
			info("Transient unit '%s' finished cleanly.\n", unitName)
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("run: timed out waiting for '%s' to settle", unitName)
}

// cmdResetFailedDispatch handles `slinitctl reset-failed [SVC | --all]`.
// The empty-payload wire form is the --all flavour; the handle form
// clears the flag on a single service.
func cmdResetFailedDispatch(conn net.Conn, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("reset-failed: expected a service name or --all")
	}
	if args[0] == "--all" {
		if err := control.WritePacket(conn, control.CmdResetFailed, nil); err != nil {
			return err
		}
		rply, _, err := readReply(conn)
		if err != nil {
			return err
		}
		if rply != control.RplyACK {
			return fmt.Errorf("reset-failed --all: unexpected reply %d", rply)
		}
		info("Reset failed state on all services.\n")
		return nil
	}
	name := args[0]
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}
	if err := control.WritePacket(conn, control.CmdResetFailed, control.EncodeHandle(handle)); err != nil {
		return err
	}
	rply, _, err := readReply(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyACK {
		return fmt.Errorf("reset-failed '%s': unexpected reply %d", name, rply)
	}
	info("Reset failed state on '%s'.\n", name)
	return nil
}

// cmdShutdownDispatch handles the shutdown command with optional time scheduling.
// Usage:
//
//	shutdown [type] [time]    - schedule or immediate shutdown
//	shutdown -c               - cancel pending shutdown
//	shutdown --status         - query pending shutdown
//
// Type: halt, poweroff (default), reboot, kexec, softreboot
// Time: now (default), +N (minutes), HH:MM (absolute time)
func cmdShutdownDispatch(conn net.Conn, args []string) error {
	// Check for -c / --cancel
	for _, a := range args {
		if a == "-c" || a == "--cancel" {
			return cmdCancelShutdown(conn)
		}
		if a == "--status" {
			return cmdQueryShutdown(conn)
		}
	}

	shutType := "poweroff"
	timeArg := "now"
	var (
		interactive bool
		warnOnly    bool
		message     string
	)

	// First pass: extract flags. We do this in a dedicated loop so the
	// second pass (positional type/time/message parsing) stays simple
	// and can keep rejecting unknown tokens as typos.
	rest := args[:0]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-i" || a == "--interactive":
			interactive = true
		case a == "-k" || a == "--warn":
			// LSB shutdown -k: warn-only. Broadcast the message and
			// return without scheduling anything.
			warnOnly = true
		case a == "-m" || a == "--message":
			if i+1 >= len(args) {
				return fmt.Errorf("%s: missing message argument", a)
			}
			message = args[i+1]
			i++
		case strings.HasPrefix(a, "--message="):
			message = a[len("--message="):]
		default:
			rest = append(rest, a)
		}
	}
	args = rest

	// Positional args: first is type (optional), second is time
	// (optional), any remainder is joined into the wall message
	// (SysV shutdown-style). Keep type-vs-time detection from before.
	var messageTokens []string
	positionalIdx := 0
	for _, a := range args {
		switch a {
		case "halt", "poweroff", "reboot", "kexec", "softreboot", "soft-reboot":
			shutType = a
			positionalIdx++
		default:
			if _, err := parseShutdownTime(a); err == nil {
				timeArg = a
				positionalIdx++
				continue
			}
			// Non-type non-time positional: only valid AFTER we've
			// already accepted the leading type/time — otherwise it's
			// a typo of the shutdown type. Guard so `shutdown wobble`
			// still fails cleanly.
			if positionalIdx == 0 {
				return fmt.Errorf("unknown shutdown type: %s (use halt, poweroff, reboot, kexec, or softreboot)", a)
			}
			messageTokens = append(messageTokens, a)
		}
	}
	if message == "" && len(messageTokens) > 0 {
		message = strings.Join(messageTokens, " ")
	}

	// -i / --interactive gate: prompt for hostname BEFORE contacting the
	// daemon so a mis-typed hostname doesn't consume a scheduling slot.
	if interactive {
		if err := shutdown.ConfirmHostname(shutType); err != nil {
			return err
		}
	}

	// -k / warning-only: broadcast the message and stop. Doesn't
	// schedule anything, doesn't require a valid shutdown type. If no
	// message was provided the daemon still walls a default warning
	// so operators aren't stuck constructing wall text every time.
	if warnOnly {
		wallMsg := message
		if wallMsg == "" {
			wallMsg = "System maintenance is planned. Please save your work."
		}
		return sendWallNotice(conn, wallMsg)
	}

	st, err := parseShutdownType(shutType)
	if err != nil {
		return err
	}

	// Kexec preflight: warn early if no kernel is loaded so the
	// operator understands why the box will just do a normal reboot.
	// This is a courtesy check — the daemon-side path also falls
	// back to a normal reboot on EINVAL, but the operator staring
	// at slinitctl might miss that log line if they're not on the
	// console.
	if st == service.ShutdownKexec && !kexecKernelLoaded() {
		fmt.Fprintf(os.Stderr,
			"slinitctl: warning: no kexec kernel loaded (see /sys/kernel/kexec_loaded); "+
				"the daemon will fall back to a normal reboot. "+
				"Pre-load a kernel with `kexec -l <kernel> --reuse-cmdline` first "+
				"to actually kexec.\n")
	}

	delay, err := parseShutdownTime(timeArg)
	if err != nil {
		return err
	}

	if delay <= 0 {
		// Immediate shutdown — use the existing CmdShutdown for compatibility.
		payload := []byte{uint8(st)}
		if err := control.WritePacket(conn, control.CmdShutdown, payload); err != nil {
			return err
		}
		rply, _, err := control.ReadPacket(conn)
		if err != nil {
			return err
		}
		if rply == control.RplyACK {
			info("Shutdown (%s) initiated.\n", shutType)
		} else {
			return fmt.Errorf("shutdown failed: reply %d", rply)
		}
		return nil
	}

	// Scheduled shutdown. Wire layout:
	//   [type(1)] [delay_secs(4, big-endian)]
	//   [msg_len(2, LE)] [msg_bytes...]
	// The message tail is only appended when non-empty so pre-message
	// slinitd instances still accept the packet (they treat the extra
	// bytes as ignored padding).
	secs := uint32(delay.Seconds())
	payload := []byte{
		uint8(st),
		byte(secs >> 24), byte(secs >> 16), byte(secs >> 8), byte(secs),
	}
	if message != "" {
		msgBytes := []byte(message)
		if len(msgBytes) > 0xFFFF {
			msgBytes = msgBytes[:0xFFFF]
		}
		payload = append(payload,
			byte(len(msgBytes)&0xFF), byte((len(msgBytes)>>8)&0xFF))
		payload = append(payload, msgBytes...)
	}
	if err := control.WritePacket(conn, control.CmdScheduleShutdown, payload); err != nil {
		return err
	}
	rply, _, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}
	if rply == control.RplyACK {
		info("Shutdown (%s) scheduled in %v.\n", shutType, delay)
	} else {
		return fmt.Errorf("schedule shutdown failed: reply %d", rply)
	}
	return nil
}

// sendWallNotice powers `slinitctl shutdown -k`: hand the operator's
// message to the daemon, which walls it to every logged-in user via
// its own utmp binding.
func sendWallNotice(conn net.Conn, msg string) error {
	msgBytes := []byte(msg)
	if len(msgBytes) > 0xFFFF {
		msgBytes = msgBytes[:0xFFFF]
	}
	payload := make([]byte, 0, 2+len(msgBytes))
	payload = append(payload,
		byte(len(msgBytes)&0xFF), byte((len(msgBytes)>>8)&0xFF))
	payload = append(payload, msgBytes...)

	if err := control.WritePacket(conn, control.CmdWallNotice, payload); err != nil {
		return err
	}
	rply, _, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyACK {
		return fmt.Errorf("wall notice failed: reply %d", rply)
	}
	info("Wall message broadcast.\n")
	return nil
}

func cmdCancelShutdown(conn net.Conn) error {
	if err := control.WritePacket(conn, control.CmdCancelShutdown, nil); err != nil {
		return err
	}
	rply, _, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}
	if rply == control.RplyACK {
		info("Scheduled shutdown cancelled.\n")
	} else {
		info("No shutdown is scheduled.\n")
	}
	return nil
}

func cmdQueryShutdown(conn net.Conn) error {
	if err := control.WritePacket(conn, control.CmdQueryShutdown, nil); err != nil {
		return err
	}
	rply, payload, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}
	if rply == control.RplyNAK {
		fmt.Println("No shutdown is scheduled.")
		return nil
	}
	if rply != control.RplyShutdownStatus || len(payload) < 5 {
		return fmt.Errorf("unexpected reply: %d", rply)
	}

	st := service.ShutdownType(payload[0])
	secs := uint32(payload[1])<<24 | uint32(payload[2])<<16 |
		uint32(payload[3])<<8 | uint32(payload[4])
	typeName := shutdownTypeToString(st)
	fmt.Printf("Shutdown (%s) scheduled in %s.\n", typeName, formatHumanDuration(time.Duration(secs)*time.Second))
	return nil
}

func parseShutdownType(s string) (service.ShutdownType, error) {
	switch s {
	case "halt":
		return service.ShutdownHalt, nil
	case "poweroff":
		return service.ShutdownPoweroff, nil
	case "reboot":
		return service.ShutdownReboot, nil
	case "kexec":
		return service.ShutdownKexec, nil
	case "softreboot", "soft-reboot":
		return service.ShutdownSoftReboot, nil
	default:
		return 0, fmt.Errorf("unknown shutdown type: %s (use halt, poweroff, reboot, kexec, or softreboot)", s)
	}
}

// kexecKernelLoaded reports whether the kernel currently has a
// kexec image loaded via kexec_load / kexec_file_load. Returns false
// on any read error — the caller uses this as a courtesy preflight,
// so an unreadable sysfs (container, older kernel without kexec)
// falls through to the daemon-side EINVAL handling.
func kexecKernelLoaded() bool {
	data, err := os.ReadFile("/sys/kernel/kexec_loaded")
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == "1"
}

// parseShutdownTime parses a time argument into a duration.
//
//	"now"      → 0 (immediate)
//	"+N"       → N minutes
//	"HH:MM"    → delay until that time today (or tomorrow if past)
func parseShutdownTime(s string) (time.Duration, error) {
	if s == "now" || s == "" {
		return 0, nil
	}

	// +N minutes
	if strings.HasPrefix(s, "+") {
		mins, err := strconv.Atoi(s[1:])
		if err != nil || mins < 0 {
			return 0, fmt.Errorf("invalid time: %s (use +N for minutes)", s)
		}
		return time.Duration(mins) * time.Minute, nil
	}

	// Plain number = minutes
	if mins, err := strconv.Atoi(s); err == nil && mins >= 0 {
		return time.Duration(mins) * time.Minute, nil
	}

	// HH:MM absolute time
	parts := strings.SplitN(s, ":", 2)
	if len(parts) == 2 {
		h, err1 := strconv.Atoi(parts[0])
		m, err2 := strconv.Atoi(parts[1])
		if err1 == nil && err2 == nil && h >= 0 && h <= 23 && m >= 0 && m <= 59 {
			now := time.Now()
			target := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
			if target.Before(now) {
				// Target time already passed today — schedule for tomorrow.
				target = target.Add(24 * time.Hour)
			}
			return target.Sub(now), nil
		}
	}

	return 0, fmt.Errorf("invalid time: %s (use 'now', '+N' for minutes, or 'HH:MM')", s)
}

func shutdownTypeToString(st service.ShutdownType) string {
	switch st {
	case service.ShutdownHalt:
		return "halt"
	case service.ShutdownPoweroff:
		return "poweroff"
	case service.ShutdownReboot:
		return "reboot"
	case service.ShutdownKexec:
		return "kexec"
	case service.ShutdownSoftReboot:
		return "softreboot"
	default:
		return "unknown"
	}
}

func formatHumanDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}

func cmdTrigger(conn net.Conn, name string) error {
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}

	payload := make([]byte, 5)
	binary.LittleEndian.PutUint32(payload, handle)
	payload[4] = 1 // trigger = true

	if err := control.WritePacket(conn, control.CmdSetTrigger, payload); err != nil {
		return err
	}

	rply, _, err := readReply(conn)
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyACK:
		info("Service '%s' triggered.\n", name)
	case control.RplyNAK:
		return fmt.Errorf("service '%s' is not a triggered service", name)
	default:
		return fmt.Errorf("unexpected reply: %d", rply)
	}
	return nil
}

func cmdUntrigger(conn net.Conn, name string) error {
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}

	payload := make([]byte, 5)
	binary.LittleEndian.PutUint32(payload, handle)
	payload[4] = 0 // trigger = false

	if err := control.WritePacket(conn, control.CmdSetTrigger, payload); err != nil {
		return err
	}

	rply, _, err := readReply(conn)
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyACK:
		info("Service '%s' untriggered.\n", name)
	case control.RplyNAK:
		return fmt.Errorf("service '%s' is not a triggered service", name)
	default:
		return fmt.Errorf("unexpected reply: %d", rply)
	}
	return nil
}

func cmdSignal(conn net.Conn, svcName string, sigStr string) error {
	handle, err := loadServiceHandle(conn, svcName)
	if err != nil {
		return err
	}

	sig, err := parseSignal(sigStr)
	if err != nil {
		return err
	}

	payload := make([]byte, 8)
	binary.LittleEndian.PutUint32(payload, handle)
	binary.LittleEndian.PutUint32(payload[4:], uint32(sig))

	if err := control.WritePacket(conn, control.CmdSignal, payload); err != nil {
		return err
	}

	rply, _, err := readReply(conn)
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyACK:
		info("Signal %s sent to service '%s'.\n", sigStr, svcName)
	case control.RplySignalNoPID:
		return fmt.Errorf("service '%s' has no running process", svcName)
	case control.RplySignalErr:
		return fmt.Errorf("failed to send signal to service '%s'", svcName)
	default:
		return fmt.Errorf("unexpected reply: %d", rply)
	}
	return nil
}

func cmdPause(conn net.Conn, svcName string) error {
	handle, err := loadServiceHandle(conn, svcName)
	if err != nil {
		return err
	}
	payload := make([]byte, 4)
	binary.LittleEndian.PutUint32(payload, handle)
	if err := control.WritePacket(conn, control.CmdPauseService, payload); err != nil {
		return err
	}
	rply, _, err := readReply(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyACK {
		return fmt.Errorf("failed to pause service '%s'", svcName)
	}
	info("Service '%s' paused.\n", svcName)
	return nil
}

func cmdContinue(conn net.Conn, svcName string) error {
	handle, err := loadServiceHandle(conn, svcName)
	if err != nil {
		return err
	}
	payload := make([]byte, 4)
	binary.LittleEndian.PutUint32(payload, handle)
	if err := control.WritePacket(conn, control.CmdContinueService, payload); err != nil {
		return err
	}
	rply, _, err := readReply(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyACK {
		return fmt.Errorf("failed to continue service '%s'", svcName)
	}
	info("Service '%s' continued.\n", svcName)
	return nil
}

func cmdOnce(conn net.Conn, svcName string) error {
	handle, err := loadServiceHandle(conn, svcName)
	if err != nil {
		return err
	}
	payload := make([]byte, 4)
	binary.LittleEndian.PutUint32(payload, handle)
	if err := control.WritePacket(conn, control.CmdOnceService, payload); err != nil {
		return err
	}
	rply, _, err := readReply(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyACK {
		return fmt.Errorf("failed to start service '%s' once", svcName)
	}
	info("Service '%s' started once (no restart).\n", svcName)
	return nil
}

func cmdRunAction(conn net.Conn, svcName, actionName string) error {
	handle, err := loadServiceHandle(conn, svcName)
	if err != nil {
		return err
	}
	// Payload: handle(4) + actionNameLen(2) + actionName(N)
	actionBytes := control.EncodeServiceName(actionName)
	payload := make([]byte, 4+len(actionBytes))
	binary.LittleEndian.PutUint32(payload, handle)
	copy(payload[4:], actionBytes)

	if err := control.WritePacket(conn, control.CmdRunAction, payload); err != nil {
		return err
	}
	rply, data, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}
	switch rply {
	case control.RplyActionOutput:
		if len(data) > 0 {
			fmt.Print(string(data))
		}
		return nil
	case control.RplyNAK:
		return fmt.Errorf("%s", string(data))
	default:
		return fmt.Errorf("unexpected reply: %d", rply)
	}
}

func cmdListActions(conn net.Conn, svcName string) error {
	handle, err := loadServiceHandle(conn, svcName)
	if err != nil {
		return err
	}
	payload := make([]byte, 4)
	binary.LittleEndian.PutUint32(payload, handle)
	if err := control.WritePacket(conn, control.CmdListActions, payload); err != nil {
		return err
	}
	rply, data, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}
	switch rply {
	case control.RplyActionList:
		if len(data) == 0 {
			info("No extra actions defined for '%s'.\n", svcName)
		} else {
			fmt.Println(string(data))
		}
		return nil
	default:
		return fmt.Errorf("unexpected reply: %d", rply)
	}
}

func cmdBootTime(conn net.Conn) error {
	if err := control.WritePacket(conn, control.CmdBootTime, nil); err != nil {
		return err
	}

	rply, payload, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}

	if rply != control.RplyBootTime {
		return fmt.Errorf("unexpected reply: %d", rply)
	}

	info, err := control.DecodeBootTime(payload)
	if err != nil {
		return err
	}

	kernelTime := time.Duration(info.KernelUptimeNs)
	bootReady := info.BootReadyNs > 0

	if bootReady {
		userspaceTime := time.Duration(info.BootReadyNs - info.BootStartNs)
		totalTime := kernelTime + userspaceTime
		fmt.Printf("Startup finished in %s (kernel) + %s (userspace) = %s\n",
			formatDuration(kernelTime),
			formatDuration(userspaceTime),
			formatDuration(totalTime))
		fmt.Printf("%s reached after %s in userspace.\n",
			info.BootSvcName,
			formatDuration(userspaceTime))
	} else {
		fmt.Printf("Startup in progress: %s (kernel) + ... (userspace)\n",
			formatDuration(kernelTime))
		fmt.Printf("Boot service '%s' has not yet reached STARTED.\n",
			info.BootSvcName)
	}

	// Collect services with timing data
	var timed []control.BootTimeEntry
	for _, entry := range info.Services {
		if entry.StartupNs > 0 {
			timed = append(timed, entry)
		}
	}

	if len(timed) > 0 {
		// Sort by startup duration descending (slowest first)
		sort.Slice(timed, func(i, j int) bool {
			return timed[i].StartupNs > timed[j].StartupNs
		})

		fmt.Println()
		fmt.Println("Service startup times:")
		for _, entry := range timed {
			dur := time.Duration(entry.StartupNs)
			suffix := ""
			if entry.PID > 0 {
				suffix = " (pid: " + strconv.FormatInt(int64(entry.PID), 10) + ")"
			}
			fmt.Printf("  %8s %s%s\n", formatDuration(dur), entry.Name, suffix)
		}
	}

	return nil
}

func formatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return strconv.FormatInt(d.Microseconds(), 10) + "us"
	}
	if d < time.Second {
		return strconv.FormatInt(d.Milliseconds(), 10) + "ms"
	}
	return strconv.FormatFloat(d.Seconds(), 'f', 3, 64) + "s"
}

func cmdReload(conn net.Conn, name string) error {
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}

	if err := control.WritePacket(conn, control.CmdReloadService, control.EncodeHandle(handle)); err != nil {
		return err
	}

	rply, _, err := readReply(conn)
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyACK:
		info("Service '%s' reloaded.\n", name)
	case control.RplyNAK:
		return fmt.Errorf("could not reload service '%s'; service may be in wrong state or have incompatible changes", name)
	default:
		return fmt.Errorf("unexpected reply: %d", rply)
	}
	return nil
}

// cmdReloadSignal sends the service's configured `reload-signal` to
// its main process. Distinct from cmdReload (which re-reads the
// service description); this is the nginx-reload / SIGHUP-style
// "tell the running process to re-read its own config" operation.
func cmdReloadSignal(conn net.Conn, name string) error {
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}
	if err := control.WritePacket(conn, control.CmdReloadSignal, control.EncodeHandle(handle)); err != nil {
		return err
	}
	rply, payload, err := readReply(conn)
	if err != nil {
		return err
	}
	switch rply {
	case control.RplyACK:
		info("Reload signal sent to '%s'.\n", name)
		return nil
	case control.RplyNAK:
		return fmt.Errorf("service '%s' has no reload-signal configured", name)
	case control.RplySignalNoPID:
		return fmt.Errorf("service '%s' has no running process", name)
	case control.RplySignalErr:
		return fmt.Errorf("reload-signal failed for '%s': %s", name, string(payload))
	default:
		return fmt.Errorf("unexpected reply: %d", rply)
	}
}

// cmdReloadAll asks the daemon to rescan every loaded service description
// from disk. The daemon returns a summary of how many succeeded and how
// many failed; transitional services (Starting/Stopping) are skipped
// silently and counted in neither bucket. Exits non-zero if any failed.
func cmdReloadAll(conn net.Conn) error {
	if err := control.WritePacket(conn, control.CmdReloadAll, nil); err != nil {
		return err
	}

	rply, payload, err := readReply(conn)
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyReloadAllResult:
		if len(payload) < 4 {
			return fmt.Errorf("reload-all: short reply (%d bytes)", len(payload))
		}
		ok := binary.LittleEndian.Uint16(payload[0:2])
		failed := binary.LittleEndian.Uint16(payload[2:4])
		if failed > 0 {
			info("Reloaded %d service(s); %d failed (see daemon log).\n", ok, failed)
			return fmt.Errorf("reload-all: %d service(s) failed", failed)
		}
		info("Reloaded %d service(s).\n", ok)
		return nil
	case control.RplyNAK:
		return fmt.Errorf("reload-all: daemon has no loader configured")
	default:
		return fmt.Errorf("unexpected reply: %d", rply)
	}
}

// cmdActivateProfile swaps the daemon's active profile. Reports the
// stopped/started/kept service lists so the operator can see the
// diff and reconcile any surprises.
func cmdActivateProfile(conn net.Conn, name string) error {
	if err := control.WritePacket(conn, control.CmdActivateProfile, control.EncodeServiceName(name)); err != nil {
		return err
	}
	rply, payload, err := readReply(conn)
	if err != nil {
		return err
	}
	switch rply {
	case control.RplyActivateResult:
		active, stopped, started, kept, derr := control.DecodeActivateResult(payload)
		if derr != nil {
			return fmt.Errorf("activate-profile: bad reply: %w", derr)
		}
		if active == "" {
			info("Profile filter deactivated.\n")
		} else {
			info("Profile '%s' now active.\n", active)
		}
		if len(stopped) > 0 {
			info("Stopped (%d): %s\n", len(stopped), strings.Join(stopped, ", "))
		}
		if len(started) > 0 {
			info("Started (%d): %s\n", len(started), strings.Join(started, ", "))
		}
		if len(kept) > 0 && !quiet {
			info("Kept (%d): %s\n", len(kept), strings.Join(kept, ", "))
		}
		return nil
	case control.RplyNAK:
		return fmt.Errorf("activate-profile: %s", string(payload))
	default:
		return fmt.Errorf("unexpected reply: %d", rply)
	}
}

// cmdActiveProfile prints the currently active profile name (empty
// output when no filter is active).
func cmdActiveProfile(conn net.Conn) error {
	if err := control.WritePacket(conn, control.CmdQueryProfile, nil); err != nil {
		return err
	}
	rply, payload, err := readReply(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyProfile {
		return fmt.Errorf("unexpected reply: %d", rply)
	}
	name, _, derr := control.DecodeServiceName(payload)
	if derr != nil {
		return fmt.Errorf("active-profile: bad reply: %w", derr)
	}
	if name == "" {
		info("(no active profile)\n")
	} else {
		fmt.Println(name)
	}
	return nil
}

// cmdListProfiles prints every profile tag declared by any loaded
// service, sorted alphabetically. Empty output = no service uses
// profiles.
func cmdListProfiles(conn net.Conn) error {
	if err := control.WritePacket(conn, control.CmdListProfiles, nil); err != nil {
		return err
	}
	rply, payload, err := readReply(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyProfileList {
		return fmt.Errorf("unexpected reply: %d", rply)
	}
	profiles, _, derr := control.DecodeStringList(payload)
	if derr != nil {
		return fmt.Errorf("list-profiles: bad reply: %w", derr)
	}
	for _, p := range profiles {
		fmt.Println(p)
	}
	return nil
}

func cmdUnload(conn net.Conn, name string) error {
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}

	if err := control.WritePacket(conn, control.CmdUnloadService, control.EncodeHandle(handle)); err != nil {
		return err
	}

	rply, _, err := readReply(conn)
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyACK:
		info("Service '%s' unloaded.\n", name)
	case control.RplyNotStopped:
		return fmt.Errorf("could not unload service '%s'; service is not stopped", name)
	case control.RplyNAK:
		return fmt.Errorf("could not unload service '%s'; service is a dependency of another service", name)
	default:
		return fmt.Errorf("unexpected reply: %d", rply)
	}
	return nil
}

func cmdCatLog(conn net.Conn, name string, clear bool) error {
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}

	payload := control.EncodeCatLogRequest(handle, clear)
	if err := control.WritePacket(conn, control.CmdCatLog, payload); err != nil {
		return err
	}

	rply, rplyPayload, err := readReply(conn)
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyNAK:
		return fmt.Errorf("service '%s': no log available (set log-type = buffer or log-file = /path for catlog support)", name)
	case control.RplySvcLog:
		_, logData, err := control.DecodeSvcLog(rplyPayload)
		if err != nil {
			return err
		}
		if len(logData) == 0 {
			fmt.Fprintf(os.Stderr, "(no buffered output for service '%s')\n", name)
			return nil
		}
		os.Stdout.Write(logData)
		if logData[len(logData)-1] != '\n' {
			fmt.Println()
		}
		return nil
	default:
		return fmt.Errorf("unexpected reply: %d", rply)
	}
}

func printSignalList() {
	signals := []struct {
		name string
		num  int
	}{
		{"HUP", 1}, {"INT", 2}, {"QUIT", 3}, {"ILL", 4},
		{"TRAP", 5}, {"ABRT", 6}, {"BUS", 7}, {"FPE", 8},
		{"KILL", 9}, {"USR1", 10}, {"SEGV", 11}, {"USR2", 12},
		{"PIPE", 13}, {"ALRM", 14}, {"TERM", 15}, {"STKFLT", 16},
		{"CHLD", 17}, {"CONT", 18}, {"STOP", 19}, {"TSTP", 20},
		{"TTIN", 21}, {"TTOU", 22}, {"URG", 23}, {"XCPU", 24},
		{"XFSZ", 25}, {"VTALRM", 26}, {"PROF", 27}, {"WINCH", 28},
		{"IO", 29}, {"PWR", 30}, {"SYS", 31},
	}
	for _, s := range signals {
		fmt.Printf("%2d) SIG%-8s", s.num, s.name)
		if s.num%4 == 0 {
			fmt.Println()
		}
	}
	if len(signals)%4 != 0 {
		fmt.Println()
	}
}

func parseSignal(s string) (syscall.Signal, error) {
	s = strings.TrimPrefix(strings.ToUpper(s), "SIG")
	switch s {
	case "HUP", "1":
		return syscall.SIGHUP, nil
	case "INT", "2":
		return syscall.SIGINT, nil
	case "QUIT", "3":
		return syscall.SIGQUIT, nil
	case "KILL", "9":
		return syscall.SIGKILL, nil
	case "TERM", "15":
		return syscall.SIGTERM, nil
	case "USR1", "10":
		return syscall.SIGUSR1, nil
	case "USR2", "12":
		return syscall.SIGUSR2, nil
	case "STOP", "19":
		return syscall.SIGSTOP, nil
	case "CONT", "18":
		return syscall.SIGCONT, nil
	default:
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("unknown signal: %s", s)
		}
		return syscall.Signal(n), nil
	}
}

func formatState(s service.ServiceState) string {
	switch s {
	case service.StateStopped:
		return "STOPPED"
	case service.StateStarting:
		return "STARTING"
	case service.StateStarted:
		return "STARTED"
	case service.StateStopping:
		return "STOPPING"
	default:
		return fmt.Sprintf("STATE(%d)", s)
	}
}

func formatTarget(s service.ServiceState) string {
	switch s {
	case service.StateStopped:
		return "stop"
	case service.StateStarted:
		return "start"
	default:
		return fmt.Sprintf("target(%d)", s)
	}
}

func cmdSetEnv(conn net.Conn, svcName, kvPair string) error {
	idx := strings.IndexByte(kvPair, '=')
	if idx < 0 {
		return fmt.Errorf("invalid format: expected KEY=VALUE, got %q", kvPair)
	}
	key := kvPair[:idx]
	value := kvPair[idx+1:]

	handle, err := loadServiceHandle(conn, svcName)
	if err != nil {
		return err
	}

	payload := control.EncodeSetEnv(handle, key, value, false)
	if err := control.WritePacket(conn, control.CmdSetEnv, payload); err != nil {
		return err
	}

	rply, _, err := readReply(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyACK {
		return fmt.Errorf("setenv failed: reply %d", rply)
	}
	info("Service '%s': set %s=%s\n", svcName, key, value)
	return nil
}

func cmdUnsetEnv(conn net.Conn, svcName, key string) error {
	handle, err := loadServiceHandle(conn, svcName)
	if err != nil {
		return err
	}

	payload := control.EncodeSetEnv(handle, key, "", true)
	if err := control.WritePacket(conn, control.CmdSetEnv, payload); err != nil {
		return err
	}

	rply, _, err := readReply(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyACK {
		return fmt.Errorf("unsetenv failed: reply %d", rply)
	}
	info("Service '%s': unset %s\n", svcName, key)
	return nil
}

func cmdResetEnv(conn net.Conn, svcName string) error {
	handle, err := loadServiceHandle(conn, svcName)
	if err != nil {
		return err
	}

	if err := control.WritePacket(conn, control.CmdResetEnv, control.EncodeHandle(handle)); err != nil {
		return err
	}

	rply, _, err := readReply(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyACK {
		return fmt.Errorf("reset-env failed: reply %d", rply)
	}
	info("Service '%s': cleared runtime environment\n", svcName)
	return nil
}

func cmdGetAllEnv(conn net.Conn, svcName string) error {
	handle, err := loadServiceHandle(conn, svcName)
	if err != nil {
		return err
	}

	if err := control.WritePacket(conn, control.CmdGetAllEnv, control.EncodeHandle(handle)); err != nil {
		return err
	}

	rply, payload, err := readReply(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyEnvList {
		return fmt.Errorf("getallenv failed: reply %d", rply)
	}

	env, err := control.DecodeEnvList(payload)
	if err != nil {
		return err
	}

	if len(env) == 0 {
		fmt.Printf("Service '%s': no runtime environment variables set.\n", svcName)
		return nil
	}

	// Sort keys for stable output
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("%s=%s\n", k, env[k])
	}
	return nil
}

func cmdSetEnvGlobal(conn net.Conn, kvPair string) error {
	idx := strings.IndexByte(kvPair, '=')
	if idx < 0 {
		return fmt.Errorf("invalid format: expected KEY=VALUE, got %q", kvPair)
	}
	key := kvPair[:idx]
	value := kvPair[idx+1:]

	payload := control.EncodeSetEnv(0, key, value, false)
	if err := control.WritePacket(conn, control.CmdSetEnv, payload); err != nil {
		return err
	}

	rply, _, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyACK {
		return fmt.Errorf("setenv-global failed: reply %d", rply)
	}
	info("Global: set %s=%s\n", key, value)
	return nil
}

func cmdUnsetEnvGlobal(conn net.Conn, key string) error {
	payload := control.EncodeSetEnv(0, key, "", true)
	if err := control.WritePacket(conn, control.CmdSetEnv, payload); err != nil {
		return err
	}

	rply, _, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyACK {
		return fmt.Errorf("unsetenv-global failed: reply %d", rply)
	}
	info("Global: unset %s\n", key)
	return nil
}

func cmdGetAllEnvGlobal(conn net.Conn) error {
	if err := control.WritePacket(conn, control.CmdGetAllEnv, control.EncodeHandle(0)); err != nil {
		return err
	}

	rply, payload, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyEnvList {
		return fmt.Errorf("getallenv-global failed: reply %d", rply)
	}

	env, err := control.DecodeEnvList(payload)
	if err != nil {
		return err
	}

	if len(env) == 0 {
		fmt.Println("No global environment variables set.")
		return nil
	}

	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("%s=%s\n", k, env[k])
	}
	return nil
}

func parseDepType(s string) (service.DependencyType, error) {
	switch s {
	case "depends-on", "regular":
		return service.DepRegular, nil
	case "waits-for", "soft":
		return service.DepWaitsFor, nil
	case "depends-ms", "milestone":
		return service.DepMilestone, nil
	case "prepared-by":
		return service.DepPreparedBy, nil
	case "before":
		return service.DepBefore, nil
	case "after":
		return service.DepAfter, nil
	default:
		return 0, fmt.Errorf("unknown dependency type: %s (use depends-on, waits-for, depends-ms, prepared-by, before, after)", s)
	}
}

func cmdAddDep(conn net.Conn, fromName, depTypeStr, toName string) error {
	depType, err := parseDepType(depTypeStr)
	if err != nil {
		return err
	}

	handleFrom, err := loadServiceHandle(conn, fromName)
	if err != nil {
		return err
	}
	handleTo, err := loadServiceHandle(conn, toName)
	if err != nil {
		return err
	}

	payload := control.EncodeDepRequest(handleFrom, handleTo, uint8(depType))
	if err := control.WritePacket(conn, control.CmdAddDep, payload); err != nil {
		return err
	}

	rply, _, err := readReply(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyACK {
		return fmt.Errorf("add-dep failed: reply %d", rply)
	}
	info("Added %s dependency: %s -> %s\n", depTypeStr, fromName, toName)
	return nil
}

func cmdRmDep(conn net.Conn, fromName, depTypeStr, toName string) error {
	depType, err := parseDepType(depTypeStr)
	if err != nil {
		return err
	}

	handleFrom, err := loadServiceHandle(conn, fromName)
	if err != nil {
		return err
	}
	handleTo, err := loadServiceHandle(conn, toName)
	if err != nil {
		return err
	}

	payload := control.EncodeDepRequest(handleFrom, handleTo, uint8(depType))
	if err := control.WritePacket(conn, control.CmdRmDep, payload); err != nil {
		return err
	}

	rply, _, err := readReply(conn)
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyACK:
		info("Removed %s dependency: %s -> %s\n", depTypeStr, fromName, toName)
	case control.RplyNAK:
		return fmt.Errorf("dependency %s -> %s (%s) not found", fromName, toName, depTypeStr)
	default:
		return fmt.Errorf("rm-dep failed: reply %d", rply)
	}
	return nil
}

func cmdEnable(conn net.Conn, name string, from string) error {
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}

	var payload []byte
	if from != "" {
		fromHandle, err := loadServiceHandle(conn, from)
		if err != nil {
			return err
		}
		payload = make([]byte, 8)
		binary.LittleEndian.PutUint32(payload, handle)
		binary.LittleEndian.PutUint32(payload[4:], fromHandle)
	} else {
		payload = control.EncodeHandle(handle)
	}

	// v7+ daemons answer CmdEnableServiceV7 with the target's status on
	// the same round-trip — that closes the race where the target could
	// finish starting between our enable request and a follow-up status
	// query. Fall back to plain CmdEnableService (ACK reply) on older
	// peers so mixed-version pairs still work.
	useV7 := peerCPVersion >= 7
	cmd := control.CmdEnableService
	if useV7 {
		cmd = control.CmdEnableServiceV7
	}
	if err := control.WritePacket(conn, cmd, payload); err != nil {
		return err
	}

	rply, replyPayload, err := readReply(conn)
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyACK:
		info("Service '%s' enabled.\n", name)
	case control.RplyServiceStatus:
		// v7 reply: [dep_exists(1B)][status_v6(22B)]
		if !useV7 || len(replyPayload) < 1 {
			return fmt.Errorf("enable failed: malformed status reply")
		}
		depExists := replyPayload[0] != 0
		if depExists {
			info("Service '%s' already enabled.\n", name)
		} else {
			info("Service '%s' enabled.\n", name)
		}
	case control.RplyNAK:
		return fmt.Errorf("could not enable service '%s': no boot service configured", name)
	case control.RplyShuttingDown:
		return fmt.Errorf("system is shutting down")
	default:
		return fmt.Errorf("enable failed: reply %d", rply)
	}
	return nil
}

func cmdUnpin(conn net.Conn, name string) error {
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}

	if err := control.WritePacket(conn, control.CmdUnpinService, control.EncodeHandle(handle)); err != nil {
		return err
	}

	rply, _, err := readReply(conn)
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyACK:
		info("Service '%s' unpinned.\n", name)
	default:
		return fmt.Errorf("unexpected reply: %d", rply)
	}
	return nil
}

func cmdDisable(conn net.Conn, name string, from string) error {
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}

	var payload []byte
	if from != "" {
		fromHandle, err := loadServiceHandle(conn, from)
		if err != nil {
			return err
		}
		payload = make([]byte, 8)
		binary.LittleEndian.PutUint32(payload, handle)
		binary.LittleEndian.PutUint32(payload[4:], fromHandle)
	} else {
		payload = control.EncodeHandle(handle)
	}

	if err := control.WritePacket(conn, control.CmdDisableService, payload); err != nil {
		return err
	}

	rply, _, err := readReply(conn)
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyACK:
		info("Service '%s' disabled.\n", name)
	case control.RplyNAK:
		return fmt.Errorf("could not disable service '%s': no boot service configured", name)
	default:
		return fmt.Errorf("disable failed: reply %d", rply)
	}
	return nil
}

func cmdQueryServiceName(conn net.Conn, svcName string) error {
	handle, err := loadServiceHandle(conn, svcName)
	if err != nil {
		return err
	}

	if err := control.WritePacket(conn, control.CmdQueryServiceName, control.EncodeHandle(handle)); err != nil {
		return err
	}

	rply, payload, err := readReply(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyServiceName {
		return fmt.Errorf("query-name failed: reply %d", rply)
	}

	name, _, err := control.DecodeServiceName(payload)
	if err != nil {
		return err
	}
	fmt.Println(name)
	return nil
}

func cmdQueryServiceDscDir(conn net.Conn) error {
	if err := control.WritePacket(conn, control.CmdQueryServiceDscDir, nil); err != nil {
		return err
	}

	rply, payload, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyServiceDscDir {
		return fmt.Errorf("service-dirs failed: reply %d", rply)
	}

	if len(payload) < 2 {
		return fmt.Errorf("response too short")
	}
	count := int(binary.LittleEndian.Uint16(payload))
	off := 2
	for i := 0; i < count; i++ {
		if len(payload) < off+2 {
			return fmt.Errorf("truncated response at dir %d", i)
		}
		dirLen := int(binary.LittleEndian.Uint16(payload[off:]))
		off += 2
		if len(payload) < off+dirLen {
			return fmt.Errorf("truncated response at dir %d", i)
		}
		fmt.Println(string(payload[off : off+dirLen]))
		off += dirLen
	}
	return nil
}

func cmdDependents(conn net.Conn, name string) error {
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}

	if err := control.WritePacket(conn, control.CmdQueryDependents, control.EncodeHandle(handle)); err != nil {
		return err
	}

	rply, payload, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyDependents {
		return fmt.Errorf("dependents query failed: reply %d", rply)
	}

	if len(payload) < 4 {
		return fmt.Errorf("response too short")
	}
	count := int(binary.LittleEndian.Uint32(payload))
	off := 4

	if count == 0 {
		fmt.Printf("Service '%s' has no dependents.\n", name)
		return nil
	}

	fmt.Printf("Service '%s' has %d dependent(s):\n", name, count)
	for i := 0; i < count; i++ {
		if len(payload) < off+4 {
			return fmt.Errorf("truncated response at dependent %d", i)
		}
		depHandle := binary.LittleEndian.Uint32(payload[off:])
		off += 4

		// Query the name of each dependent
		if err := control.WritePacket(conn, control.CmdQueryServiceName, control.EncodeHandle(depHandle)); err != nil {
			fmt.Printf("  handle=%d (name query failed)\n", depHandle)
			continue
		}
		rply2, payload2, err := control.ReadPacket(conn)
		if err != nil || rply2 != control.RplyServiceName {
			fmt.Printf("  handle=%d\n", depHandle)
			continue
		}
		depName, _, _ := control.DecodeServiceName(payload2)
		fmt.Printf("  %s\n", depName)
	}
	return nil
}

// cmdGraph queries all services and their forward dependencies, then outputs
// a DOT-format directed graph suitable for Graphviz visualization.
//
// Usage: slinitctl graph | dot -Tpng -o services.png
//
//	slinitctl graph | dot -Tsvg -o services.svg
func cmdGraph(conn net.Conn) error {
	// Phase 1: list all services (collect names + handles)
	type svcEntry struct {
		name   string
		state  service.ServiceState
		stype  service.ServiceType
		handle uint32
	}

	if err := control.WritePacket(conn, control.CmdListServices, nil); err != nil {
		return err
	}

	var entries []svcEntry
	for {
		rply, payload, err := control.ReadPacket(conn)
		if err != nil {
			return err
		}
		if rply == control.RplyListDone {
			break
		}
		if rply != control.RplySvcInfo {
			return fmt.Errorf("unexpected reply: %d", rply)
		}
		entry, _, err := control.DecodeSvcInfo(payload)
		if err != nil {
			return err
		}
		entries = append(entries, svcEntry{
			name:  entry.Name,
			state: entry.State,
			stype: entry.SvcType,
		})
	}

	// Phase 2: get handle for each service
	for i := range entries {
		namePayload := control.EncodeServiceName(entries[i].name)
		if err := control.WritePacket(conn, control.CmdFindService, namePayload); err != nil {
			return err
		}
		rply, payload, err := control.ReadPacket(conn)
		if err != nil {
			return err
		}
		if rply != control.RplyServiceRecord || len(payload) < 5 {
			continue
		}
		entries[i].handle = binary.LittleEndian.Uint32(payload[1:])
	}

	// Phase 3: query forward dependencies for each service
	type depEdge struct {
		from    string
		to      string
		depType service.DependencyType
	}

	handleNames := make(map[uint32]string)
	for _, e := range entries {
		handleNames[e.handle] = e.name
	}

	var edges []depEdge
	for _, e := range entries {
		if e.handle == 0 {
			continue
		}
		if err := control.WritePacket(conn, control.CmdQueryDependencies, control.EncodeHandle(e.handle)); err != nil {
			return err
		}
		rply, payload, err := control.ReadPacket(conn)
		if err != nil {
			return err
		}
		if rply != control.RplyDependencies || len(payload) < 4 {
			continue
		}

		count := int(binary.LittleEndian.Uint32(payload))
		off := 4
		for j := 0; j < count; j++ {
			if len(payload) < off+5 {
				break
			}
			depHandle := binary.LittleEndian.Uint32(payload[off:])
			dt := service.DependencyType(payload[off+4])
			off += 5

			depName, ok := handleNames[depHandle]
			if !ok {
				// Resolve name for newly allocated handle
				if err := control.WritePacket(conn, control.CmdQueryServiceName, control.EncodeHandle(depHandle)); err != nil {
					continue
				}
				rply2, payload2, err := control.ReadPacket(conn)
				if err != nil || rply2 != control.RplyServiceName {
					depName = fmt.Sprintf("handle_%d", depHandle)
				} else {
					depName, _, _ = control.DecodeServiceName(payload2)
				}
				handleNames[depHandle] = depName
			}

			edges = append(edges, depEdge{from: e.name, to: depName, depType: dt})
		}
	}

	// Phase 4: emit DOT output
	fmt.Println("digraph services {")
	fmt.Println("  rankdir=LR;")
	fmt.Println("  node [fontname=\"sans-serif\" fontsize=10];")
	fmt.Println("  edge [fontname=\"sans-serif\" fontsize=8];")
	fmt.Println()

	// Node styling by state and type
	for _, e := range entries {
		shape := graphNodeShape(e.stype)
		color, fillcolor := graphNodeColor(e.state)
		fmt.Printf("  %q [shape=%s style=filled fillcolor=%q color=%q];\n",
			e.name, shape, fillcolor, color)
	}
	fmt.Println()

	// Edges with dep type styling
	for _, edge := range edges {
		style, color, label := graphEdgeStyle(edge.depType)
		attrs := fmt.Sprintf("style=%s color=%q", style, color)
		if label != "" {
			attrs += fmt.Sprintf(" label=%q", label)
		}
		fmt.Printf("  %q -> %q [%s];\n", edge.from, edge.to, attrs)
	}

	fmt.Println("}")
	return nil
}

// graphNodeShape returns the DOT shape for a service type.
func graphNodeShape(t service.ServiceType) string {
	switch t {
	case service.TypeInternal:
		return "diamond"
	case service.TypeTriggered:
		return "hexagon"
	case service.TypeScripted:
		return "box"
	case service.TypeBGProcess:
		return "doubleoctagon"
	default: // TypeProcess
		return "ellipse"
	}
}

// graphNodeColor returns (border, fill) colors based on service state.
func graphNodeColor(s service.ServiceState) (string, string) {
	switch s {
	case service.StateStarted:
		return "#2e7d32", "#c8e6c9" // green
	case service.StateStarting:
		return "#f9a825", "#fff9c4" // yellow
	case service.StateStopping:
		return "#ef6c00", "#ffe0b2" // orange
	case service.StateStopped:
		return "#c62828", "#ffcdd2" // red
	default:
		return "#616161", "#eeeeee" // grey
	}
}

// graphEdgeStyle returns (style, color, label) for a dependency type.
func graphEdgeStyle(dt service.DependencyType) (string, string, string) {
	switch dt {
	case service.DepRegular:
		return "solid", "#1565c0", "" // blue solid
	case service.DepSoft:
		return "dashed", "#7b1fa2", "soft" // purple dashed
	case service.DepWaitsFor:
		return "dashed", "#00838f", "waits-for" // teal dashed
	case service.DepMilestone:
		return "bold", "#e65100", "milestone" // orange bold
	case service.DepPreparedBy:
		return "bold", "#1565c0", "prepared-by" // blue bold (hard + restart-link)
	case service.DepBefore:
		return "dotted", "#616161", "before" // grey dotted
	case service.DepAfter:
		return "dotted", "#9e9e9e", "after" // light grey dotted
	default:
		return "solid", "#000000", ""
	}
}

func cmdQueryLoadMech(conn net.Conn) error {
	if err := control.WritePacket(conn, control.CmdQueryLoadMech, nil); err != nil {
		return err
	}

	rply, payload, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyLoaderMech {
		return fmt.Errorf("query-load-mech failed: reply %d", rply)
	}

	// Wire format: loaderType(1) + cwdLen(4) + cwd(N) + numDirs(4) + [dirLen(4) + dir(N)]*
	if len(payload) < 9 {
		return fmt.Errorf("response too short")
	}
	loaderType := payload[0]
	off := 1
	cwdLen := int(binary.LittleEndian.Uint32(payload[off:]))
	off += 4
	if len(payload) < off+cwdLen {
		return fmt.Errorf("truncated cwd")
	}
	cwd := string(payload[off : off+cwdLen])
	off += cwdLen

	if len(payload) < off+4 {
		return fmt.Errorf("truncated dir count")
	}
	numDirs := int(binary.LittleEndian.Uint32(payload[off:]))
	off += 4

	fmt.Printf("Loader type: %d (directory)\n", loaderType)
	fmt.Printf("Working dir: %s\n", cwd)
	fmt.Printf("Service directories (%d):\n", numDirs)
	for i := 0; i < numDirs; i++ {
		if len(payload) < off+4 {
			return fmt.Errorf("truncated dir %d", i)
		}
		dirLen := int(binary.LittleEndian.Uint32(payload[off:]))
		off += 4
		if len(payload) < off+dirLen {
			return fmt.Errorf("truncated dir %d", i)
		}
		fmt.Printf("  %s\n", string(payload[off:off+dirLen]))
		off += dirLen
	}
	return nil
}

func stopReasonStr(r uint8) string {
	switch service.StoppedReason(r) {
	case service.ReasonNormal:
		return "normal"
	case service.ReasonDepRestart:
		return "dependency-restart"
	case service.ReasonDepFailed:
		return "dependency-failed"
	case service.ReasonFailed:
		return "failed"
	case service.ReasonExecFailed:
		return "exec-failed"
	case service.ReasonTimedOut:
		return "timed-out"
	case service.ReasonTerminated:
		return "terminated"
	default:
		return fmt.Sprintf("unknown(%d)", r)
	}
}

func cmdListServices5(conn net.Conn) error {
	if err := control.WritePacket(conn, control.CmdListServices5, nil); err != nil {
		return err
	}

	for {
		rply, payload, err := control.ReadPacket(conn)
		if err != nil {
			return err
		}

		if rply == control.RplyListDone {
			break
		}

		if rply != control.RplySvcInfo {
			return fmt.Errorf("unexpected reply: %d", rply)
		}

		entry, _, err := control.DecodeSvcInfo5(payload)
		if err != nil {
			return err
		}

		state := service.ServiceState(entry.Status.State).String()
		target := service.ServiceState(entry.Status.TargetState).String()
		reason := stopReasonStr(entry.Status.StopReason)

		fmt.Printf("%-20s state=%-10s target=%-10s stop-reason=%-20s",
			entry.Name, state, target, reason)
		if entry.Status.Flags&control.StatusFlagHasPID != 0 {
			fmt.Printf(" [has-pid]")
		}
		if entry.Status.ExecStage != 0 {
			fmt.Printf(" exec-stage=%d", entry.Status.ExecStage)
		}
		fmt.Println()
	}
	return nil
}

func cmdServiceStatus5(conn net.Conn, svcName string) error {
	handle, err := loadServiceHandle(conn, svcName)
	if err != nil {
		return err
	}

	if err := control.WritePacket(conn, control.CmdServiceStatus5, control.EncodeHandle(handle)); err != nil {
		return err
	}

	rply, payload, err := readReply(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyServiceStatus {
		return fmt.Errorf("status5 failed: reply %d", rply)
	}

	status, err := control.DecodeServiceStatus5(payload)
	if err != nil {
		return err
	}

	state := service.ServiceState(status.State).String()
	target := service.ServiceState(status.TargetState).String()
	reason := stopReasonStr(status.StopReason)

	fmt.Printf("Service: %s\n", svcName)
	fmt.Printf("  State:       %s\n", state)
	fmt.Printf("  Target:      %s\n", target)
	fmt.Printf("  Stop-reason: %s\n", reason)
	fmt.Printf("  Flags:       0x%02x", status.Flags)
	if status.Flags&control.StatusFlagHasPID != 0 {
		fmt.Printf(" [has-pid]")
	}
	if status.Flags&control.StatusFlagMarkedActive != 0 {
		fmt.Printf(" [active]")
	}
	if status.Flags&control.StatusFlagHasConsole != 0 {
		fmt.Printf(" [console]")
	}
	if status.Flags&control.StatusFlagStartFailed != 0 {
		fmt.Printf(" [start-failed]")
	}
	fmt.Println()
	if status.ExecStage != 0 {
		fmt.Printf("  Exec-stage:  %d\n", status.ExecStage)
	}
	fmt.Printf("  si_code:     %d\n", status.SiCode)
	fmt.Printf("  si_status:   %d\n", status.SiStatus)
	return nil
}

// cmdCompletion outputs a shell completion script to stdout.
func cmdPlatform() {
	detected := platform.Detect()
	if detected == platform.None {
		fmt.Println("Platform: bare-metal")
	} else {
		fmt.Printf("Platform: %s\n", detected)
	}
}

// evalFileCompare returns the exit code that cmdFileCompare would use for
// the given operation and two paths. Extracted so it can be unit-tested
// without process termination.
//
// Return codes:
//
//	0 — condition is true
//	1 — condition is false (or a path is missing)
//	2 — a genuine stat error (other than ENOENT)
func evalFileCompare(op, a, b string) (int, error) {
	statA, errA := os.Stat(a)
	statB, errB := os.Stat(b)
	if errA != nil || errB != nil {
		// Match OpenRC semantics: a missing file means the comparison is
		// false rather than a hard error. Only surface a non-fatal exit 2
		// for genuine stat errors other than ENOENT, so service scripts
		// can still branch on the result.
		if errA != nil && !os.IsNotExist(errA) {
			return 2, errA
		}
		if errB != nil && !os.IsNotExist(errB) {
			return 2, errB
		}
		return 1, nil
	}

	var result bool
	switch op {
	case "is-newer-than":
		result = statA.ModTime().After(statB.ModTime())
	case "is-older-than":
		result = statA.ModTime().Before(statB.ModTime())
	}
	if result {
		return 0, nil
	}
	return 1, nil
}

// cmdFileCompare implements the is-newer-than and is-older-than subcommands,
// mirroring OpenRC's is_newer_than(1) / is_older_than(1) helpers used by
// service scripts for conditional restarts when a config file changes.
func cmdFileCompare(op, a, b string) {
	code, err := evalFileCompare(op, a, b)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinitctl %s: %v\n", op, err)
	}
	os.Exit(code)
}

func cmdCompletion(shell string) {
	switch shell {
	case "bash":
		fmt.Print(bashCompletion)
	case "zsh":
		printZshCompletion()
	case "fish":
		printFishCompletion()
	default:
		fatal("Unsupported shell: %s (use bash, zsh, or fish)", shell)
	}
}

const bashCompletion = `# Bash completion for slinitctl
# Usage: eval "$(slinitctl completion bash)"

_slinitctl_commands() {
    echo "list ls start wake stop release restart status is-started is-failed is-newer-than is-older-than shutdown trigger untrigger signal pause continue cont once reload reload-all reload-signal unload boot-time analyze catlog setenv unsetenv getallenv reset-env setenv-global unsetenv-global getallenv-global add-dep rm-dep unpin enable disable graph dependents query-name service-dirs load-mech list5 status5 attach platform completion"
}

_slinitctl_services() {
    slinitctl --system list 2>/dev/null | sed 's/^\[.*\] //' | sed 's/ (.*//'
}

_slinitctl() {
    local cur prev cmd
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"

    cmd=""
    local i
    for ((i=1; i < COMP_CWORD; i++)); do
        case "${COMP_WORDS[i]}" in
            --socket-path|-p|--services-dir|-d|--from) ((i++)) ;;
            -*) ;;
            *) cmd="${COMP_WORDS[i]}"; break ;;
        esac
    done

    case "$prev" in
        --socket-path|-p) COMPREPLY=( $(compgen -f -- "$cur") ); return 0 ;;
        --services-dir|-d) COMPREPLY=( $(compgen -d -- "$cur") ); return 0 ;;
        --from) COMPREPLY=( $(compgen -W "$(_slinitctl_services)" -- "$cur") ); return 0 ;;
    esac

    if [ -z "$cmd" ]; then
        if [[ "$cur" == -* ]]; then
            COMPREPLY=( $(compgen -W "--socket-path -p --system -s --user -u --no-wait -w --wait --pin --force -f --ignore-unstarted --offline -o --services-dir -d --from --use-passed-cfd --quiet -q --help -h --version" -- "$cur") )
        else
            COMPREPLY=( $(compgen -W "$(_slinitctl_commands)" -- "$cur") )
        fi
        return 0
    fi

    case "$cmd" in
        start|stop|wake|release|restart|status|is-started|is-failed|trigger|untrigger|pause|continue|cont|once|reload|reload-signal|unload|unpin|enable|disable|query-name|getallenv|catlog|dependents|setenv|unsetenv|status5|attach)
            COMPREPLY=( $(compgen -W "$(_slinitctl_services)" -- "$cur") ) ;;
        shutdown)
            COMPREPLY=( $(compgen -W "halt poweroff reboot kexec softreboot" -- "$cur") ) ;;
        signal)
            local args_after=0
            for ((i=i+1; i < COMP_CWORD; i++)); do
                case "${COMP_WORDS[i]}" in -*) ;; *) ((args_after++)) ;; esac
            done
            if [ "$args_after" -eq 0 ]; then
                COMPREPLY=( $(compgen -W "SIGHUP SIGINT SIGQUIT SIGKILL SIGUSR1 SIGUSR2 SIGTERM SIGCONT SIGSTOP SIGTSTP --list -l" -- "$cur") )
            else
                COMPREPLY=( $(compgen -W "$(_slinitctl_services)" -- "$cur") )
            fi ;;
        add-dep|rm-dep)
            local args_after=0
            for ((i=i+1; i < COMP_CWORD; i++)); do
                case "${COMP_WORDS[i]}" in -*) ;; *) ((args_after++)) ;; esac
            done
            case "$args_after" in
                0|2) COMPREPLY=( $(compgen -W "$(_slinitctl_services)" -- "$cur") ) ;;
                1) COMPREPLY=( $(compgen -W "regular waits-for milestone soft before after" -- "$cur") ) ;;
            esac ;;
        completion)
            COMPREPLY=( $(compgen -W "bash zsh fish" -- "$cur") ) ;;
        is-newer-than|is-older-than)
            COMPREPLY=( $(compgen -f -- "$cur") ) ;;
        graph|list5|getallenv-global|boot-time|analyze|service-dirs|load-mech)
            ;;
    esac
    return 0
}

complete -F _slinitctl slinitctl
`

func printZshCompletion() {
	fmt.Println(`#compdef slinitctl
# Zsh completion for slinitctl
# Usage: eval "$(slinitctl completion zsh)"

_slinitctl_services() {
    local -a services`)
	fmt.Println("    services=( ${(f)\"$(slinitctl --system list 2>/dev/null | sed 's/^\\[.*\\] //' | sed 's/ (.*//')\"} )")
	fmt.Println(`    _describe 'service' services
}

_slinitctl() {
    local -a commands global_opts
    commands=(
        'list:List all loaded services'
        'ls:List all loaded services'
        'start:Start a service'
        'wake:Start without marking active'
        'stop:Stop a service'
        'release:Remove active mark'
        'restart:Restart a service'
        'status:Show service status'
        'is-started:Check if started'
        'is-failed:Check if failed'
        'is-newer-than:Check if file A is newer than file B'
        'is-older-than:Check if file A is older than file B'
        'shutdown:Initiate shutdown'
        'trigger:Trigger a service'
        'untrigger:Reset trigger'
        'signal:Send signal to service'
        'pause:Pause (SIGSTOP) a service'
        'continue:Continue (SIGCONT) a paused service'
        'cont:Continue (SIGCONT) a paused service'
        'once:Start service without restart on exit'
        'reload:Reload service config'
        'reload-all:Reload every loaded service from disk'
        'reload-signal:Send configured reload-signal to service process'
        'unload:Unload stopped service'
        'boot-time:Boot timing analysis'
        'analyze:Boot timing analysis'
        'catlog:Show service log buffer'
        'setenv:Set service env var'
        'unsetenv:Remove service env var'
        'getallenv:List service env vars'
        'setenv-global:Set global env var'
        'unsetenv-global:Remove global env var'
        'getallenv-global:List global env vars'
        'add-dep:Add runtime dependency'
        'rm-dep:Remove runtime dependency'
        'unpin:Remove pins'
        'enable:Enable service'
        'disable:Disable service'
        'graph:Export dependency graph (DOT format)'
        'dependents:List dependents'
        'query-name:Query service name'
        'service-dirs:List service dirs'
        'load-mech:Query loader mechanism'
        'list5:List services (protocol v5)'
        'status5:Show status (protocol v5)'
        'attach:Attach to service terminal'
        'completion:Output shell completion script'
    )
    global_opts=(
        '(-p --socket-path)'{-p,--socket-path}'[Control socket path]:path:_files'
        '(-s --system)'{-s,--system}'[System service manager]'
        '(-u --user)'{-u,--user}'[User service manager]'
        '--no-wait[Do not wait]'
        '--pin[Pin service state]'
        '(-f --force)'{-f,--force}'[Force stop]'
        '--ignore-unstarted[Exit 0 if already stopped]'
        '(-o --offline)'{-o,--offline}'[Offline mode]'
        '(-d --services-dir)'{-d,--services-dir}'[Service directory]:dir:_directories'
        '--from[Source service]:service:_slinitctl_services'
        '--use-passed-cfd[Use SLINIT_CS_FD]'
        '(-q --quiet)'{-q,--quiet}'[Suppress output]'
        '(-h --help)'{-h,--help}'[Show help]'
        '--version[Show version]'
    )
    _arguments -C $global_opts '1:command:->command' '*::arg:->args'
    case $state in
        command) _describe 'command' commands ;;
        args)
            case ${words[1]} in
                start|stop|wake|release|restart|status|is-started|is-failed|trigger|untrigger|pause|continue|cont|once|reload|reload-signal|unload|unpin|enable|disable|query-name|getallenv|catlog|dependents|setenv|unsetenv|status5|attach)
                    _slinitctl_services ;;
                shutdown) _describe 'type' '(halt poweroff reboot kexec softreboot)' ;;
                signal) case $CURRENT in 2) _describe 'signal' '(SIGHUP SIGINT SIGQUIT SIGKILL SIGUSR1 SIGUSR2 SIGTERM)' ;; 3) _slinitctl_services ;; esac ;;
                add-dep|rm-dep) case $CURRENT in 2|4) _slinitctl_services ;; 3) _describe 'dep type' '(regular waits-for milestone soft before after)' ;; esac ;;
                is-newer-than|is-older-than) _files ;;
                completion) _describe 'shell' '(bash zsh fish)' ;;
            esac ;;
    esac
}
_slinitctl "$@"`)
}

func printFishCompletion() {
	fmt.Println(`# Fish completion for slinitctl
# Usage: slinitctl completion fish | source

function __slinitctl_services
    slinitctl --system list 2>/dev/null | string replace -r '^\[.*\] ' '' | string replace -r ' \(.*' ''
end

set -l cmds list ls start wake stop release restart status is-started is-failed is-newer-than is-older-than shutdown trigger untrigger signal pause continue cont once reload reload-all reload-signal unload boot-time analyze catlog setenv unsetenv getallenv reset-env setenv-global unsetenv-global getallenv-global add-dep rm-dep unpin enable disable graph dependents query-name service-dirs load-mech list5 status5 attach completion

complete -c slinitctl -f
complete -c slinitctl -n "not __fish_seen_subcommand_from $cmds" -s p -l socket-path -rF -d 'Socket path'
complete -c slinitctl -n "not __fish_seen_subcommand_from $cmds" -s s -l system -d 'System mode'
complete -c slinitctl -n "not __fish_seen_subcommand_from $cmds" -s u -l user -d 'User mode'
complete -c slinitctl -n "not __fish_seen_subcommand_from $cmds" -l no-wait -d 'No wait'
complete -c slinitctl -n "not __fish_seen_subcommand_from $cmds" -l pin -d 'Pin state'
complete -c slinitctl -n "not __fish_seen_subcommand_from $cmds" -s f -l force -d 'Force'
complete -c slinitctl -n "not __fish_seen_subcommand_from $cmds" -s q -l quiet -d 'Quiet'
complete -c slinitctl -n "not __fish_seen_subcommand_from $cmds" -s h -l help -d 'Help'
complete -c slinitctl -n "not __fish_seen_subcommand_from $cmds" -l version -d 'Version'

for cmd in list ls start wake stop release restart status is-started is-failed is-newer-than is-older-than shutdown trigger untrigger signal pause continue cont once reload reload-all reload-signal unload boot-time analyze catlog setenv unsetenv getallenv reset-env setenv-global unsetenv-global getallenv-global add-dep rm-dep unpin enable disable graph dependents query-name service-dirs load-mech list5 status5 attach completion
    complete -c slinitctl -n "not __fish_seen_subcommand_from $cmds" -a $cmd
end

for cmd in start stop wake release restart status is-started is-failed trigger untrigger pause continue cont once reload reload-signal unload unpin enable disable query-name getallenv reset-env catlog dependents setenv unsetenv status5 attach
    complete -c slinitctl -n "__fish_seen_subcommand_from $cmd" -a '(__slinitctl_services)'
end

complete -c slinitctl -n "__fish_seen_subcommand_from shutdown" -a 'halt poweroff reboot kexec softreboot'
complete -c slinitctl -n "__fish_seen_subcommand_from signal" -a 'SIGHUP SIGINT SIGQUIT SIGKILL SIGUSR1 SIGUSR2 SIGTERM SIGCONT SIGSTOP'
complete -c slinitctl -n "__fish_seen_subcommand_from add-dep rm-dep" -a 'regular waits-for milestone soft before after'
complete -c slinitctl -n "__fish_seen_subcommand_from is-newer-than is-older-than" -F
complete -c slinitctl -n "__fish_seen_subcommand_from completion" -a 'bash zsh fish'`)
}

// cmdAttach connects to a service's vtty Unix socket for interactive terminal access.
// Puts the local terminal in raw mode, forwards I/O bidirectionally, and
// detaches on Ctrl+] (0x1d).
func cmdAttach(svcName, socketPath string, systemMode bool) error {
	// Determine vtty socket path
	vttyDir := "/run/slinit"
	if !systemMode {
		home := os.Getenv("HOME")
		if home != "" {
			vttyDir = filepath.Join(home, ".slinit")
		}
	}
	vttyPath := filepath.Join(vttyDir, fmt.Sprintf("vtty-%s.sock", svcName))

	conn, err := net.Dial("unix", vttyPath)
	if err != nil {
		return fmt.Errorf("cannot attach to '%s': %v (vtty socket: %s)", svcName, err, vttyPath)
	}
	defer conn.Close()

	// Save terminal state and set raw mode
	oldState, err := makeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw mode: %v", err)
	}
	defer restoreTerminal(int(os.Stdin.Fd()), oldState)

	fmt.Fprintf(os.Stderr, "\r\n[attached to %s — press Ctrl+] to detach]\r\n", svcName)

	// Forward vtty output → stdout
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				os.Stdout.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	// Forward stdin → vtty (with Ctrl+] detach detection)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				conn.Close()
				return
			}
			for i := 0; i < n; i++ {
				if buf[i] == 0x1d { // Ctrl+]
					fmt.Fprintf(os.Stderr, "\r\n[detached from %s]\r\n", svcName)
					conn.Close()
					return
				}
			}
			conn.Write(buf[:n])
		}
	}()

	<-doneCh
	return nil
}

// makeRaw sets the terminal to raw mode and returns the old state.
func makeRaw(fd int) (*syscall.Termios, error) {
	var oldState syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, uintptr(fd),
		uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&oldState)), 0, 0, 0)
	if errno != 0 {
		return nil, errno
	}

	newState := oldState
	// Disable canonical mode, echo, signals
	newState.Lflag &^= syscall.ICANON | syscall.ECHO | syscall.ISIG | syscall.IEXTEN
	// Disable input processing
	newState.Iflag &^= syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON
	// Disable output processing
	newState.Oflag &^= syscall.OPOST
	// Character size mask to 8 bits
	newState.Cflag &^= syscall.CSIZE | syscall.PARENB
	newState.Cflag |= syscall.CS8
	// Read at least 1 byte, no timeout
	newState.Cc[syscall.VMIN] = 1
	newState.Cc[syscall.VTIME] = 0

	_, _, errno = syscall.Syscall6(syscall.SYS_IOCTL, uintptr(fd),
		uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(&newState)), 0, 0, 0)
	if errno != 0 {
		return nil, errno
	}
	return &oldState, nil
}

// restoreTerminal restores the terminal to the given state.
func restoreTerminal(fd int, state *syscall.Termios) {
	syscall.Syscall6(syscall.SYS_IOCTL, uintptr(fd),
		uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(state)), 0, 0, 0)
}

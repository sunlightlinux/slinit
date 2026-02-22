// slinitctl is the control CLI for the slinit service manager.
// It communicates with a running slinit instance via a Unix domain socket.
package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/service"
)

const (
	defaultSystemSocket = "/run/slinit.socket"
	defaultUserSocket   = ".slinitctl"
)

func main() {
	args := os.Args[1:]

	// Parse global flags
	socketPath := ""
	for len(args) > 0 {
		if args[0] == "--socket-path" || args[0] == "-s" {
			if len(args) < 2 {
				fatal("--socket-path requires an argument")
			}
			socketPath = args[1]
			args = args[2:]
		} else if strings.HasPrefix(args[0], "--socket-path=") {
			socketPath = strings.TrimPrefix(args[0], "--socket-path=")
			args = args[1:]
		} else if args[0] == "--help" || args[0] == "-h" {
			printUsage()
			os.Exit(0)
		} else if args[0] == "--version" {
			fmt.Println("slinitctl version 0.1.0")
			os.Exit(0)
		} else {
			break
		}
	}

	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}

	sockPath := resolveSocketPath(socketPath)
	command := args[0]
	cmdArgs := args[1:]

	conn, err := connectSocket(sockPath)
	if err != nil {
		fatal("Failed to connect to slinit at %s: %v", sockPath, err)
	}
	defer conn.Close()

	switch command {
	case "list", "ls":
		err = cmdList(conn)
	case "start":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdStart(conn, name)
		})
	case "stop":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdStop(conn, name)
		})
	case "restart":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdRestart(conn, name)
		})
	case "status":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdStatus(conn, name)
		})
	case "shutdown":
		shutType := "poweroff"
		if len(cmdArgs) > 0 {
			shutType = cmdArgs[0]
		}
		err = cmdShutdown(conn, shutType)
	case "trigger":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdTrigger(conn, name)
		})
	case "signal":
		if len(cmdArgs) < 2 {
			fatal("Usage: slinitctl signal <signal> <service>")
		}
		err = cmdSignal(conn, cmdArgs[1], cmdArgs[0])
	case "boot-time", "analyze":
		err = cmdBootTime(conn)
	case "reload":
		err = requireServiceArg(cmdArgs, func(name string) error {
			return cmdReload(conn, name)
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
  --socket-path, -s PATH   Control socket path
  --help, -h               Show this help
  --version                Show version

Commands:
  list                     List all loaded services
  start <service>          Start a service
  stop <service>           Stop a service
  restart <service>        Restart a service (stop + start)
  status <service>         Show detailed service status
  shutdown [type]          Initiate shutdown (halt|poweroff|reboot)
  trigger <service>        Trigger a triggered service
  signal <sig> <service>   Send signal to service process
  reload <service>         Reload service configuration from disk
  boot-time                Show boot timing analysis
  catlog [--clear] <svc>   Show buffered service output
`)
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "slinitctl: "+format+"\n", args...)
	os.Exit(1)
}

func requireServiceArg(args []string, fn func(string) error) error {
	if len(args) < 1 {
		fatal("Service name required")
	}
	return fn(args[0])
}

func resolveSocketPath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	// If running as root, use system socket
	if os.Getuid() == 0 {
		return defaultSystemSocket
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

// loadServiceHandle sends LoadService and returns the handle.
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
	var parts []string
	if e.PID > 0 {
		parts = append(parts, "pid: "+strconv.Itoa(int(e.PID)))
	}
	if e.Flags&control.StatusFlagHasConsole != 0 {
		parts = append(parts, "has console")
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func cmdStart(conn net.Conn, name string) error {
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}

	if err := control.WritePacket(conn, control.CmdStartService, control.EncodeHandle(handle)); err != nil {
		return err
	}

	rply, _, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyACK:
		fmt.Printf("Service '%s' started.\n", name)
	case control.RplyAlreadySS:
		fmt.Printf("Service '%s' is already started.\n", name)
	case control.RplyShuttingDown:
		return fmt.Errorf("system is shutting down")
	default:
		return fmt.Errorf("unexpected reply: %d", rply)
	}
	return nil
}

func cmdStop(conn net.Conn, name string) error {
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}

	if err := control.WritePacket(conn, control.CmdStopService, control.EncodeHandle(handle)); err != nil {
		return err
	}

	rply, _, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyACK:
		fmt.Printf("Service '%s' stopped.\n", name)
	case control.RplyAlreadySS:
		fmt.Printf("Service '%s' is already stopped.\n", name)
	default:
		return fmt.Errorf("unexpected reply: %d", rply)
	}
	return nil
}

func cmdRestart(conn net.Conn, name string) error {
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}

	// Stop first
	if err := control.WritePacket(conn, control.CmdStopService, control.EncodeHandle(handle)); err != nil {
		return err
	}
	rply, _, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}
	if rply != control.RplyACK && rply != control.RplyAlreadySS {
		return fmt.Errorf("stop failed: reply %d", rply)
	}

	// Then start
	if err := control.WritePacket(conn, control.CmdStartService, control.EncodeHandle(handle)); err != nil {
		return err
	}
	rply, _, err = control.ReadPacket(conn)
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyACK:
		fmt.Printf("Service '%s' restarted.\n", name)
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

	rply, payload, err := control.ReadPacket(conn)
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

	fmt.Printf("Service: %s\n", name)
	fmt.Printf("  State:   %s\n", formatState(status.State))
	fmt.Printf("  Target:  %s\n", formatTarget(status.TargetState))
	fmt.Printf("  Type:    %s\n", status.SvcType)
	if status.Flags&control.StatusFlagHasPID != 0 {
		fmt.Printf("  PID:     %d\n", status.PID)
	}
	if status.ExitStatus != 0 {
		fmt.Printf("  Exit:    %d\n", status.ExitStatus)
	}
	return nil
}

func cmdShutdown(conn net.Conn, shutType string) error {
	var st service.ShutdownType
	switch shutType {
	case "halt":
		st = service.ShutdownHalt
	case "poweroff":
		st = service.ShutdownPoweroff
	case "reboot":
		st = service.ShutdownReboot
	default:
		return fmt.Errorf("unknown shutdown type: %s (use halt, poweroff, or reboot)", shutType)
	}

	payload := []byte{uint8(st)}
	if err := control.WritePacket(conn, control.CmdShutdown, payload); err != nil {
		return err
	}

	rply, _, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}

	if rply == control.RplyACK {
		fmt.Printf("Shutdown (%s) initiated.\n", shutType)
	} else {
		return fmt.Errorf("shutdown failed: reply %d", rply)
	}
	return nil
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

	rply, _, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyACK:
		fmt.Printf("Service '%s' triggered.\n", name)
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

	rply, _, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyACK:
		fmt.Printf("Signal %s sent to service '%s'.\n", sigStr, svcName)
	case control.RplySignalNoPID:
		return fmt.Errorf("service '%s' has no running process", svcName)
	case control.RplySignalErr:
		return fmt.Errorf("failed to send signal to service '%s'", svcName)
	default:
		return fmt.Errorf("unexpected reply: %d", rply)
	}
	return nil
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
				suffix = fmt.Sprintf(" (pid: %d)", entry.PID)
			}
			fmt.Printf("  %8s %s%s\n", formatDuration(dur), entry.Name, suffix)
		}
	}

	return nil
}

func formatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%dus", d.Microseconds())
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.3fs", d.Seconds())
}

func cmdReload(conn net.Conn, name string) error {
	handle, err := loadServiceHandle(conn, name)
	if err != nil {
		return err
	}

	if err := control.WritePacket(conn, control.CmdReloadService, control.EncodeHandle(handle)); err != nil {
		return err
	}

	rply, _, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyACK:
		fmt.Printf("Service '%s' reloaded.\n", name)
	case control.RplyNAK:
		return fmt.Errorf("could not reload service '%s'; service may be in wrong state or have incompatible changes", name)
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

	rply, rplyPayload, err := control.ReadPacket(conn)
	if err != nil {
		return err
	}

	switch rply {
	case control.RplyNAK:
		return fmt.Errorf("service '%s' is not configured to buffer output (log-type != buffer)", name)
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

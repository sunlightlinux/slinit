// slinitctl is the control CLI for the slinit service manager.
// It communicates with a running slinit instance via a Unix domain socket.
package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"

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

	fmt.Printf("%-30s %-12s %-10s %-8s %s\n", "SERVICE", "STATE", "TARGET", "TYPE", "PID")
	fmt.Println(strings.Repeat("-", 72))

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

		pidStr := "-"
		if entry.PID > 0 {
			pidStr = strconv.Itoa(int(entry.PID))
		}

		stateStr := formatState(entry.State)
		targetStr := formatTarget(entry.TargetState)

		fmt.Printf("%-30s %-12s %-10s %-8s %s\n",
			entry.Name, stateStr, targetStr, entry.SvcType, pidStr)
	}
	return nil
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

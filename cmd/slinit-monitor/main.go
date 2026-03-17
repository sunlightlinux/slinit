// slinit-monitor — watches slinit service events and optionally executes
// commands on state changes. Connects to a running slinit instance and
// subscribes to push notifications.
//
// Usage:
//
//	slinit-monitor [options] -c COMMAND service-name [...]
//	slinit-monitor [options] -E [-c COMMAND] [var-name ...]
package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/control"
)

const (
	defaultSystemSocket = "/run/slinit.socket"
	defaultUserSocket   = ".slinitctl"
)

type config struct {
	socketPath string
	systemMode bool
	userMode   bool
	envMode    bool
	initial    bool
	exitFirst  bool
	command    string

	// Status text customization
	strStarted string
	strStopped string
	strFailed  string
	strSet     string
	strUnset   string

	// Positional args: service names or env var names
	names []string
}

func main() {
	cfg := parseArgs()

	sockPath := resolveSocketPath(cfg.socketPath, cfg.systemMode, cfg.userMode)
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		fatal("connect: %v", err)
	}
	defer conn.Close()

	// Version handshake
	if err := versionHandshake(conn); err != nil {
		fatal("%v", err)
	}

	if cfg.envMode {
		runEnvMonitor(conn, cfg)
	} else {
		runServiceMonitor(conn, cfg)
	}
}

func runServiceMonitor(conn net.Conn, cfg config) {
	if len(cfg.names) == 0 {
		fatal("no service names specified")
	}
	if cfg.command == "" {
		fatal("-c/--command is required for service monitoring")
	}

	// Load services and map handles to names
	handles := make(map[uint32]string)
	for _, name := range cfg.names {
		handle, state, err := loadService(conn, name)
		if err != nil {
			fatal("loading service %q: %v", name, err)
		}
		handles[handle] = name

		// If --initial, fire command for current state
		if cfg.initial {
			status := stateToText(state, cfg)
			executeCommand(cfg.command, name, status, "")
		}
	}

	// Main event loop
	for {
		pktType, payload, err := control.ReadPacket(conn)
		if err != nil {
			fatal("read: %v", err)
		}

		switch pktType {
		case control.InfoServiceEvent5:
			h, evt, _, err := control.DecodeServiceEvent5(payload)
			if err != nil {
				continue
			}
			name, ok := handles[h]
			if !ok {
				continue
			}
			status := eventToText(evt, cfg)
			executeCommand(cfg.command, name, status, "")
			if cfg.exitFirst {
				return
			}

		case control.InfoServiceEvent:
			h, evt, _, err := control.DecodeServiceEvent(payload)
			if err != nil {
				continue
			}
			name, ok := handles[h]
			if !ok {
				continue
			}
			// Skip v4 if we already processed v5 for same event
			// In practice v5 always comes first, but be safe
			_ = name
			_ = evt
			continue

		case control.InfoEnvEvent:
			// Ignore env events in service mode
			continue
		}
	}
}

func runEnvMonitor(conn net.Conn, cfg config) {
	if cfg.command == "" {
		fatal("-c/--command is required for environment monitoring")
	}

	// Subscribe to env events
	if err := control.WritePacket(conn, control.CmdListenEnv, nil); err != nil {
		fatal("listen-env: %v", err)
	}
	rply, _, err := readMonitorReply(conn)
	if err != nil {
		fatal("listen-env reply: %v", err)
	}
	if rply != control.RplyACK {
		fatal("listen-env: unexpected reply %d", rply)
	}

	// If --initial, get current env and fire commands
	if cfg.initial {
		if err := control.WritePacket(conn, control.CmdGetAllEnv, control.EncodeHandle(0)); err != nil {
			fatal("getallenv: %v", err)
		}
		rply, payload, err := readMonitorReply(conn)
		if err != nil {
			fatal("getallenv reply: %v", err)
		}
		if rply == control.RplyEnvList {
			env, _ := control.DecodeEnvList(payload)
			for k, v := range env {
				if cfg.shouldMonitorVar(k) {
					executeCommand(cfg.command, k, cfg.strSet, v)
				}
			}
		}
	}

	// Main event loop
	for {
		pktType, payload, err := control.ReadPacket(conn)
		if err != nil {
			fatal("read: %v", err)
		}

		if pktType != control.InfoEnvEvent {
			continue
		}

		_, varStr, err := control.DecodeEnvEvent(payload)
		if err != nil {
			continue
		}

		var name, value, status string
		if idx := strings.IndexByte(varStr, '='); idx >= 0 {
			name = varStr[:idx]
			value = varStr[idx+1:]
			status = cfg.strSet
		} else {
			name = varStr
			status = cfg.strUnset
		}

		if !cfg.shouldMonitorVar(name) {
			continue
		}

		executeCommand(cfg.command, name, status, value)
		if cfg.exitFirst {
			return
		}
	}
}

func (c config) shouldMonitorVar(name string) bool {
	if len(c.names) == 0 {
		return true // monitor all
	}
	for _, n := range c.names {
		if n == name {
			return true
		}
	}
	return false
}

func loadService(conn net.Conn, name string) (handle uint32, state uint8, err error) {
	payload := control.EncodeServiceName(name)
	if err = control.WritePacket(conn, control.CmdLoadService, payload); err != nil {
		return
	}

	rply, data, err := readMonitorReply(conn)
	if err != nil {
		return
	}
	if rply == control.RplyNoService {
		err = fmt.Errorf("service not found: %s", name)
		return
	}
	if rply != control.RplyServiceRecord {
		err = fmt.Errorf("unexpected reply: %d", rply)
		return
	}

	if len(data) < 5 {
		err = fmt.Errorf("reply too short")
		return
	}
	state = data[0]
	handle = binary.LittleEndian.Uint32(data[1:5])
	return
}

// readMonitorReply reads packets, skipping unsolicited info/event packets.
func readMonitorReply(conn net.Conn) (uint8, []byte, error) {
	for {
		rply, payload, err := control.ReadPacket(conn)
		if err != nil {
			return 0, nil, err
		}
		switch rply {
		case control.InfoServiceEvent, control.InfoServiceEvent5, control.InfoEnvEvent:
			continue
		default:
			return rply, payload, nil
		}
	}
}

func stateToText(state uint8, cfg config) string {
	switch state {
	case 2: // StateStarted
		return cfg.strStarted
	case 0: // StateStopped
		return cfg.strStopped
	default:
		return cfg.strStopped
	}
}

func eventToText(evt uint8, cfg config) string {
	switch evt {
	case control.SvcEventStarted:
		return cfg.strStarted
	case control.SvcEventStopped:
		return cfg.strStopped
	case control.SvcEventFailedStart:
		return cfg.strFailed
	case control.SvcEventStartCancelled:
		return cfg.strStopped
	case control.SvcEventStopCancelled:
		return cfg.strStarted
	default:
		return fmt.Sprintf("unknown(%d)", evt)
	}
}

func executeCommand(cmdTemplate, name, status, value string) {
	// Perform substitutions
	cmd := cmdTemplate
	cmd = strings.ReplaceAll(cmd, "%%", "\x00") // preserve %%
	cmd = strings.ReplaceAll(cmd, "%n", name)
	cmd = strings.ReplaceAll(cmd, "%s", status)
	cmd = strings.ReplaceAll(cmd, "%v", value)
	cmd = strings.ReplaceAll(cmd, "\x00", "%") // restore %%

	// Split command
	parts := splitCommand(cmd)
	if len(parts) == 0 {
		return
	}

	c := exec.Command(parts[0], parts[1:]...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-monitor: command failed: %v\n", err)
	}
}

// splitCommand splits a command string on whitespace, respecting quoted strings.
func splitCommand(s string) []string {
	var parts []string
	var current strings.Builder
	inQuote := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '"':
			inQuote = !inQuote
		case ch == ' ' || ch == '\t':
			if inQuote {
				current.WriteByte(ch)
			} else if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

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
		serverMin := binary.LittleEndian.Uint16(payload[0:])
		serverActual := binary.LittleEndian.Uint16(payload[2:])
		if serverActual < control.MinCompatVersion {
			return fmt.Errorf("server protocol version %d is too old", serverActual)
		}
		if control.CPVersion < serverMin {
			return fmt.Errorf("client protocol version %d is too old for server", control.CPVersion)
		}
	} else if len(payload) >= 2 {
		serverVer := binary.LittleEndian.Uint16(payload)
		if serverVer < control.MinCompatVersion {
			return fmt.Errorf("server protocol version %d is too old", serverVer)
		}
	}
	return nil
}

func resolveSocketPath(flagValue string, systemMode, userMode bool) string {
	if flagValue != "" {
		return flagValue
	}
	if systemMode {
		return defaultSystemSocket
	}
	if userMode {
		home, err := os.UserHomeDir()
		if err != nil {
			return defaultUserSocket
		}
		return home + "/" + defaultUserSocket
	}
	if os.Getuid() == 0 {
		return defaultSystemSocket
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return defaultUserSocket
	}
	return home + "/" + defaultUserSocket
}

func parseArgs() config {
	cfg := config{
		strStarted: "started",
		strStopped: "stopped",
		strFailed:  "failed",
		strSet:     "set",
		strUnset:   "unset",
	}

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c", "--command":
			if i+1 >= len(args) {
				fatal("missing argument for %s", args[i])
			}
			i++
			cfg.command = args[i]
		case "-s", "--system":
			cfg.systemMode = true
		case "-u", "--user":
			cfg.userMode = true
		case "-p", "--socket-path":
			if i+1 >= len(args) {
				fatal("missing argument for %s", args[i])
			}
			i++
			cfg.socketPath = args[i]
		case "-E", "--env":
			cfg.envMode = true
		case "-i", "--initial":
			cfg.initial = true
		case "-e", "--exit":
			cfg.exitFirst = true
		case "--str-started":
			if i+1 >= len(args) {
				fatal("missing argument for %s", args[i])
			}
			i++
			cfg.strStarted = args[i]
		case "--str-stopped":
			if i+1 >= len(args) {
				fatal("missing argument for %s", args[i])
			}
			i++
			cfg.strStopped = args[i]
		case "--str-failed":
			if i+1 >= len(args) {
				fatal("missing argument for %s", args[i])
			}
			i++
			cfg.strFailed = args[i]
		case "--str-set":
			if i+1 >= len(args) {
				fatal("missing argument for %s", args[i])
			}
			i++
			cfg.strSet = args[i]
		case "--str-unset":
			if i+1 >= len(args) {
				fatal("missing argument for %s", args[i])
			}
			i++
			cfg.strUnset = args[i]
		case "--help", "-h":
			printUsage()
			os.Exit(0)
		default:
			if strings.HasPrefix(args[i], "-") {
				fatal("unknown option: %s", args[i])
			}
			cfg.names = append(cfg.names, args[i])
		}
	}

	return cfg
}

func printUsage() {
	//nolint:lll
	os.Stdout.WriteString(`Usage:
  slinit-monitor [options] -c COMMAND service-name [...]
  slinit-monitor [options] -E -c COMMAND [var-name ...]

Monitors slinit service events or environment changes and executes
a command on each change.

Command substitutions:
  %n  service or variable name
  %s  status text (started/stopped/failed or set/unset)
  %v  variable value (env mode only)
  %%  literal percent sign

Options:
  -c, --command COMMAND      Command to execute on state change
  -E, --env                  Monitor environment changes instead of services
  -i, --initial              Execute command for initial state
  -e, --exit                 Exit after first command execution
  -s, --system               Monitor system instance
  -u, --user                 Monitor user instance
  -p, --socket-path PATH     Explicit socket path
  --str-started TEXT          Custom text for started (default: started)
  --str-stopped TEXT          Custom text for stopped (default: stopped)
  --str-failed TEXT           Custom text for failed (default: failed)
  --str-set TEXT              Custom text for set (default: set)
  --str-unset TEXT            Custom text for unset (default: unset)
  -h, --help                 Show this help message
`)
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "slinit-monitor: "+format+"\n", args...)
	os.Exit(1)
}

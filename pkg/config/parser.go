package config

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/IonutNechita/slinit/pkg/service"
)

// ServiceDescription holds the parsed configuration of a service.
type ServiceDescription struct {
	Name string
	Type service.ServiceType

	// Commands
	Command     []string
	StopCommand []string
	WorkingDir  string
	EnvFile     string

	// Dependencies (by name, resolved by the loader)
	DependsOn  []string // depends-on (REGULAR)
	DependsMS  []string // depends-ms (MILESTONE)
	WaitsFor   []string // waits-for (WAITS_FOR)
	Before     []string // before
	After      []string // after

	// Dependency directories
	DependsOnD []string // depends-on.d
	DependsMSD []string // depends-ms.d
	WaitsForD  []string // waits-for.d

	// Behavior
	AutoRestart    service.AutoRestartMode
	SmoothRecovery bool
	Flags          service.ServiceFlags

	// Logging
	LogType      service.LogType
	LogFile      string
	LogBufMax    int

	// Process management
	StopTimeout       time.Duration
	StartTimeout      time.Duration
	RestartDelay      time.Duration
	RestartInterval   time.Duration
	RestartLimitCount int
	TermSignal        syscall.Signal
	PIDFile           string
	ReadyNotification string

	// Credentials
	RunAs string

	// Socket activation
	SocketPath  string
	SocketPerms int

	// Chaining
	ChainTo string

	// Consumer
	ConsumerOf string

	// Description
	Description string
}

// NewServiceDescription creates a ServiceDescription with default values.
func NewServiceDescription(name string) *ServiceDescription {
	return &ServiceDescription{
		Name:        name,
		Type:        service.TypeProcess,
		TermSignal:  syscall.SIGTERM,
		StopTimeout: 10 * time.Second,
		AutoRestart: service.RestartNever,
		SocketPerms: 0600,
	}
}

// ParseError represents an error during service description parsing.
type ParseError struct {
	ServiceName string
	FileName    string
	Line        int
	Setting     string
	Message     string
}

func (e *ParseError) Error() string {
	if e.Line > 0 {
		if e.Setting != "" {
			return fmt.Sprintf("%s:%d: setting '%s': %s (service: %s)", e.FileName, e.Line, e.Setting, e.Message, e.ServiceName)
		}
		return fmt.Sprintf("%s:%d: %s (service: %s)", e.FileName, e.Line, e.Message, e.ServiceName)
	}
	return fmt.Sprintf("service '%s': %s", e.ServiceName, e.Message)
}

// Parse reads a dinit-compatible service description file.
//
// Format:
//   - Lines starting with '#' are comments
//   - Empty lines are ignored
//   - Settings use "key = value" or "key: value" format
//   - Dependency settings use ':' operator
//   - Value settings use '=' operator
func Parse(r io.Reader, name string, fileName string) (*ServiceDescription, error) {
	desc := NewServiceDescription(name)
	scanner := bufio.NewScanner(r)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Skip empty lines and comments
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Parse setting
		setting, value, op, err := parseLine(trimmed)
		if err != nil {
			return nil, &ParseError{
				ServiceName: name,
				FileName:    fileName,
				Line:        lineNum,
				Message:     err.Error(),
			}
		}

		if !IsKnownSetting(setting) {
			return nil, &ParseError{
				ServiceName: name,
				FileName:    fileName,
				Line:        lineNum,
				Setting:     setting,
				Message:     "unknown setting",
			}
		}

		if !ValidOperator(setting, op) {
			expectedOp := "="
			if KnownSettings[setting]&OpColon != 0 {
				expectedOp = ":"
			}
			return nil, &ParseError{
				ServiceName: name,
				FileName:    fileName,
				Line:        lineNum,
				Setting:     setting,
				Message:     fmt.Sprintf("invalid operator, expected '%s'", expectedOp),
			}
		}

		if err := applySetting(desc, setting, value, op); err != nil {
			return nil, &ParseError{
				ServiceName: name,
				FileName:    fileName,
				Line:        lineNum,
				Setting:     setting,
				Message:     err.Error(),
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading service description for %s: %w", name, err)
	}

	return desc, nil
}

// parseLine splits a config line into setting, value, and operator.
func parseLine(line string) (setting string, value string, op OperatorType, err error) {
	// Try += first
	if idx := strings.Index(line, "+="); idx >= 0 {
		setting = strings.TrimSpace(line[:idx])
		value = strings.TrimSpace(line[idx+2:])
		op = OpPlusEqual
		return
	}

	// Try = (but not after :)
	eqIdx := strings.IndexByte(line, '=')
	colonIdx := strings.IndexByte(line, ':')

	if colonIdx >= 0 && (eqIdx < 0 || colonIdx < eqIdx) {
		// Colon comes first
		setting = strings.TrimSpace(line[:colonIdx])
		value = strings.TrimSpace(line[colonIdx+1:])
		op = OpColon
		return
	}

	if eqIdx >= 0 {
		setting = strings.TrimSpace(line[:eqIdx])
		value = strings.TrimSpace(line[eqIdx+1:])
		op = OpEquals
		return
	}

	err = fmt.Errorf("missing operator ('=' or ':')")
	return
}

// applySetting applies a parsed setting to the service description.
func applySetting(desc *ServiceDescription, setting, value string, op OperatorType) error {
	switch setting {
	case "type":
		return applyType(desc, value)
	case "description":
		desc.Description = value
	case "command":
		desc.Command = splitCommand(value)
	case "stop-command":
		desc.StopCommand = splitCommand(value)
	case "working-dir":
		desc.WorkingDir = value
	case "env-file":
		desc.EnvFile = value

	// Dependencies
	case "depends-on":
		desc.DependsOn = append(desc.DependsOn, value)
	case "depends-ms":
		desc.DependsMS = append(desc.DependsMS, value)
	case "waits-for":
		desc.WaitsFor = append(desc.WaitsFor, value)
	case "before":
		desc.Before = append(desc.Before, value)
	case "after":
		desc.After = append(desc.After, value)
	case "depends-on.d":
		desc.DependsOnD = append(desc.DependsOnD, value)
	case "depends-ms.d":
		desc.DependsMSD = append(desc.DependsMSD, value)
	case "waits-for.d":
		desc.WaitsForD = append(desc.WaitsForD, value)

	// Restart
	case "restart":
		return applyRestart(desc, value)
	case "smooth-recovery":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		desc.SmoothRecovery = b

	// Timeouts
	case "stop-timeout":
		d, err := parseDuration(value)
		if err != nil {
			return err
		}
		desc.StopTimeout = d
	case "start-timeout":
		d, err := parseDuration(value)
		if err != nil {
			return err
		}
		desc.StartTimeout = d
	case "restart-delay":
		d, err := parseDuration(value)
		if err != nil {
			return err
		}
		desc.RestartDelay = d
	case "restart-limit-interval":
		d, err := parseDuration(value)
		if err != nil {
			return err
		}
		desc.RestartInterval = d
	case "restart-limit-count":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid count: %w", err)
		}
		desc.RestartLimitCount = n

	// Signal
	case "term-signal":
		sig, err := parseSignal(value)
		if err != nil {
			return err
		}
		desc.TermSignal = sig

	// Logging
	case "logfile":
		desc.LogFile = value
		if desc.LogType == service.LogNone {
			desc.LogType = service.LogFile
		}
	case "log-type":
		return applyLogType(desc, value)
	case "log-buffer-size":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid buffer size: %w", err)
		}
		desc.LogBufMax = n

	// Process management
	case "pid-file":
		desc.PIDFile = value
	case "ready-notification":
		desc.ReadyNotification = value
	case "run-as":
		desc.RunAs = value

	// Socket
	case "socket-listen":
		desc.SocketPath = value
	case "socket-permissions":
		perms, err := strconv.ParseInt(value, 8, 32)
		if err != nil {
			return fmt.Errorf("invalid socket permissions: %w", err)
		}
		desc.SocketPerms = int(perms)

	// Chaining
	case "chain-to":
		desc.ChainTo = value

	// Consumer
	case "consumer-of":
		desc.ConsumerOf = value

	// Options
	case "options":
		return applyOptions(desc, value, op == OpPlusEqual)

	// These settings are recognized but not yet implemented (Phase 2+)
	case "load-options", "socket-uid", "socket-gid",
		"rlimit-nofile", "rlimit-core", "rlimit-data", "rlimit-as",
		"cgroup", "nice", "ioprio", "oom-score-adj":
		// Silently accept for forward compatibility
	}

	return nil
}

func applyType(desc *ServiceDescription, value string) error {
	switch strings.ToLower(value) {
	case "process":
		desc.Type = service.TypeProcess
	case "bgprocess":
		desc.Type = service.TypeBGProcess
	case "scripted":
		desc.Type = service.TypeScripted
	case "internal":
		desc.Type = service.TypeInternal
	case "triggered":
		desc.Type = service.TypeTriggered
	default:
		return fmt.Errorf("unknown service type: %s", value)
	}
	return nil
}

func applyRestart(desc *ServiceDescription, value string) error {
	switch strings.ToLower(value) {
	case "yes", "true":
		desc.AutoRestart = service.RestartAlways
	case "no", "false":
		desc.AutoRestart = service.RestartNever
	case "on-failure":
		desc.AutoRestart = service.RestartOnFailure
	default:
		return fmt.Errorf("invalid restart value: %s (expected yes/no/on-failure)", value)
	}
	return nil
}

func applyLogType(desc *ServiceDescription, value string) error {
	switch strings.ToLower(value) {
	case "none":
		desc.LogType = service.LogNone
	case "file":
		desc.LogType = service.LogFile
	case "buffer":
		desc.LogType = service.LogBuffer
	case "pipe":
		desc.LogType = service.LogPipe
	default:
		return fmt.Errorf("unknown log type: %s", value)
	}
	return nil
}

func applyOptions(desc *ServiceDescription, value string, append bool) error {
	if !append {
		desc.Flags = service.ServiceFlags{}
	}
	for _, opt := range strings.Fields(value) {
		switch opt {
		case "runs-on-console":
			desc.Flags.RunsOnConsole = true
		case "starts-on-console":
			desc.Flags.StartsOnConsole = true
		case "shares-console":
			desc.Flags.SharesConsole = true
		case "pass-cs-fd":
			desc.Flags.PassCSFD = true
		case "start-interruptible":
			desc.Flags.StartInterruptible = true
		case "skippable":
			desc.Flags.Skippable = true
		case "signal-process-only":
			desc.Flags.SignalProcessOnly = true
		case "always-chain":
			desc.Flags.AlwaysChain = true
		case "kill-all-on-stop":
			desc.Flags.KillAllOnStop = true
		default:
			return fmt.Errorf("unknown option: %s", opt)
		}
	}
	return nil
}

// splitCommand splits a command string into parts, respecting quotes.
func splitCommand(cmd string) []string {
	var parts []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)
	escaped := false

	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]

		if escaped {
			current.WriteByte(ch)
			escaped = false
			continue
		}

		if ch == '\\' {
			escaped = true
			continue
		}

		if inQuote {
			if ch == quoteChar {
				inQuote = false
			} else {
				current.WriteByte(ch)
			}
			continue
		}

		if ch == '"' || ch == '\'' {
			inQuote = true
			quoteChar = ch
			continue
		}

		if ch == ' ' || ch == '\t' {
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
			continue
		}

		current.WriteByte(ch)
	}

	if current.Len() > 0 {
		parts = append(parts, current.String())
	}

	return parts
}

// parseBool parses a boolean value (yes/true/no/false).
func parseBool(value string) (bool, error) {
	switch strings.ToLower(value) {
	case "yes", "true", "1":
		return true, nil
	case "no", "false", "0":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean value: %s (expected yes/no/true/false)", value)
	}
}

// parseDuration parses a duration value in seconds (as a decimal number).
func parseDuration(value string) (time.Duration, error) {
	f, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid duration: %w", err)
	}
	if f < 0 {
		return 0, fmt.Errorf("duration must be non-negative")
	}
	return time.Duration(f * float64(time.Second)), nil
}

// parseSignal parses a signal name or number.
func parseSignal(value string) (syscall.Signal, error) {
	signals := map[string]syscall.Signal{
		"SIGHUP":  syscall.SIGHUP,
		"SIGINT":  syscall.SIGINT,
		"SIGQUIT": syscall.SIGQUIT,
		"SIGKILL": syscall.SIGKILL,
		"SIGTERM": syscall.SIGTERM,
		"SIGUSR1": syscall.SIGUSR1,
		"SIGUSR2": syscall.SIGUSR2,
		"SIGSTOP": syscall.SIGSTOP,
		"SIGCONT": syscall.SIGCONT,
		"HUP":     syscall.SIGHUP,
		"INT":     syscall.SIGINT,
		"QUIT":    syscall.SIGQUIT,
		"KILL":    syscall.SIGKILL,
		"TERM":    syscall.SIGTERM,
		"USR1":    syscall.SIGUSR1,
		"USR2":    syscall.SIGUSR2,
		"STOP":    syscall.SIGSTOP,
		"CONT":    syscall.SIGCONT,
	}

	upper := strings.ToUpper(value)
	if sig, ok := signals[upper]; ok {
		return sig, nil
	}

	// Try numeric
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("unknown signal: %s", value)
	}
	return syscall.Signal(n), nil
}

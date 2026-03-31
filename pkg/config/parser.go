package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// maxIncludeDepth limits the nesting depth of @include directives to prevent
// infinite recursion from circular includes.
const maxIncludeDepth = 10

// ServiceDescription holds the parsed configuration of a service.
type ServiceDescription struct {
	Name string
	Type service.ServiceType

	// Commands
	Command           []string
	StopCommand       []string
	FinishCommand     []string      // runs after process exits (before restart)
	ReadyCheckCommand []string      // polls to verify service readiness
	ReadyCheckInterval time.Duration // polling interval for ready-check (default 1s)
	PreStopHook       []string      // runs before SIGTERM in BringDown
	WorkingDir        string
	EnvFile           string
	EnvDir            string // runit-style: directory with one file per env var

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
	LogFilePerms int
	LogFileUID   int
	LogFileGID   int
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
	ReadyNotifyFD     int    // parsed from pipefd:N (-1 if unset)
	ReadyNotifyVar    string // parsed from pipevar:VARNAME

	// Credentials
	RunAs string

	// Socket activation
	SocketPath  string
	SocketPerms int
	SocketUID   int
	SocketGID   int

	// Chaining
	ChainTo string

	// Alias
	Provides string

	// Enable-via: default "from" service for enable/disable commands
	EnableVia string

	// Consumer
	ConsumerOf string

	// Description
	Description string

	// Process attributes
	Nice        *int   // -20..19
	OOMScoreAdj *int   // -1000..1000
	NoNewPrivs  bool
	IOPrio      string // "class:level" e.g. "be:4", "idle"
	CgroupPath  string // run-in-cgroup path
	CPUAffinity []uint // CPU numbers to pin to

	// Resource limits (soft:hard or just value for both)
	RlimitNofile *[2]uint64
	RlimitCore   *[2]uint64
	RlimitData   *[2]uint64
	RlimitAs     *[2]uint64

	// Capabilities and securebits
	Capabilities string // comma/space-separated capability names
	Securebits   string // space-separated securebits flag names

	// UTMP/WTMP
	InittabID   string // inittab-id for utmpx
	InittabLine string // inittab-line for utmpx

	// Load options
	ExportPasswdVars  bool // export USER, LOGNAME, HOME, SHELL, UID, GID from passwd
	ExportServiceName bool // export DINIT_SERVICENAME + DINIT_SERVICEDSCDIR env vars
}

// NewServiceDescription creates a ServiceDescription with default values.
func NewServiceDescription(name string) *ServiceDescription {
	return &ServiceDescription{
		Name:          name,
		Type:          service.TypeProcess,
		TermSignal:    syscall.SIGTERM,
		StopTimeout:   10 * time.Second,
		AutoRestart:   service.RestartNever,
		LogFilePerms:  0600,
		LogFileUID:    -1,
		LogFileGID:    -1,
		SocketPerms:   0600,
		SocketUID:     -1,
		SocketGID:     -1,
		ReadyNotifyFD: -1,
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
	return parseImpl(r, name, fileName, desc, 0, nil)
}

// ParseWithArg parses a service description with a service argument ($1 substitution).
// Used for service templates where name@argument loads the base service
// file and substitutes $1/${1} with the argument value.
func ParseWithArg(r io.Reader, name string, fileName string, serviceArg string) (*ServiceDescription, error) {
	desc := NewServiceDescription(name)
	return parseImpl(r, name, fileName, desc, 0, &serviceArg)
}

func parseImpl(r io.Reader, name string, fileName string, desc *ServiceDescription, depth int, serviceArg *string) (*ServiceDescription, error) {
	if depth > maxIncludeDepth {
		return nil, &ParseError{
			ServiceName: name,
			FileName:    fileName,
			Message:     fmt.Sprintf("include nesting depth exceeds %d", maxIncludeDepth),
		}
	}

	scanner := bufio.NewScanner(r)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Skip empty lines and comments
		// Fast-path: most config lines have no leading whitespace
		trimmed := line
		if len(line) == 0 {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			trimmed = strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
		}
		if trimmed[0] == '#' {
			continue
		}

		// Handle @include and @include-opt directives
		if strings.HasPrefix(trimmed, "@") {
			if err := handleInclude(trimmed, name, fileName, lineNum, desc, depth, serviceArg); err != nil {
				return nil, err
			}
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

		if err := applySetting(desc, setting, value, op, serviceArg); err != nil {
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

// handleInclude processes @include and @include-opt directives.
func handleInclude(line, name, fileName string, lineNum int, desc *ServiceDescription, depth int, serviceArg *string) error {
	var optional bool
	var incPath string

	switch {
	case strings.HasPrefix(line, "@meta "), line == "@meta":
		// @meta directives: most are metadata for external tools; some are
		// meaningful to the daemon (e.g. enable-via).
		if strings.HasPrefix(line, "@meta enable-via ") {
			desc.EnableVia = strings.TrimSpace(line[len("@meta enable-via "):])
		}
		return nil
	case strings.HasPrefix(line, "@include-opt "):
		optional = true
		incPath = strings.TrimSpace(line[len("@include-opt "):])
	case strings.HasPrefix(line, "@include "):
		incPath = strings.TrimSpace(line[len("@include "):])
	default:
		return &ParseError{
			ServiceName: name,
			FileName:    fileName,
			Line:        lineNum,
			Message:     fmt.Sprintf("unknown directive: %s", line),
		}
	}

	if incPath == "" {
		return &ParseError{
			ServiceName: name,
			FileName:    fileName,
			Line:        lineNum,
			Message:     "include path is empty",
		}
	}

	// Perform environment variable substitution on the path
	incPath = os.ExpandEnv(incPath)

	// Resolve relative paths against the directory of the current file
	if !filepath.IsAbs(incPath) {
		dir := filepath.Dir(fileName)
		incPath = filepath.Join(dir, incPath)
	}

	f, err := os.Open(incPath)
	if err != nil {
		if optional && os.IsNotExist(err) {
			return nil
		}
		return &ParseError{
			ServiceName: name,
			FileName:    fileName,
			Line:        lineNum,
			Message:     fmt.Sprintf("cannot open include %q: %v", incPath, err),
		}
	}
	defer f.Close()

	_, err = parseImpl(f, name, incPath, desc, depth+1, serviceArg)
	return err
}

// parseLine splits a config line into setting, value, and operator.
func parseLine(line string) (setting string, value string, op OperatorType, err error) {
	// Find = and : positions in a single scan approach
	eqIdx := strings.IndexByte(line, '=')
	colonIdx := strings.IndexByte(line, ':')

	// Check for += (eqIdx > 0 and previous char is '+')
	if eqIdx > 0 && line[eqIdx-1] == '+' {
		setting = strings.TrimSpace(line[:eqIdx-1])
		value = strings.TrimSpace(line[eqIdx+1:])
		op = OpPlusEqual
		return
	}

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
func applySetting(desc *ServiceDescription, setting, value string, op OperatorType, serviceArg *string) error {
	switch setting {
	case "type":
		return applyType(desc, value)
	case "description":
		desc.Description = value
	case "command":
		if op == OpPlusEqual {
			desc.Command = append(desc.Command, splitCommand(expandEnvVarsForCommand(value, serviceArg))...)
		} else {
			desc.Command = splitCommand(expandEnvVarsForCommand(value, serviceArg))
		}
	case "stop-command":
		if op == OpPlusEqual {
			desc.StopCommand = append(desc.StopCommand, splitCommand(expandEnvVarsForCommand(value, serviceArg))...)
		} else {
			desc.StopCommand = splitCommand(expandEnvVarsForCommand(value, serviceArg))
		}
	case "finish-command":
		if op == OpPlusEqual {
			desc.FinishCommand = append(desc.FinishCommand, splitCommand(expandEnvVarsForCommand(value, serviceArg))...)
		} else {
			desc.FinishCommand = splitCommand(expandEnvVarsForCommand(value, serviceArg))
		}
	case "ready-check-command":
		if op == OpPlusEqual {
			desc.ReadyCheckCommand = append(desc.ReadyCheckCommand, splitCommand(expandEnvVarsForCommand(value, serviceArg))...)
		} else {
			desc.ReadyCheckCommand = splitCommand(expandEnvVarsForCommand(value, serviceArg))
		}
	case "ready-check-interval":
		d, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("invalid ready-check-interval: %w", err)
		}
		desc.ReadyCheckInterval = d
	case "working-dir":
		desc.WorkingDir = expandEnvVars(value, serviceArg)
	case "env-file":
		desc.EnvFile = expandEnvVars(value, serviceArg)
	case "env-dir":
		desc.EnvDir = expandEnvVars(value, serviceArg)
	case "pre-stop-hook":
		if op == OpPlusEqual {
			desc.PreStopHook = append(desc.PreStopHook, splitCommand(expandEnvVarsForCommand(value, serviceArg))...)
		} else {
			desc.PreStopHook = splitCommand(expandEnvVarsForCommand(value, serviceArg))
		}

	// Dependencies
	case "depends-on":
		depName := expandEnvVars(value, serviceArg)
		if err := ValidateServiceName(depName); err != nil {
			return fmt.Errorf("invalid dependency name: %w", err)
		}
		desc.DependsOn = append(desc.DependsOn, depName)
	case "depends-ms":
		depName := expandEnvVars(value, serviceArg)
		if err := ValidateServiceName(depName); err != nil {
			return fmt.Errorf("invalid dependency name: %w", err)
		}
		desc.DependsMS = append(desc.DependsMS, depName)
	case "waits-for":
		depName := expandEnvVars(value, serviceArg)
		if err := ValidateServiceName(depName); err != nil {
			return fmt.Errorf("invalid dependency name: %w", err)
		}
		desc.WaitsFor = append(desc.WaitsFor, depName)
	case "before":
		depName := expandEnvVars(value, serviceArg)
		if err := ValidateServiceName(depName); err != nil {
			return fmt.Errorf("invalid dependency name: %w", err)
		}
		desc.Before = append(desc.Before, depName)
	case "after":
		depName := expandEnvVars(value, serviceArg)
		if err := ValidateServiceName(depName); err != nil {
			return fmt.Errorf("invalid dependency name: %w", err)
		}
		desc.After = append(desc.After, depName)
	case "depends-on.d":
		desc.DependsOnD = append(desc.DependsOnD, expandEnvVars(value, serviceArg))
	case "depends-ms.d":
		desc.DependsMSD = append(desc.DependsMSD, expandEnvVars(value, serviceArg))
	case "waits-for.d":
		desc.WaitsForD = append(desc.WaitsForD, expandEnvVars(value, serviceArg))

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
	case "term-signal", "termsignal":
		sig, err := parseSignal(value)
		if err != nil {
			return err
		}
		desc.TermSignal = sig

	// Logging
	case "logfile":
		desc.LogFile = expandEnvVars(value, serviceArg)
		if desc.LogType == service.LogNone {
			desc.LogType = service.LogToFile
		}
	case "log-type":
		return applyLogType(desc, value)
	case "log-buffer-size":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid buffer size: %w", err)
		}
		desc.LogBufMax = n
	case "logfile-permissions":
		perms, err := strconv.ParseInt(value, 8, 32)
		if err != nil {
			return fmt.Errorf("invalid logfile permissions: %w", err)
		}
		desc.LogFilePerms = int(perms)
	case "logfile-uid":
		uid, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid logfile uid: %w", err)
		}
		desc.LogFileUID = uid
	case "logfile-gid":
		gid, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid logfile gid: %w", err)
		}
		desc.LogFileGID = gid

	// Process management
	case "pid-file":
		desc.PIDFile = expandEnvVars(value, serviceArg)
	case "ready-notification":
		desc.ReadyNotification = value
		if err := parseReadyNotification(desc, value); err != nil {
			return err
		}
	case "run-as":
		desc.RunAs = value

	// Socket
	case "socket-listen":
		desc.SocketPath = expandEnvVars(value, serviceArg)
	case "socket-permissions":
		perms, err := strconv.ParseInt(value, 8, 32)
		if err != nil {
			return fmt.Errorf("invalid socket permissions: %w", err)
		}
		desc.SocketPerms = int(perms)
	case "socket-uid":
		uid, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid socket uid: %w", err)
		}
		desc.SocketUID = uid
	case "socket-gid":
		gid, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid socket gid: %w", err)
		}
		desc.SocketGID = gid

	// Chaining
	case "chain-to":
		chainName := expandEnvVars(value, serviceArg)
		if err := ValidateServiceName(chainName); err != nil {
			return fmt.Errorf("invalid chain-to name: %w", err)
		}
		desc.ChainTo = chainName

	// Alias
	case "provides":
		desc.Provides = value

	// Consumer
	case "consumer-of":
		consName := expandEnvVars(value, serviceArg)
		if err := ValidateServiceName(consName); err != nil {
			return fmt.Errorf("invalid consumer-of name: %w", err)
		}
		desc.ConsumerOf = consName

	// Options
	case "options":
		return applyOptions(desc, value, op == OpPlusEqual)

	// Process attributes
	case "nice":
		n, err := strconv.Atoi(value)
		if err != nil || n < -20 || n > 19 {
			return fmt.Errorf("invalid nice value: %s (expected -20..19)", value)
		}
		desc.Nice = &n

	case "oom-score-adj":
		n, err := strconv.Atoi(value)
		if err != nil || n < -1000 || n > 1000 {
			return fmt.Errorf("invalid oom-score-adj: %s (expected -1000..1000)", value)
		}
		desc.OOMScoreAdj = &n

	case "ioprio":
		desc.IOPrio = value

	case "cgroup", "run-in-cgroup":
		desc.CgroupPath = value

	case "cpu-affinity":
		cpus, err := ParseCPUAffinity(value)
		if err != nil {
			return fmt.Errorf("invalid cpu-affinity: %v", err)
		}
		desc.CPUAffinity = cpus

	case "rlimit-nofile":
		lim, err := parseRlimit(value)
		if err != nil {
			return fmt.Errorf("invalid rlimit-nofile: %v", err)
		}
		desc.RlimitNofile = lim

	case "rlimit-core":
		lim, err := parseRlimit(value)
		if err != nil {
			return fmt.Errorf("invalid rlimit-core: %v", err)
		}
		desc.RlimitCore = lim

	case "rlimit-data":
		lim, err := parseRlimit(value)
		if err != nil {
			return fmt.Errorf("invalid rlimit-data: %v", err)
		}
		desc.RlimitData = lim

	case "rlimit-as", "rlimit-addrspace":
		lim, err := parseRlimit(value)
		if err != nil {
			return fmt.Errorf("invalid rlimit-as: %v", err)
		}
		desc.RlimitAs = lim

	case "capabilities":
		desc.Capabilities = value

	case "securebits":
		desc.Securebits = value

	case "inittab-id":
		desc.InittabID = value
	case "inittab-line":
		desc.InittabLine = value

	case "load-options":
		for _, opt := range strings.Fields(value) {
			switch opt {
			case "export-passwd-vars":
				desc.ExportPasswdVars = true
			case "export-service-name":
				desc.ExportServiceName = true
			case "sub-vars":
				// Always on in slinit, silently accept
			}
		}
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
		desc.LogType = service.LogToFile
	case "buffer":
		desc.LogType = service.LogToBuffer
	case "pipe":
		desc.LogType = service.LogToPipe
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
		case "unmask-intr":
			desc.Flags.UnmaskIntr = true
		case "starts-rwfs":
			desc.Flags.RWReady = true
		case "starts-log":
			desc.Flags.LogReady = true
		case "no-new-privs":
			desc.NoNewPrivs = true
		default:
			return fmt.Errorf("unknown option: %s", opt)
		}
	}
	return nil
}

// splitCommand splits a command string into parts, respecting quotes.
func splitCommand(cmd string) []string {
	// Fast-path: no quotes, escapes, or NUL separators → use strings.Fields
	if strings.IndexByte(cmd, '"') < 0 &&
		strings.IndexByte(cmd, '\'') < 0 &&
		strings.IndexByte(cmd, '\\') < 0 &&
		strings.IndexByte(cmd, wordSplitSep) < 0 {
		return strings.Fields(cmd)
	}

	// Slow path: handle quotes and escapes
	parts := make([]string, 0, 8)
	var current strings.Builder
	current.Grow(len(cmd) / 4) // estimate average arg length
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

		// NUL byte = word-split boundary from $/NAME expansion
		if ch == wordSplitSep {
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

// parseReadyNotification parses a ready-notification value.
// Supported formats: "pipefd:N" or "pipevar:VARNAME".
func parseReadyNotification(desc *ServiceDescription, value string) error {
	if strings.HasPrefix(value, "pipefd:") {
		fdStr := value[7:]
		fd, err := strconv.Atoi(fdStr)
		if err != nil || fd < 0 {
			return fmt.Errorf("invalid pipefd value: %s", fdStr)
		}
		desc.ReadyNotifyFD = fd
		return nil
	}
	if strings.HasPrefix(value, "pipevar:") {
		varName := value[8:]
		if varName == "" {
			return fmt.Errorf("empty pipevar variable name")
		}
		desc.ReadyNotifyVar = varName
		return nil
	}
	return fmt.Errorf("unrecognised ready-notification setting: %s (expected pipefd:N or pipevar:VARNAME)", value)
}

// parseRlimit parses an rlimit value. Formats: "N" (both soft and hard),
// "soft:hard", or "unlimited".
func parseRlimit(value string) (*[2]uint64, error) {
	const unlimited = ^uint64(0) // RLIM_INFINITY

	parseOne := func(s string) (uint64, error) {
		s = strings.TrimSpace(s)
		if strings.ToLower(s) == "unlimited" {
			return unlimited, nil
		}
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid rlimit value: %s", s)
		}
		return n, nil
	}

	if idx := strings.IndexByte(value, ':'); idx >= 0 {
		soft, err := parseOne(value[:idx])
		if err != nil {
			return nil, err
		}
		hard, err := parseOne(value[idx+1:])
		if err != nil {
			return nil, err
		}
		return &[2]uint64{soft, hard}, nil
	}
	v, err := parseOne(value)
	if err != nil {
		return nil, err
	}
	return &[2]uint64{v, v}, nil
}

// wordSplitSep is a NUL byte used as internal marker for word-split
// boundaries introduced by the $/NAME expansion syntax.
const wordSplitSep = '\x00'

// expandEnvVarsForCommand expands environment variables with word-splitting
// support. The $/NAME and $/{NAME} syntax splits the expanded value on
// whitespace, inserting NUL byte markers at word boundaries. The caller
// (splitCommand) treats NUL as a word-split boundary.
func expandEnvVarsForCommand(s string, serviceArg *string) string {
	return expandEnvVarsImpl(s, true, serviceArg)
}

// expandEnvVars expands environment variable references in a string.
// Supported syntax: $VAR, ${VAR}, $1/${1} (service arg), and $$ (literal dollar sign).
// Unset variables expand to an empty string.
func expandEnvVars(s string, serviceArg *string) string {
	return expandEnvVarsImpl(s, false, serviceArg)
}

func expandEnvVarsImpl(s string, allowWordSplit bool, serviceArg *string) string {
	// Fast path: no dollar signs means no expansion needed
	if strings.IndexByte(s, '$') < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))

	i := 0
	for i < len(s) {
		if s[i] != '$' {
			b.WriteByte(s[i])
			i++
			continue
		}

		// We have a '$'
		i++ // skip '$'
		if i >= len(s) {
			// Trailing '$' — keep it literal
			b.WriteByte('$')
			break
		}

		// $$ → literal '$'
		if s[i] == '$' {
			b.WriteByte('$')
			i++
			continue
		}

		// $1 — service argument substitution (only $1 is valid, not $2+)
		// Note: $1 is always treated as a service argument, even if followed
		// by alphanumeric chars (unlike env vars which are greedy).
		if s[i] == '1' {
			i++ // skip '1'
			if serviceArg == nil {
				// $1 without argument: silently expand to empty
				continue
			}
			b.WriteString(*serviceArg)
			continue
		}

		// $/NAME or $/{NAME} — word-splitting expansion
		wsplit := allowWordSplit && s[i] == '/'
		if wsplit {
			i++ // skip '/'
			if i >= len(s) {
				b.WriteString("$/")
				break
			}
			// $/1 — word-split service argument
			if s[i] == '1' && (i+1 >= len(s) || !isVarChar(s[i+1], false)) {
				i++ // skip '1'
				if serviceArg != nil {
					writeWordSplit(&b, *serviceArg)
				}
				continue
			}
		}

		// ${VAR}, ${VAR:-default}, ${VAR:+alt}, ${VAR-default}, ${VAR+alt},
		// $/{VAR}, $/{1} syntax
		if s[i] == '{' {
			i++ // skip '{'
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				// No closing brace — keep literal
				if wsplit {
					b.WriteString("$/{")
				} else {
					b.WriteString("${")
				}
				continue
			}
			expr := s[i : i+end]
			i += end + 1 // skip past '}'

			// Resolve variable or service argument ($1)
			var resolved string
			if colonIdx := strings.IndexByte(expr, ':'); colonIdx >= 0 && colonIdx+1 < len(expr) {
				// Colon variants: ${VAR:-default}, ${VAR:+alt}
				// Check unset OR empty
				varName := expr[:colonIdx]
				op := expr[colonIdx+1]
				operand := expr[colonIdx+2:]
				var val string
				var set bool
				if varName == "1" {
					if serviceArg != nil {
						val = *serviceArg
						set = true
					}
				} else {
					val, set = os.LookupEnv(varName)
				}
				switch op {
				case '-': // ${VAR:-default} — use default if unset or empty
					if !set || val == "" {
						resolved = operand
					} else {
						resolved = val
					}
				case '+': // ${VAR:+alt} — use alt if set and non-empty
					if set && val != "" {
						resolved = operand
					}
				default:
					// Unknown operator, treat as plain var name with colon
					if varName == "1" && serviceArg != nil {
						resolved = *serviceArg
					} else {
						resolved = os.Getenv(expr)
					}
				}
			} else if varName, op, operand, ok := parseNonColonOp(expr); ok {
				// Non-colon variants: ${VAR-default}, ${VAR+alt}
				// Check unset only (empty value is considered "set")
				var val string
				var set bool
				if varName == "1" {
					if serviceArg != nil {
						val = *serviceArg
						set = true
					}
				} else {
					val, set = os.LookupEnv(varName)
				}
				switch op {
				case '-': // ${VAR-default} — use default only if unset
					if !set {
						resolved = operand
					} else {
						resolved = val
					}
				case '+': // ${VAR+alt} — use alt if set (even if empty)
					if set {
						resolved = operand
					}
				}
			} else if expr == "1" {
				// ${1} — service argument
				if serviceArg != nil {
					resolved = *serviceArg
				}
			} else {
				resolved = os.Getenv(expr)
			}

			if wsplit {
				writeWordSplit(&b, resolved)
			} else {
				b.WriteString(resolved)
			}
			continue
		}

		// $VAR or $/VAR syntax: variable name is [A-Za-z_][A-Za-z0-9_]*
		start := i
		for i < len(s) && isVarChar(s[i], i == start) {
			i++
		}
		if i == start {
			// '$' followed by non-variable char — keep literal '$'
			b.WriteByte('$')
			if wsplit {
				b.WriteByte('/')
			}
			continue
		}
		name := s[start:i]
		resolved := os.Getenv(name)
		if wsplit {
			writeWordSplit(&b, resolved)
		} else {
			b.WriteString(resolved)
		}
	}

	return b.String()
}

// writeWordSplit writes a word-split expanded value to the builder.
// Whitespace in the value is replaced with NUL byte markers that splitCommand
// interprets as forced word boundaries (even mid-token).
func writeWordSplit(b *strings.Builder, val string) {
	inWS := true // start in whitespace state to trim leading whitespace
	for _, ch := range val {
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
			if !inWS {
				b.WriteByte(wordSplitSep)
				inWS = true
			}
		} else {
			b.WriteRune(ch)
			inWS = false
		}
	}
	// trailing whitespace is naturally trimmed (no trailing NUL)
}

// isVarChar returns true if ch is valid in an environment variable name.
// The first character must be a letter or underscore; subsequent chars
// may also be digits.
func isVarChar(ch byte, first bool) bool {
	if ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch == '_' {
		return true
	}
	if !first && ch >= '0' && ch <= '9' {
		return true
	}
	return false
}

// parseNonColonOp checks if expr contains a non-colon operator: ${VAR-default}
// or ${VAR+alt}. Returns the variable name, operator byte, operand, and true
// if found. The variable name must be a valid identifier (or "1" for service arg).
func parseNonColonOp(expr string) (varName string, op byte, operand string, ok bool) {
	for j := 0; j < len(expr); j++ {
		if expr[j] == '-' || expr[j] == '+' {
			name := expr[:j]
			if name == "" {
				return "", 0, "", false
			}
			// Validate that name is a valid variable name or "1"
			if name != "1" {
				for k, ch := range []byte(name) {
					if !isVarChar(ch, k == 0) {
						return "", 0, "", false
					}
				}
			}
			return name, expr[j], expr[j+1:], true
		}
		// Stop scanning if we hit a char that can't be part of a var name
		// (and isn't the operator itself)
		if expr[j] != '_' &&
			!(expr[j] >= 'A' && expr[j] <= 'Z') &&
			!(expr[j] >= 'a' && expr[j] <= 'z') &&
			!(j > 0 && expr[j] >= '0' && expr[j] <= '9') &&
			expr[j] != '1' {
			return "", 0, "", false
		}
	}
	return "", 0, "", false
}

// signalNames maps signal names (uppercase) to their syscall values.
// Package-level to avoid re-allocating on every parseSignal call.
var signalNames = map[string]syscall.Signal{
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

// parseSignal parses a signal name or number.
func parseSignal(value string) (syscall.Signal, error) {
	if strings.EqualFold(value, "none") {
		return 0, nil
	}

	if sig, ok := signalNames[strings.ToUpper(value)]; ok {
		return sig, nil
	}

	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("unknown signal: %s", value)
	}
	return syscall.Signal(n), nil
}

// ParseCPUAffinity parses a CPU affinity spec like "0 1 2 3", "0-3",
// "0,2,4", or "0-3 8-11" into a list of CPU numbers.
func ParseCPUAffinity(value string) ([]uint, error) {
	var cpus []uint
	seen := map[uint]bool{}

	// Split on spaces and commas in a single pass (avoids ReplaceAll allocation)
	tokens := strings.FieldsFunc(value, func(r rune) bool {
		return r == ' ' || r == ',' || r == '\t'
	})

	for _, tok := range tokens {
		if idx := strings.Index(tok, "-"); idx > 0 && idx < len(tok)-1 {
			// Range: "0-3"
			lo, err := strconv.ParseUint(tok[:idx], 10, 32)
			if err != nil {
				return nil, fmt.Errorf("invalid CPU number %q", tok[:idx])
			}
			hi, err := strconv.ParseUint(tok[idx+1:], 10, 32)
			if err != nil {
				return nil, fmt.Errorf("invalid CPU number %q", tok[idx+1:])
			}
			if lo > hi {
				return nil, fmt.Errorf("invalid range %s (start > end)", tok)
			}
			for c := lo; c <= hi; c++ {
				if !seen[uint(c)] {
					cpus = append(cpus, uint(c))
					seen[uint(c)] = true
				}
			}
		} else {
			// Single CPU number
			c, err := strconv.ParseUint(tok, 10, 32)
			if err != nil {
				return nil, fmt.Errorf("invalid CPU number %q", tok)
			}
			if !seen[uint(c)] {
				cpus = append(cpus, uint(c))
				seen[uint(c)] = true
			}
		}
	}

	if len(cpus) == 0 {
		return nil, fmt.Errorf("empty CPU list")
	}
	return cpus, nil
}

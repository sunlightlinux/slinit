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

	"github.com/sunlightlinux/slinit/pkg/process"
	"github.com/sunlightlinux/slinit/pkg/seccomp"
	"github.com/sunlightlinux/slinit/pkg/service"
	"golang.org/x/sys/unix"
)

// maxIncludeDepth limits the nesting depth of @include directives to prevent
// infinite recursion from circular includes.
const maxIncludeDepth = 10

// IDMapping represents a user/group ID mapping for user namespaces.
// Format: ContainerID:HostID:Size (e.g., "0:1000:65536").
type IDMapping struct {
	ContainerID int
	HostID      int
	Size        int
}

// ParseIDMapping parses a "container:host:size" string into an IDMapping.
func ParseIDMapping(s string) (IDMapping, error) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 {
		return IDMapping{}, fmt.Errorf("invalid id mapping %q: expected container:host:size", s)
	}
	cid, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return IDMapping{}, fmt.Errorf("invalid container id in %q: %w", s, err)
	}
	hid, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return IDMapping{}, fmt.Errorf("invalid host id in %q: %w", s, err)
	}
	size, err := strconv.Atoi(strings.TrimSpace(parts[2]))
	if err != nil {
		return IDMapping{}, fmt.Errorf("invalid size in %q: %w", s, err)
	}
	if cid < 0 || hid < 0 || size <= 0 {
		return IDMapping{}, fmt.Errorf("invalid id mapping %q: ids must be >= 0, size must be > 0", s)
	}
	return IDMapping{ContainerID: cid, HostID: hid, Size: size}, nil
}

// CgroupSetting is a cgroup v2 controller knob: the filename within the
// cgroup directory and the value to write. For example, {"memory.max", "512M"}.
type CgroupSetting struct {
	File  string
	Value string
}

// ServiceDescription holds the parsed configuration of a service.
type ServiceDescription struct {
	Name string
	Type service.ServiceType

	// Commands
	Command              []string
	ScriptBlock          bool // command came from a script...end script block
	StopCommand          []string
	FinishCommand        []string            // runs after process exits (before restart)
	ReadyCheckCommand    []string            // polls to verify service readiness
	ReadyCheckInterval   time.Duration       // polling interval for ready-check (default 1s)
	PreStopHook          []string            // runs before SIGTERM in BringDown
	ControlCommands      map[string][]string // signal→custom command (runit control/)
	ExtraCommands        map[string][]string // custom actions (available in any state)
	ExtraStartedCommands map[string][]string // custom actions (only when STARTED)
	WorkingDir           string
	EnvFile              string
	EnvDir               string // runit-style: directory with one file per env var
	Chroot               string // chroot directory before exec
	LockFile             string // exclusive flock file path
	NewSession           bool   // setsid() before exec
	CloseStdin           bool   // close fd 0
	CloseStdout          bool   // close fd 1
	CloseStderr          bool   // close fd 2

	// Namespace isolation (Linux clone flags)
	NamespacePID    bool // CLONE_NEWPID
	NamespaceMount  bool // CLONE_NEWNS
	NamespaceNet    bool // CLONE_NEWNET
	NamespaceUTS    bool // CLONE_NEWUTS
	NamespaceIPC    bool // CLONE_NEWIPC
	NamespaceUser   bool // CLONE_NEWUSER
	NamespaceCgroup bool // CLONE_NEWCGROUP

	// User namespace UID/GID mappings (container:host:size format)
	NamespaceUidMap []IDMapping
	NamespaceGidMap []IDMapping

	// Dependencies (by name, resolved by the loader)
	DependsOn  []string // depends-on (REGULAR)
	DependsMS  []string // depends-ms (MILESTONE)
	WaitsFor   []string // waits-for (WAITS_FOR)
	PreparedBy []string // prepared-by (PREPARED_BY)
	Before     []string // before
	After      []string // after

	// Dependency directories
	DependsOnD  []string // depends-on.d
	DependsMSD  []string // depends-ms.d
	WaitsForD   []string // waits-for.d
	PreparedByD []string // prepared-by.d

	// Behavior
	AutoRestart    service.AutoRestartMode
	SmoothRecovery bool
	ManualStart    bool // upstart-style "manual" — blocks auto-activation
	// upstart-style "normal exit": exit codes / signals that count as
	// success and suppress respawn even with restart=yes. Empty means
	// "use the built-in defaults" (code 0 + admin signals like SIGTERM
	// for restart=on-failure; nothing extra for restart=yes).
	NormalExitCodes   []int
	NormalExitSignals []syscall.Signal
	Flags             service.ServiceFlags

	// Logging
	LogType       service.LogType
	LogFile       string
	LogFilePerms  int
	LogFileUID    int
	LogFileGID    int
	LogBufMax     int
	LogMaxSize    int64         // max logfile size before rotation (bytes)
	LogMaxFiles   int           // max number of rotated log files to keep
	LogRotateTime time.Duration // rotate logfile at this interval
	LogProcessor  []string      // command to run on rotated logfile
	LogInclude    []string      // include only lines matching these patterns
	LogExclude    []string      // exclude lines matching these patterns
	OutputLogger  []string      // OpenRC OUTPUT_LOGGER: pipe stdout to external command
	ErrorLogger   []string      // OpenRC ERROR_LOGGER: pipe stderr to external command

	// Process management
	StopTimeout       time.Duration
	StartTimeout      time.Duration
	RestartDelay      time.Duration
	RestartDelayStep  time.Duration // additive backoff increment per failed restart
	RestartDelayCap   time.Duration // max capped delay for progressive backoff
	RestartInterval   time.Duration
	RestartLimitCount int
	TermSignal        syscall.Signal
	ReloadSignal      syscall.Signal // upstart-inspired; 0 = unset
	PIDFile           string
	ReadyNotification string
	ReadyNotifyFD     int           // parsed from pipefd:N (-1 if unset)
	ReadyNotifyVar    string        // parsed from pipevar:VARNAME
	WatchdogTimeout   time.Duration // 0 = disabled; piggybacks on ready-notification pipe

	// Credentials
	RunAs string

	// Socket activation
	SocketPath       string   // primary socket path (first socket-listen)
	SocketPaths      []string // all socket-listen paths (for multiple sockets)
	SocketPerms      int
	SocketUID        int
	SocketGID        int
	SocketActivation string // "immediate" (default) or "on-demand"

	// Chaining
	ChainTo string

	// Alias
	Provides string

	// Enable-via: default "from" service for enable/disable commands
	EnableVia string

	// Consumer
	ConsumerOf   string
	SharedLogger string // shared-logger: multiple producers → single logger service

	// Description
	Description string
	// Upstart-style metadata stanzas (informational only).
	Author  string
	Version string
	Usage   string

	// Process attributes
	Nice        *int    // -20..19
	OOMScoreAdj *int    // -1000..1000
	Umask       *uint32 // file-creation mask, octal 000..777

	// AppArmor confinement. AppArmorLoad is an absolute path to a
	// profile parsed before start; AppArmorSwitch is a profile name the
	// process transitions into on exec. Either may be empty.
	AppArmorLoad   string
	AppArmorSwitch string

	// Debug, when true, makes the child raise SIGSTOP before exec so a
	// developer can `gdb -p` it and then `kill -CONT` to proceed.
	Debug bool

	// systemd-style auto-managed service directories (relative names,
	// resolved by the loader against /run, /var/lib, /var/cache,
	// /var/log, /etc). Modes default to 0755 when the *Mode field is
	// nil. RuntimeDirPreserve: 0=no (remove on stop), 1=yes (never
	// remove), 2=restart (keep across restart, remove on full stop).
	RuntimeDirs, StateDirs, CacheDirs, LogsDirs, ConfigDirs                []string
	RuntimeDirMode, StateDirMode, CacheDirMode, LogsDirMode, ConfigDirMode *uint32
	RuntimeDirPreserve                                                     int

	// Path-based activation. StartOnPath is empty when no trigger is
	// configured; otherwise StartOnPathTrigger is 1..4 corresponding to
	// pathwatch.Trigger{Exists,Changed,Modified,DirNotEmpty}. The four
	// stanzas are mutually exclusive — setting any clears the others.
	StartOnPath        string
	StartOnPathTrigger int
	NoNewPrivs         bool
	IOPrio             string          // "class:level" e.g. "be:4", "idle"
	CgroupPath         string          // run-in-cgroup path
	CgroupSettings     []CgroupSetting // cgroup v2 controller knobs
	CPUAffinity        []uint          // CPU numbers to pin to

	// Real-time scheduling
	SchedPolicy         uint32 // unix.SCHED_* (0 = unset / SCHED_NORMAL)
	SchedPolicySet      bool   // distinguishes "explicit SCHED_NORMAL" from unset
	SchedPriority       uint32 // 1..99 for FIFO/RR
	SchedRuntime        uint64 // nanoseconds, SCHED_DEADLINE
	SchedDeadline       uint64 // nanoseconds, SCHED_DEADLINE
	SchedPeriod         uint64 // nanoseconds, SCHED_DEADLINE
	SchedResetOnFork    bool   // SCHED_FLAG_RESET_ON_FORK (default true)
	SchedResetOnForkSet bool   // tracks whether the user gave an explicit value

	// Memory locking and NUMA — applied via the slinit-runner exec helper.
	MlockallFlags    int    // mlockall(2) bitmask (MCL_CURRENT | MCL_FUTURE | MCL_ONFAULT)
	NumaMempolicy    uint32 // unix.MPOL_*
	NumaMempolicySet bool   // distinguishes explicit MPOL_DEFAULT from unset
	NumaNodes        []uint // node list for BIND/INTERLEAVE/PREFERRED

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

	// Virtual TTY
	VTTYEnabled    bool // run attached to a PTY (screen-like)
	VTTYScrollback int  // scrollback buffer size (default 64KB)

	// Cron-like periodic tasks
	CronCommand  []string      // command to run periodically while STARTED
	CronInterval time.Duration // interval between runs (default 60s)
	CronDelay    time.Duration // initial delay before first run
	CronOnError  string        // "continue" (default) or "stop"

	// Continuous health checking (post-STARTED, OpenRC supervise-daemon inspired)
	HealthCheckCommand  []string      // command to run periodically (exit 0 = healthy)
	HealthCheckInterval time.Duration // interval between checks (default 30s)
	HealthCheckDelay    time.Duration // initial delay before first check
	HealthCheckMaxFail  int           // consecutive failures before restart (0 = never)
	UnhealthyCommand    []string      // command to run on each failure

	// Load options
	ExportPasswdVars  bool // export USER, LOGNAME, HOME, SHELL, UID, GID from passwd
	ExportServiceName bool // export DINIT_SERVICENAME + DINIT_SERVICEDSCDIR env vars

	// Platform keywords: services with "-docker", "-lxc", etc. are skipped
	// on matching platforms (OpenRC-compatible keyword directive)
	Keywords []string

	// Pre-start path checks (OpenRC-inspired fail-fast):
	// the service refuses to start if any required path is missing.
	RequiredFiles []string // files that must exist and be readable
	RequiredDirs  []string // directories that must exist

	// systemd-style start predicates. condition-* failures skip the
	// service silently (start is treated as successful, no process
	// runs); assert-* failures fail the start and cascade like any
	// other failed start. Negation with leading "!".
	Predicates []service.Predicate

	// systemd-style failure-action / success-action: a system-level
	// transition (reboot/poweroff/halt/exit) triggered when the service
	// reaches STOPPED in a failure (start failed, non-zero exit, etc.)
	// or clean-finish state respectively. RebootArgument is forwarded
	// to reboot(2) for kexec-style transitions.
	FailureAction  service.SystemAction
	SuccessAction  service.SystemAction
	RebootArgument string

	// RuntimeMaxSec is a hard cap on how long the service may stay in
	// STARTED. Zero means no cap. When the timer fires the service is
	// asked to stop via the same path an operator stop uses.
	RuntimeMaxSec time.Duration

	// OOMPolicy controls how slinit reacts when the service's cgroup v2
	// reports an OOM kill. Continue lets the kernel proceed unattended;
	// Stop asks the service to stop cleanly; Kill SIGKILLs the whole
	// cgroup. Off by default.
	OOMPolicy service.OOMPolicy

	// Credentials are file/inline secrets exposed to the service via a
	// fresh tmpfs at /run/credentials/<svc>/ pointed to by
	// $CREDENTIALS_DIRECTORY. load-credential = NAME:PATH copies from a
	// file; set-credential = NAME:VALUE writes the literal.
	Credentials []process.CredentialSource

	// systemd-style filesystem sandbox. Any non-zero value implies a
	// private mount namespace (CLONE_NEWNS) — the loader OR's the flag
	// into Cloneflags automatically. Applied child-side by slinit-runner.
	//
	// PrivateTmp: replace /tmp and /var/tmp with per-service tmpfs.
	// ProtectSystem: "" (off), "yes" (ro /usr,/boot,/efi),
	//   "full" (yes + /etc), "strict" (whole / ro except
	//   ReadWritePaths and /dev,/proc,/sys,/tmp,/var/tmp,/run).
	// ReadOnlyPaths/ReadWritePaths: explicit per-path overrides applied
	//   after ProtectSystem. Order matters: ReadWritePaths override
	//   ProtectSystem ro; ReadOnlyPaths add further ro on top.
	PrivateTmp     bool
	ProtectSystem  string
	ReadOnlyPaths  []string
	ReadWritePaths []string

	// Sandbox expansion (#3b).
	//
	// ProtectHome: "" (off), "yes" (make /home,/root,/run/user
	//   inaccessible), "read-only" (ro remount), "tmpfs" (empty tmpfs
	//   over each).
	// InaccessiblePaths: absolute paths over-mounted with an empty,
	//   restrictively-permissioned tmpfs (or /dev/null for files).
	// ProtectProc: "" (off / default), "noaccess" | "invisible" |
	//   "ptraceable" — passed as hidepid= when remounting /proc.
	// ProcSubset: "" (off / "all"), "pid" — remount /proc with
	//   subset=pid so the service sees only PID directories.
	// BindPaths / BindReadOnlyPaths: entries "src" or "src:dst" (dst
	//   defaults to src). Repeatable with '+='.
	// TemporaryFileSystem: entries "path" or "path:options" — tmpfs
	//   mounted at path; the optional comma-separated options string
	//   is forwarded to mount(2) verbatim.
	ProtectHome         string
	InaccessiblePaths   []string
	ProtectProc         string
	ProcSubset          string
	BindPaths           []string
	BindReadOnlyPaths   []string
	TemporaryFileSystem []string

	// systemd-style seccomp-bpf filter (#4). The parser validates and
	// expands @group tokens at load time; the runner compiles the
	// resolved list into BPF and installs it before exec. Setting any
	// of these auto-implies PR_SET_NO_NEW_PRIVS in the child so the
	// seccomp install succeeds without CAP_SYS_ADMIN.
	//
	// SystemCallFilter: raw items (names, @group, leading '~' on first
	//   entry to flip into deny mode). The parser preserves them as
	//   given so '+=' across multiple lines composes naturally.
	// SystemCallArchitectures: list of canonical arch names (native,
	//   x86-64, x86, arm64, arm). Defaults to the running arch.
	// SystemCallErrorNumber: action for non-allowed syscalls. Empty
	//   means KILL; otherwise "log" / "trap" / errno name / number.
	// SystemCallLog: syscall names / @groups that always trigger
	//   SECCOMP_RET_LOG regardless of the main filter mode.
	SystemCallFilter        []string
	SystemCallArchitectures []string
	SystemCallErrorNumber   string
	SystemCallLog           []string

	// systemd-style Restrict*/Protect* hardening cluster (#7 v1). Each
	// is a bool that expands at runner-side to a small fixed deny
	// syscall list and/or a mount op. The arg-checking variants
	// (RestrictRealtime, RestrictSUIDSGID, MemoryDenyWriteExecute,
	// RestrictNamespaces, RestrictAddressFamilies) need a BPF compiler
	// extension and are tracked as a v2 follow-on.
	ProtectKernelTunables bool
	ProtectKernelModules  bool
	ProtectKernelLogs     bool
	ProtectClock          bool
	ProtectControlGroups  bool
	ProtectHostname       bool
	LockPersonality       bool
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
		// Default sched-reset-on-fork=yes is intentional: an RT
		// service that fork()s a shell or build script must NOT pass
		// FIFO priority to that child, or a runaway child can starve
		// the scheduler. The user can override by setting
		// sched-reset-on-fork=no explicitly.
		SchedResetOnFork: true,
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

// ParseOverlay parses an overlay file and merges its settings into an existing
// ServiceDescription. Overlays reuse the full parser (including +=, @include,
// depends-on, and every known directive), so scalar settings in the overlay
// replace those from the main file, while += directives append.
//
// Typical use: ops-friendly overrides under /etc/slinit.conf.d/<service>
// that adjust env, arguments, or dependencies without touching the service
// file shipped by the distribution.
func ParseOverlay(r io.Reader, name string, fileName string, desc *ServiceDescription, serviceArg *string) error {
	if desc == nil {
		return fmt.Errorf("ParseOverlay: desc must not be nil")
	}
	_, err := parseImpl(r, name, fileName, desc, 0, serviceArg)
	return err
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

		// Handle "keyword -docker -lxc ..." (no operator required, OpenRC compat)
		// Only match the bare form (no '=' or ':' present), otherwise let
		// parseLine handle "keyword = ..." via the normal applySetting path.
		if strings.HasPrefix(trimmed, "keyword ") &&
			!strings.ContainsAny(trimmed, "=:") {
			for _, kw := range strings.Fields(trimmed[8:]) {
				desc.Keywords = append(desc.Keywords, kw)
			}
			continue
		}

		// Handle upstart-style "script ... end script" inline shell.
		// A bare `script` line opens a block; following lines are taken
		// verbatim until a bare `end script` line, then wrapped as
		// `/bin/sh -c <body>`. It is pure sugar over the `command`
		// setting, so the body undergoes the same load-time env/$1
		// substitution and the two are mutually exclusive.
		if trimmed == "script" {
			if len(desc.Command) > 0 || desc.ScriptBlock {
				return nil, &ParseError{
					ServiceName: name,
					FileName:    fileName,
					Line:        lineNum,
					Message:     "script block conflicts with command",
				}
			}
			var body []string
			closed := false
			for scanner.Scan() {
				lineNum++
				bl := scanner.Text()
				if strings.TrimSpace(bl) == "end script" {
					closed = true
					break
				}
				body = append(body, bl)
			}
			if !closed {
				return nil, &ParseError{
					ServiceName: name,
					FileName:    fileName,
					Line:        lineNum,
					Message:     "unterminated script block (missing 'end script')",
				}
			}
			script := expandEnvVarsForCommand(strings.Join(body, "\n"), serviceArg)
			desc.Command = []string{"/bin/sh", "-c", script}
			desc.ScriptBlock = true
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

// parseServiceDirNames splits a space-separated list of relative
// directory names for the *-directory settings, expanding $1/$VAR and
// rejecting absolute paths or '.'/'..' components (the loader prefixes
// a trusted base; a name must not escape it).
func parseServiceDirNames(setting, value string, serviceArg *string) ([]string, error) {
	var out []string
	for _, raw := range strings.Fields(value) {
		n := expandEnvVars(raw, serviceArg)
		if n == "" {
			continue
		}
		if strings.HasPrefix(n, "/") {
			return nil, fmt.Errorf("%s: name must be relative: %q", setting, n)
		}
		for _, comp := range strings.Split(n, "/") {
			if comp == "." || comp == ".." {
				return nil, fmt.Errorf("%s: '.'/'..' not allowed: %q", setting, n)
			}
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no directory name given", setting)
	}
	return out, nil
}

// parseSandboxPaths splits a space-separated list of absolute paths for
// the sandbox path settings (read-only-paths, read-write-paths and
// peers), expanding $1/$VAR. Each path must be absolute and free of '.'
// or '..' components — the runner applies them as bind mounts in the
// service's private mount namespace, so escapes via traversal must be
// rejected at parse time.
func parseSandboxPaths(setting, value string, serviceArg *string) ([]string, error) {
	var out []string
	for _, raw := range strings.Fields(value) {
		p := expandEnvVars(raw, serviceArg)
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("%s: path must be absolute: %q", setting, p)
		}
		// Refuse '..' on the raw path: filepath.Clean would collapse
		// "/etc/../root" → "/root" before we could reject the escape
		// attempt, silently widening the sandbox.
		for _, comp := range strings.Split(p, "/") {
			if comp == ".." {
				return nil, fmt.Errorf("%s: '..' not allowed: %q", setting, p)
			}
		}
		out = append(out, filepath.Clean(p))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no path given", setting)
	}
	return out, nil
}

// parseBindEntries splits a space-separated list of bind-mount entries
// of the form "src" or "src:dst" (where dst defaults to src). Both sides
// must be absolute and free of '..'. The runner consumes the joined
// "src:dst" form directly, so this helper normalises every entry into
// that shape.
func parseBindEntries(setting, value string, serviceArg *string) ([]string, error) {
	var out []string
	for _, raw := range strings.Fields(value) {
		entry := expandEnvVars(raw, serviceArg)
		if entry == "" {
			continue
		}
		src, dst, hasDst := strings.Cut(entry, ":")
		if !hasDst {
			dst = src
		}
		for _, p := range []string{src, dst} {
			if !filepath.IsAbs(p) {
				return nil, fmt.Errorf("%s: path must be absolute: %q", setting, entry)
			}
			for _, comp := range strings.Split(p, "/") {
				if comp == ".." {
					return nil, fmt.Errorf("%s: '..' not allowed: %q", setting, entry)
				}
			}
		}
		out = append(out, filepath.Clean(src)+":"+filepath.Clean(dst))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no entry given", setting)
	}
	return out, nil
}

// parseTmpfsEntries splits a space-separated list of "path" or
// "path:options" entries. options is forwarded verbatim to mount(2) so
// no parsing or validation happens here beyond rejecting an absent path.
func parseTmpfsEntries(setting, value string, serviceArg *string) ([]string, error) {
	var out []string
	for _, raw := range strings.Fields(value) {
		entry := expandEnvVars(raw, serviceArg)
		if entry == "" {
			continue
		}
		path, _, _ := strings.Cut(entry, ":")
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("%s: path must be absolute: %q", setting, entry)
		}
		for _, comp := range strings.Split(path, "/") {
			if comp == ".." {
				return nil, fmt.Errorf("%s: '..' not allowed: %q", setting, entry)
			}
		}
		out = append(out, entry)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no entry given", setting)
	}
	return out, nil
}

// validateSeccompItems checks that every system-call-filter /
// system-call-log entry is one of: a known syscall name on this arch,
// a recognised @group, or — only as the very first item across the
// merged list — the '~' deny-mode prefix. Errors here surface at parse
// time so a typo can never produce a silently-empty filter at boot.
func validateSeccompItems(setting string, items []string) error {
	for i, it := range items {
		it = strings.TrimSpace(it)
		if it == "" {
			continue
		}
		if strings.HasPrefix(it, "~") {
			if i != 0 {
				return fmt.Errorf("%s: '~' prefix only allowed on the first item, got %q", setting, it)
			}
			it = strings.TrimPrefix(it, "~")
			if it == "" {
				continue
			}
		}
		if strings.HasPrefix(it, "@") {
			if _, ok := seccomp.ExpandGroup(it); !ok {
				return fmt.Errorf("%s: unknown group %q", setting, it)
			}
			continue
		}
		if _, ok := seccomp.SyscallNumber(it); !ok {
			return fmt.Errorf("%s: unknown syscall %q on this arch", setting, it)
		}
	}
	return nil
}

// parsePredicate decodes a setting named "condition-XXX" or "assert-XXX"
// into a service.Predicate. Returns (predicate, true, nil) on a match,
// (zero, false, nil) when the setting does not start with one of the
// recognised prefixes, and (zero, true, err) on a malformed value.
//
// The "!" prefix on the value flips Negate; whitespace around it is
// stripped. Environment substitution applies to the parameter so a
// description can reference $1 or ${VAR}.
func parsePredicate(setting, value string, serviceArg *string) (service.Predicate, bool, error) {
	var (
		name     string
		isAssert bool
	)
	switch {
	case strings.HasPrefix(setting, "condition-"):
		name = setting[len("condition-"):]
	case strings.HasPrefix(setting, "assert-"):
		name = setting[len("assert-"):]
		isAssert = true
	default:
		return service.Predicate{}, false, nil
	}

	kind, ok := service.PredicateKindByName(name)
	if !ok {
		return service.Predicate{}, false, nil
	}

	param, negate := splitPredicateNegation(expandEnvVars(value, serviceArg))
	return service.Predicate{
		Kind:     kind,
		Param:    param,
		Negate:   negate,
		IsAssert: isAssert,
	}, true, nil
}

// splitNameValue parses a "NAME:VALUE" form used by load-credential
// and set-credential. Returns (name, value, true) on success and
// (empty, empty, false) when the colon is missing or the name is
// empty. Whitespace around the colon is trimmed; the value is taken
// verbatim from the first non-space character (so trailing whitespace
// in literals is preserved).
func splitNameValue(s string) (string, string, bool) {
	i := strings.IndexByte(s, ':')
	if i < 0 {
		return "", "", false
	}
	name := strings.TrimSpace(s[:i])
	if name == "" {
		return "", "", false
	}
	rest := s[i+1:]
	// Trim only leading whitespace; trailing whitespace may be part
	// of a credential literal the operator deliberately included.
	rest = strings.TrimLeft(rest, " \t")
	return name, rest, true
}

// splitPredicateNegation strips a single leading "!" from a predicate
// value. Whitespace around the bang and the value is trimmed so users
// can write either "! kvm" or "!kvm".
func splitPredicateNegation(value string) (string, bool) {
	v := strings.TrimSpace(value)
	if strings.HasPrefix(v, "!") {
		return strings.TrimSpace(v[1:]), true
	}
	return v, false
}

// parseDirMode parses an octal directory mode (000..777) for the
// *-directory-mode settings.
func parseDirMode(setting, value string) (*uint32, error) {
	m, err := strconv.ParseUint(value, 8, 32)
	if err != nil || m > 0o777 {
		return nil, fmt.Errorf("invalid %s: %s (expected octal 000..777)", setting, value)
	}
	u := uint32(m)
	return &u, nil
}

// applySetting applies a parsed setting to the service description.
func applySetting(desc *ServiceDescription, setting, value string, op OperatorType, serviceArg *string) error {
	switch setting {
	case "type":
		return applyType(desc, value)
	case "description":
		desc.Description = value
	case "author":
		desc.Author = value
	case "version":
		desc.Version = value
	case "usage":
		desc.Usage = value
	case "command":
		if desc.ScriptBlock {
			return fmt.Errorf("command conflicts with script block")
		}
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
	case "chroot":
		desc.Chroot = expandEnvVars(value, serviceArg)
	case "lock-file":
		desc.LockFile = expandEnvVars(value, serviceArg)
	case "new-session":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		desc.NewSession = b
	case "debug":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		desc.Debug = b
	case "namespace-pid":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		desc.NamespacePID = b
	case "namespace-mount":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		desc.NamespaceMount = b
	case "namespace-net":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		desc.NamespaceNet = b
	case "namespace-uts":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		desc.NamespaceUTS = b
	case "namespace-ipc":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		desc.NamespaceIPC = b
	case "namespace-user":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		desc.NamespaceUser = b
	case "namespace-cgroup":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		desc.NamespaceCgroup = b
	case "namespace-uid-map":
		m, err := ParseIDMapping(value)
		if err != nil {
			return err
		}
		if op == OpPlusEqual {
			desc.NamespaceUidMap = append(desc.NamespaceUidMap, m)
		} else {
			desc.NamespaceUidMap = []IDMapping{m}
		}
	case "namespace-gid-map":
		m, err := ParseIDMapping(value)
		if err != nil {
			return err
		}
		if op == OpPlusEqual {
			desc.NamespaceGidMap = append(desc.NamespaceGidMap, m)
		} else {
			desc.NamespaceGidMap = []IDMapping{m}
		}
	case "close-stdin":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		desc.CloseStdin = b
	case "close-stdout":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		desc.CloseStdout = b
	case "close-stderr":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		desc.CloseStderr = b

	// Virtual TTY
	case "vtty":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		desc.VTTYEnabled = b
	case "vtty-scrollback":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return fmt.Errorf("invalid vtty-scrollback: %s", value)
		}
		desc.VTTYScrollback = n

	// Cron-like periodic tasks
	case "cron-command":
		if op == OpPlusEqual {
			desc.CronCommand = append(desc.CronCommand, splitCommand(expandEnvVarsForCommand(value, serviceArg))...)
		} else {
			desc.CronCommand = splitCommand(expandEnvVarsForCommand(value, serviceArg))
		}
	case "cron-interval":
		d, err := time.ParseDuration(value)
		if err != nil {
			// Try as plain seconds
			secs, err2 := strconv.ParseFloat(value, 64)
			if err2 != nil {
				return fmt.Errorf("invalid cron-interval: %w", err)
			}
			d = time.Duration(secs * float64(time.Second))
		}
		desc.CronInterval = d
	case "cron-delay":
		d, err := time.ParseDuration(value)
		if err != nil {
			secs, err2 := strconv.ParseFloat(value, 64)
			if err2 != nil {
				return fmt.Errorf("invalid cron-delay: %w", err)
			}
			d = time.Duration(secs * float64(time.Second))
		}
		desc.CronDelay = d
	case "cron-on-error":
		switch value {
		case "continue", "stop":
			desc.CronOnError = value
		default:
			return fmt.Errorf("invalid cron-on-error: %q (must be 'continue' or 'stop')", value)
		}

	// Continuous health checking
	case "healthcheck-command":
		if op == OpPlusEqual {
			desc.HealthCheckCommand = append(desc.HealthCheckCommand, splitCommand(expandEnvVarsForCommand(value, serviceArg))...)
		} else {
			desc.HealthCheckCommand = splitCommand(expandEnvVarsForCommand(value, serviceArg))
		}
	case "healthcheck-interval":
		d, err := time.ParseDuration(value)
		if err != nil {
			secs, err2 := strconv.ParseFloat(value, 64)
			if err2 != nil {
				return fmt.Errorf("invalid healthcheck-interval: %w", err)
			}
			d = time.Duration(secs * float64(time.Second))
		}
		desc.HealthCheckInterval = d
	case "healthcheck-delay":
		d, err := time.ParseDuration(value)
		if err != nil {
			secs, err2 := strconv.ParseFloat(value, 64)
			if err2 != nil {
				return fmt.Errorf("invalid healthcheck-delay: %w", err)
			}
			d = time.Duration(secs * float64(time.Second))
		}
		desc.HealthCheckDelay = d
	case "healthcheck-max-failures":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return fmt.Errorf("invalid healthcheck-max-failures: %s (must be >= 0)", value)
		}
		desc.HealthCheckMaxFail = n
	case "unhealthy-command":
		if op == OpPlusEqual {
			desc.UnhealthyCommand = append(desc.UnhealthyCommand, splitCommand(expandEnvVarsForCommand(value, serviceArg))...)
		} else {
			desc.UnhealthyCommand = splitCommand(expandEnvVarsForCommand(value, serviceArg))
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
	case "prepared-by":
		depName := expandEnvVars(value, serviceArg)
		if err := ValidateServiceName(depName); err != nil {
			return fmt.Errorf("invalid dependency name: %w", err)
		}
		desc.PreparedBy = append(desc.PreparedBy, depName)
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
	case "prepared-by.d":
		desc.PreparedByD = append(desc.PreparedByD, expandEnvVars(value, serviceArg))

	// Pre-start fail-fast path checks (OpenRC-inspired)
	case "required-files":
		// Accept both one-per-line and space-separated on a single line,
		// matching OpenRC's shell-array semantics.
		for _, p := range strings.Fields(expandEnvVars(value, serviceArg)) {
			desc.RequiredFiles = append(desc.RequiredFiles, p)
		}
	case "required-dirs":
		for _, p := range strings.Fields(expandEnvVars(value, serviceArg)) {
			desc.RequiredDirs = append(desc.RequiredDirs, p)
		}

	// systemd-style failure-action / success-action (appliance basics).
	case "failure-action":
		act, err := service.ParseSystemAction(strings.TrimSpace(value))
		if err != nil {
			return err
		}
		desc.FailureAction = act
	case "success-action":
		act, err := service.ParseSystemAction(strings.TrimSpace(value))
		if err != nil {
			return err
		}
		desc.SuccessAction = act
	case "reboot-argument":
		desc.RebootArgument = expandEnvVars(value, serviceArg)
	case "runtime-max-sec":
		d, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("runtime-max-sec: %w", err)
		}
		desc.RuntimeMaxSec = d
	case "oom-policy":
		p, err := service.ParseOOMPolicy(strings.TrimSpace(value))
		if err != nil {
			return err
		}
		desc.OOMPolicy = p
	case "load-credential":
		// load-credential = NAME:PATH — copy a file from disk into
		// /run/credentials/<svc>/NAME. NAME : PATH form is the only
		// one we accept (v1 — no parent inheritance, no encrypted).
		name, src, ok := splitNameValue(value)
		if !ok {
			return fmt.Errorf("load-credential: expected NAME:PATH, got %q", value)
		}
		desc.Credentials = append(desc.Credentials, process.CredentialSource{
			Name: name,
			Path: expandEnvVars(src, serviceArg),
		})
	case "set-credential":
		// set-credential = NAME:VALUE — write literal VALUE as
		// /run/credentials/<svc>/NAME. Value is passed verbatim; no
		// escape interpretation (use load-credential from a file for
		// values that need newlines or NULs).
		name, val, ok := splitNameValue(value)
		if !ok {
			return fmt.Errorf("set-credential: expected NAME:VALUE, got %q", value)
		}
		desc.Credentials = append(desc.Credentials, process.CredentialSource{
			Name:  name,
			Value: expandEnvVars(val, serviceArg),
		})

	// Restart
	case "restart":
		return applyRestart(desc, value)
	case "smooth-recovery":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		desc.SmoothRecovery = b
	case "manual":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		desc.ManualStart = b
	case "normal-exit":
		codes, sigs, err := parseNormalExit(value)
		if err != nil {
			return err
		}
		if op == OpEquals {
			desc.NormalExitCodes = codes
			desc.NormalExitSignals = sigs
		} else { // OpPlusEqual
			desc.NormalExitCodes = append(desc.NormalExitCodes, codes...)
			desc.NormalExitSignals = append(desc.NormalExitSignals, sigs...)
		}

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
	case "restart-delay-step":
		d, err := time.ParseDuration(value)
		if err != nil {
			secs, err2 := strconv.ParseFloat(value, 64)
			if err2 != nil {
				return fmt.Errorf("invalid restart-delay-step: %w", err)
			}
			d = time.Duration(secs * float64(time.Second))
		}
		if d < 0 {
			return fmt.Errorf("restart-delay-step must be >= 0")
		}
		desc.RestartDelayStep = d
	case "restart-delay-cap":
		d, err := time.ParseDuration(value)
		if err != nil {
			secs, err2 := strconv.ParseFloat(value, 64)
			if err2 != nil {
				return fmt.Errorf("invalid restart-delay-cap: %w", err)
			}
			d = time.Duration(secs * float64(time.Second))
		}
		if d < 0 {
			return fmt.Errorf("restart-delay-cap must be >= 0")
		}
		desc.RestartDelayCap = d
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

	// Signal — OpenRC uses "stopsig" as the shell var name; slinit's
	// canonical form is "term-signal", with "termsignal" kept as a dinit
	// alias and "stopsig" as an OpenRC alias.
	case "term-signal", "termsignal", "stopsig":
		sig, err := parseSignal(value)
		if err != nil {
			return err
		}
		desc.TermSignal = sig
	case "reload-signal":
		sig, err := parseSignal(value)
		if err != nil {
			return err
		}
		desc.ReloadSignal = sig

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
	case "logfile-max-size":
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid logfile-max-size: %w", err)
		}
		desc.LogMaxSize = n
	case "logfile-max-files":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid logfile-max-files: %w", err)
		}
		desc.LogMaxFiles = n
	case "logfile-rotate-time":
		d, err := parseDuration(value)
		if err != nil {
			return fmt.Errorf("invalid logfile-rotate-time: %w", err)
		}
		desc.LogRotateTime = d
	case "log-processor":
		if op == OpPlusEqual {
			desc.LogProcessor = append(desc.LogProcessor, splitCommand(expandEnvVarsForCommand(value, serviceArg))...)
		} else {
			desc.LogProcessor = splitCommand(expandEnvVarsForCommand(value, serviceArg))
		}
	case "log-include":
		desc.LogInclude = append(desc.LogInclude, value)
	case "log-exclude":
		desc.LogExclude = append(desc.LogExclude, value)

	// Output/error logger (OpenRC OUTPUT_LOGGER / ERROR_LOGGER)
	case "output-logger":
		if op == OpPlusEqual {
			desc.OutputLogger = append(desc.OutputLogger, splitCommand(expandEnvVarsForCommand(value, serviceArg))...)
		} else {
			desc.OutputLogger = splitCommand(expandEnvVarsForCommand(value, serviceArg))
		}
		if desc.LogType == service.LogNone {
			desc.LogType = service.LogToCommand
		}
	case "error-logger":
		if op == OpPlusEqual {
			desc.ErrorLogger = append(desc.ErrorLogger, splitCommand(expandEnvVarsForCommand(value, serviceArg))...)
		} else {
			desc.ErrorLogger = splitCommand(expandEnvVarsForCommand(value, serviceArg))
		}
		if desc.LogType == service.LogNone {
			desc.LogType = service.LogToCommand
		}

	// Process management
	case "pid-file":
		desc.PIDFile = expandEnvVars(value, serviceArg)
	case "ready-notification":
		desc.ReadyNotification = value
		if err := parseReadyNotification(desc, value); err != nil {
			return err
		}
	case "watchdog-timeout":
		// Accept both Go duration syntax ("30s", "2m") and bare-seconds
		// floats ("30", "0.5") to match the surrounding settings.
		d, err := time.ParseDuration(value)
		if err != nil {
			secs, err2 := strconv.ParseFloat(value, 64)
			if err2 != nil {
				return fmt.Errorf("watchdog-timeout: invalid duration %q", value)
			}
			d = time.Duration(secs * float64(time.Second))
		}
		if d <= 0 {
			return fmt.Errorf("watchdog-timeout must be > 0 (got %s)", d)
		}
		desc.WatchdogTimeout = d
	case "run-as":
		desc.RunAs = value

	// Socket
	case "socket-listen":
		path := expandEnvVars(value, serviceArg)
		if op == OpPlusEqual {
			desc.SocketPaths = append(desc.SocketPaths, path)
		} else {
			desc.SocketPath = path
			// Reset paths when = is used (override)
			desc.SocketPaths = []string{path}
		}
	case "socket-activation":
		switch value {
		case "immediate", "on-demand":
			desc.SocketActivation = value
		default:
			return fmt.Errorf("invalid socket-activation: %q (must be 'immediate' or 'on-demand')", value)
		}
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

	// Platform keywords (OpenRC-compatible: keyword -docker -lxc ...)
	case "keyword":
		for _, kw := range strings.Fields(value) {
			desc.Keywords = append(desc.Keywords, kw)
		}

	// Consumer
	case "consumer-of":
		consName := expandEnvVars(value, serviceArg)
		if err := ValidateServiceName(consName); err != nil {
			return fmt.Errorf("invalid consumer-of name: %w", err)
		}
		desc.ConsumerOf = consName

	// Shared logger (multi-service → single logger)
	case "shared-logger":
		loggerName := expandEnvVars(value, serviceArg)
		if err := ValidateServiceName(loggerName); err != nil {
			return fmt.Errorf("invalid shared-logger name: %w", err)
		}
		desc.SharedLogger = loggerName
		// Implicitly set log-type to pipe
		desc.LogType = service.LogToPipe

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

	case "umask":
		m, err := strconv.ParseUint(value, 8, 32)
		if err != nil || m > 0o777 {
			return fmt.Errorf("invalid umask: %s (expected octal 000..777)", value)
		}
		u := uint32(m)
		desc.Umask = &u

	case "apparmor-load":
		if !filepath.IsAbs(value) {
			return fmt.Errorf("apparmor-load: path must be absolute: %q", value)
		}
		desc.AppArmorLoad = value

	case "apparmor-switch":
		if value == "" {
			return fmt.Errorf("apparmor-switch: profile name must not be empty")
		}
		desc.AppArmorSwitch = value

	case "private-tmp":
		b, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("private-tmp: %w", err)
		}
		desc.PrivateTmp = b

	case "protect-system":
		v := strings.ToLower(strings.TrimSpace(value))
		switch v {
		case "", "no", "off", "false", "0":
			desc.ProtectSystem = ""
		case "yes", "true", "1":
			desc.ProtectSystem = "yes"
		case "full":
			desc.ProtectSystem = "full"
		case "strict":
			desc.ProtectSystem = "strict"
		default:
			return fmt.Errorf("protect-system: expected no|yes|full|strict, got %q", value)
		}

	case "read-only-paths":
		paths, err := parseSandboxPaths(setting, value, serviceArg)
		if err != nil {
			return err
		}
		desc.ReadOnlyPaths = append(desc.ReadOnlyPaths, paths...)

	case "read-write-paths":
		paths, err := parseSandboxPaths(setting, value, serviceArg)
		if err != nil {
			return err
		}
		desc.ReadWritePaths = append(desc.ReadWritePaths, paths...)

	case "protect-home":
		v := strings.ToLower(strings.TrimSpace(value))
		switch v {
		case "", "no", "off", "false", "0":
			desc.ProtectHome = ""
		case "yes", "true", "1":
			desc.ProtectHome = "yes"
		case "read-only", "ro":
			desc.ProtectHome = "read-only"
		case "tmpfs":
			desc.ProtectHome = "tmpfs"
		default:
			return fmt.Errorf("protect-home: expected no|yes|read-only|tmpfs, got %q", value)
		}

	case "inaccessible-paths":
		paths, err := parseSandboxPaths(setting, value, serviceArg)
		if err != nil {
			return err
		}
		desc.InaccessiblePaths = append(desc.InaccessiblePaths, paths...)

	case "protect-proc":
		v := strings.ToLower(strings.TrimSpace(value))
		switch v {
		case "", "default":
			desc.ProtectProc = ""
		case "noaccess", "invisible", "ptraceable":
			desc.ProtectProc = v
		default:
			return fmt.Errorf("protect-proc: expected default|noaccess|invisible|ptraceable, got %q", value)
		}

	case "proc-subset":
		v := strings.ToLower(strings.TrimSpace(value))
		switch v {
		case "", "all":
			desc.ProcSubset = ""
		case "pid":
			desc.ProcSubset = "pid"
		default:
			return fmt.Errorf("proc-subset: expected all|pid, got %q", value)
		}

	case "bind-paths":
		entries, err := parseBindEntries(setting, value, serviceArg)
		if err != nil {
			return err
		}
		desc.BindPaths = append(desc.BindPaths, entries...)

	case "bind-read-only-paths":
		entries, err := parseBindEntries(setting, value, serviceArg)
		if err != nil {
			return err
		}
		desc.BindReadOnlyPaths = append(desc.BindReadOnlyPaths, entries...)

	case "temporary-filesystem":
		entries, err := parseTmpfsEntries(setting, value, serviceArg)
		if err != nil {
			return err
		}
		desc.TemporaryFileSystem = append(desc.TemporaryFileSystem, entries...)

	case "system-call-filter":
		items := strings.Fields(expandEnvVars(value, serviceArg))
		if err := validateSeccompItems(setting, items); err != nil {
			return err
		}
		desc.SystemCallFilter = append(desc.SystemCallFilter, items...)

	case "system-call-architectures":
		for _, a := range strings.Fields(expandEnvVars(value, serviceArg)) {
			if _, err := seccomp.ResolveArch(a); err != nil {
				return fmt.Errorf("system-call-architectures: %w", err)
			}
			desc.SystemCallArchitectures = append(desc.SystemCallArchitectures, a)
		}

	case "system-call-error-number":
		v := strings.TrimSpace(value)
		if _, err := seccomp.ParseAction(v); err != nil {
			return fmt.Errorf("system-call-error-number: %w", err)
		}
		desc.SystemCallErrorNumber = v

	case "system-call-log":
		items := strings.Fields(expandEnvVars(value, serviceArg))
		if err := validateSeccompItems(setting, items); err != nil {
			return err
		}
		desc.SystemCallLog = append(desc.SystemCallLog, items...)

	case "protect-kernel-tunables", "protect-kernel-modules",
		"protect-kernel-logs", "protect-clock",
		"protect-control-groups", "protect-hostname",
		"lock-personality":
		b, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("%s: %w", setting, err)
		}
		switch setting {
		case "protect-kernel-tunables":
			desc.ProtectKernelTunables = b
		case "protect-kernel-modules":
			desc.ProtectKernelModules = b
		case "protect-kernel-logs":
			desc.ProtectKernelLogs = b
		case "protect-clock":
			desc.ProtectClock = b
		case "protect-control-groups":
			desc.ProtectControlGroups = b
		case "protect-hostname":
			desc.ProtectHostname = b
		case "lock-personality":
			desc.LockPersonality = b
		}

	case "runtime-directory", "state-directory", "cache-directory",
		"logs-directory", "configuration-directory":
		names, err := parseServiceDirNames(setting, value, serviceArg)
		if err != nil {
			return err
		}
		switch setting {
		case "runtime-directory":
			desc.RuntimeDirs = names
		case "state-directory":
			desc.StateDirs = names
		case "cache-directory":
			desc.CacheDirs = names
		case "logs-directory":
			desc.LogsDirs = names
		case "configuration-directory":
			desc.ConfigDirs = names
		}

	case "runtime-directory-mode", "state-directory-mode",
		"cache-directory-mode", "logs-directory-mode",
		"configuration-directory-mode":
		m, err := parseDirMode(setting, value)
		if err != nil {
			return err
		}
		switch setting {
		case "runtime-directory-mode":
			desc.RuntimeDirMode = m
		case "state-directory-mode":
			desc.StateDirMode = m
		case "cache-directory-mode":
			desc.CacheDirMode = m
		case "logs-directory-mode":
			desc.LogsDirMode = m
		case "configuration-directory-mode":
			desc.ConfigDirMode = m
		}

	case "runtime-directory-preserve":
		switch value {
		case "no":
			desc.RuntimeDirPreserve = 0
		case "yes":
			desc.RuntimeDirPreserve = 1
		case "restart":
			desc.RuntimeDirPreserve = 2
		default:
			return fmt.Errorf("invalid runtime-directory-preserve: %q (expected no, yes, or restart)", value)
		}

	case "start-on-path-exists", "start-on-path-changed",
		"start-on-path-modified", "start-on-directory-not-empty":
		if !filepath.IsAbs(value) {
			return fmt.Errorf("%s: path must be absolute: %q", setting, value)
		}
		if desc.StartOnPathTrigger != 0 {
			return fmt.Errorf("%s: only one start-on-* stanza is allowed per service", setting)
		}
		desc.StartOnPath = value
		switch setting {
		case "start-on-path-exists":
			desc.StartOnPathTrigger = 1
		case "start-on-path-changed":
			desc.StartOnPathTrigger = 2
		case "start-on-path-modified":
			desc.StartOnPathTrigger = 3
		case "start-on-directory-not-empty":
			desc.StartOnPathTrigger = 4
		}

	case "ioprio":
		desc.IOPrio = value

	case "cgroup", "run-in-cgroup":
		desc.CgroupPath = value

	// Cgroup v2 resource limits — dedicated settings for common controllers.
	// Values are written as-is to the corresponding cgroup v2 knob file.
	case "cgroup-memory-max":
		desc.CgroupSettings = append(desc.CgroupSettings, CgroupSetting{"memory.max", value})
	case "cgroup-memory-high":
		desc.CgroupSettings = append(desc.CgroupSettings, CgroupSetting{"memory.high", value})
	case "cgroup-memory-min":
		desc.CgroupSettings = append(desc.CgroupSettings, CgroupSetting{"memory.min", value})
	case "cgroup-memory-low":
		desc.CgroupSettings = append(desc.CgroupSettings, CgroupSetting{"memory.low", value})
	case "cgroup-swap-max":
		desc.CgroupSettings = append(desc.CgroupSettings, CgroupSetting{"memory.swap.max", value})
	case "cgroup-pids-max":
		desc.CgroupSettings = append(desc.CgroupSettings, CgroupSetting{"pids.max", value})
	case "cgroup-cpu-weight":
		desc.CgroupSettings = append(desc.CgroupSettings, CgroupSetting{"cpu.weight", value})
	case "cgroup-cpu-max":
		desc.CgroupSettings = append(desc.CgroupSettings, CgroupSetting{"cpu.max", value})
	case "cgroup-io-weight":
		desc.CgroupSettings = append(desc.CgroupSettings, CgroupSetting{"io.weight", value})
	case "cgroup-cpuset-cpus":
		desc.CgroupSettings = append(desc.CgroupSettings, CgroupSetting{"cpuset.cpus", value})
	case "cgroup-cpuset-mems":
		desc.CgroupSettings = append(desc.CgroupSettings, CgroupSetting{"cpuset.mems", value})
	case "cgroup-hugetlb":
		// Format: "size value" e.g. "2MB 4" → hugetlb.2MB.max = 4
		parts := strings.SplitN(value, " ", 2)
		if len(parts) != 2 {
			return fmt.Errorf("cgroup-hugetlb requires 'size value' (e.g., '2MB 4')")
		}
		desc.CgroupSettings = append(desc.CgroupSettings, CgroupSetting{
			"hugetlb." + strings.TrimSpace(parts[0]) + ".max",
			strings.TrimSpace(parts[1]),
		})

	// Generic cgroup v2 setting: write any controller knob.
	// Format: cgroup-setting = <file> <value>
	case "cgroup-setting":
		parts := strings.SplitN(value, " ", 2)
		if len(parts) != 2 {
			return fmt.Errorf("cgroup-setting requires '<file> <value>'")
		}
		desc.CgroupSettings = append(desc.CgroupSettings, CgroupSetting{
			strings.TrimSpace(parts[0]),
			strings.TrimSpace(parts[1]),
		})

	case "cpu-affinity":
		cpus, err := ParseCPUAffinity(value)
		if err != nil {
			return fmt.Errorf("invalid cpu-affinity: %v", err)
		}
		desc.CPUAffinity = cpus

	case "sched-policy":
		pol, err := parseSchedPolicy(value)
		if err != nil {
			return err
		}
		desc.SchedPolicy = pol
		desc.SchedPolicySet = true

	case "sched-priority":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("sched-priority: %w", err)
		}
		if n < 1 || n > 99 {
			return fmt.Errorf("sched-priority %d out of range 1..99", n)
		}
		desc.SchedPriority = uint32(n)

	case "sched-runtime":
		ns, err := parseSchedDuration(value)
		if err != nil {
			return fmt.Errorf("sched-runtime: %w", err)
		}
		desc.SchedRuntime = ns
	case "sched-deadline":
		ns, err := parseSchedDuration(value)
		if err != nil {
			return fmt.Errorf("sched-deadline: %w", err)
		}
		desc.SchedDeadline = ns
	case "sched-period":
		ns, err := parseSchedDuration(value)
		if err != nil {
			return fmt.Errorf("sched-period: %w", err)
		}
		desc.SchedPeriod = ns

	case "sched-reset-on-fork":
		b, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("sched-reset-on-fork: %w", err)
		}
		desc.SchedResetOnFork = b
		desc.SchedResetOnForkSet = true

	case "mlockall":
		flags, err := parseMlockallFlags(value)
		if err != nil {
			return err
		}
		desc.MlockallFlags = flags

	case "numa-mempolicy":
		mode, err := parseMempolicyMode(value)
		if err != nil {
			return err
		}
		desc.NumaMempolicy = mode
		desc.NumaMempolicySet = true

	case "numa-nodes":
		nodes, err := ParseCPUAffinity(value) // same numeric-list grammar
		if err != nil {
			return fmt.Errorf("numa-nodes: %w", err)
		}
		desc.NumaNodes = nodes

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

	// Extra commands (OpenRC-style custom actions)
	// Format: extra-command = <action-name> <command> [args...]
	// The first word is the action name, the rest is the command to run.
	case "extra-command":
		parts := strings.Fields(expandEnvVarsForCommand(value, serviceArg))
		if len(parts) < 2 {
			return fmt.Errorf("extra-command requires an action name and a command")
		}
		actionName := parts[0]
		cmd := splitCommand(strings.Join(parts[1:], " "))
		if desc.ExtraCommands == nil {
			desc.ExtraCommands = make(map[string][]string)
		}
		desc.ExtraCommands[actionName] = cmd
	case "extra-started-command":
		parts := strings.Fields(expandEnvVarsForCommand(value, serviceArg))
		if len(parts) < 2 {
			return fmt.Errorf("extra-started-command requires an action name and a command")
		}
		actionName := parts[0]
		cmd := splitCommand(strings.Join(parts[1:], " "))
		if desc.ExtraStartedCommands == nil {
			desc.ExtraStartedCommands = make(map[string][]string)
		}
		desc.ExtraStartedCommands[actionName] = cmd

	// Control commands (runit-style custom signal handlers)
	// Format: control-command-HUP = /path/to/script
	default:
		if strings.HasPrefix(setting, "control-command-") {
			sigName := strings.ToUpper(setting[len("control-command-"):])
			cmd := splitCommand(expandEnvVarsForCommand(value, serviceArg))
			if len(cmd) > 0 {
				if desc.ControlCommands == nil {
					desc.ControlCommands = make(map[string][]string)
				}
				if op == OpPlusEqual {
					desc.ControlCommands[sigName] = append(desc.ControlCommands[sigName], cmd...)
				} else {
					desc.ControlCommands[sigName] = cmd
				}
			}
			return nil
		}
		// systemd-style start predicates: condition-* / assert-*.
		if pred, ok, perr := parsePredicate(setting, value, serviceArg); perr != nil {
			return perr
		} else if ok {
			desc.Predicates = append(desc.Predicates, pred)
			return nil
		}
		return fmt.Errorf("unknown setting: %s", setting)
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
	case "command":
		desc.LogType = service.LogToCommand
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

// parseSchedPolicy maps a config string to a Linux SCHED_* constant.
// Accepts both the kernel name (fifo, rr, batch, idle, deadline, other)
// and conventional aliases (realtime → fifo, normal → other).
func parseSchedPolicy(value string) (uint32, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "other", "normal":
		return unix.SCHED_NORMAL, nil
	case "fifo", "realtime":
		return unix.SCHED_FIFO, nil
	case "rr":
		return unix.SCHED_RR, nil
	case "batch":
		return unix.SCHED_BATCH, nil
	case "idle":
		return unix.SCHED_IDLE, nil
	case "deadline":
		return unix.SCHED_DEADLINE, nil
	default:
		return 0, fmt.Errorf("sched-policy: unknown policy %q (expected one of fifo, rr, batch, idle, deadline, other)", value)
	}
}

// parseSchedDuration accepts Go duration syntax ("500us", "10ms",
// "100ns") or a bare nanosecond integer, and returns the value in
// nanoseconds. SCHED_DEADLINE expresses everything in ns, so we
// normalise here and store ns directly in the description.
func parseSchedDuration(value string) (uint64, error) {
	if d, err := time.ParseDuration(value); err == nil {
		if d <= 0 {
			return 0, fmt.Errorf("must be > 0")
		}
		return uint64(d.Nanoseconds()), nil
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (expected Go duration like 5ms or a bare ns integer)", value)
	}
	if n == 0 {
		return 0, fmt.Errorf("must be > 0")
	}
	return n, nil
}

// parseMlockallFlags accepts the symbolic names current/future/both/onfault
// (combinable with '+' or ',') and returns the mlockall(2) bitmask.
// "both" is shorthand for current+future.
func parseMlockallFlags(value string) (int, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return 0, fmt.Errorf("mlockall: empty value")
	}
	if value == "no" || value == "off" || value == "0" {
		return 0, nil
	}
	var out int
	for _, part := range strings.FieldsFunc(value, func(r rune) bool {
		return r == '+' || r == ',' || r == ' '
	}) {
		switch part {
		case "current":
			out |= unix.MCL_CURRENT
		case "future":
			out |= unix.MCL_FUTURE
		case "both", "yes", "on":
			out |= unix.MCL_CURRENT | unix.MCL_FUTURE
		case "onfault":
			out |= unix.MCL_ONFAULT
		default:
			return 0, fmt.Errorf("mlockall: unknown flag %q (expected current|future|both|onfault)", part)
		}
	}
	if out == 0 {
		return 0, fmt.Errorf("mlockall: no valid flags in %q", value)
	}
	return out, nil
}

// parseMempolicyMode maps a config string to a Linux MPOL_* constant.
func parseMempolicyMode(value string) (uint32, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "default":
		return unix.MPOL_DEFAULT, nil
	case "bind":
		return unix.MPOL_BIND, nil
	case "preferred":
		return unix.MPOL_PREFERRED, nil
	case "interleave":
		return unix.MPOL_INTERLEAVE, nil
	case "local":
		return unix.MPOL_LOCAL, nil
	default:
		return 0, fmt.Errorf("numa-mempolicy: unknown mode %q (expected bind|preferred|interleave|local|default)", value)
	}
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

// parseNormalExit parses an upstart-style `normal exit` value: a
// space-separated list of decimal exit codes and/or signal names
// (or numeric signal values). Examples:
//
//	normal-exit = 0 2 SIGTERM
//	normal-exit = 0 SIGUSR1 15
//
// Returns the codes and signals as separate slices. A token that
// looks like a small decimal (0–255) is interpreted as an exit code;
// anything else is run through parseSignal. Strict bounds avoid the
// ambiguity where a signal number and an exit code share a value
// (e.g. 15 = SIGTERM but also a valid exit code) — in slinit a bare
// number is always an exit code, and a signal must be named.
//
// An empty value clears the lists (useful for `normal-exit =` to
// reset, mirroring how empty `command =` resets argv).
func parseNormalExit(value string) ([]int, []syscall.Signal, error) {
	tokens := strings.Fields(value)
	if len(tokens) == 0 {
		return nil, nil, nil
	}

	var codes []int
	var sigs []syscall.Signal

	for _, tok := range tokens {
		// Try as exit code first when the token is bare digits.
		// Signals must be named (SIGTERM, TERM) to avoid the
		// number-vs-signal ambiguity.
		if n, err := strconv.Atoi(tok); err == nil {
			if n < 0 || n > 255 {
				return nil, nil, fmt.Errorf("normal-exit: exit code %d out of range [0,255]", n)
			}
			codes = append(codes, n)
			continue
		}
		sig, err := parseSignal(tok)
		if err != nil {
			return nil, nil, fmt.Errorf("normal-exit: %w", err)
		}
		sigs = append(sigs, sig)
	}

	return codes, sigs, nil
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

// systemd .service unit support.
//
// This holds the single systemd->slinit mapping in the tree. Two
// consumers share it: slinit-systemd-convert, which writes a slinit
// file for the operator to review and edit, and the loader, which
// reads a unit directly at service-resolution time (see
// SystemdUnitToServiceDescription and DirLoader.SetSystemdDirs).
//
// The direct path deliberately goes through EmitSlinitFile and then
// the real Parse rather than building a ServiceDescription of its
// own. A second mapping would drift from the converter's the first
// time either changed, and the two would disagree about what a unit
// means — the worst possible outcome for a compatibility layer. It
// also means the live path inherits every default Parse applies.
//
// Only .service is handled. Timer, socket, path, mount and target
// units are systemd abstractions that land on different slinit
// facilities (cron=, path activation directives, the boot graph),
// and mechanical translation would produce something that looks
// right and behaves wrong.
package config

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/sunlightlinux/slinit/pkg/process"
	"github.com/sunlightlinux/slinit/pkg/seccomp"
	"golang.org/x/sys/unix"
)

// --- Data structures ---

// SystemdWarning is one conversion note. Level is NOTE for a mapping
// the operator should be aware of, WARN for a directive with no slinit
// equivalent that was dropped.
type SystemdWarning struct {
	Level string // NOTE / WARN
	Msg   string
}

type SystemdConfig struct {
	svcName  string
	unitPath string

	startWhenIdle bool // Type=idle

	svcType      string // process | bgprocess | scripted
	command      string
	argv0        string // ExecStart=@path argv0 …
	stopCommand  string
	preStart     string // slinit takes one; multiples get warned
	postStart    string
	runAs        string
	envFile      string
	stopSignal   string // from ExecStop=kill -s SIG $MAINPID, if terminating
	stopLine     string // that ExecStop= value, for the note
	workingDir   string
	chroot       string
	pidFile      string
	termSignal   string
	umask        string
	noNewPrivs   bool
	restart      string
	restartDelay string
	stopTimeout  string
	closeStdin   bool

	// rlimit-*
	rlimitNofile string
	rlimitCore   string
	rlimitData   string
	rlimitAS     string

	rlimitMemlock string

	// CapabilityBoundingSet= and AmbientCapabilities=, folded across
	// their assignments; emitted once the whole unit is read.
	capBounding capFold
	capAmbient  capFold

	// RestrictAddressFamilies=, folded across its assignments.
	addrFamilies afFold

	// SystemCallFilter= lines since the last empty one, merged once the
	// whole unit is read.
	syscallLines []syscallLine

	// errs are confinement directives slinit cannot apply: the unit
	// fails to convert rather than run less confined than it says.
	errs []string

	description string   // [Unit] Description=
	mapped      []kvPair // directives translated 1:1, emitted in order
	depends     []string // depends-on: X
	waitsFor    []string // waits-for: X
	aliases     []string // [Install] Alias= .service names, bare
	conditions  []condDir
	comments    []string
}

// kvPair is one emitted "key = value" line, kept ordered so the output
// is stable across runs.
type kvPair struct{ key, value string }

// systemdDirNames maps systemd's per-service directory directives to
// slinit's, which take the same space-separated list of names.
var systemdDirNames = map[string]string{
	"RuntimeDirectory":       "runtime-directory",
	"StateDirectory":         "state-directory",
	"CacheDirectory":         "cache-directory",
	"LogsDirectory":          "logs-directory",
	"ConfigurationDirectory": "configuration-directory",
}

// systemdHardeningNames maps the systemd spelling to slinit's for the
// plain yes/no hardening switches.
var systemdHardeningNames = map[string]string{
	"PrivateTmp":             "private-tmp",
	"ProtectKernelTunables":  "protect-kernel-tunables",
	"ProtectKernelModules":   "protect-kernel-modules",
	"ProtectControlGroups":   "protect-control-groups",
	"ProtectClock":           "protect-clock",
	"RestrictRealtime":       "restrict-realtime",
	"LockPersonality":        "lock-personality",
	"MemoryDenyWriteExecute": "memory-deny-write-execute",
	"RestrictSUIDSGID":       "restrict-suidsgid",
	"ProtectKernelLogs":      "protect-kernel-logs",
}

// boolWord normalises systemd's true/false/1/0/on/off to slinit's yes/no.
func boolWord(v string) string {
	if truthy(v) {
		return "yes"
	}
	return "no"
}

type condDir struct {
	name  string // e.g. "condition-path-exists"
	value string
}

// --- Convert entry point ---

func ConvertSystemdUnit(path string) (*SystemdConfig, []SystemdWarning, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	base := filepath.Base(abs)
	// Guard: only .service. Also accept the .service.in template
	// form (systemd source ships units with a trailing .in that
	// meson expands at install time — real-world convert targets
	// might live in either form).
	if !strings.Contains(base, ".service") {
		suffix := filepath.Ext(base)
		return nil, nil, fmt.Errorf("unsupported unit type %q — only .service is handled", suffix)
	}
	if strings.Contains(base, "@") {
		return nil, nil, fmt.Errorf("template unit (contains '@') — instantiate first, then convert")
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, nil, fmt.Errorf("read: %w", err)
	}

	// Strip .service or .service.in — leave a bare svc name for slinit.
	name := strings.TrimSuffix(base, ".in")
	name = strings.TrimSuffix(name, ".service")
	cfg := &SystemdConfig{
		svcName:  name,
		unitPath: abs,
		svcType:  "process", // default; overridden by Type=
		restart:  "no",      // systemd default is Restart=no
	}

	warns := parseSystemdUnit(cfg, string(data))
	if len(cfg.errs) > 0 {
		return nil, nil, fmt.Errorf("%s", strings.Join(cfg.errs, "; "))
	}
	cfg.comments = append([]string{
		fmt.Sprintf("Converted from systemd unit: %s", abs),
		"Review the notes above before enabling.",
	}, cfg.comments...)
	return cfg, warns, nil
}

// --- Parser ---

var (
	sectionRe = regexp.MustCompile(`^\[([A-Za-z]+)\]\s*$`)
	kvRe      = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*)\s*=\s*(.*)$`)
)

// parseSystemdUnit is a section-aware INI parser that also joins
// backslash-continued values (systemd allows `Foo=bar \` newline
// `continuation` as one logical assignment). Only [Unit] and
// [Service] sections influence the output; [Install] is read for
// the WantedBy note but doesn't drive any directive.
func parseSystemdUnit(cfg *SystemdConfig, content string) []SystemdWarning {
	var warns []SystemdWarning
	section := ""

	// Multi-line join: iterate the raw lines, if a line ends with
	// `\` (after trimming trailing whitespace), fuse it with the
	// next.
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	var joined []string
	var acc strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimRight(line, " \t")
		if strings.HasSuffix(trimmed, "\\") {
			acc.WriteString(strings.TrimSuffix(trimmed, "\\"))
			acc.WriteByte(' ')
			continue
		}
		if acc.Len() > 0 {
			acc.WriteString(line)
			joined = append(joined, acc.String())
			acc.Reset()
		} else {
			joined = append(joined, line)
		}
	}
	if acc.Len() > 0 {
		joined = append(joined, acc.String())
	}

	// State for the multiple-ExecStartPre check.
	preStartCount := 0
	postStartCount := 0

	for _, raw := range joined {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if m := sectionRe.FindStringSubmatch(line); m != nil {
			section = m[1]
			continue
		}
		m := kvRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key := m[1]
		val := strings.TrimSpace(m[2])
		// Strip surrounding double quotes if present (single-quote
		// isn't standard here but be tolerant). Not on an Exec*= line:
		// there quotes group words, as in slinit's command parser, and
		// "/opt/my app/run" --name "a b" would lose the first and last.
		if !strings.HasPrefix(key, "Exec") {
			val = trimQuotes(val)
		}

		switch section {
		case "Unit":
			warns = append(warns, applyUnitKey(cfg, key, val)...)
		case "Service":
			w := applyServiceKey(cfg, key, val, &preStartCount, &postStartCount)
			warns = append(warns, w...)
		case "Install":
			warns = append(warns, applyInstallKey(cfg, key, val)...)
		}
	}
	if cfg.capBounding.set {
		switch cfg.capBounding.mask {
		case allCaps:
			// No limit, as systemd leaves the set alone.
		case 0:
			cfg.errs = append(cfg.errs, "CapabilityBoundingSet= leaves no capability, and slinit's capability-bounding-set "+
				"cannot express an empty set; the service would keep every capability")
		default:
			cfg.mapped = append(cfg.mapped, kvPair{"capability-bounding-set", capNames(cfg.capBounding.mask)})
		}
	}
	if f := cfg.addrFamilies; f.set {
		if f.allow {
			cfg.mapped = append(cfg.mapped, kvPair{"restrict-address-families", strings.Join(f.fams, " ")})
		} else if allowed, err := f.complement(); err != nil {
			cfg.errs = append(cfg.errs, err.Error())
		} else {
			cfg.mapped = append(cfg.mapped, kvPair{"restrict-address-families", allowed})
			warns = append(warns, SystemdWarning{"NOTE", "RestrictAddressFamilies=~" + strings.Join(f.fams, " ") +
				" → restrict-address-families allowing every other family, by number"})
		}
	}
	if len(cfg.syscallLines) > 0 {
		v, note, err := mergeSyscallFilter(cfg.syscallLines)
		if err != nil {
			cfg.errs = append(cfg.errs, err.Error())
		} else {
			cfg.mapped = append(cfg.mapped, kvPair{"system-call-filter", v})
			if note != "" {
				warns = append(warns, SystemdWarning{"NOTE", note})
			}
		}
	}
	if cfg.capAmbient.mask != 0 {
		cfg.mapped = append(cfg.mapped, kvPair{"capabilities", capNames(cfg.capAmbient.mask)})
	}
	if cfg.argv0 != "" && cfg.svcType == "scripted" {
		// A scripted service takes no command-argv0.
		warns = append(warns, warnArgv0("ExecStart", cfg.argv0)...)
		cfg.argv0 = ""
	}
	warns = append(warns, applyStopSignal(cfg)...)
	warns = append(warns, dropAliasRefs(cfg)...)
	return warns
}

// applyStopSignal turns ExecStop=kill -s SIG $MAINPID into term-signal
// when SIG stops the daemon: TERM, INT, QUIT, KILL, or the signal
// KillSignal= already names. Many units send a reload or log-reopen
// signal that way instead (kill -s HUP, USR1, USR2), and as term-signal
// it would override KillSignal=: the daemon would take the HUP, keep
// running until stop-timeout, and be SIGKILLed. Such an ExecStop= is
// dropped with a warning and KillSignal= (or TERM) stops the service,
// as on 4.0.2, where the kill without a PID failed and TERM followed.
func applyStopSignal(cfg *SystemdConfig) []SystemdWarning {
	if cfg.stopSignal == "" {
		return nil
	}
	sig, err := parseSignal(cfg.stopSignal)
	if err != nil {
		return nil // killMainPID only returns parseable signals
	}
	ok := sig == syscall.SIGTERM || sig == syscall.SIGINT || sig == syscall.SIGQUIT || sig == syscall.SIGKILL
	if !ok && cfg.termSignal != "" {
		k, err := parseSignal(cfg.termSignal)
		ok = err == nil && k == sig
	}
	if ok {
		cfg.termSignal = cfg.stopSignal
		return []SystemdWarning{{"NOTE", fmt.Sprintf("ExecStop=%s → term-signal = %s", cfg.stopLine, cfg.stopSignal)}}
	}
	return []SystemdWarning{{"WARN", fmt.Sprintf(
		"ExecStop=%s uses $MAINPID, which slinit does not provide — dropped; %s does not stop a daemon as KillSignal= does, "+
			"so it is not made the term-signal; slinit stops the service with its term-signal",
		cfg.stopLine, cfg.stopSignal)}}
}

// dropAliasRefs leaves out the dependencies naming one of the unit's own
// aliases, which [Install] gives only after [Unit] has been read:
// dbus-broker.service is Alias=dbus.service and Requires=dbus.socket,
// its own socket, which would otherwise be a dependency on a "dbus"
// service — itself, or nothing.
func dropAliasRefs(cfg *SystemdConfig) []SystemdWarning {
	if len(cfg.aliases) == 0 {
		return nil
	}
	var warns []SystemdWarning
	isAlias := func(n string) bool {
		for _, a := range cfg.aliases {
			if a == n {
				return true
			}
		}
		return false
	}
	filter := func(key string, refs []string) []string {
		var out []string
		for _, r := range refs {
			if isAlias(r) {
				warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf(
					"%s %s dropped — it names this service itself, through its Alias=", key, r)})
				continue
			}
			out = append(out, r)
		}
		return out
	}
	cfg.depends = filter("depends-on:", cfg.depends)
	cfg.waitsFor = filter("waits-for:", cfg.waitsFor)
	conds := cfg.conditions[:0]
	for _, c := range cfg.conditions {
		if c.name == "assert-service-started" && isAlias(c.value) {
			warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf(
				"Requisite on %s dropped — it names this service itself, through its Alias=", c.value)})
			continue
		}
		conds = append(conds, c)
	}
	cfg.conditions = conds
	return warns
}

// [Unit] section directives.
func applyUnitKey(cfg *SystemdConfig, key, val string) []SystemdWarning {
	var warns []SystemdWarning
	switch key {
	case "Description":
		// A real directive, not a comment. Emitting it as `# description:`
		// dropped it on the floor: the text survived in the generated
		// file for a human to read, but never reached the service, so
		// `slinitctl status` showed nothing.
		cfg.description = trimQuotes(val)
	case "Documentation", "DefaultDependencies", "IgnoreOnIsolate", "RefuseManualStart", "RefuseManualStop":
		// Informational or systemd-internal; skip silently.
	case "After":
		// waits-for:, as on 4.0.2: it orders and also pulls the unit in,
		// which systemd's After= does not, but units installed on 4.0
		// rely on that pull-in. after: (ordering only) is for 4.1. A
		// unit loaded directly leaves out a missing one, as systemd
		// ignores it: see dropMissingSystemdRefs.
		deps, targets, self := splitUnitRefs(val, cfg.svcName)
		cfg.waitsFor = append(cfg.waitsFor, deps...)
		warns = append(warns, noteDroppedTargets(key, targets, self)...)
	case "Before":
		warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("Unit `Before=%s` not mappable — invert on the target side", val)})
	case "Requires":
		deps, targets, self := splitUnitRefs(val, cfg.svcName)
		cfg.depends = append(cfg.depends, deps...)
		warns = append(warns, noteDroppedTargets(key, targets, self)...)
	case "Requisite":
		// NOT the same directive as Requires=, though this used to map
		// them to the same thing. Requires=B starts B alongside the
		// unit; Requisite=B says B must ALREADY be active, must not be
		// started by this unit, and the unit fails if it is not. Mapped
		// to depends-on, a unit written to refuse to start without a
		// precondition instead pulled the precondition up and
		// succeeded — the opposite of what it asked for, and silently.
		//
		// assert-service-started is the precondition form: it fails the
		// start (as a dependency failure should) rather than skipping
		// the service the way a condition- would.
		deps, targets, self := splitUnitRefs(val, cfg.svcName)
		for _, d := range deps {
			cfg.conditions = append(cfg.conditions,
				condDir{"assert-service-started", d})
		}
		warns = append(warns, noteDroppedTargets(key, targets, self)...)
		if len(deps) > 0 {
			warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf(
				"Unit `Requisite=%s` mapped to assert-service-started — add `after:` "+
					"if the two could be starting at the same moment", val)})
		}
	case "Wants":
		deps, targets, self := splitUnitRefs(val, cfg.svcName)
		cfg.waitsFor = append(cfg.waitsFor, deps...)
		warns = append(warns, noteDroppedTargets(key, targets, self)...)
	case "Conflicts":
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("Unit `Conflicts=%s` — slinit has no negative dep; managed via stop-command / start-limit-action", val)})
	case "OnFailure":
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("Unit `OnFailure=%s` — map to slinit failure-action directive if the target is a system action", val)})
	case "ConditionPathExists":
		cfg.conditions = append(cfg.conditions, condDir{"condition-path-exists", val})
	case "ConditionPathIsDirectory":
		cfg.conditions = append(cfg.conditions, condDir{"condition-path-is-directory", val})
	case "ConditionPathIsMountPoint":
		cfg.conditions = append(cfg.conditions, condDir{"condition-path-is-mount-point", val})
	case "ConditionFileNotEmpty":
		cfg.conditions = append(cfg.conditions, condDir{"condition-file-not-empty", val})
	case "ConditionKernelCommandLine":
		cfg.conditions = append(cfg.conditions, condDir{"condition-kernel-command-line", val})
	case "ConditionVirtualization":
		cfg.conditions = append(cfg.conditions, condDir{"condition-virtualization", val})
	default:
		if strings.HasPrefix(key, "Condition") || strings.HasPrefix(key, "Assert") {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("Unit `%s=%s` not mapped — check slinit condition-* / assert-* directives manually", key, val)})
		}
	}
	return warns
}

// [Service] section directives.
func applyServiceKey(cfg *SystemdConfig, key, val string, preN, postN *int) []SystemdWarning {
	var warns []SystemdWarning
	switch key {
	case "Type":
		switch val {
		case "simple", "exec":
			cfg.svcType = "process"
		case "forking":
			cfg.svcType = "bgprocess"
		case "oneshot":
			cfg.svcType = "scripted"
			// Oneshots don't auto-respawn; systemd defaults
			// Restart=no here too.
			cfg.restart = "no"
		case "notify", "notify-reload":
			cfg.svcType = "process"
			warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("Type=%s — slinit supports readiness via notify-fd; add `notify = yes` if needed", val)})
		case "dbus":
			warns = append(warns, SystemdWarning{"WARN", "Type=dbus not supported — slinit has no dbus activation; convert to Type=simple manually"})
		case "idle":
			cfg.svcType = "process"
			cfg.startWhenIdle = true
		default:
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("Type=%s not recognised", val)})
		}
	case "ExecStart":
		// A oneshot may list several, run in turn; slinit runs one
		// command. The last is kept, so that an empty ExecStart=
		// followed by a new one replaces it as in systemd.
		cmd, argv0, w := stripExecPrefixes(val)
		if cfg.command != "" && cmd != "" {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf(
				"multiple ExecStart= — slinit runs one command, keeping the last (dropped: %q); "+
					"run the steps from one script if they all matter", cfg.command)})
		}
		cfg.command, cfg.argv0 = cmd, argv0
		warns = append(warns, w...)
		warns = append(warns, warnExecVars(key, cmd)...)
	case "ExecStop":
		cmd, argv0, w := stripExecPrefixes(val)
		warns = append(warns, w...)
		warns = append(warns, warnArgv0(key, argv0)...)
		// slinit has no $MAINPID: it would expand to nothing. The usual
		// `kill -s SIG $MAINPID` is what term-signal does; any other use
		// cannot act on the right process.
		if strings.Contains(cmd, "$MAINPID") || strings.Contains(cmd, "${MAINPID}") {
			if sig, ok := killMainPID(cmd); ok {
				// Decided once KillSignal=, which may follow, is known.
				cfg.stopSignal, cfg.stopLine = sig, val
			} else {
				warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf(
					"ExecStop=%s uses $MAINPID, which slinit does not provide — dropped; slinit stops the service with its term-signal", val)})
			}
			break
		}
		cfg.stopCommand = cmd
		warns = append(warns, warnExecVars(key, cmd)...)
	case "ExecStartPre":
		*preN++
		if *preN > 1 {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("multiple ExecStartPre= — slinit takes one pre-start-command, keeping the first (dropped: %q)", val)})
		} else {
			cmd, argv0, w := stripExecPrefixes(val)
			cfg.preStart = cmd
			warns = append(warns, w...)
			warns = append(warns, warnArgv0(key, argv0)...)
			warns = append(warns, warnExecVars(key, cmd)...)
		}
	case "ExecStartPost":
		*postN++
		if *postN > 1 {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("multiple ExecStartPost= — slinit takes one post-start-command, keeping the first (dropped: %q)", val)})
		} else {
			cmd, argv0, w := stripExecPrefixes(val)
			cfg.postStart = cmd
			warns = append(warns, w...)
			warns = append(warns, warnArgv0(key, argv0)...)
			warns = append(warns, warnExecVars(key, cmd)...)
		}
	case "ExecStopPost":
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("ExecStopPost=%s — slinit has no equivalent; wire via finish-command manually", val)})
	case "ExecReload":
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("ExecReload=%s — slinit reload is signal-based; use reload-signal directive", val)})
	case "User":
		// run-as is user[:group]; either key may come first.
		if _, group, ok := strings.Cut(cfg.runAs, ":"); ok {
			cfg.runAs = val + ":" + group
		} else {
			cfg.runAs = val
		}
	case "Group":
		user, _, _ := strings.Cut(cfg.runAs, ":")
		cfg.runAs = user + ":" + val
	case "WorkingDirectory":
		cfg.workingDir = val
	case "RootDirectory":
		cfg.chroot = val
	case "EnvironmentFile":
		// Systemd tolerates a leading "-" for optional; drop it.
		// systemd reads every file listed, later ones overriding; slinit
		// reads one env-file. An empty value resets the list.
		f := strings.TrimPrefix(val, "-")
		if cfg.envFile != "" && f != "" {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf(
				"multiple EnvironmentFile= — slinit reads one env-file, keeping the last (dropped: %q); merge them by hand", cfg.envFile)})
		}
		cfg.envFile = f
	case "Environment":
		warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("Environment=%s inline — slinit has env-file only; consolidate into a KEY=VAL file", val)})
	case "PassEnvironment", "UnsetEnvironment":
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("%s=%s not directly mapped", key, val)})
	case "Restart":
		switch val {
		case "no":
			cfg.restart = "no"
		case "always":
			cfg.restart = "yes"
		case "on-failure", "on-abnormal", "on-abort":
			cfg.restart = "on-failure"
		case "on-success":
			cfg.restart = "on-success"
		case "on-watchdog":
			warns = append(warns, SystemdWarning{"NOTE", "Restart=on-watchdog — slinit uses watchdog directives; map manually"})
			cfg.restart = "on-failure"
		default:
			cfg.restart = "on-failure"
			warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("Restart=%s → on-failure", val)})
		}
	case "RestartSec":
		cfg.restartDelay = systemdSeconds(val, "")
	case "TimeoutStopSec", "TimeoutSec":
		// infinity disables the timeout in both; slinit spells it 0.
		cfg.stopTimeout = systemdSeconds(val, "0")
	case "TimeoutStartSec":
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("TimeoutStartSec=%s — slinit start-timeout maps this; add manually", val)})
	case "PIDFile":
		cfg.pidFile = val
	case "NoNewPrivileges":
		cfg.noNewPrivs = truthy(val)
	case "UMask":
		// systemd takes up to 07777 (parse_mode); umask(2) keeps the
		// 0777 bits, which is all slinit's umask accepts.
		cfg.umask = val
		if m, err := strconv.ParseUint(strings.TrimSpace(val), 8, 32); err == nil && m > 0o777 && m <= 0o7777 {
			cfg.umask = fmt.Sprintf("%04o", m&0o777)
		}
	case "KillSignal":
		cfg.termSignal = val
	case "StandardInput":
		if val == "null" {
			cfg.closeStdin = true
		} else {
			warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("StandardInput=%s — slinit routes stdin from /dev/null by default; other values need manual mapping", val)})
		}
	case "StandardOutput", "StandardError":
		if val != "journal" && val != "inherit" && val != "null" {
			warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("%s=%s — slinit routes stdout/err to its logger; other targets need manual wiring", key, val)})
		}
	case "LimitNOFILE":
		cfg.rlimitNofile = systemdRlimit(val, false)
	case "LimitCORE":
		cfg.rlimitCore = systemdRlimit(val, true)
	case "LimitDATA":
		cfg.rlimitData = systemdRlimit(val, true)
	case "LimitAS":
		cfg.rlimitAS = systemdRlimit(val, true)
	case "LimitMEMLOCK":
		cfg.rlimitMemlock = systemdRlimit(val, true)
	// The sandbox and capability directives below used to be dropped
	// with a note although slinit implements each: a unit loaded
	// directly ran without the confinement it declared.
	case "CapabilityBoundingSet":
		warns = append(warns, cfg.capBounding.apply(key, val, allCaps)...)
	case "AmbientCapabilities":
		warns = append(warns, cfg.capAmbient.apply(key, val, 0)...)
	case "ReadOnlyPaths", "ReadWritePaths", "InaccessiblePaths":
		slinitKey := map[string]string{
			"ReadOnlyPaths":     "read-only-paths",
			"ReadWritePaths":    "read-write-paths",
			"InaccessiblePaths": "inaccessible-paths",
		}[key]
		// slinit hides a missing inaccessible path already; a missing
		// read-only or read-write path fails the start.
		paths, w := systemdPaths(key, val, key != "InaccessiblePaths")
		warns = append(warns, w...)
		cfg.setMappedList(slinitKey, val, paths)
	case "BindReadOnlyPaths":
		entries, w := systemdBindPaths(key, val)
		warns = append(warns, w...)
		cfg.setMappedList("bind-read-only-paths", val, entries)
	case "TemporaryFileSystem":
		entries, w := systemdTmpfs(key, val)
		warns = append(warns, w...)
		cfg.setMappedList("temporary-filesystem", val, entries)
	case "ProtectProc":
		switch v := strings.ToLower(trimQuotes(val)); v {
		case "default", "noaccess", "invisible", "ptraceable":
			cfg.mapped = append(cfg.mapped, kvPair{"protect-proc", v})
		default:
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("ProtectProc=%s not recognised — not applied", val)})
		}
	case "ProcSubset":
		switch v := strings.ToLower(trimQuotes(val)); v {
		case "all", "pid":
			cfg.mapped = append(cfg.mapped, kvPair{"proc-subset", v})
		default:
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("ProcSubset=%s not recognised — not applied", val)})
		}
	case "PrivateNetwork":
		if truthy(val) {
			cfg.mapped = append(cfg.mapped, kvPair{"namespace-net", "yes"})
			warns = append(warns, SystemdWarning{"NOTE", "PrivateNetwork=yes → namespace-net = yes; unlike systemd, slinit leaves the namespace's loopback interface down"})
		}
	// Hardening directives slinit implements as deliberate equivalents of
	// systemd's, sharing the same value vocabulary — these translate
	// rather than earning a "look it up yourself" note.
	case "PrivateTmp", "ProtectKernelTunables", "ProtectKernelModules",
		"ProtectControlGroups", "ProtectClock", "RestrictRealtime",
		"LockPersonality", "MemoryDenyWriteExecute", "RestrictSUIDSGID",
		"ProtectKernelLogs":
		cfg.mapped = append(cfg.mapped, kvPair{systemdHardeningNames[key], boolWord(val)})
	case "ProtectSystem":
		// no|yes|full|strict on both sides.
		cfg.mapped = append(cfg.mapped, kvPair{"protect-system", strings.ToLower(trimQuotes(val))})
	case "ProtectHome":
		// no|yes|read-only|tmpfs on both sides.
		cfg.mapped = append(cfg.mapped, kvPair{"protect-home", strings.ToLower(trimQuotes(val))})
	case "RestrictNamespaces":
		// systemd also takes a list of namespace types to allow, or
		// with ~ to refuse. slinit's restrict-namespaces refuses them
		// all, which is the stricter reading of any list; leaving the
		// list out ran the service with no restriction at all.
		switch v := strings.ToLower(strings.TrimSpace(trimQuotes(val))); v {
		case "yes", "no", "true", "false", "1", "0", "on", "off":
			cfg.mapped = append(cfg.mapped, kvPair{"restrict-namespaces", boolWord(val)})
		case "", "~":
			// Empty resets; ~ with nothing refuses nothing.
			cfg.mapped = append(cfg.mapped, kvPair{"restrict-namespaces", "no"})
		default:
			cfg.mapped = append(cfg.mapped, kvPair{"restrict-namespaces", "yes"})
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("RestrictNamespaces=%s lists namespace types; "+
				"slinit's restrict-namespaces refuses every type — set to yes, stricter than the unit asks", val)})
		}
	case "RestrictAddressFamilies":
		// Same AF_* names. A ~ deny-list becomes the allow-list of
		// every other family, once the whole unit is read.
		cfg.addrFamilies.apply(val)
	case "SystemCallFilter":
		// Same grammar: syscall names, @groups, and a leading ~ on the
		// first item to switch allow-list to deny-list. Lines are
		// merged once the unit is read (mergeSyscallFilter).
		v := strings.TrimSpace(trimQuotes(val))
		if v == "" {
			cfg.syscallLines = nil
			break
		}
		invert := strings.HasPrefix(v, "~")
		cfg.syscallLines = append(cfg.syscallLines, syscallLine{invert,
			strings.FieldsFunc(strings.TrimPrefix(v, "~"), func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })})
	// The rest of the seccomp family has the same value shape on both
	// sides. These used to be dropped with a "no slinit equivalent"
	// note, or a "not recognised" warning, while slinit implements each:
	// a unit asking for them ran without them.
	case "SystemCallArchitectures":
		cfg.mapped = append(cfg.mapped, kvPair{"system-call-architectures", trimQuotes(val)})
	case "SystemCallErrorNumber":
		// kill, or any errno name — slinit knows every one.
		cfg.mapped = append(cfg.mapped, kvPair{"system-call-error-number", trimQuotes(val)})
	case "SystemCallLog":
		cfg.mapped = append(cfg.mapped, kvPair{"system-call-log", trimQuotes(val)})
	case "IOUringFilter":
		// Operation names, comma- or space-separated, ~ to deny.
		cfg.mapped = append(cfg.mapped, kvPair{"io-uring-filter", trimQuotes(val)})
	case "ProtectHostname":
		// systemd also has private[:name], a UTS namespace with its own
		// hostname; slinit's protect-hostname refuses the calls and
		// makes the hostname sysctl files read-only, which is stricter:
		// the service cannot change even a hostname of its own. Left
		// out, the service could change the host's.
		switch v := strings.ToLower(trimQuotes(val)); {
		case v == "yes" || v == "no" || v == "true" || v == "false" || v == "1" || v == "0" || v == "on" || v == "off":
			cfg.mapped = append(cfg.mapped, kvPair{"protect-hostname", boolWord(val)})
		case v == "private" || strings.HasPrefix(v, "private:"):
			cfg.mapped = append(cfg.mapped, kvPair{"protect-hostname", "yes"})
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("ProtectHostname=%s → protect-hostname = yes; "+
				"slinit has no private hostname, so the service cannot set one at all", val)})
		default:
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("ProtectHostname=%s — slinit's protect-hostname is yes/no only; set it by hand", val)})
		}
	case "PrivateDevices":
		// slinit cannot build a private /dev. Refusing the unit, as is
		// done for an empty CapabilityBoundingSet=, would stop a great
		// many upstream units from loading at all, so for now this is a
		// loud warning; whether it should refuse is an open decision.
		if truthy(val) {
			warns = append(warns, SystemdWarning{"WARN", "PrivateDevices=yes has no slinit " +
				"equivalent: the service sees every device of the host"})
		}
	// No slinit equivalent — these stay notes.
	case "PrivateUsers":
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("hardening %s=%s — no slinit equivalent; review by hand", key, val)})
	// Same directive, same value shape: a space-separated list of names
	// created under /run, /var/lib and so on. Deferring these was not a
	// missing feature, and it mattered: a unit with RuntimeDirectory=
	// relies on the directory existing before ExecStart runs, and /run
	// is a tmpfs, so without it the service starts into a directory
	// that is not there.
	case "RuntimeDirectory", "StateDirectory", "CacheDirectory", "LogsDirectory", "ConfigurationDirectory":
		cfg.mapped = append(cfg.mapped, kvPair{systemdDirNames[key], trimQuotes(val)})
	case "OOMScoreAdjust":
		// oom-score-adj, not oom-score-adjust: the note this replaces
		// named a directive that does not exist, so an operator
		// following it wrote a file slinit rejects.
		cfg.mapped = append(cfg.mapped, kvPair{"oom-score-adj", trimQuotes(val)})
	case "Nice":
		// `nice`, not `nice-level` as the old note claimed.
		cfg.mapped = append(cfg.mapped, kvPair{"nice", trimQuotes(val)})
	case "Slice":
		// This used to warn that "slinit cgroup grouping differs; review
		// manually", which was wrong on both counts: slinit's `slice=` is
		// systemd-style by construction, and the only real difference is
		// the naming convention. systemd encodes nesting in dashes, so
		// user-1000.slice lives under user.slice, where slinit takes the
		// path. Expanding one into the other is mechanical, and a unit
		// that said Slice= was silently losing its grouping until now.
		if sl := expandSystemdSlice(val); sl != "" {
			cfg.mapped = append(cfg.mapped, kvPair{"slice", sl})
		}
	case "Delegate":
		// Same grammar on both sides: yes/no or a controller list —
		// except empty, which in systemd turns delegation ON with no
		// controllers (config_parse_delegate). slinit has no such form:
		// yes would grant every controller the parent has. Leave it out
		// and say so, rather than refuse the unit or grant more.
		if v := trimQuotes(val); v != "" {
			cfg.mapped = append(cfg.mapped, kvPair{"delegate", v})
		} else {
			warns = append(warns, SystemdWarning{"WARN", "Delegate= (empty) delegates the cgroup with no controllers; " +
				"slinit has no such form — not delegated. Use `delegate = yes` or a controller list if the service needs it"})
		}
	case "WatchdogSec":
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("WatchdogSec=%s — slinit watchdog-interval maps this; add manually", val)})
	case "FileDescriptorStoreMax":
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("FileDescriptorStoreMax=%s — slinit has fd-store directives; review manually", val)})
	case "ImportCredential", "LoadCredential", "SetCredential":
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("%s=%s — credential passing needs manual wiring", key, val)})
	default:
		// Unrecognised [Service] key. Warn once so the operator
		// knows something got skipped.
		warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("[Service] `%s=%s` not mapped", key, val)})
	}
	return warns
}

// [Install] section — informational only for the conversion, but
// worth surfacing so the operator knows the systemd enable target.
func applyInstallKey(cfg *SystemdConfig, key, val string) []SystemdWarning {
	switch key {
	case "WantedBy", "RequiredBy":
		return []SystemdWarning{{"NOTE", fmt.Sprintf("[Install] %s=%s — on slinit, add to your boot service graph or run `slinitctl enable`", key, val)}}
	case "Alias":
		for _, a := range strings.Fields(val) {
			if n, ok := strings.CutSuffix(a, ".service"); ok {
				cfg.aliases = append(cfg.aliases, n)
			}
		}
		return []SystemdWarning{{"NOTE", fmt.Sprintf("[Install] Alias=%s — slinit doesn't alias services; symlink %s → %s manually", val, val, cfg.svcName)}}
	}
	return nil
}

// stripExecPrefixes handles the ExecStart= prefix chars:
//
//   - (ignore non-zero exit)
//   - (no privilege drop)
//     ! (no permission drop)
//     : (no environment substitution)
//     @ (argv[0] alias — value is command; next token is displayed argv[0])
//
// Any prefix but @ generates a NOTE so the operator knows a behavioural
// hint was in the original that isn't captured by slinit directives.
// With @, the word after the path is argv[0], not an argument: it is
// cut from the command and returned, as stripping the @ alone made it
// argv[1] and moved every argument one place.
func stripExecPrefixes(v string) (cmd, argv0 string, warns []SystemdWarning) {
	s := v
	seen := map[byte]bool{}
	for len(s) > 0 {
		c := s[0]
		switch c {
		case '-', '+', '!', ':', '@':
			if seen[c] {
				break
			}
			seen[c] = true
			s = s[1:]
			continue
		}
		break
	}
	for c := range seen {
		if c == '@' {
			continue
		}
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("ExecStart prefix %q stripped — behaviour not carried into slinit", string(c))})
	}
	if seen['@'] {
		path, rest := cutExecWord(s)
		var word string
		word, rest = cutExecWord(rest)
		s, argv0 = path+rest, trimQuotes(word)
	}
	return strings.TrimSpace(s), argv0, warns
}

// cutExecWord splits s after its first word, a quoted run counting as
// part of the word as in slinit's command parser.
func cutExecWord(s string) (word, rest string) {
	s = strings.TrimLeft(s, " \t")
	var quote byte
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == ' ' || c == '\t':
			return s[:i], s[i:]
		}
	}
	return s, ""
}

// warnArgv0 warns that the argv[0] an @ gave key's command is dropped:
// slinit's command-argv0 applies to the main command of a process or
// bgprocess service only.
func warnArgv0(key, argv0 string) []SystemdWarning {
	if argv0 == "" {
		return nil
	}
	return []SystemdWarning{{"WARN", fmt.Sprintf(
		"%s=@: argv[0] %q dropped — slinit sets argv[0] (command-argv0) only for the main command of a process or bgprocess service", key, argv0)}}
}

// warnExecVars warns about the $VAR and ${VAR} references in an Exec
// line. systemd substitutes them from Environment= and EnvironmentFile=
// when it runs the command; slinit expands them when the file is
// loaded, from slinit's own environment, so a variable the unit's
// env-file sets comes out empty. $$ is a literal $ on both sides.
func warnExecVars(key, cmd string) []SystemdWarning {
	var refs []string
	for i := 0; i < len(cmd); i++ {
		if cmd[i] != '$' || i+1 >= len(cmd) {
			continue
		}
		if cmd[i+1] == '$' {
			i++
			continue
		}
		j := i + 1
		if cmd[j] == '{' {
			if k := strings.IndexByte(cmd[j:], '}'); k >= 0 {
				j += k + 1
			}
		} else {
			for j < len(cmd) && (cmd[j] == '_' || cmd[j] >= 'A' && cmd[j] <= 'Z' ||
				cmd[j] >= 'a' && cmd[j] <= 'z' || cmd[j] >= '0' && cmd[j] <= '9') {
				j++
			}
		}
		if j > i+1 {
			refs = append(refs, cmd[i:j])
		}
		i = j - 1
	}
	if len(refs) == 0 {
		return nil
	}
	return []SystemdWarning{{"WARN", fmt.Sprintf(
		"%s= uses %s — systemd takes these from the unit's environment when it runs the command; "+
			"slinit expands them when the file is loaded, from its own environment, not the env-file, "+
			"so they are likely empty — write the values in, or run the command through a shell script that reads the file",
		key, strings.Join(refs, " "))}}
}

// killMainPID recognises `kill [-s SIG | -SIG] $MAINPID` and returns the
// signal it sends (TERM if none is named), which slinit's term-signal
// delivers to the main process the same way.
func killMainPID(cmd string) (string, bool) {
	f := strings.Fields(cmd)
	if len(f) < 2 || filepath.Base(f[0]) != "kill" ||
		(f[len(f)-1] != "$MAINPID" && f[len(f)-1] != "${MAINPID}") {
		return "", false
	}
	sig := "TERM"
	switch args := f[1 : len(f)-1]; {
	case len(args) == 0:
	case len(args) == 2 && (args[0] == "-s" || args[0] == "--signal"):
		sig = args[1]
	case len(args) == 1 && strings.HasPrefix(args[0], "-") && len(args[0]) > 1:
		sig = args[0][1:]
	default:
		return "", false
	}
	if _, err := parseSignal(sig); err != nil {
		return "", false
	}
	return sig, true
}

// splitTargets splits a whitespace-separated list of unit names and
// returns the ones that become slinit dependencies, as bare names.
func splitTargets(v string) []string {
	deps, _, _ := splitUnitRefs(v, "")
	return deps
}

// systemdUnitTypes are the unit-name suffixes systemd knows.
var systemdUnitTypes = []string{
	".service", ".socket", ".target", ".device", ".mount", ".automount",
	".swap", ".path", ".timer", ".slice", ".scope",
}

// splitUnitRefs splits a whitespace-separated list of unit names into
// the slinit service names they stand for, the references dropped
// because they name no service, and the ones naming self, the unit
// being converted.
//
// A .service is the service of that name. A .socket is too: X.socket
// activates X.service unless it says otherwise, and slinit has no
// socket units, so Requires=dm-event.socket means the dm-event service
// must run — lvm2-monitor needs it. A name with no unit suffix is kept
// as written, as it always was.
//
// slinit has no target concept at all: targets are systemd's grouping
// and ordering abstraction, and the boot service graph covers the same
// ground natively. Turning network.target into a dependency on a
// service literally called "network" is what the suffix-stripping used
// to do, and it makes almost every real-world unit unloadable — a
// missing dependency is fatal, and After=network.target appears in
// nearly all of them. Dropping the reference and saying so is the only
// honest option; the operator adds depends-on: for a service that
// actually exists on their system.
//
// The other unit types are no different: a .mount, .device, .slice,
// .path or .timer is not a service, and sysroot.mount cut down to
// "sysroot" named something else, while an escaped device name
// (dev-virtio\x2dports-...device) is not a valid service name at all,
// so the unit did not load. Neither is a name slinit cannot spell.
//
// A unit's own socket (avahi-daemon.service: Requires=avahi-daemon.socket)
// maps to the unit itself; a service cannot depend on itself, so those
// are returned apart, to be dropped with a note of their own.
func splitUnitRefs(v, self string) (deps, dropped, selfRefs []string) {
	for _, f := range strings.Fields(v) {
		name := f
		for _, suf := range systemdUnitTypes {
			if base, ok := strings.CutSuffix(f, suf); ok {
				name = base
				if suf != ".service" && suf != ".socket" {
					name = ""
				}
				break
			}
		}
		switch {
		case name == "" || ValidateServiceName(name) != nil:
			dropped = append(dropped, f)
		case self != "" && name == self:
			selfRefs = append(selfRefs, f)
		default:
			deps = append(deps, name)
		}
	}
	return deps, dropped, selfRefs
}

// noteDroppedTargets turns dropped references into one note per
// directive, so the ordering that was asked for is visible rather than
// silently gone: refs that name no service, and self, refs that name
// the unit itself.
func noteDroppedTargets(key string, targets, self []string) []SystemdWarning {
	var warns []SystemdWarning
	if len(targets) > 0 {
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf(
			"%s=%s dropped — only .service and .socket units become slinit dependencies (slinit has no targets, mounts or devices as services); "+
				"add `depends-on:`/`waits-for:` naming a real service if the ordering matters",
			key, strings.Join(targets, " "))})
	}
	if len(self) > 0 {
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf(
			"%s=%s dropped — it names this service itself (a unit's own socket is the service in slinit)",
			key, strings.Join(self, " "))})
	}
	return warns
}

// trimSec strips a trailing "s"/"sec"/"ms" unit — systemd allows
// `5`, `5s`, `5sec`. Slinit's *-delay/*-timeout directives take a
// bare integer number of seconds.
// systemdUnits is parse_time's multiplier table from systemd's
// time-util.c, in its order: suffixes match as prefixes, first hit wins,
// so "min" and "ms" are tried before "m".
var systemdUnits = []struct {
	suffix string
	usec   uint64
}{
	{"seconds", 1e6}, {"second", 1e6}, {"sec", 1e6}, {"s", 1e6},
	{"minutes", 60e6}, {"minute", 60e6}, {"min", 60e6},
	{"months", 2629800e6}, {"month", 2629800e6}, {"M", 2629800e6},
	{"msec", 1e3}, {"ms", 1e3},
	{"m", 60e6},
	{"hours", 3600e6}, {"hour", 3600e6}, {"hr", 3600e6}, {"h", 3600e6},
	{"days", 86400e6}, {"day", 86400e6}, {"d", 86400e6},
	{"weeks", 604800e6}, {"week", 604800e6}, {"w", 604800e6},
	{"years", 31557600e6}, {"year", 31557600e6}, {"y", 31557600e6},
	{"usec", 1}, {"us", 1}, {"μs", 1}, {"µs", 1},
}

// systemdSeconds translates a systemd time span (RestartSec=,
// TimeoutStopSec=) into slinit's plain decimal seconds, following
// systemd's parse_time: a bare number is seconds, and a span is a list
// of numbers with units, "1min 30s" or "5min". This replaced a helper
// that only cut a trailing "sec"/"s"/"ms", so "5min" was copied through
// and rejected, and "500ms" became "500m". "infinity" becomes inf; a
// value that is not a span is passed through for the parser to reject
// by name.
func systemdSeconds(v, inf string) string {
	v = strings.TrimSpace(v)
	if v == "infinity" && inf != "" {
		return inf
	}
	var usec uint64
	p := v
	something := false
	for {
		p = strings.TrimLeft(p, " \t")
		if p == "" {
			break
		}
		i := 0
		for i < len(p) && p[i] >= '0' && p[i] <= '9' {
			i++
		}
		if i == 0 {
			return v
		}
		whole, err := strconv.ParseUint(p[:i], 10, 64)
		if err != nil {
			return v
		}
		p = p[i:]
		frac := ""
		if strings.HasPrefix(p, ".") {
			j := 1
			for j < len(p) && p[j] >= '0' && p[j] <= '9' {
				j++
			}
			if j == 1 {
				return v
			}
			frac, p = p[1:j], p[j:]
		}
		mult := uint64(1e6)
		rest := strings.TrimLeft(p, " \t")
		for _, u := range systemdUnits {
			if strings.HasPrefix(rest, u.suffix) {
				mult, p = u.usec, rest[len(u.suffix):]
				break
			}
		}
		if whole > ^uint64(0)/mult {
			return v
		}
		usec += whole * mult
		for k, m := 0, mult/10; k < len(frac) && m > 0; k, m = k+1, m/10 {
			usec += uint64(frac[k]-'0') * m
		}
		something = true
	}
	if !something {
		return v
	}
	s := strconv.FormatUint(usec/1e6, 10)
	if r := usec % 1e6; r != 0 {
		s += strings.TrimRight(fmt.Sprintf(".%06d", r), "0")
	}
	return s
}

// setMappedList records a list directive: systemd appends each
// assignment to the list, and an empty one empties it.
func (cfg *SystemdConfig) setMappedList(slinitKey, val string, entries []string) {
	if strings.TrimSpace(trimQuotes(val)) == "" {
		kept := cfg.mapped[:0]
		for _, kv := range cfg.mapped {
			if kv.key != slinitKey {
				kept = append(kept, kv)
			}
		}
		cfg.mapped = kept
		return
	}
	if len(entries) > 0 {
		cfg.mapped = append(cfg.mapped, kvPair{slinitKey, strings.Join(entries, " ")})
	}
}

// systemdPath checks one path from a sandbox directive and strips its
// "-" prefix (ignore the path if it does not exist). ok is false, with a
// warning, for a form slinit has no equivalent of: "+" (relative to
// RootDirectory=), a specifier (%h), an escape or a variable, a relative
// path or one with "..". missingOK reports whether a "-" path that is
// not there now must be left out, as slinit would fail the start on it.
func systemdPath(key, p string, missingOK bool) (path string, ok bool, warns []SystemdWarning) {
	path = p
	ignore := false
	for strings.HasPrefix(path, "-") || strings.HasPrefix(path, "+") {
		if path[0] == '+' {
			return "", false, []SystemdWarning{{"WARN", fmt.Sprintf("%s: %q is relative to RootDirectory=, which slinit cannot express — not applied", key, p)}}
		}
		ignore = true
		path = path[1:]
	}
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "%\\$") ||
		strings.Contains("/"+path+"/", "/../") {
		return "", false, []SystemdWarning{{"WARN", fmt.Sprintf("%s: %q cannot be expressed in slinit — not applied", key, p)}}
	}
	if ignore && missingOK {
		if _, err := os.Stat(path); err != nil {
			return "", false, []SystemdWarning{{"NOTE", fmt.Sprintf("%s: %q does not exist — left out, as systemd would ignore it", key, p)}}
		}
	}
	return path, true, nil
}

// systemdPaths maps a ReadOnlyPaths=-style list.
func systemdPaths(key, val string, missingOK bool) ([]string, []SystemdWarning) {
	var out []string
	var warns []SystemdWarning
	for _, word := range strings.Fields(val) {
		p, ok, w := systemdPath(key, trimQuotes(word), missingOK)
		warns = append(warns, w...)
		if ok {
			out = append(out, p)
		}
	}
	return out, warns
}

// systemdBindPaths maps BindReadOnlyPaths= entries, "[-]src[:dst[:opts]]",
// to slinit's "src:dst". slinit binds recursively, which is systemd's
// rbind (the default); norbind has no slinit form.
func systemdBindPaths(key, val string) ([]string, []SystemdWarning) {
	var out []string
	var warns []SystemdWarning
	for _, word := range strings.Fields(val) {
		parts := strings.Split(trimQuotes(word), ":")
		if len(parts) > 3 || (len(parts) == 3 && parts[2] != "rbind") {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("%s: %q — slinit binds recursively only — not applied", key, word)})
			continue
		}
		src, ok, w := systemdPath(key, parts[0], true)
		warns = append(warns, w...)
		if !ok {
			continue
		}
		dst := src
		if len(parts) > 1 {
			if dst, ok, w = systemdPath(key, parts[1], false); !ok {
				warns = append(warns, w...)
				continue
			}
		}
		out = append(out, src+":"+dst)
	}
	return out, warns
}

// tmpfsMountFlags are TemporaryFileSystem= options that are mount flags
// rather than tmpfs options, which slinit's temporary-filesystem passes
// to tmpfs as they are; tmpfs refuses them. slinit's tmpfs is always
// nosuid and nodev.
var tmpfsMountFlags = map[string]bool{
	"nosuid": true, "suid": true, "nodev": true, "dev": true,
	"noexec": true, "exec": true, "nosymfollow": true, "symfollow": true,
	"atime": true, "noatime": true, "diratime": true, "nodiratime": true,
	"relatime": true, "norelatime": true, "strictatime": true, "nostrictatime": true,
}

// systemdTmpfs maps TemporaryFileSystem= entries, "path[:options]". The
// mount flags are left out (with a warning for noexec and nosymfollow,
// which slinit cannot apply), and systemd's default mode=0755 is made
// explicit: a tmpfs's own default is 1777.
func systemdTmpfs(key, val string) ([]string, []SystemdWarning) {
	var out []string
	var warns []SystemdWarning
	for _, word := range strings.Fields(val) {
		p, opts, _ := strings.Cut(trimQuotes(word), ":")
		path, ok, w := systemdPath(key, p, false)
		warns = append(warns, w...)
		if !ok {
			continue
		}
		var keep []string
		mode := false
		for _, o := range strings.Split(opts, ",") {
			switch {
			case o == "":
			case tmpfsMountFlags[o]:
				if o == "noexec" || o == "nosymfollow" {
					warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("%s: %s on %s — slinit's tmpfs cannot be %s; mounted without it", key, o, path, o)})
				}
			default:
				mode = mode || strings.HasPrefix(o, "mode=")
				keep = append(keep, o)
			}
		}
		if !mode {
			keep = append(keep, "mode=0755")
		}
		out = append(out, path+":"+strings.Join(keep, ","))
	}
	return out, warns
}

// allCaps is every capability slinit knows, the bounding set systemd
// starts from.
const allCaps = uint64(1)<<(process.CapCheckpointRestore+1) - 1

// capFold is a systemd capability set built up over its assignments.
type capFold struct {
	set  bool
	mask uint64
}

// apply folds one assignment in as systemd's parse_capability_set does:
// the first one (or one after the set was emptied) replaces initial, a
// later one adds to it, or with ~ takes away; an empty one empties it.
// An unknown name is ignored, as systemd ignores it.
func (c *capFold) apply(key, val string, initial uint64) []SystemdWarning {
	if !c.set {
		c.set, c.mask = true, initial
	}
	v := strings.TrimSpace(trimQuotes(val))
	if v == "" {
		c.mask = 0
		return nil
	}
	invert := strings.HasPrefix(v, "~")
	var parsed uint64
	var warns []SystemdWarning
	for _, word := range strings.Fields(strings.TrimPrefix(v, "~")) {
		n, err := process.ParseCapabilities(trimQuotes(word))
		if err != nil || len(n) != 1 {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("%s: unknown capability %q ignored", key, word)})
			continue
		}
		parsed |= 1 << n[0]
	}
	switch {
	case parsed == 0 || c.mask == initial:
		c.mask = parsed
		if invert {
			c.mask = allCaps &^ parsed
		}
	case invert:
		c.mask &^= parsed
	default:
		c.mask |= parsed
	}
	return warns
}

// syscallLine is one SystemCallFilter= assignment.
type syscallLine struct {
	invert bool
	items  []string
}

// mergeSyscallFilter turns a unit's SystemCallFilter= lines into one
// system-call-filter value, as systemd's config_parse_syscall_filter
// merges them: the first line says allow-list or (with ~) deny-list, a
// later line of the same kind adds to it, one of the other kind takes
// away. Lines of one kind join as they are. Mixed ones — the common
//
//	SystemCallFilter=@system-service
//	SystemCallFilter=~@privileged @resources
//
// — have no slinit form with groups (slinit takes ~ on the first item
// only), so the groups are expanded and the resulting set listed by
// name. An allow-list always admits @default in both; one that takes
// some of @default away cannot be written, and is an error.
func mergeSyscallFilter(lines []syscallLine) (value, note string, err error) {
	allow := !lines[0].invert
	mixed := false
	var all []string
	for _, l := range lines {
		mixed = mixed || l.invert == allow
		all = append(all, l.items...)
	}
	if !mixed {
		if allow {
			return strings.Join(all, " "), "", nil
		}
		return "~" + strings.Join(all, " "), "", nil
	}
	expand := func(item string) []string {
		if names, ok := seccomp.ExpandGroup(item); ok {
			return names
		}
		return []string{item}
	}
	set := map[string]bool{}
	if allow {
		for _, n := range expand("@default") {
			set[n] = true
		}
	}
	for _, l := range lines {
		for _, item := range l.items {
			for _, n := range expand(item) {
				set[n] = l.invert != allow
			}
		}
	}
	if allow {
		for _, n := range expand("@default") {
			if !set[n] {
				return "", "", fmt.Errorf("SystemCallFilter= takes %s out of an allow-list, which always admits "+
					"@default in slinit; the service would be allowed it", n)
			}
		}
	}
	var names []string
	for n, in := range set {
		if in {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	value = strings.Join(names, " ")
	if !allow {
		value = "~" + value
	}
	return value, "SystemCallFilter= lines that add and take away are merged into one list of syscall names", nil
}

// afFold is a systemd RestrictAddressFamilies= set built up over its
// assignments, as parse_address_families does: the first assignment
// (or the first after an empty one, or none) says allow or deny, a
// later one of the same kind adds its families, one of the other kind
// takes them away. none is an empty allow-list.
type afFold struct {
	set   bool     // the directive is in force
	alloc bool     // systemd has its set: a later line keeps its kind
	allow bool     // allow-list, else deny-list
	fams  []string // canonical names ("AF_INET"), in order
}

func (f *afFold) apply(val string) {
	v := strings.TrimSpace(trimQuotes(val))
	switch v {
	case "":
		*f = afFold{}
		return
	case "none":
		*f = afFold{set: true, allow: true}
		return
	}
	invert := strings.HasPrefix(v, "~")
	v = strings.TrimPrefix(v, "~")
	if !f.alloc {
		f.set, f.alloc, f.allow, f.fams = true, true, !invert, nil
	}
	for _, word := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		name := strings.ToUpper(word)
		if !strings.HasPrefix(name, "AF_") {
			name = "AF_" + name
		}
		i := slices.Index(f.fams, name)
		switch {
		case invert != f.allow && i < 0:
			f.fams = append(f.fams, name)
		case invert == f.allow && i >= 0:
			f.fams = slices.Delete(f.fams, i, i+1)
		}
	}
}

// complement turns a deny-list into the allow-list of every other
// family, as numbers. A family past AF_MAX is refused, where systemd's
// deny-list would let socket(2) refuse it.
func (f afFold) complement() (string, error) {
	denied := map[int]bool{}
	for _, name := range f.fams {
		n, ok := addressFamilies[name]
		if !ok {
			return "", fmt.Errorf("RestrictAddressFamilies=~ names unknown family %s; "+
				"the deny-list cannot be turned into an allow-list without it", name)
		}
		denied[n] = true
	}
	var allowed []string
	for n := 1; n < unix.AF_MAX; n++ {
		if !denied[n] {
			allowed = append(allowed, strconv.Itoa(n))
		}
	}
	return strings.Join(allowed, " "), nil
}

// addressFamilies maps the AF_* names systemd takes, upper-cased, onto
// their numbers.
var addressFamilies = map[string]int{
	"AF_UNSPEC": unix.AF_UNSPEC, "AF_UNIX": unix.AF_UNIX, "AF_LOCAL": unix.AF_LOCAL,
	"AF_INET": unix.AF_INET, "AF_AX25": unix.AF_AX25, "AF_IPX": unix.AF_IPX,
	"AF_APPLETALK": unix.AF_APPLETALK, "AF_NETROM": unix.AF_NETROM, "AF_BRIDGE": unix.AF_BRIDGE,
	"AF_ATMPVC": unix.AF_ATMPVC, "AF_X25": unix.AF_X25, "AF_INET6": unix.AF_INET6,
	"AF_ROSE": unix.AF_ROSE, "AF_DECNET": unix.AF_DECnet, "AF_NETBEUI": unix.AF_NETBEUI,
	"AF_SECURITY": unix.AF_SECURITY, "AF_KEY": unix.AF_KEY, "AF_NETLINK": unix.AF_NETLINK,
	"AF_ROUTE": unix.AF_ROUTE, "AF_PACKET": unix.AF_PACKET, "AF_ASH": unix.AF_ASH,
	"AF_ECONET": unix.AF_ECONET, "AF_ATMSVC": unix.AF_ATMSVC, "AF_RDS": unix.AF_RDS,
	"AF_SNA": unix.AF_SNA, "AF_IRDA": unix.AF_IRDA, "AF_PPPOX": unix.AF_PPPOX,
	"AF_WANPIPE": unix.AF_WANPIPE, "AF_LLC": unix.AF_LLC, "AF_IB": unix.AF_IB,
	"AF_MPLS": unix.AF_MPLS, "AF_CAN": unix.AF_CAN, "AF_TIPC": unix.AF_TIPC,
	"AF_BLUETOOTH": unix.AF_BLUETOOTH, "AF_IUCV": unix.AF_IUCV, "AF_RXRPC": unix.AF_RXRPC,
	"AF_ISDN": unix.AF_ISDN, "AF_PHONET": unix.AF_PHONET, "AF_IEEE802154": unix.AF_IEEE802154,
	"AF_CAIF": unix.AF_CAIF, "AF_ALG": unix.AF_ALG, "AF_NFC": unix.AF_NFC,
	"AF_VSOCK": unix.AF_VSOCK, "AF_KCM": unix.AF_KCM, "AF_QIPCRTR": unix.AF_QIPCRTR,
	"AF_SMC": unix.AF_SMC, "AF_XDP": unix.AF_XDP, "AF_MCTP": unix.AF_MCTP,
}

// capNames lists the capabilities in mask by name, lowest first.
func capNames(mask uint64) string {
	var names []string
	for n := uintptr(0); n <= process.CapCheckpointRestore; n++ {
		if mask&(1<<n) != 0 {
			names = append(names, process.CapabilityName(n))
		}
	}
	return strings.Join(names, " ")
}

func trimQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func truthy(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "yes" || v == "true" || v == "1" || v == "on"
}

// systemdRlimit translates a systemd Limit*= value into slinit's rlimit
// syntax. systemd writes "infinity" where slinit writes "unlimited", and
// takes the size limits (CORE, DATA, AS, MEMLOCK) with base-1024
// suffixes, as its rlimit_parse_size does: LimitMEMLOCK=64M. Copied
// through unchanged, a unit with LimitNOFILE=infinity — one of the most
// common lines in a unit — could not be loaded at all. A value this does
// not recognise is passed through for the parser to reject by name.
func systemdRlimit(val string, size bool) string {
	parts := strings.SplitN(val, ":", 2)
	for i, p := range parts {
		p = strings.TrimSpace(p)
		parts[i] = p
		if p == "infinity" {
			parts[i] = "unlimited"
			continue
		}
		if !size || p == "" {
			continue
		}
		var shift uint
		switch p[len(p)-1] {
		case 'K':
			shift = 10
		case 'M':
			shift = 20
		case 'G':
			shift = 30
		case 'T':
			shift = 40
		case 'P':
			shift = 50
		case 'E':
			shift = 60
		case 'B':
		default:
			continue
		}
		n, err := strconv.ParseUint(p[:len(p)-1], 10, 64)
		if err != nil || n > ^uint64(0)>>shift {
			continue
		}
		parts[i] = strconv.FormatUint(n<<shift, 10)
	}
	return strings.Join(parts, ":")
}

// --- Emitter ---

func EmitSlinitFile(w io.Writer, c *SystemdConfig) {
	for _, cm := range c.comments {
		fmt.Fprintf(w, "# %s\n", cm)
	}
	fmt.Fprintln(w)
	if c.description != "" {
		fmt.Fprintf(w, "description = %s\n", c.description)
	}
	fmt.Fprintf(w, "type = %s\n", c.svcType)
	if c.command != "" {
		fmt.Fprintf(w, "command = %s\n", c.command)
	}
	if c.argv0 != "" {
		fmt.Fprintf(w, "command-argv0 = %s\n", c.argv0)
	}
	if c.stopCommand != "" {
		fmt.Fprintf(w, "stop-command = %s\n", c.stopCommand)
	}
	if c.preStart != "" {
		fmt.Fprintf(w, "pre-start-command = %s\n", c.preStart)
	}
	if c.postStart != "" {
		fmt.Fprintf(w, "post-start-command = %s\n", c.postStart)
	}
	if c.runAs != "" {
		fmt.Fprintf(w, "run-as = %s\n", c.runAs)
	}
	if c.envFile != "" {
		fmt.Fprintf(w, "env-file = %s\n", c.envFile)
	}
	if c.workingDir != "" {
		fmt.Fprintf(w, "working-dir = %s\n", c.workingDir)
	}
	if c.chroot != "" {
		fmt.Fprintf(w, "chroot = %s\n", c.chroot)
	}
	if c.pidFile != "" {
		fmt.Fprintf(w, "pid-file = %s\n", c.pidFile)
	}
	if c.termSignal != "" {
		fmt.Fprintf(w, "term-signal = %s\n", c.termSignal)
	}
	if c.umask != "" {
		fmt.Fprintf(w, "umask = %s\n", c.umask)
	}
	if c.noNewPrivs {
		// An options member, not a setting of its own. Emitting
		// `no-new-privs = yes` produced a file the parser rejects with
		// "unknown setting", so every converted unit that set
		// NoNewPrivileges was unloadable.
		fmt.Fprintln(w, "options = no-new-privs")
	}
	if c.closeStdin {
		fmt.Fprintln(w, "close-stdin = yes")
	}
	for _, h := range c.mapped {
		fmt.Fprintf(w, "%s = %s\n", h.key, h.value)
	}
	if c.restart != "" {
		fmt.Fprintf(w, "restart = %s\n", c.restart)
	}
	if c.startWhenIdle {
		fmt.Fprintln(w, "start-when-idle = yes")
	}
	if c.restartDelay != "" {
		fmt.Fprintf(w, "restart-delay = %s\n", c.restartDelay)
	}
	if c.stopTimeout != "" {
		fmt.Fprintf(w, "stop-timeout = %s\n", c.stopTimeout)
	}
	if c.rlimitNofile != "" {
		fmt.Fprintf(w, "rlimit-nofile = %s\n", c.rlimitNofile)
	}
	if c.rlimitCore != "" {
		fmt.Fprintf(w, "rlimit-core = %s\n", c.rlimitCore)
	}
	if c.rlimitData != "" {
		fmt.Fprintf(w, "rlimit-data = %s\n", c.rlimitData)
	}
	if c.rlimitAS != "" {
		fmt.Fprintf(w, "rlimit-as = %s\n", c.rlimitAS)
	}
	if c.rlimitMemlock != "" {
		fmt.Fprintf(w, "rlimit-memlock = %s\n", c.rlimitMemlock)
	}
	for _, cnd := range c.conditions {
		fmt.Fprintf(w, "%s = %s\n", cnd.name, cnd.value)
	}
	for _, d := range c.depends {
		fmt.Fprintf(w, "depends-on: %s\n", d)
	}
	for _, d := range c.waitsFor {
		fmt.Fprintf(w, "waits-for: %s\n", d)
	}
}

// --- Direct loading (no intermediate file) ---

// DefaultSystemdDirs are searched for a .service unit when no native
// slinit description exists, in systemd's own precedence order: local
// admin config wins over runtime-generated, which wins over packaged.
var DefaultSystemdDirs = []string{
	"/etc/systemd/system",
	"/run/systemd/system",
	"/usr/lib/systemd/system",
	"/lib/systemd/system",
}

// OnSystemdUnitWarning, when set, receives every conversion note raised
// while loading a unit directly. The converter prints these to a
// terminal; loading one live has no terminal, so they would otherwise
// vanish — and a silently dropped hardening directive is exactly the
// kind of thing an operator must be told about. Mirrors
// OnDeprecatedDirective.
var OnSystemdUnitWarning func(service, unitPath, level, message string)

// IsSystemdUnit reports whether path looks like a .service unit slinit
// can load. Only regular files ending in .service qualify: the other
// unit types are systemd abstractions with no mechanical translation,
// and quietly treating a .timer as a service would produce something
// that starts but never does what the operator asked.
func IsSystemdUnit(path string) bool {
	if !strings.HasSuffix(path, ".service") {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// systemdUnitMasked reports whether path masks its unit, as systemctl
// mask does: a symlink to /dev/null, or an empty file. A mask ends the
// search; the unit is not loaded from any later directory.
func systemdUnitMasked(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	if fi.Mode().IsRegular() {
		return fi.Size() == 0
	}
	null, err := os.Stat("/dev/null")
	return err == nil && os.SameFile(fi, null)
}

// SystemdUnitToServiceDescription reads a .service unit and returns the
// description slinit would run, without writing anything to disk.
//
// It converts, emits, and then runs the production parser over the
// result rather than assembling a description field by field. That
// keeps one mapping in the tree instead of two that drift, and the
// description picks up every default Parse applies — the same reason
// InitDToServiceDescription routes through NewServiceDescription.
func SystemdUnitToServiceDescription(unitPath, name string) (*ServiceDescription, error) {
	cfg, warns, err := ConvertSystemdUnit(unitPath)
	if err != nil {
		return nil, fmt.Errorf("systemd unit %q: %w", unitPath, err)
	}

	if OnSystemdUnitWarning != nil {
		for _, w := range warns {
			OnSystemdUnitWarning(name, unitPath, w.Level, w.Msg)
		}
	}

	var buf bytes.Buffer
	EmitSlinitFile(&buf, cfg)

	desc, err := Parse(bytes.NewReader(buf.Bytes()), name, unitPath)
	if err != nil {
		// The converter produced something slinit cannot load. That is a
		// converter bug rather than operator error, so say so plainly and
		// include the text: without it the operator sees a parse error
		// pointing at a file that has no such line, because the file they
		// wrote is a systemd unit.
		return nil, fmt.Errorf("systemd unit %q converted to a service slinit cannot load: %w\n--- converted ---\n%s",
			unitPath, err, buf.String())
	}
	return desc, nil
}

// dropMissingSystemdRefs leaves out the waits-for: names of a unit
// loaded directly that resolve to nothing, with a note. They come from
// After= and Wants=, which systemd ignores for a unit that is not
// installed; slinit fails the load on a missing dependency of any kind,
// so display-manager.service, which orders itself after ypbind.service,
// could not load anywhere ypbind was not installed. Only the systemd
// path does this: a native waits-for: on a missing service stays fatal,
// and so does a Requires= (depends-on:), as systemd fails that unit too.
func (dl *DirLoader) dropMissingSystemdRefs(desc *ServiceDescription, name, unitPath string) {
	keep := func(key string, refs []string) []string {
		var out []string
		for _, r := range refs {
			if dl.descriptionExists(r) {
				out = append(out, r)
			} else if OnSystemdUnitWarning != nil {
				OnSystemdUnitWarning(name, unitPath, "NOTE", fmt.Sprintf(
					"%s %s left out: no such service or unit (systemd ignores it too)", key, r))
			}
		}
		return out
	}
	desc.WaitsFor = keep("waits-for:", desc.WaitsFor)
}

// descriptionExists reports whether findAndParse would find a
// description for name — a loaded service, a service file, an init.d
// script or a .service unit — without parsing it, which could recurse
// through a unit that orders itself against this one.
func (dl *DirLoader) descriptionExists(name string) bool {
	if dl.set.FindService(name, false) != nil {
		return true
	}
	names := []string{name}
	if i := strings.IndexByte(name, '@'); i >= 0 {
		names = append(names, name[:i])
	}
	for _, dir := range dl.dirs {
		for _, n := range names {
			if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
				return true
			}
		}
	}
	for _, dir := range dl.initDirs {
		if IsInitDScript(filepath.Join(dir, name)) {
			return true
		}
	}
	unitName := name
	if !strings.HasSuffix(unitName, ".service") {
		unitName += ".service"
	}
	for _, dir := range dl.systemdDirs {
		p := filepath.Join(dir, unitName)
		if systemdUnitMasked(p) {
			return false
		}
		if IsSystemdUnit(p) {
			return true
		}
	}
	return false
}

// expandSystemdSlice turns a systemd slice name into the path slinit's
// `slice=` takes.
//
// systemd encodes the hierarchy in the name: a-b-c.slice is a child of
// a-b.slice, which is a child of a.slice. slinit takes the path directly,
// so the dashes become the levels they stand for. "-.slice" is systemd's
// name for the root and means no grouping at all, which is the empty
// string here.
func expandSystemdSlice(val string) string {
	name := strings.TrimSpace(trimQuotes(val))
	name = strings.TrimSuffix(name, ".slice")
	if name == "" || name == "-" {
		return ""
	}
	parts := strings.Split(name, "-")
	segs := make([]string, 0, len(parts))
	for i := range parts {
		seg := strings.Join(parts[:i+1], "-")
		if seg == "" {
			return ""
		}
		segs = append(segs, seg+".slice")
	}
	return strings.Join(segs, "/")
}

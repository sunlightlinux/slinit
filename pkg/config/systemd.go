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
	"strings"
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
	svcName      string
	unitPath     string
	svcType      string // process | bgprocess | scripted
	command      string
	stopCommand  string
	preStart     string // slinit takes one; multiples get warned
	postStart    string
	runAs        string
	envFile      string
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

	description string   // [Unit] Description=
	hardening   []kvPair // protect-*, restrict-*, private-tmp, ...
	depends     []string // depends-on: X
	waitsFor    []string // waits-for: X
	conditions  []condDir
	comments    []string
}

// kvPair is one emitted "key = value" line, kept ordered so the output
// is stable across runs.
type kvPair struct{ key, value string }

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
		// isn't standard here but be tolerant).
		val = trimQuotes(val)

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
		cfg.waitsFor = append(cfg.waitsFor, splitTargets(val)...)
	case "Before":
		warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("Unit `Before=%s` not mappable — invert on the target side", val)})
	case "Requires", "Requisite":
		cfg.depends = append(cfg.depends, splitTargets(val)...)
	case "Wants":
		cfg.waitsFor = append(cfg.waitsFor, splitTargets(val)...)
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
			warns = append(warns, SystemdWarning{"NOTE", "Type=idle — slinit has no idle-wait; treated as process"})
		default:
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("Type=%s not recognised", val)})
		}
	case "ExecStart":
		cmd, w := stripExecPrefixes(val)
		cfg.command = cmd
		warns = append(warns, w...)
	case "ExecStop":
		cmd, w := stripExecPrefixes(val)
		cfg.stopCommand = cmd
		warns = append(warns, w...)
	case "ExecStartPre":
		*preN++
		if *preN > 1 {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("multiple ExecStartPre= — slinit takes one pre-start-command, keeping the first (dropped: %q)", val)})
		} else {
			cmd, w := stripExecPrefixes(val)
			cfg.preStart = cmd
			warns = append(warns, w...)
		}
	case "ExecStartPost":
		*postN++
		if *postN > 1 {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("multiple ExecStartPost= — slinit takes one post-start-command, keeping the first (dropped: %q)", val)})
		} else {
			cmd, w := stripExecPrefixes(val)
			cfg.postStart = cmd
			warns = append(warns, w...)
		}
	case "ExecStopPost":
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("ExecStopPost=%s — slinit has no equivalent; wire via finish-command manually", val)})
	case "ExecReload":
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("ExecReload=%s — slinit reload is signal-based; use reload-signal directive", val)})
	case "User":
		if cfg.runAs == "" {
			cfg.runAs = val
		} else {
			// User was set via Group first; assemble.
			cfg.runAs = val + ":" + cfg.runAs
		}
	case "Group":
		if cfg.runAs == "" {
			cfg.runAs = ":" + val
		} else if !strings.Contains(cfg.runAs, ":") {
			cfg.runAs = cfg.runAs + ":" + val
		}
	case "WorkingDirectory":
		cfg.workingDir = val
	case "RootDirectory":
		cfg.chroot = val
	case "EnvironmentFile":
		// Systemd tolerates a leading "-" for optional; drop it.
		cfg.envFile = strings.TrimPrefix(val, "-")
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
			warns = append(warns, SystemdWarning{"NOTE", "Restart=on-success is unusual; treated as restart = yes"})
			cfg.restart = "yes"
		case "on-watchdog":
			warns = append(warns, SystemdWarning{"NOTE", "Restart=on-watchdog — slinit uses watchdog directives; map manually"})
			cfg.restart = "on-failure"
		default:
			cfg.restart = "on-failure"
			warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("Restart=%s → on-failure", val)})
		}
	case "RestartSec":
		cfg.restartDelay = trimSec(val)
	case "TimeoutStopSec", "TimeoutSec":
		cfg.stopTimeout = trimSec(val)
	case "TimeoutStartSec":
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("TimeoutStartSec=%s — slinit start-timeout maps this; add manually", val)})
	case "PIDFile":
		cfg.pidFile = val
	case "NoNewPrivileges":
		cfg.noNewPrivs = truthy(val)
	case "UMask":
		cfg.umask = val
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
		cfg.rlimitNofile = val
	case "LimitCORE":
		cfg.rlimitCore = val
	case "LimitDATA":
		cfg.rlimitData = val
	case "LimitAS":
		cfg.rlimitAS = val
	case "CapabilityBoundingSet", "AmbientCapabilities":
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("%s=%s — map to slinit capabilities / ambient-caps directives manually", key, val)})
	// Hardening directives slinit implements as deliberate equivalents of
	// systemd's, sharing the same value vocabulary — these translate
	// rather than earning a "look it up yourself" note.
	case "PrivateTmp", "ProtectKernelTunables", "ProtectKernelModules",
		"ProtectControlGroups", "ProtectClock", "RestrictRealtime",
		"LockPersonality", "MemoryDenyWriteExecute":
		cfg.hardening = append(cfg.hardening, kvPair{systemdHardeningNames[key], boolWord(val)})
	case "ProtectSystem":
		// no|yes|full|strict on both sides.
		cfg.hardening = append(cfg.hardening, kvPair{"protect-system", strings.ToLower(trimQuotes(val))})
	case "ProtectHome":
		// no|yes|read-only|tmpfs on both sides.
		cfg.hardening = append(cfg.hardening, kvPair{"protect-home", strings.ToLower(trimQuotes(val))})
	case "RestrictNamespaces":
		// systemd also accepts a list of namespace types to deny;
		// slinit's directive is a blanket yes/no, so a list would
		// silently become something stricter than asked for.
		if v := strings.ToLower(trimQuotes(val)); v == "yes" || v == "no" || v == "true" || v == "false" {
			cfg.hardening = append(cfg.hardening, kvPair{"restrict-namespaces", boolWord(val)})
		} else {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("RestrictNamespaces=%s lists namespace types; slinit's restrict-namespaces is all-or-nothing — set it by hand", val)})
		}
	case "RestrictAddressFamilies":
		// Same AF_* token list. systemd's leading ~ inverts to a
		// deny-list; slinit's is an allow-list only, so inverting
		// would reverse the operator's intent.
		if strings.HasPrefix(strings.TrimSpace(val), "~") {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("RestrictAddressFamilies=%s is a deny-list; slinit's restrict-address-families only allow-lists — invert it by hand", val)})
		} else {
			cfg.hardening = append(cfg.hardening, kvPair{"restrict-address-families", trimQuotes(val)})
		}
	case "SystemCallFilter":
		// Same grammar: syscall names, @groups, and a leading ~ on the
		// first item to switch allow-list to deny-list.
		cfg.hardening = append(cfg.hardening, kvPair{"system-call-filter", trimQuotes(val)})
	// No slinit equivalent — these stay notes.
	case "PrivateDevices", "PrivateNetwork", "PrivateUsers", "RestrictSUIDSGID",
		"SystemCallArchitectures", "SystemCallErrorNumber":
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("hardening %s=%s — no slinit equivalent; review by hand", key, val)})
	case "RuntimeDirectory", "StateDirectory", "CacheDirectory", "LogsDirectory", "ConfigurationDirectory":
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("%s=%s — slinit provides runtime-dir / state-dir directives; add manually", key, val)})
	case "OOMScoreAdjust":
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("OOMScoreAdjust=%s — slinit has oom-score-adjust directive; add manually", val)})
	case "Nice":
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("Nice=%s — slinit has nice-level directive; add manually", val)})
	case "Slice":
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("Slice=%s — slinit cgroup grouping differs; review manually", val)})
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
// Any prefix generates a NOTE so the operator knows a behavioural
// hint was in the original that isn't captured by slinit directives.
func stripExecPrefixes(v string) (string, []SystemdWarning) {
	var warns []SystemdWarning
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
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("ExecStart prefix %q stripped — behaviour not carried into slinit", string(c))})
	}
	return strings.TrimSpace(s), warns
}

// splitTargets splits a whitespace-separated list of unit names and
// normalises each by stripping the systemd unit-type suffix. slinit
// deps are on bare names — the unit type is a systemd abstraction
// (socket/path/mount/timer are all reified as their own slinit
// mechanisms rather than as separate service files).
func splitTargets(v string) []string {
	suffixes := []string{".service", ".target", ".socket", ".path", ".mount", ".timer", ".swap", ".device"}
	fields := strings.Fields(v)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		for _, s := range suffixes {
			if strings.HasSuffix(f, s) {
				f = strings.TrimSuffix(f, s)
				break
			}
		}
		out = append(out, f)
	}
	return out
}

// trimSec strips a trailing "s"/"sec"/"ms" unit — systemd allows
// `5`, `5s`, `5sec`. Slinit's *-delay/*-timeout directives take a
// bare integer number of seconds.
func trimSec(v string) string {
	v = strings.TrimSpace(v)
	for _, suf := range []string{"sec", "s", "ms"} {
		if strings.HasSuffix(v, suf) {
			v = strings.TrimSuffix(v, suf)
		}
	}
	return strings.TrimSpace(v)
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
	for _, h := range c.hardening {
		fmt.Fprintf(w, "%s = %s\n", h.key, h.value)
	}
	if c.restart != "" {
		fmt.Fprintf(w, "restart = %s\n", c.restart)
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

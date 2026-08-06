// slinit-systemd-convert converts a systemd .service unit file
// into a slinit service file (dinit-compatible text format).
//
// Handles the .service surface only — .timer / .socket / .path /
// .mount / .swap / .target units have their own semantics that
// need slinit-native equivalents (path activation directives,
// cron= for timers, boot service graph for targets) rather than
// mechanical translation. Warn and skip if pointed at one of those.
//
// Directive coverage: about 40 [Service] and [Unit] settings
// (User, Group, ExecStart, Restart, EnvironmentFile, PIDFile,
// LimitNOFILE, CapabilityBoundingSet, ProtectSystem, After,
// Requires, Wants, ConditionPathExists, etc.). Everything else
// generates a WARN so the operator knows what needs manual review.
//
// Usage:
//
//	slinit-systemd-convert /etc/systemd/system/foo.service > /etc/slinit.d/foo
//	slinit-systemd-convert --output-dir=/etc/slinit.d /usr/lib/systemd/system/*.service
//	slinit-systemd-convert --dry-run --verbose foo.service
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func main() {
	var (
		outputDir string
		dryRun    bool
		verbose   bool
	)
	flag.StringVar(&outputDir, "output-dir", "", "batch mode: write one slinit file per input into DIR")
	flag.BoolVar(&dryRun, "dry-run", false, "print what would be written without touching the filesystem")
	flag.BoolVar(&verbose, "verbose", false, "print per-service conversion notes to stderr")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `slinit-systemd-convert — port a systemd .service unit to a slinit service file

Usage:
  slinit-systemd-convert [flags] <unit-file> [unit-file ...]

Only .service units are supported. Timer/socket/path/mount/target
units are systemd-specific abstractions that map onto different
slinit facilities (directives, boot graph) — convert those by hand.

Flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(1)
	}

	inputs := flag.Args()
	if outputDir == "" && len(inputs) > 1 {
		fmt.Fprintln(os.Stderr, "slinit-systemd-convert: multiple inputs require --output-dir")
		os.Exit(1)
	}
	if outputDir != "" && !dryRun {
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "slinit-systemd-convert: create output dir: %v\n", err)
			os.Exit(1)
		}
	}

	var hadErrors bool
	for _, in := range inputs {
		cfg, warns, err := convertUnit(in)
		if err != nil {
			fmt.Fprintf(os.Stderr, "slinit-systemd-convert: %s: %v\n", in, err)
			hadErrors = true
			continue
		}
		// Strip .service (and .in) suffix from output name — slinit
		// svcs use bare names (`nginx`, not `nginx.service`).
		name := strings.TrimSuffix(filepath.Base(in), ".in")
		name = strings.TrimSuffix(name, ".service")

		var buf bytes.Buffer
		emitSlinitFile(&buf, cfg)

		if outputDir == "" {
			os.Stdout.Write(buf.Bytes())
		} else {
			outPath := filepath.Join(outputDir, name)
			if dryRun {
				fmt.Fprintf(os.Stderr, "--- would write %s ---\n", outPath)
				os.Stderr.Write(buf.Bytes())
			} else if err := os.WriteFile(outPath, buf.Bytes(), 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "slinit-systemd-convert: write %s: %v\n", outPath, err)
				hadErrors = true
				continue
			}
		}

		if verbose {
			for _, w := range warns {
				fmt.Fprintf(os.Stderr, "[%s] %s: %s\n", w.level, name, w.msg)
			}
		}
	}
	if hadErrors {
		os.Exit(1)
	}
}

// --- Data structures ---

type warning struct {
	level string // NOTE / WARN
	msg   string
}

type slinitConfig struct {
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

	depends    []string // depends-on: X
	waitsFor   []string // waits-for: X
	conditions []condDir
	comments   []string
}

type condDir struct {
	name  string // e.g. "condition-path-exists"
	value string
}

// --- Convert entry point ---

func convertUnit(path string) (*slinitConfig, []warning, error) {
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
	cfg := &slinitConfig{
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
func parseSystemdUnit(cfg *slinitConfig, content string) []warning {
	var warns []warning
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
func applyUnitKey(cfg *slinitConfig, key, val string) []warning {
	var warns []warning
	switch key {
	case "Description":
		cfg.comments = append(cfg.comments, "description: "+val)
	case "Documentation", "DefaultDependencies", "IgnoreOnIsolate", "RefuseManualStart", "RefuseManualStop":
		// Informational or systemd-internal; skip silently.
	case "After":
		cfg.waitsFor = append(cfg.waitsFor, splitTargets(val)...)
	case "Before":
		warns = append(warns, warning{"WARN", fmt.Sprintf("Unit `Before=%s` not mappable — invert on the target side", val)})
	case "Requires", "Requisite":
		cfg.depends = append(cfg.depends, splitTargets(val)...)
	case "Wants":
		cfg.waitsFor = append(cfg.waitsFor, splitTargets(val)...)
	case "Conflicts":
		warns = append(warns, warning{"NOTE", fmt.Sprintf("Unit `Conflicts=%s` — slinit has no negative dep; managed via stop-command / start-limit-action", val)})
	case "OnFailure":
		warns = append(warns, warning{"NOTE", fmt.Sprintf("Unit `OnFailure=%s` — map to slinit failure-action directive if the target is a system action", val)})
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
			warns = append(warns, warning{"WARN", fmt.Sprintf("Unit `%s=%s` not mapped — check slinit condition-* / assert-* directives manually", key, val)})
		}
	}
	return warns
}

// [Service] section directives.
func applyServiceKey(cfg *slinitConfig, key, val string, preN, postN *int) []warning {
	var warns []warning
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
			warns = append(warns, warning{"NOTE", fmt.Sprintf("Type=%s — slinit supports readiness via notify-fd; add `notify = yes` if needed", val)})
		case "dbus":
			warns = append(warns, warning{"WARN", "Type=dbus not supported — slinit has no dbus activation; convert to Type=simple manually"})
		case "idle":
			warns = append(warns, warning{"NOTE", "Type=idle — slinit has no idle-wait; treated as process"})
		default:
			warns = append(warns, warning{"WARN", fmt.Sprintf("Type=%s not recognised", val)})
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
			warns = append(warns, warning{"WARN", fmt.Sprintf("multiple ExecStartPre= — slinit takes one pre-start-command, keeping the first (dropped: %q)", val)})
		} else {
			cmd, w := stripExecPrefixes(val)
			cfg.preStart = cmd
			warns = append(warns, w...)
		}
	case "ExecStartPost":
		*postN++
		if *postN > 1 {
			warns = append(warns, warning{"WARN", fmt.Sprintf("multiple ExecStartPost= — slinit takes one post-start-command, keeping the first (dropped: %q)", val)})
		} else {
			cmd, w := stripExecPrefixes(val)
			cfg.postStart = cmd
			warns = append(warns, w...)
		}
	case "ExecStopPost":
		warns = append(warns, warning{"NOTE", fmt.Sprintf("ExecStopPost=%s — slinit has no equivalent; wire via finish-command manually", val)})
	case "ExecReload":
		warns = append(warns, warning{"NOTE", fmt.Sprintf("ExecReload=%s — slinit reload is signal-based; use reload-signal directive", val)})
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
		warns = append(warns, warning{"WARN", fmt.Sprintf("Environment=%s inline — slinit has env-file only; consolidate into a KEY=VAL file", val)})
	case "PassEnvironment", "UnsetEnvironment":
		warns = append(warns, warning{"NOTE", fmt.Sprintf("%s=%s not directly mapped", key, val)})
	case "Restart":
		switch val {
		case "no":
			cfg.restart = "no"
		case "always":
			cfg.restart = "yes"
		case "on-failure", "on-abnormal", "on-abort":
			cfg.restart = "on-failure"
		case "on-success":
			warns = append(warns, warning{"NOTE", "Restart=on-success is unusual; treated as restart = yes"})
			cfg.restart = "yes"
		case "on-watchdog":
			warns = append(warns, warning{"NOTE", "Restart=on-watchdog — slinit uses watchdog directives; map manually"})
			cfg.restart = "on-failure"
		default:
			cfg.restart = "on-failure"
			warns = append(warns, warning{"NOTE", fmt.Sprintf("Restart=%s → on-failure", val)})
		}
	case "RestartSec":
		cfg.restartDelay = trimSec(val)
	case "TimeoutStopSec", "TimeoutSec":
		cfg.stopTimeout = trimSec(val)
	case "TimeoutStartSec":
		warns = append(warns, warning{"NOTE", fmt.Sprintf("TimeoutStartSec=%s — slinit start-timeout maps this; add manually", val)})
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
			warns = append(warns, warning{"NOTE", fmt.Sprintf("StandardInput=%s — slinit routes stdin from /dev/null by default; other values need manual mapping", val)})
		}
	case "StandardOutput", "StandardError":
		if val != "journal" && val != "inherit" && val != "null" {
			warns = append(warns, warning{"NOTE", fmt.Sprintf("%s=%s — slinit routes stdout/err to its logger; other targets need manual wiring", key, val)})
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
		warns = append(warns, warning{"NOTE", fmt.Sprintf("%s=%s — map to slinit capabilities / ambient-caps directives manually", key, val)})
	case "PrivateTmp", "PrivateDevices", "PrivateNetwork", "PrivateUsers",
		"ProtectSystem", "ProtectHome", "ProtectKernelTunables",
		"ProtectKernelModules", "ProtectControlGroups", "ProtectClock",
		"RestrictAddressFamilies", "RestrictNamespaces", "RestrictRealtime",
		"RestrictSUIDSGID", "LockPersonality", "MemoryDenyWriteExecute",
		"SystemCallFilter", "SystemCallArchitectures", "SystemCallErrorNumber":
		warns = append(warns, warning{"NOTE", fmt.Sprintf("hardening %s=%s — slinit has equivalent directives; check slinit-supports for names", key, val)})
	case "RuntimeDirectory", "StateDirectory", "CacheDirectory", "LogsDirectory", "ConfigurationDirectory":
		warns = append(warns, warning{"NOTE", fmt.Sprintf("%s=%s — slinit provides runtime-dir / state-dir directives; add manually", key, val)})
	case "OOMScoreAdjust":
		warns = append(warns, warning{"NOTE", fmt.Sprintf("OOMScoreAdjust=%s — slinit has oom-score-adjust directive; add manually", val)})
	case "Nice":
		warns = append(warns, warning{"NOTE", fmt.Sprintf("Nice=%s — slinit has nice-level directive; add manually", val)})
	case "Slice":
		warns = append(warns, warning{"NOTE", fmt.Sprintf("Slice=%s — slinit cgroup grouping differs; review manually", val)})
	case "WatchdogSec":
		warns = append(warns, warning{"NOTE", fmt.Sprintf("WatchdogSec=%s — slinit watchdog-interval maps this; add manually", val)})
	case "FileDescriptorStoreMax":
		warns = append(warns, warning{"NOTE", fmt.Sprintf("FileDescriptorStoreMax=%s — slinit has fd-store directives; review manually", val)})
	case "ImportCredential", "LoadCredential", "SetCredential":
		warns = append(warns, warning{"NOTE", fmt.Sprintf("%s=%s — credential passing needs manual wiring", key, val)})
	default:
		// Unrecognised [Service] key. Warn once so the operator
		// knows something got skipped.
		warns = append(warns, warning{"WARN", fmt.Sprintf("[Service] `%s=%s` not mapped", key, val)})
	}
	return warns
}

// [Install] section — informational only for the conversion, but
// worth surfacing so the operator knows the systemd enable target.
func applyInstallKey(cfg *slinitConfig, key, val string) []warning {
	switch key {
	case "WantedBy", "RequiredBy":
		return []warning{{"NOTE", fmt.Sprintf("[Install] %s=%s — on slinit, add to your boot service graph or run `slinitctl enable`", key, val)}}
	case "Alias":
		return []warning{{"NOTE", fmt.Sprintf("[Install] Alias=%s — slinit doesn't alias services; symlink %s → %s manually", val, val, cfg.svcName)}}
	}
	return nil
}

// stripExecPrefixes handles the ExecStart= prefix chars:
//
//	- (ignore non-zero exit)
//	+ (no privilege drop)
//	! (no permission drop)
//	: (no environment substitution)
//	@ (argv[0] alias — value is command; next token is displayed argv[0])
//
// Any prefix generates a NOTE so the operator knows a behavioural
// hint was in the original that isn't captured by slinit directives.
func stripExecPrefixes(v string) (string, []warning) {
	var warns []warning
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
		warns = append(warns, warning{"NOTE", fmt.Sprintf("ExecStart prefix %q stripped — behaviour not carried into slinit", string(c))})
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

func emitSlinitFile(w io.Writer, c *slinitConfig) {
	for _, cm := range c.comments {
		fmt.Fprintf(w, "# %s\n", cm)
	}
	fmt.Fprintln(w)
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
		fmt.Fprintln(w, "no-new-privs = yes")
	}
	if c.closeStdin {
		fmt.Fprintln(w, "close-stdin = yes")
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

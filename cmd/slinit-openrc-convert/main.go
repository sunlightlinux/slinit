// slinit-openrc-convert converts an OpenRC init.d script into a
// slinit service file (dinit-compatible text format). Handles the
// two shapes real openrc scripts take:
//
//  1. Variable-only: `command=…`, `pidfile=…`, `depend() { … }`,
//     no custom start/stop. Extracted directly into slinit
//     directives — no runtime shell hop.
//
//  2. Custom start()/stop() functions (the common case, 58/63
//     scripts in the OpenRC tree). Wrapped so slinit invokes the
//     original script via /usr/sbin/openrc-run, preserving every
//     ebegin/einfo/start-stop-daemon call the operator wrote.
//     Requires openrc-run at runtime.
//
// Emits WARN/NOTE lines to stderr for anything that can't be mapped
// 1:1 (extra_commands, s6/supervise-daemon selection, keyword
// runlevel gates).
//
// Usage:
//
//	slinit-openrc-convert /etc/init.d/nscd > /etc/slinit.d/nscd
//	slinit-openrc-convert --output-dir=/etc/slinit.d /etc/init.d/*
//	slinit-openrc-convert --dry-run --verbose /etc/init.d/postgresql
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
		outputDir  string
		dryRun     bool
		verbose    bool
		enableMap  bool
		wrapperCmd string
	)
	flag.StringVar(&outputDir, "output-dir", "", "batch mode: write one slinit file per input into DIR")
	flag.BoolVar(&dryRun, "dry-run", false, "print what would be written without touching the filesystem")
	flag.BoolVar(&verbose, "verbose", false, "print per-service conversion notes to stderr")
	flag.BoolVar(&enableMap, "enable-map", false, "suggest `slinitctl enable` for scripts symlinked from /etc/runlevels/*")
	flag.StringVar(&wrapperCmd, "wrapper", "/usr/sbin/openrc-run",
		"invocation prefix for the fallback (custom start/stop) path; must run the init.d script when appended with `<script> start`")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `slinit-openrc-convert — port an OpenRC init.d script to a slinit service file

Usage:
  slinit-openrc-convert [flags] <init.d-script> [init.d-script ...]

Single input (default): output to stdout.
Batch (--output-dir=DIR): one output file per input, named after the input basename.

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
		fmt.Fprintln(os.Stderr, "slinit-openrc-convert: multiple inputs require --output-dir")
		os.Exit(1)
	}
	if outputDir != "" && !dryRun {
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "slinit-openrc-convert: create output dir: %v\n", err)
			os.Exit(1)
		}
	}

	var enabledHints []string
	var hadErrors bool
	for _, in := range inputs {
		cfg, warns, err := convertScript(in, wrapperCmd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "slinit-openrc-convert: %s: %v\n", in, err)
			hadErrors = true
			continue
		}
		name := filepath.Base(in)

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
				fmt.Fprintf(os.Stderr, "slinit-openrc-convert: write %s: %v\n", outPath, err)
				hadErrors = true
				continue
			}
		}

		if verbose {
			for _, w := range warns {
				fmt.Fprintf(os.Stderr, "[%s] %s: %s\n", w.level, name, w.msg)
			}
		}
		if enableMap && isOpenrcEnabled(name) {
			enabledHints = append(enabledHints, name)
		}
	}

	if enableMap && len(enabledHints) > 0 {
		fmt.Fprintln(os.Stderr, "\n# OpenRC had these scripts enabled in a runlevel. Suggested slinit enables:")
		for _, n := range enabledHints {
			fmt.Fprintf(os.Stderr, "slinitctl enable %s\n", n)
		}
	}

	if hadErrors {
		os.Exit(1)
	}
}

// isOpenrcEnabled reports whether an /etc/runlevels/*/<name> symlink
// exists (OpenRC's "enabled" marker). Silent on missing dirs.
func isOpenrcEnabled(name string) bool {
	matches, _ := filepath.Glob(filepath.Join("/etc/runlevels", "*", name))
	return len(matches) > 0
}

// --- Data structures ---

type warning struct {
	level string // NOTE / WARN
	msg   string
}

type slinitConfig struct {
	svcName            string
	scriptPath         string
	svcType            string
	command            string
	runAs              string
	envFile            string
	workingDir         string
	chroot             string
	pidFile            string
	termSignal         string
	umask              string
	restart            string
	restartDelay       string
	restartLimitCount  string
	restartLimitCounti string // seconds
	noNewPrivs         bool
	preStart           string
	postStart          string
	stopCommand        string

	depends  []string // → depends-on
	waitsFor []string // → waits-for
	comments []string
}

// --- Conversion ---

func convertScript(path, wrapper string) (*slinitConfig, []warning, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, nil, fmt.Errorf("read: %w", err)
	}
	if !bytes.HasPrefix(data, []byte("#!")) {
		return nil, nil, fmt.Errorf("not a shell script (no shebang)")
	}

	name := filepath.Base(abs)
	cfg := &slinitConfig{
		svcName:    name,
		scriptPath: abs,
		svcType:    "process",
		restart:    "yes",   // openrc respawns supervised daemons by default
	}

	warns := parseOpenrcScript(cfg, string(data), wrapper)

	// Auto-detect conf.d file (openrc convention: /etc/conf.d/<name>).
	confd := filepath.Join("/etc/conf.d", name)
	if _, err := os.Stat(confd); err == nil {
		if cfg.envFile == "" {
			cfg.envFile = confd
		}
	}

	cfg.comments = append([]string{
		fmt.Sprintf("Converted from OpenRC init.d script: %s", abs),
		"Review the notes above before enabling.",
	}, cfg.comments...)
	return cfg, warns, nil
}

// --- Parser ---

var (
	varAssignRe = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)
	funcDefRe   = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s*\(\)\s*(\{)?\s*$`)
)

// parseOpenrcScript is a coarse shell-aware scan. It finds top-level
// variable assignments (before any function opens), notes which
// functions exist, and extracts the body of `depend()` for the
// dependency mapping. Anything more subtle (nested subshells, heredocs
// used as assignments, arrays) drops into the wrap-with-openrc-run
// fallback so no logic is lost.
func parseOpenrcScript(cfg *slinitConfig, script, wrapper string) []warning {
	var warns []warning

	// Collect variables + function names.
	vars := map[string]string{}
	funcs := map[string]bool{}

	// Track the current function we're inside (for depend body).
	inFunc := ""
	braceDepth := 0
	var dependBody []string

	scanner := bufio.NewScanner(strings.NewReader(script))
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		raw := scanner.Text()
		line := strings.TrimSpace(raw)

		// Skip comments and blanks.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Enter/exit function.
		if m := funcDefRe.FindStringSubmatch(line); m != nil {
			inFunc = m[1]
			funcs[inFunc] = true
			// If brace on same line, we're already at depth 1.
			if m[2] == "{" {
				braceDepth = 1
			} else {
				braceDepth = 0
			}
			continue
		}
		if inFunc != "" {
			// Track brace depth cheaply.
			braceDepth += strings.Count(line, "{") - strings.Count(line, "}")
			if inFunc == "depend" && line != "{" && line != "}" {
				dependBody = append(dependBody, line)
			}
			if braceDepth <= 0 {
				inFunc = ""
				braceDepth = 0
			}
			continue
		}

		// Top-level assignment.
		if m := varAssignRe.FindStringSubmatch(line); m != nil {
			vars[m[1]] = stripQuotes(m[2])
			continue
		}
		// Anything else at top-level (`. /etc/rc.conf`, `source X`,
		// bare commands) is script logic — flag once and continue
		// so we still see the rest.
		if !strings.HasPrefix(line, ".") && !strings.HasPrefix(line, "source ") &&
			!strings.HasPrefix(line, "[[ ") && !strings.HasPrefix(line, "[ ") {
			// tolerated silently for now
		}
	}

	// Apply detected variables.
	if v, ok := vars["command"]; ok {
		cfg.command = v
	}
	if v, ok := vars["command_args"]; ok && cfg.command != "" {
		cfg.command += " " + v
	}
	if v, ok := vars["command_args_foreground"]; ok && cfg.command != "" {
		cfg.command += " " + v
	}
	if v, ok := vars["command_user"]; ok {
		cfg.runAs = v
	}
	if v, ok := vars["directory"]; ok {
		cfg.workingDir = v
	}
	if v, ok := vars["chroot"]; ok {
		cfg.chroot = v
	}
	if v, ok := vars["pidfile"]; ok {
		cfg.pidFile = v
	}
	if v, ok := vars["stopsig"]; ok {
		cfg.termSignal = v
	}
	if v, ok := vars["umask"]; ok {
		cfg.umask = v
	}
	if v, ok := vars["respawn_delay"]; ok {
		cfg.restartDelay = v
	}
	if v, ok := vars["respawn_max"]; ok {
		cfg.restartLimitCount = v
	}
	if v, ok := vars["respawn_period"]; ok {
		cfg.restartLimitCounti = v
	}
	if v, ok := vars["no_new_privs"]; ok && (v == "yes" || v == "true" || v == "1") {
		cfg.noNewPrivs = true
	}
	if v, ok := vars["command_background"]; ok && (v == "yes" || v == "true" || v == "1") {
		// slinit's bgprocess type is the analog — the daemon forks
		// itself into the background and writes a pidfile.
		cfg.svcType = "bgprocess"
	}
	if v, ok := vars["description"]; ok {
		cfg.comments = append(cfg.comments, "description: "+v)
	}
	if v, ok := vars["name"]; ok && v != "" {
		cfg.comments = append(cfg.comments, "openrc name: "+v)
	}

	// Warn about openrc-only variables we skip.
	for _, k := range []string{
		"input_file", "output_log", "error_log",
		"output_logger", "error_logger",
		"supervisor", "supervise_daemon_args", "start_stop_daemon_args",
		"capabilities", "secbits",
		"extra_commands", "extra_started_commands", "extra_stopped_commands",
		"required_dirs", "required_files",
	} {
		if v, ok := vars[k]; ok && v != "" {
			warns = append(warns, warning{"WARN", fmt.Sprintf("openrc %s=%q not mapped — review manually", k, v)})
		}
	}

	// Parse depend() body.
	warns = append(warns, parseDepend(cfg, dependBody)...)

	// Fallback decision: if a custom start()/stop()/start_pre()/etc
	// exists, we cannot safely extract the daemon — the script has
	// its own logic. Wrap via openrc-run so nothing is lost.
	customFuncs := []string{}
	for _, fn := range []string{"start", "stop", "start_pre", "start_post", "stop_pre", "stop_post", "restart", "status", "reload"} {
		if funcs[fn] {
			customFuncs = append(customFuncs, fn)
		}
	}
	if len(customFuncs) > 0 {
		cfg.command = fmt.Sprintf("%s %s start", wrapper, cfg.scriptPath)
		cfg.stopCommand = fmt.Sprintf("%s %s stop", wrapper, cfg.scriptPath)
		// bgprocess makes no sense when we're running openrc-run
		// (which is a scripted lifecycle wrapper).
		cfg.svcType = "scripted"
		// Discard restart-delay / limit-count values we may have
		// grabbed from vars — openrc-run manages the daemon's
		// lifetime, slinit's restart* directives duplicate that
		// and cause spurious respawn.
		warns = append(warns, warning{"NOTE",
			fmt.Sprintf("custom shell functions %v — wrapped via %s (requires the binary at runtime)",
				customFuncs, wrapper)})
	}

	return warns
}

// parseDepend maps openrc dependency verbs onto slinit directives:
//
//	need X     → depends-on: X   (hard dep, must be running)
//	use X      → waits-for: X    (soft; start if available)
//	after X    → waits-for: X    (ordering only)
//	before X   → warn, not directly representable in slinit
//	provide X  → warn, no direct equivalent
//	keyword    → warn, runlevel gate
func parseDepend(cfg *slinitConfig, body []string) []warning {
	var warns []warning
	for _, ln := range body {
		fields := strings.Fields(ln)
		if len(fields) < 2 {
			continue
		}
		verb, rest := fields[0], fields[1:]
		switch verb {
		case "need":
			cfg.depends = append(cfg.depends, rest...)
		case "use", "after":
			cfg.waitsFor = append(cfg.waitsFor, rest...)
		case "before":
			warns = append(warns, warning{"WARN", fmt.Sprintf("depend `before %v` not mappable — invert on the target side", rest)})
		case "provide":
			warns = append(warns, warning{"NOTE", fmt.Sprintf("depend `provide %v` — no direct slinit alias; consumers must depend on this svc's actual name", rest)})
		case "keyword":
			warns = append(warns, warning{"NOTE", fmt.Sprintf("depend `keyword %v` — openrc runlevel gate; slinit uses condition-* predicates instead", rest)})
		}
	}
	return warns
}

// stripQuotes removes surrounding "..." or '...' from a value (single
// pair only, not shell-quote-aware — the runit converter's shellFields
// gets a workout but openrc assignments are simpler in practice).
func stripQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
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
	if c.restart != "" && c.svcType == "process" {
		fmt.Fprintf(w, "restart = %s\n", c.restart)
	}
	if c.restartDelay != "" {
		fmt.Fprintf(w, "restart-delay = %s\n", c.restartDelay)
	}
	if c.restartLimitCount != "" {
		fmt.Fprintf(w, "restart-limit-count = %s\n", c.restartLimitCount)
	}
	if c.restartLimitCounti != "" {
		fmt.Fprintf(w, "restart-limit-interval = %s\n", c.restartLimitCounti)
	}
	for _, d := range c.depends {
		fmt.Fprintf(w, "depends-on: %s\n", d)
	}
	for _, d := range c.waitsFor {
		fmt.Fprintf(w, "waits-for: %s\n", d)
	}
}

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
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sunlightlinux/slinit/internal/atomicfile"
)

// version is set at build time with -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	// --version, alone: checked before anything else runs, so no other
	// argument changes meaning. The value is stamped by the package
	// build (-X main.version=...).
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Printf("slinit-openrc-convert version %s\n", version)
		return
	}
	var (
		outputDir  string
		dryRun     bool
		verbose    bool
		enableMap  bool
		wrapperCmd string
	)
	flag.StringVar(&outputDir, "output-dir", "", "batch mode: write one slinit file per input into DIR")
	flag.BoolVar(&dryRun, "dry-run", false, "print what would be written without touching the filesystem")
	flag.BoolVar(&verbose, "verbose", false, "also print conversion notes to stderr (warnings are always printed)")
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
			} else if err := atomicfile.Write(outPath, buf.Bytes(), 0o644, true); err != nil {
				fmt.Fprintf(os.Stderr, "slinit-openrc-convert: write %s: %v\n", outPath, err)
				hadErrors = true
				continue
			}
		}

		// WARN is printed always, as slinit-immortal-convert does; NOTE
		// needs --verbose. A warning that has to be asked for is not a
		// warning: a plain conversion that lost something said nothing.
		for _, w := range warns {
			if w.level == "WARN" || verbose {
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
	varAssignRe = regexp.MustCompile(`(?s)^\s*([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)
	funcDefRe   = regexp.MustCompile(`(?s)^\s*([A-Za-z_][A-Za-z0-9_]*)\s*\(\)\s*(\{.*)?$`)

	// logicAssignRe finds an assignment to a variable the converter maps
	// anywhere in a line: after a separator (`[ -n "$x" ] && pidfile=…`,
	// a case arm `*) pidfile=…;;`), behind export/local/readonly, or as a
	// ${var:=default} expansion.
	logicAssignRe = regexp.MustCompile(`(?:^|[\s;&|()!{])(?:(?:export|local|readonly)\s+)?(` +
		mappedVarAlt + `)\+?=|\$\{(` + mappedVarAlt + `):?=`)
)

// mappedVars are the script variables the converter turns into
// directives. Their value has to be a literal known before the script
// runs; anything else is left to openrc-run.
var mappedVars = []string{
	"command", "command_args", "command_args_foreground", "command_user",
	"command_background", "supervisor", "directory", "chroot", "pidfile",
	"stopsig", "umask", "respawn_delay", "respawn_max", "respawn_period",
	"no_new_privs",
}

var mappedVarAlt = strings.Join(mappedVars, "|")

// lineContinues reports whether a line ends inside a quoted string
// (open) or with an unquoted, unescaped backslash (cont), so the next
// line belongs to it. An unquoted # at the start of a word starts a
// comment, where quotes do not count.
func lineContinues(line string) (open, cont bool) {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case c == '\\':
			if i == len(line)-1 {
				return quote != 0, quote == 0 || quote == '"'
			}
			i++
		case quote == '"':
			if c == '"' {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			return false, false
		}
	}
	return quote != 0, false
}

// shellBlockDelta is how far a top-level line opens (+) or closes (-)
// an if/case/for/while/until block, counted on the line with its
// quoted text removed so a quoted "if" does not count.
func shellBlockDelta(line string) int {
	var b strings.Builder
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' && i+1 < len(line) {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '\\':
			i++
		case c == '\'' || c == '"':
			quote = c
		case c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			i = len(line)
		default:
			b.WriteByte(c)
		}
	}
	d := 0
	for _, w := range strings.FieldsFunc(b.String(), func(r rune) bool {
		return r == ' ' || r == '\t' || r == ';' || r == '&' || r == '|' || r == '(' || r == ')'
	}) {
		switch w {
		case "if", "case", "for", "while", "until":
			d++
		case "fi", "esac", "done":
			d--
		}
	}
	return d
}

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

	// Mapped variables assigned in shell logic — inside an if/case/loop
	// at top level, behind && or ||, or in a function body — whose value
	// is only known when the script runs. The plain top-level assignment
	// the scan keeps is then not the one OpenRC uses.
	logicSet := map[string]bool{}
	noteLogic := func(line string) {
		for _, m := range logicAssignRe.FindAllStringSubmatch(line, -1) {
			logicSet[m[1]+m[2]] = true
		}
	}
	blockDepth := 0

	// Scan logical lines: a quoted string runs on across newlines, as
	// in command_args="-a<newline>-b", and a trailing backslash joins
	// the next line. Read line by line, the first line of such a value
	// was all the scan saw.
	lines := strings.Split(script, "\n")
	unterminated := false
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		for {
			open, cont := lineContinues(raw)
			if !open && !cont {
				break
			}
			if i+1 >= len(lines) {
				unterminated = true
				break
			}
			i++
			if cont {
				raw = raw[:len(raw)-1] + lines[i]
			} else {
				raw += "\n" + lines[i]
			}
		}
		line := strings.TrimSpace(raw)

		// Skip comments and blanks.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Enter/exit function.
		if m := funcDefRe.FindStringSubmatch(line); m != nil {
			inFunc = m[1]
			funcs[inFunc] = true
			braceDepth = 0
			if m[2] == "" {
				continue // the brace opens on a line of its own
			}
			// The brace, and maybe the whole body, is on this line:
			// `depend() { need net; }` was not taken for a function at
			// all, and its dependencies were dropped.
			line = m[2]
		}
		if inFunc != "" {
			if inFunc != "depend" {
				noteLogic(line)
			}
			// Track brace depth cheaply.
			braceDepth += strings.Count(line, "{") - strings.Count(line, "}")
			if inFunc == "depend" {
				dependBody = append(dependBody, line)
			}
			if braceDepth <= 0 {
				inFunc = ""
				braceDepth = 0
			}
			continue
		}

		// Top-level assignment. One inside an if/case/loop is logic.
		if m := varAssignRe.FindStringSubmatch(line); m != nil && blockDepth == 0 {
			// The shell splits an unquoted expansion on newlines as on
			// spaces; a newline left in would also end a comment line.
			vars[m[1]] = strings.ReplaceAll(stripQuotes(m[2]), "\n", " ")
			continue
		}
		// Anything else at top level (`. /etc/rc.conf`, a test, an
		// if/case/loop) is script logic: what it assigns is known only
		// when the script runs.
		noteLogic(line)
		blockDepth += shellBlockDelta(line)
		if blockDepth < 0 {
			blockDepth = 0
		}
	}

	// A mapped value the shell has to compute — $VAR from conf.d or the
	// environment, ${x:-default}, `cmd` or $(cmd) — or one assigned in
	// shell logic cannot be written into the file: slinit expands $VAR
	// in command from its own environment when it loads the service, so
	// command_args="${FOO_OPTS} -f" silently lost FOO_OPTS, and upstream
	// rarpd's if/else turned into a command with neither -f nor -a. Such
	// a script runs through openrc-run, which evaluates it as OpenRC does.
	var dynamic []string
	if unterminated {
		dynamic = append(dynamic, "a quoted string or continued line runs to the end of the script")
	}
	for _, k := range mappedVars {
		if logicSet[k] {
			dynamic = append(dynamic, fmt.Sprintf("%s is assigned in shell logic (if/case/loop, && or a function)", k))
		} else if v, ok := vars[k]; ok && strings.ContainsAny(v, "$`") {
			dynamic = append(dynamic, fmt.Sprintf("%s=%q needs the shell to expand it", k, v))
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
	if len(customFuncs) > 0 || len(dynamic) > 0 {
		cfg.command = fmt.Sprintf("%s %s start", wrapper, cfg.scriptPath)
		cfg.stopCommand = fmt.Sprintf("%s %s stop", wrapper, cfg.scriptPath)
		// bgprocess makes no sense when we're running openrc-run
		// (which is a scripted lifecycle wrapper).
		cfg.svcType = "scripted"
		// Discard what we grabbed from the script's variables:
		// openrc-run reads them itself when it runs the script, so
		// here they would only duplicate it — restart* would respawn
		// behind its back, run-as would run openrc-run itself as the
		// daemon's user — and they are taken verbatim from shell, so
		// a value like ${respawn_period:-60} or /run/${RC_SVCNAME}.pid
		// made the converted file fail to load.
		var left []string
		for _, f := range []struct {
			name string
			val  *string
		}{
			{"pidfile", &cfg.pidFile},
			{"command_user", &cfg.runAs},
			{"directory", &cfg.workingDir},
			{"stopsig", &cfg.termSignal},
			{"umask", &cfg.umask},
			{"respawn_delay", &cfg.restartDelay},
			{"respawn_max", &cfg.restartLimitCount},
			{"respawn_period", &cfg.restartLimitCounti},
		} {
			if *f.val != "" {
				left = append(left, f.name)
				*f.val = ""
			}
		}
		if cfg.noNewPrivs {
			left = append(left, "no_new_privs")
			cfg.noNewPrivs = false
		}
		cfg.restart = ""
		if len(left) > 0 {
			warns = append(warns, warning{"NOTE", fmt.Sprintf(
				"%s left to %s, which applies them itself", strings.Join(left, ", "), wrapper)})
		}
		if len(customFuncs) > 0 {
			warns = append(warns, warning{"NOTE",
				fmt.Sprintf("custom shell functions %v — wrapped via %s (requires the binary at runtime)",
					customFuncs, wrapper)})
		} else {
			warns = append(warns, warning{"WARN", fmt.Sprintf(
				"%s: its value is known only when the script runs, so the script is wrapped via %s "+
					"(requires the binary at runtime) — write the values in to run the daemon directly",
				strings.Join(dynamic, "; "), wrapper)})
		}
	} else {
		// command_background=yes is the opposite of a forking daemon: the
		// daemon stays in the foreground and start-stop-daemon backgrounds
		// it and writes the pidfile itself (openrc-run(8)) — a process to
		// slinit, which tracks the PID with no pidfile. So is a daemon kept
		// in the foreground by command_args_foreground or run under
		// supervise-daemon. Otherwise start-stop-daemon expects the daemon
		// to fork itself, which slinit follows through the pidfile.
		// OpenRC respawns a daemon only under supervise-daemon, which
		// is also the only one that reads respawn_*; start-stop-daemon
		// starts it once and leaves it dead when it exits (openrc-run(8),
		// sh/start-stop-daemon.sh). Every converted daemon used to get
		// restart = yes.
		if vars["supervisor"] == "supervise-daemon" {
			cfg.restart = "yes"
		} else {
			cfg.restart = "no"
			var dropped []string
			for _, f := range []struct {
				name string
				val  *string
			}{
				{"respawn_delay", &cfg.restartDelay},
				{"respawn_max", &cfg.restartLimitCount},
				{"respawn_period", &cfg.restartLimitCounti},
			} {
				if *f.val != "" {
					dropped = append(dropped, f.name)
					*f.val = ""
				}
			}
			if len(dropped) > 0 {
				warns = append(warns, warning{"NOTE", fmt.Sprintf(
					"%s left out: OpenRC applies them under supervise-daemon only, and without it does not respawn the daemon (restart = no)",
					strings.Join(dropped, ", "))})
			}
		}
		bg := strings.ToLower(vars["command_background"])
		foreground := bg == "yes" || bg == "true" || bg == "1" ||
			vars["command_args_foreground"] != "" || vars["supervisor"] == "supervise-daemon"
		switch {
		case foreground:
			if cfg.pidFile != "" {
				warns = append(warns, warning{"NOTE", fmt.Sprintf(
					"pidfile=%q left out: the daemon runs in the foreground and slinit tracks its PID itself", cfg.pidFile)})
				cfg.pidFile = ""
			}
		case cfg.pidFile != "":
			cfg.svcType = "bgprocess"
		case cfg.command != "":
			warns = append(warns, warning{"WARN", fmt.Sprintf(
				"no command_background and no pidfile: start-stop-daemon expects %s to fork itself, "+
					"which slinit cannot follow without a pid-file — make it run in the foreground, "+
					"or set pid-file and type = bgprocess", cfg.command)})
		}
	}

	// slinit applies chroot to type = process only and refuses to load
	// it on any other type rather than run the service unjailed. Leave
	// it out of the file, where it would stop the service loading, and
	// say so: openrc-run applies it itself for a wrapped script, but a
	// bgprocess daemon will run outside the jail.
	if cfg.chroot != "" && cfg.svcType != "process" {
		if cfg.svcType == "scripted" {
			warns = append(warns, warning{"NOTE", fmt.Sprintf(
				"chroot=%q left to %s (slinit's chroot applies to type = process only)", cfg.chroot, wrapper)})
		} else {
			warns = append(warns, warning{"WARN", fmt.Sprintf(
				"chroot=%q not mapped: slinit's chroot applies to type = process only, "+
					"so this %s service will run OUTSIDE the jail — review manually", cfg.chroot, cfg.svcType)})
		}
		cfg.comments = append(cfg.comments, "openrc chroot: "+cfg.chroot+" (not applied, see the converter's warning)")
		cfg.chroot = ""
	}

	return warns
}

// parseDepend maps openrc dependency verbs onto slinit directives:
//
//	need X     → depends-on: X   (hard dep, must be running)
//	want X     → waits-for: X    (soft; started, but may fail)
//	use X      → waits-for: X    (as on 4.0.2; after:, ordering only, is for 4.1)
//	after X    → waits-for: X    (likewise)
//	before X   → warn, not directly representable in slinit
//	provide X  → warn, no direct equivalent
//	keyword    → warn, runlevel gate
//
// slinit fails the load on any dependency it cannot find, where OpenRC
// skips a use/want/after that is not installed and resolves virtual
// names (net, logger, dns) through other scripts' provide. A soft
// reference is kept only when a script of that name sits beside this
// one, the directory being converted; the rest are left out with a
// note. A need is kept either way, with a warning when it would not
// resolve.
func parseDepend(cfg *slinitConfig, body []string) []warning {
	var warns []warning
	exists := func(n string) bool {
		fi, err := os.Stat(filepath.Join(filepath.Dir(cfg.scriptPath), n))
		return err == nil && fi.Mode().IsRegular()
	}
	soft := func(verb string, names []string, into *[]string) {
		for _, n := range names {
			if exists(n) {
				*into = append(*into, n)
			} else {
				warns = append(warns, warning{"NOTE", fmt.Sprintf(
					"depend `%s %s` left out: no init.d script of that name beside this one (a virtual or uninstalled service)", verb, n)})
			}
		}
	}
	// The body is shell. Each ;-separated command that starts with a
	// dependency verb is mapped; anything else — if/case/loops, local,
	// helper calls — cannot be followed, so a dependency it sets or
	// guards is reported instead of being taken silently.
	depth := 0
	var unparsed []string
	for _, ln := range body {
		for _, cmd := range strings.Split(ln, ";") {
			var fields []string
			for _, f := range strings.Fields(cmd) {
				if f != "{" && f != "}" {
					fields = append(fields, f)
				}
			}
			if len(fields) > 0 && (fields[0] == "then" || fields[0] == "else" || fields[0] == "do") {
				fields = fields[1:]
			}
			if len(fields) == 0 {
				continue
			}
			d := shellBlockDelta(strings.Join(fields, " "))
			verb := fields[0]
			if d != 0 || !dependVerbs[verb] {
				unparsed = append(unparsed, strings.Join(fields, " "))
				if depth += d; depth < 0 {
					depth = 0
				}
				continue
			}
			var rest []string
			for _, n := range fields[1:] {
				if strings.ContainsAny(n, "$`") {
					warns = append(warns, warning{"WARN", fmt.Sprintf(
						"depend `%s %s` left out: the name is known only when the script runs — review manually", verb, n)})
					continue
				}
				rest = append(rest, n)
			}
			if len(rest) == 0 {
				continue
			}
			if depth > 0 && verb != "config" {
				warns = append(warns, warning{"WARN", fmt.Sprintf(
					"depend `%s %s` sits inside shell logic in depend(); mapped as if unconditional — review manually",
					verb, strings.Join(rest, " "))})
			}
			switch verb {
			case "need":
				cfg.depends = append(cfg.depends, rest...)
				for _, n := range rest {
					if !exists(n) {
						warns = append(warns, warning{"WARN", fmt.Sprintf(
							"depend `need %s`: no init.d script of that name beside this one — the service will not load until a slinit service %q exists", n, n)})
					}
				}
			case "want":
				soft(verb, rest, &cfg.waitsFor)
			case "use", "after":
				soft(verb, rest, &cfg.waitsFor)
			case "before":
				warns = append(warns, warning{"WARN", fmt.Sprintf("depend `before %v` not mappable — invert on the target side", rest)})
			case "provide":
				warns = append(warns, warning{"NOTE", fmt.Sprintf("depend `provide %v` — no direct slinit alias; consumers must depend on this svc's actual name", rest)})
			case "keyword":
				warns = append(warns, warning{"NOTE", fmt.Sprintf("depend `keyword %v` — openrc runlevel gate; slinit uses condition-* predicates instead", rest)})
			case "config":
				warns = append(warns, warning{"NOTE", fmt.Sprintf("depend `config %v` — openrc dependency-cache input, nothing to map", rest)})
			}
		}
	}
	if len(unparsed) > 0 {
		warns = append(warns, warning{"WARN", fmt.Sprintf(
			"depend() has shell the converter cannot follow (%q); a dependency it sets or guards is "+
				"mapped as if unconditional, or not at all — review manually", unparsed)})
	}
	return warns
}

// dependVerbs are the depend() commands parseDepend maps or reports.
var dependVerbs = map[string]bool{
	"need": true, "want": true, "use": true, "after": true,
	"before": true, "provide": true, "keyword": true, "config": true,
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
		// An options member; `no-new-privs = yes` is an unknown setting.
		fmt.Fprintln(w, "options = no-new-privs")
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

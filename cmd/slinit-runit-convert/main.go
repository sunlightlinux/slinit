// slinit-runit-convert converts a runit service directory
// (`/etc/sv/<name>/`) into a slinit service file (dinit-compatible
// text format). Accelerates the runit → slinit migration path
// without hand-editing dozens of service files.
//
// Handles the void-linux convention (which extends bare runit with a
// sourced `conf` file) and chpst's 25 flags. Emits warnings on
// stderr for anything that can't be mapped 1:1 so the operator
// knows what to hand-check.
//
// Usage:
//
//	slinit-runit-convert /etc/sv/nginx > /etc/slinit.d/nginx
//	slinit-runit-convert --output-dir=/etc/slinit.d /etc/sv/*
//	slinit-runit-convert --dry-run --verbose /etc/sv/postgres
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	var (
		outputDir  string
		dryRun     bool
		verbose    bool
		enableMap  bool
	)
	flag.StringVar(&outputDir, "output-dir", "", "batch mode: write one slinit file per input into DIR (default: stdout single file)")
	flag.BoolVar(&dryRun, "dry-run", false, "print what would be written without touching the filesystem")
	flag.BoolVar(&verbose, "verbose", false, "print per-service conversion notes to stderr")
	flag.BoolVar(&enableMap, "enable-map", false, "check /var/service/<name> symlink and suggest `slinitctl enable` for enabled runit svcs")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `slinit-runit-convert — port a runit service directory to a slinit service file

Usage:
  slinit-runit-convert [flags] <runit-sv-dir> [runit-sv-dir ...]

Single input mode (default):
  slinit-runit-convert /etc/sv/nginx > /etc/slinit.d/nginx

Batch mode (--output-dir):
  slinit-runit-convert --output-dir=/etc/slinit.d /etc/sv/*

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
		fmt.Fprintln(os.Stderr, "slinit-runit-convert: multiple inputs require --output-dir")
		os.Exit(1)
	}

	if outputDir != "" && !dryRun {
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "slinit-runit-convert: create output dir: %v\n", err)
			os.Exit(1)
		}
	}

	enabledHints := []string{}
	var hadErrors bool
	for _, in := range inputs {
		cfg, warns, err := convertService(in)
		if err != nil {
			fmt.Fprintf(os.Stderr, "slinit-runit-convert: %s: %v\n", in, err)
			hadErrors = true
			continue
		}

		name := filepath.Base(strings.TrimRight(in, "/"))

		// Assemble every file this conversion produces: the primary
		// plus (optionally) a `<name>-log` companion sourced from
		// runit's log/ subdir.
		type out struct {
			name string
			buf  *bytes.Buffer
		}
		var outputs []out
		primary := new(bytes.Buffer)
		emitSlinitFile(primary, cfg)
		outputs = append(outputs, out{name, primary})
		if cfg.logChild != nil {
			logBuf := new(bytes.Buffer)
			emitSlinitFile(logBuf, cfg.logChild)
			outputs = append(outputs, out{cfg.logChild.svcName, logBuf})
		}

		if outputDir == "" {
			for i, o := range outputs {
				if i > 0 {
					fmt.Fprintf(os.Stdout, "\n# --- %s ---\n", o.name)
				}
				os.Stdout.Write(o.buf.Bytes())
			}
		} else {
			for _, o := range outputs {
				outPath := filepath.Join(outputDir, o.name)
				if dryRun {
					fmt.Fprintf(os.Stderr, "--- would write %s ---\n", outPath)
					os.Stderr.Write(o.buf.Bytes())
				} else {
					if err := os.WriteFile(outPath, o.buf.Bytes(), 0o644); err != nil {
						fmt.Fprintf(os.Stderr, "slinit-runit-convert: write %s: %v\n", outPath, err)
						hadErrors = true
					}
				}
			}
		}

		if verbose {
			for _, w := range warns {
				fmt.Fprintf(os.Stderr, "[%s] %s: %s\n", w.level, name, w.msg)
			}
		}

		if enableMap {
			if isRunitEnabled(name) {
				enabledHints = append(enabledHints, name)
			}
		}
	}

	if enableMap && len(enabledHints) > 0 {
		fmt.Fprintln(os.Stderr, "\n# Runit had these services enabled via /var/service/<name> symlink.")
		fmt.Fprintln(os.Stderr, "# To match on slinit, add them to your boot service graph or run:")
		for _, n := range enabledHints {
			fmt.Fprintf(os.Stderr, "slinitctl enable %s\n", n)
		}
	}

	if hadErrors {
		os.Exit(1)
	}
}

// isRunitEnabled reports whether /var/service/<name> is a symlink
// (the runit "enabled" marker on void). Silent on missing files —
// this is an advisory-only path.
func isRunitEnabled(name string) bool {
	fi, err := os.Lstat(filepath.Join("/var/service", name))
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeSymlink != 0
}

// --- Data structures for the parsed model ---

type warning struct {
	level string // "NOTE", "WARN"
	msg   string
}

// slinitConfig collects every directive we'll emit. Only fields we
// actually set are written — the emitter skips zero-values.
type slinitConfig struct {
	svcName    string
	runitDir   string // absolute path back to the original sv dir
	svcType    string // "process" (only value we ever emit today)
	command    string
	argv0      string
	runAs      string
	envFile    string
	workingDir string
	chroot     string

	newSession   bool
	closeStdin   bool
	closeStdout  bool
	closeStderr  bool
	lockFile     string
	rlimitNofile string
	rlimitCore   string
	rlimitData   string

	finishCommand string
	readyCheckCmd string
	manual        bool // `down` file present
	restart       string
	restartDelay  string

	waitsFor   []string      // auto-emitted from `sv check DEP` in run script
	consumerOf string        // set only on log-companion configs
	logChild   *slinitConfig // set on primary when log/run exists — emitted separately
	logTypePipe bool         // primary needs `log-type = pipe` when a consumer companion attaches

	comments []string // free-form comments prepended to the output
}

// --- Conversion entry point ---

func convertService(dir string) (*slinitConfig, []warning, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Stat(absDir)
	if err != nil {
		return nil, nil, fmt.Errorf("stat %s: %w", absDir, err)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("%s is not a directory", absDir)
	}

	runPath := filepath.Join(absDir, "run")
	runBytes, err := os.ReadFile(runPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read run: %w", err)
	}

	name := filepath.Base(absDir)
	cfg := &slinitConfig{
		svcName:      name,
		runitDir:     absDir,
		svcType:      "process",
		// runsv chdir()s into the service dir before starting run/finish,
		// so scripts that reference ./conf, use ${PWD##*-} in finish
		// (agetty), etc. rely on it. Set the same default so any finish
		// or wrapped run script sees the same $PWD. Overridden if the
		// run script itself uses `chpst -C DIR`.
		workingDir:   absDir,
		restart:      "yes", // runit default is auto-respawn
		restartDelay: "1",   // runit default respawn cooldown
	}

	var warns []warning
	warns = append(warns, analyzeRunScript(cfg, string(runBytes))...)

	// finish → finish-command. Wrap in /bin/sh so the script can use
	// runit's $1 (exit code) / $2 (signal or status byte) args. Slinit's
	// execFinishCommand appends exitCode+signalNum after the configured
	// argv, so `/bin/sh <path>` receives them as $1/$2 — matching runit.
	if _, err := os.Stat(filepath.Join(absDir, "finish")); err == nil {
		cfg.finishCommand = "/bin/sh " + filepath.Join(absDir, "finish")
	}

	// check → ready-check-command. Same /bin/sh wrap. Slinit polls the
	// command periodically; exit 0 means ready (runit convention).
	if _, err := os.Stat(filepath.Join(absDir, "check")); err == nil {
		cfg.readyCheckCmd = "/bin/sh " + filepath.Join(absDir, "check")
	}

	// down (any file, touched) → manual = yes
	if _, err := os.Stat(filepath.Join(absDir, "down")); err == nil {
		cfg.manual = true
	}

	// conf (void convention) — auto-detected inside analyzeRunScript
	// via the ". ./conf" pattern; if not detected there but the file
	// exists, assume it's sourced anyway. Void run scripts guard
	// sourcing with `[ -r conf ]` so a missing conf is expected and
	// harmless at runtime — but slinit's env-file directive is
	// unconditional and warns on missing files, so drop the directive
	// if the file isn't actually there.
	if cfg.envFile == "" {
		if _, err := os.Stat(filepath.Join(absDir, "conf")); err == nil {
			cfg.envFile = filepath.Join(absDir, "conf")
		}
	} else {
		if _, err := os.Stat(cfg.envFile); err != nil {
			cfg.envFile = ""
		}
	}

	// log/run → companion `<name>-log` service. Recursively convert
	// the log subdirectory (which is itself a valid sv dir with its
	// own run/finish/conf), then wire it as consumer-of=<name>. Slinit
	// splices the primary's stdout into the log consumer's stdin.
	if _, err := os.Stat(filepath.Join(absDir, "log", "run")); err == nil {
		logCfg, logWarns, err := convertService(filepath.Join(absDir, "log"))
		if err != nil {
			warns = append(warns, warning{"WARN", fmt.Sprintf(
				"log/run present but conversion failed: %v — wire manually",
				err)})
		} else {
			logCfg.svcName = name + "-log"
			logCfg.consumerOf = name
			// The log service's own comments already reference the log
			// subdir path; prepend one making the parent link explicit.
			logCfg.comments = append([]string{
				fmt.Sprintf("Auto-generated companion for `%s` (runit log/run)", name),
			}, logCfg.comments...)
			cfg.logChild = logCfg
			// Slinit validates consumer-of against the producer's log
			// type: a consumer only attaches when the producer declares
			// `log-type = pipe`. Set it now so both files round-trip
			// through slinit-check cleanly.
			cfg.logTypePipe = true
			for _, w := range logWarns {
				warns = append(warns, warning{w.level, "log/" + w.msg})
			}
		}
	}

	// Resolve bare commands via PATH. Slinit's exec path is bare
	// execve — no PATH search — so a runit `chpst -u nobody daemon`
	// with just "daemon" would fail with ENOENT at start. If we can
	// resolve on the host running the converter, rewrite to absolute;
	// otherwise leave alone and let slinit-check warn.
	cfg.command = resolveBareCommand(cfg.command)

	// control/X — custom scripts. Warn per file found.
	if entries, err := os.ReadDir(filepath.Join(absDir, "control")); err == nil {
		for _, e := range entries {
			warns = append(warns, warning{"WARN", fmt.Sprintf(
				"custom control script control/%s — slinit uses signal directives; review manually",
				e.Name())})
		}
	}

	// Prepend a provenance comment.
	cfg.comments = append([]string{
		fmt.Sprintf("Converted from runit service dir: %s", absDir),
		"Review the notes above before enabling.",
	}, cfg.comments...)

	return cfg, warns, nil
}

// --- run script analyzer ---

// analyzeRunScript scans the shell script line-by-line to find the
// last `exec` line (the daemon starter) and any conf-sourcing or
// `sv check` calls. Anything it can't confidently extract falls back
// to a safe `/bin/sh <sv-dir>/run` invocation so the runit dir stays
// authoritative.
func analyzeRunScript(cfg *slinitConfig, script string) []warning {
	var warns []warning
	var lastExec string
	var sawComplexLogic bool

	for _, raw := range strings.Split(script, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Detect conf sourcing (void convention).
		if strings.Contains(line, ". ./conf") || strings.Contains(line, "source ./conf") {
			cfg.envFile = filepath.Join(cfg.runitDir, "conf")
			continue
		}
		// Detect `sv check DEP` — this is runit's ordering primitive.
		// Emit as slinit `waits-for: DEP` (semantically stronger:
		// waits for the dep to reach STARTED rather than just have a
		// running run script). We still fall back to /bin/sh wrap for
		// the exec because the check-then-exit-1 pattern is a real
		// runtime guard and dropping it would change behavior on the
		// unhappy path.
		if fields := strings.Fields(line); len(fields) >= 3 && fields[0] == "sv" && fields[1] == "check" {
			dep := fields[2]
			cfg.waitsFor = append(cfg.waitsFor, dep)
			warns = append(warns, warning{"NOTE", fmt.Sprintf(
				"`sv check %s` → auto-emitted `waits-for: %s`", dep, dep)})
			sawComplexLogic = true
			continue
		}
		// `exec 2>&1` — stderr merge into stdout. Slinit already
		// treats the child stdout/stderr as one stream, so this is
		// a no-op for us. Skip silently.
		if line == "exec 2>&1" {
			continue
		}
		// Remember the last exec line — that's the daemon launcher.
		if strings.HasPrefix(line, "exec ") {
			lastExec = strings.TrimPrefix(line, "exec ")
			continue
		}
		// Any other non-trivial line means the run script does real
		// work before exec (loops, conditionals, external commands).
		// Note it and fall back to /bin/sh wrapping later.
		sawComplexLogic = true
	}

	if lastExec == "" {
		// No exec line at all — the run script IS the daemon (it
		// runs to completion). Wrap as /bin/sh.
		cfg.command = "/bin/sh " + filepath.Join(cfg.runitDir, "run")
		warns = append(warns, warning{"NOTE", "no `exec` line in run — wrapped as /bin/sh invocation"})
		return warns
	}

	if sawComplexLogic {
		// Shell setup before exec means we can't safely extract
		// just the daemon — the setup matters.
		cfg.command = "/bin/sh " + filepath.Join(cfg.runitDir, "run")
		warns = append(warns, warning{"NOTE", "run script has setup logic before exec — wrapped as /bin/sh invocation"})
		return warns
	}

	// Shell metachars in the exec line mean we can't safely fork+exec
	// the extracted args — slinit invokes command via execve, not
	// through a shell, so `$VAR` (which slinit further pre-expands at
	// parse time, dropping it), `>redirect`, `|pipe`, `&background`,
	// `$(subshell)`, backticks all lose their meaning. Fall back to
	// wrapping so the original run script keeps working as-is.
	if hasShellMetachars(lastExec) {
		cfg.command = "/bin/sh " + filepath.Join(cfg.runitDir, "run")
		warns = append(warns, warning{"NOTE", "exec line uses shell metachars ($VAR, >, |, `…`, etc.) — wrapped as /bin/sh invocation to preserve semantics"})
		return warns
	}

	// Simple case: the exec line is either `DAEMON args` or
	// `chpst FLAGS DAEMON args`. Try to peel chpst.
	fields := shellFields(lastExec)
	if len(fields) > 0 && filepath.Base(fields[0]) == "chpst" {
		warns = append(warns, parseChpst(cfg, fields[1:])...)
	} else {
		cfg.command = lastExec
	}
	return warns
}

// resolveBareCommand rewrites the first word of s to an absolute path
// via PATH lookup, if that word is a bare name (no leading /). If the
// binary isn't in PATH the original string is returned unchanged — the
// operator can install it later and the config still lints (a slinit
// warning, not an error).
func resolveBareCommand(s string) string {
	if s == "" || strings.HasPrefix(s, "/") {
		return s
	}
	fields := shellFields(s)
	if len(fields) == 0 || strings.Contains(fields[0], "/") {
		return s
	}
	abs, err := exec.LookPath(fields[0])
	if err != nil {
		return s
	}
	fields[0] = abs
	return strings.Join(fields, " ")
}

// hasShellMetachars returns true if s contains characters that need
// a real shell to interpret correctly (variable expansion, I/O
// redirects, pipes, backgrounding, command substitution). Any of
// these mean the extracted daemon command line would misbehave when
// passed straight to execve.
func hasShellMetachars(s string) bool {
	return strings.ContainsAny(s, "$>|<&`;")
}

// shellFields splits a shell-ish command line on whitespace,
// respecting single/double quotes. Doesn't attempt full shell
// grammar (no variable expansion, no command substitution) — just
// enough to peel `chpst FLAGS DAEMON args` cleanly.
func shellFields(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuote != 0 {
			if c == inQuote {
				inQuote = 0
			} else {
				cur.WriteByte(c)
			}
			continue
		}
		if c == '"' || c == '\'' {
			inQuote = c
			continue
		}
		if c == ' ' || c == '\t' {
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteByte(c)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// --- chpst flag mapper ---

// parseChpst walks chpst's argv-shape flag stream. Options that take
// values may appear as either `-uUSER` or `-u USER`; we handle both.
// Unknown or currently-unmapped flags produce warnings; the daemon
// command still gets pulled out so the service can run.
func parseChpst(cfg *slinitConfig, args []string) []warning {
	var warns []warning
	i := 0
	for i < len(args) {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "--" {
			// Positional args from here onwards are the daemon.
			if a == "--" {
				i++
			}
			cfg.command = strings.Join(args[i:], " ")
			return warns
		}
		// Multi-char flag like `-uUSER` or standalone `-P`.
		flag := a[1:]
		if len(flag) == 0 {
			// Weird bare `-`; skip.
			i++
			continue
		}
		flagChar := flag[0]
		var val string
		takesValue := strings.ContainsRune("uUbemdopfcrtCnlL/", rune(flagChar))
		if takesValue {
			if len(flag) > 1 {
				val = flag[1:]
				i++
			} else if i+1 < len(args) {
				val = args[i+1]
				i += 2
			} else {
				warns = append(warns, warning{"WARN", fmt.Sprintf("chpst -%c missing value", flagChar)})
				i++
				continue
			}
		} else {
			i++
		}
		switch flagChar {
		case 'u':
			cfg.runAs = val
		case 'U':
			// Sets USER/UID/GID env vars only, not real uid. No
			// direct slinit equivalent; user can hand-craft an
			// env-file.
			warns = append(warns, warning{"WARN", fmt.Sprintf("chpst -U %s (env-only user) not mapped — set USER/UID/GID via env-file manually", val)})
		case 'b':
			cfg.argv0 = val
		case 'e':
			// runit envdir → slinit env-file is a rough
			// approximation (envdir is dir-of-files, env-file is
			// KEY=VALUE lines). Warn.
			cfg.envFile = val
			warns = append(warns, warning{"WARN", fmt.Sprintf("chpst -e %s is an envdir; slinit env-file expects KEY=VALUE lines — you may need to convert the dir to a single file", val)})
		case 'm':
			cfg.rlimitData = val
			warns = append(warns, warning{"WARN", "chpst -m sets data+stack+locked+memlock; slinit rlimit-data maps only the data segment — review whether that's enough"})
		case 'd':
			cfg.rlimitData = val
		case 'o':
			cfg.rlimitNofile = val
		case 'c':
			cfg.rlimitCore = val
		case 'p', 'f', 'r', 't':
			warns = append(warns, warning{"WARN", fmt.Sprintf("chpst -%c %s not mapped (slinit lacks rlimit-%s in the config grammar)", flagChar, val, chpstFlagToRlimit(flagChar))})
		case '/':
			cfg.chroot = val
		case 'C':
			cfg.workingDir = val
		case 'n':
			warns = append(warns, warning{"WARN", fmt.Sprintf("chpst -n %s (nice level) not mapped", val)})
		case 'l', 'L':
			cfg.lockFile = val
		case 'P':
			cfg.newSession = true
		case '0':
			cfg.closeStdin = true
		case '1':
			cfg.closeStdout = true
		case '2':
			cfg.closeStderr = true
		case 'N':
			cfg.closeStdin, cfg.closeStdout, cfg.closeStderr = true, true, true
		case 'F', 'I':
			warns = append(warns, warning{"WARN", fmt.Sprintf("chpst -%c (PID namespace) not mapped — consider slinit's namespace-pid directive", flagChar)})
		case 'v', 'V':
			// verbose / version — irrelevant to the converted svc.
		default:
			warns = append(warns, warning{"WARN", fmt.Sprintf("unknown chpst flag -%c", flagChar)})
		}
	}
	// We consumed all args without finding a daemon — chpst-only
	// invocation, unlikely but possible. Emit a NOTE and set no
	// command (emitter will complain).
	warns = append(warns, warning{"NOTE", "chpst ran with no trailing daemon command — resulting slinit file will lack `command =`"})
	return warns
}

func chpstFlagToRlimit(c byte) string {
	switch c {
	case 'p':
		return "nproc"
	case 'f':
		return "fsize"
	case 'r':
		return "rss"
	case 't':
		return "cpu"
	}
	return string(c)
}

// --- emitter ---

func emitSlinitFile(w io.Writer, c *slinitConfig) {
	for _, cm := range c.comments {
		fmt.Fprintf(w, "# %s\n", cm)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "type = %s\n", c.svcType)
	if c.command != "" {
		fmt.Fprintf(w, "command = %s\n", c.command)
	}
	if c.argv0 != "" {
		fmt.Fprintf(w, "argv0 = %s\n", c.argv0)
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
	if c.lockFile != "" {
		fmt.Fprintf(w, "lock-file = %s\n", c.lockFile)
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
	// Options bundle newSession + close-fd flags via space-separated
	// tokens per dinit convention.
	var opts []string
	if c.newSession {
		opts = append(opts, "new-session")
	}
	// close-stdin/out/err are their own directives, but slinit also
	// accepts a `close-fds` bundle via options — keeping them separate
	// is more readable.
	sort.Strings(opts)
	if len(opts) > 0 {
		fmt.Fprintf(w, "options = %s\n", strings.Join(opts, " "))
	}
	if c.closeStdin {
		fmt.Fprintln(w, "close-stdin = yes")
	}
	if c.closeStdout {
		fmt.Fprintln(w, "close-stdout = yes")
	}
	if c.closeStderr {
		fmt.Fprintln(w, "close-stderr = yes")
	}
	if c.finishCommand != "" {
		fmt.Fprintf(w, "finish-command = %s\n", c.finishCommand)
	}
	if c.readyCheckCmd != "" {
		fmt.Fprintf(w, "ready-check-command = %s\n", c.readyCheckCmd)
	}
	if c.manual {
		fmt.Fprintln(w, "manual = yes")
	}
	if c.restart != "" {
		fmt.Fprintf(w, "restart = %s\n", c.restart)
	}
	if c.restartDelay != "" {
		fmt.Fprintf(w, "restart-delay = %s\n", c.restartDelay)
	}
	if c.consumerOf != "" {
		fmt.Fprintf(w, "consumer-of = %s\n", c.consumerOf)
	}
	if c.logTypePipe {
		fmt.Fprintln(w, "log-type = pipe")
	}
	for _, d := range c.waitsFor {
		fmt.Fprintf(w, "waits-for: %s\n", d)
	}
}


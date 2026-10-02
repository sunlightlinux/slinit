// slinit-sysvinit-convert converts /etc/inittab into slinit service
// files (dinit-compatible text format), completing the converter set
// alongside slinit-{openrc,runit,systemd}-convert.
//
// inittab is one file holding many entries rather than one directory
// per service, so batch output is the normal mode: each entry that
// carries a process becomes its own file, and the entries that do not
// are reported with what slinit does instead.
//
// Both dialects are supported, each read out of its own source rather
// than assumed: sysvinit's fifteen actions from its inittab(5) and
// init.c, busybox's eight from the action table in its init/init.c
// (1.39.0.git). Five names are shared — sysinit, wait, once, respawn,
// ctrlaltdel — and mean the same thing in both; the other thirteen
// belong to exactly one dialect, so the action column alone is never
// ambiguous.
//
// What IS ambiguous is the first column: sysvinit treats it as a short
// utmp id, busybox as the tty to run the command on. That is what
// --dialect decides, and the chosen dialect is always reported rather
// than applied silently.
//
// An action neither dialect defines is refused by name and line number
// instead of guessed at.
//
// Usage:
//
//	slinit-sysvinit-convert /etc/inittab                       # report + files to stdout
//	slinit-sysvinit-convert --output-dir=/etc/slinit.d /etc/inittab
//	slinit-sysvinit-convert --dry-run --verbose /etc/inittab
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	var (
		outputDir string
		dryRun    bool
		verbose   bool
	)
	var dialect string
	flag.StringVar(&dialect, "dialect", "auto", "inittab dialect: sysvinit, busybox, or auto")
	flag.StringVar(&outputDir, "output-dir", "", "write one slinit file per entry into DIR (default: stdout)")
	flag.BoolVar(&dryRun, "dry-run", false, "print what would be written without touching the filesystem")
	flag.BoolVar(&verbose, "verbose", false, "print per-entry notes to stderr")
	flag.Usage = func() {
		fmt.Fprint(os.Stderr, `slinit-sysvinit-convert — convert /etc/inittab to slinit services

Usage:
  slinit-sysvinit-convert [flags] INITTAB

Converts sysvinit's inittab. Entries carrying a process become service
files; entries that do not (initdefault, ctrlaltdel, the powerfail
family) are reported with slinit's equivalent, because they are settings
rather than services.

Flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	f, err := os.Open(flag.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-sysvinit-convert: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	entries, errs := parseInittab(f)
	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "slinit-sysvinit-convert: %s\n", e)
	}

	d, err := resolveDialect(dialect, entries)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-sysvinit-convert: %v\n", err)
		os.Exit(2)
	}
	// Reported, never silent: the first column means different things in
	// the two dialects, so getting this wrong changes the output.
	fmt.Fprintf(os.Stderr, "dialect: %s%s\n", d, dialectWhy(dialect, d))

	result := convert(entries, d)

	// Advisory notes first: an operator who reads nothing else should
	// still see what was not turned into a file and why.
	for _, n := range result.notes {
		fmt.Fprintf(os.Stderr, "note: %s\n", n)
	}
	for _, w := range result.warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}

	names := make([]string, 0, len(result.services))
	for name := range result.services {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		body := result.services[name]
		switch {
		case dryRun:
			fmt.Fprintf(os.Stderr, "would write %s (%d bytes)\n", name, len(body))
		case outputDir != "":
			path := filepath.Join(outputDir, name)
			if err := os.WriteFile(path, []byte(body), 0644); err != nil {
				fmt.Fprintf(os.Stderr, "slinit-sysvinit-convert: %v\n", err)
				os.Exit(1)
			}
			if verbose {
				fmt.Fprintf(os.Stderr, "wrote %s\n", path)
			}
		default:
			fmt.Printf("# ---- %s ----\n%s\n", name, body)
		}
	}

	// The runlevel wiring is a command the operator runs, not something
	// a service file can express: slinit models a runlevel as a service
	// whose waits-for list is its members (see rc-update).
	for _, c := range result.enableCmds {
		fmt.Fprintf(os.Stderr, "wire: %s\n", c)
	}

	if len(errs) > 0 {
		os.Exit(1)
	}
}

// dialect is which inittab grammar a file is written in. They differ in
// the meaning of the first column and in six of their actions.
type dialect string

const (
	dialectSysv    dialect = "sysvinit"
	dialectBusybox dialect = "busybox"
)

// busyboxOnly and sysvOnly are the actions unique to each. Taken from
// the sources: busybox's table is
// "sysinit\0wait\0once\0respawn\0askfirst\0ctrlaltdel\0shutdown\0restart\0"
// in init/init.c, sysvinit's fifteen from inittab(5).
var busyboxOnly = map[string]bool{
	"askfirst": true, "shutdown": true, "restart": true,
}

var sysvOnly = map[string]bool{
	"boot": true, "bootwait": true, "off": true, "ondemand": true,
	"initdefault": true, "kbrequest": true,
	"powerwait": true, "powerfail": true, "powerokwait": true, "powerfailnow": true,
}

// resolveDialect honours an explicit choice and otherwise infers one.
func resolveDialect(want string, entries []entry) (dialect, error) {
	switch want {
	case "sysvinit":
		return dialectSysv, nil
	case "busybox":
		return dialectBusybox, nil
	case "auto":
		return detectDialect(entries), nil
	default:
		return "", fmt.Errorf("unknown dialect %q: want sysvinit, busybox or auto", want)
	}
}

// detectDialect guesses from evidence in the file, in order of how much
// the evidence is worth.
//
// An action unique to one dialect settles it outright. Failing that, the
// giveaway is that busybox never reads the runlevel column and uses the
// first column as a device: a file where every runlevel column is empty
// and every first column names a tty is busybox's shape and not
// sysvinit's, which would normally carry digits there.
//
// Ties go to sysvinit, because a file with neither signal is almost
// certainly a conventional inittab and because its reading of the first
// column (a utmp id) is the harmless one to apply by mistake.
func detectDialect(entries []entry) dialect {
	for _, e := range entries {
		if busyboxOnly[e.action] {
			return dialectBusybox
		}
		if sysvOnly[e.action] {
			return dialectSysv
		}
	}
	sawEntry := false
	for _, e := range entries {
		sawEntry = true
		if e.levels != "" {
			return dialectSysv
		}
		if e.id != "" && !looksLikeTTY(e.id) {
			return dialectSysv
		}
	}
	if sawEntry {
		return dialectBusybox
	}
	return dialectSysv
}

func dialectWhy(want string, got dialect) string {
	if want != "auto" {
		return " (given)"
	}
	return fmt.Sprintf(" (detected; pass --dialect=%s to override)", oppositeOf(got))
}

func oppositeOf(d dialect) dialect {
	if d == dialectSysv {
		return dialectBusybox
	}
	return dialectSysv
}

// devPath turns busybox's first column into an absolute device path,
// the way its parser does: strip any /dev/ prefix, then prepend it.
func devPath(s string) string {
	return "/dev/" + strings.TrimPrefix(s, "/dev/")
}

func looksLikeTTY(s string) bool {
	s = strings.TrimPrefix(s, "/dev/")
	for _, p := range []string{"tty", "console", "hvc", "ttyS", "ttyAMA", "ttyUSB"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// entry is one inittab line: id:runlevels:action:process.
type entry struct {
	line    int
	id      string
	levels  string
	action  string
	process string
}

// parseInittab reads inittab's grammar: four colon-separated fields,
// `#` comments, blank lines ignored. The process field may itself
// contain colons, so it is never split further.
func parseInittab(r io.Reader) ([]entry, []string) {
	var (
		out  []entry
		errs []string
	)
	sc := bufio.NewScanner(r)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		// SplitN with 4: a command like `sh -c "a:b"` keeps its colons.
		parts := strings.SplitN(raw, ":", 4)
		if len(parts) != 4 {
			errs = append(errs, fmt.Sprintf("line %d: expected id:runlevels:action:process, got %q", lineNo, raw))
			continue
		}
		out = append(out, entry{
			line:    lineNo,
			id:      strings.TrimSpace(parts[0]),
			levels:  strings.TrimSpace(parts[1]),
			action:  strings.ToLower(strings.TrimSpace(parts[2])),
			process: strings.TrimSpace(parts[3]),
		})
	}
	if err := sc.Err(); err != nil {
		errs = append(errs, fmt.Sprintf("read: %v", err))
	}
	return out, errs
}

type conversion struct {
	services   map[string]string
	notes      []string
	warnings   []string
	enableCmds []string
}

// convert turns parsed entries into service files plus advice.
//
// The fifteen actions split three ways: those that describe a process
// slinit can supervise, those that are really settings slinit expresses
// elsewhere, and one with no equivalent at all.
func convert(entries []entry, d dialect) conversion {
	res := conversion{services: map[string]string{}}

	for _, e := range entries {
		switch e.action {
		// ---- settings, not services ---------------------------------
		case "initdefault":
			// Names the runlevel to enter at boot. slinit's equivalent
			// is which target the boot service pulls in.
			res.notes = append(res.notes, fmt.Sprintf(
				"line %d: initdefault names runlevel %q. slinit boots whatever its boot "+
					"service waits for — point that at runlevel-%s, or pass --boot-target.",
				e.line, e.levels, defaultLevelName(e.levels)))
		case "ctrlaltdel":
			res.notes = append(res.notes, fmt.Sprintf(
				"line %d: ctrlaltdel (%s) dropped — slinit handles Ctrl+Alt+Del itself "+
					"and reboots; nothing to configure.", e.line, e.process))
		case "powerwait", "powerfail", "powerokwait", "powerfailnow":
			// These became a real slinit feature rather than a gap:
			// SIGPWR is dispatched to a single hook with the state as
			// its argument, so four inittab entries collapse into four
			// cases of one `case` statement.
			res.notes = append(res.notes, fmt.Sprintf(
				"line %d: %s (%s) — slinit runs /etc/slinit/power-hook with %s; "+
					"move this command there under that case.",
				e.line, e.action, e.process, powerHookArg(e.action)))
		case "shutdown":
			// busybox runs these during shutdown and waits for them.
			// slinit's equivalent is the shutdown hook, which already
			// gets the shutdown type as $1.
			res.notes = append(res.notes, fmt.Sprintf(
				"line %d: busybox `shutdown` (%s) — slinit runs "+
					"/etc/slinit/shutdown-hook with the shutdown type as $1; move this "+
					"command there. A per-service equivalent is `stop-command`.",
				e.line, e.process))
		case "restart":
			// busybox exec()s the first RESTART action on SIGQUIT,
			// replacing init with it. That is a soft reboot.
			res.notes = append(res.notes, fmt.Sprintf(
				"line %d: busybox `restart` (%s) replaces init on SIGQUIT — slinit's "+
					"equivalent is `slinitctl soft-reboot`, which re-execs slinit itself "+
					"and keeps the service graph. Only busybox's first restart entry was "+
					"ever used.", e.line, e.process))
		case "kbrequest":
			res.warnings = append(res.warnings, fmt.Sprintf(
				"line %d: kbrequest has no slinit equivalent (it needs the kernel's "+
					"KDSIGACCEPT); %s was not converted.", e.line, e.process))

		// ---- processes slinit can supervise -------------------------
		case "respawn", "wait", "once", "boot", "bootwait", "sysinit", "off", "ondemand", "askfirst":
			if e.process == "" {
				res.warnings = append(res.warnings, fmt.Sprintf(
					"line %d: %s with no command — skipped", e.line, e.action))
				continue
			}
			name, body, notes := serviceFor(e, d)
			if _, clash := res.services[name]; clash {
				// Two entries whose ids sanitise to the same name would
				// silently overwrite each other, which is worse than
				// refusing: the operator loses a service without being
				// told.
				res.warnings = append(res.warnings, fmt.Sprintf(
					"line %d: service name %q already used by an earlier entry — "+
						"rename the inittab id and re-run", e.line, name))
				continue
			}
			res.services[name] = body
			res.notes = append(res.notes, notes...)
			if d == dialectSysv {
				for _, lvl := range splitLevels(e.levels) {
					res.enableCmds = append(res.enableCmds,
						fmt.Sprintf("slinitctl --from runlevel-%s enable %s", lvl, name))
				}
			} else if e.levels != "" {
				// busybox's parser never reads token[1]. Converting it
				// into runlevel membership would invent behaviour the
				// original file never had.
				res.notes = append(res.notes, fmt.Sprintf(
					"line %d: runlevels %q ignored — busybox init does not read that "+
						"column, so %s was not wired into any runlevel.",
					e.line, e.levels, name))
			}

		default:
			// Neither dialect defines it, so it is refused rather than
			// guessed at: emitting a service that silently does the
			// wrong thing is the worst outcome available mid-migration.
			res.warnings = append(res.warnings, fmt.Sprintf(
				"line %d: unknown action %q — not converted. sysvinit has respawn, wait, "+
					"once, boot, bootwait, off, ondemand, initdefault, sysinit, ctrlaltdel, "+
					"kbrequest and the powerfail family; busybox has sysinit, wait, once, "+
					"respawn, askfirst, ctrlaltdel, shutdown and restart.",
				e.line, e.action))
		}
	}
	return res
}

// serviceFor renders one entry as a slinit service file.
func serviceFor(e entry, d dialect) (name, body string, notes []string) {
	var b strings.Builder
	name = serviceName(e)

	fmt.Fprintf(&b, "# Converted from /etc/inittab line %d (%s):\n", e.line, d)
	fmt.Fprintf(&b, "#   %s:%s:%s:%s\n", e.id, e.levels, e.action, e.process)

	// busybox strips a leading dash and gives the command a controlling
	// tty (FEATURE_INIT_SCTTY). slinit's tty-path already does the
	// Setsid+Setctty part, so the dash has nowhere left to go — dropping
	// it silently would be wrong, so it is dropped and said.
	command := e.process
	if d == dialectBusybox && strings.HasPrefix(command, "-") {
		command = command[1:]
		notes = append(notes, fmt.Sprintf(
			"line %d: leading `-` dropped from the command — it asked busybox for a "+
				"controlling tty, which `tty-path` already arranges (Setsid+Setctty).",
			e.line))
	}

	tty, isGetty := gettyTTY(command)

	switch e.action {
	case "respawn", "askfirst":
		b.WriteString("type = process\n")
	case "off":
		b.WriteString("type = process\n")
	case "ondemand":
		b.WriteString("type = process\n")
	default:
		// wait / once / boot / bootwait / sysinit all run to
		// completion; `scripted` is the type that models that.
		b.WriteString("type = scripted\n")
	}

	fmt.Fprintf(&b, "command = %s\n", command)

	switch e.action {
	case "respawn", "askfirst":
		b.WriteString("restart = yes\n")
		if isGetty {
			// Matches what slinit-init-maker emits for a getty, so a
			// converted inittab and a generated one agree.
			b.WriteString("restart-delay = 1\n")
		}
	case "off":
		b.WriteString("manual = yes\n")
		notes = append(notes, fmt.Sprintf(
			"line %d: action `off` means disabled — %s was written with `manual = yes` "+
				"so it is kept but never started on its own.", e.line, name))
	case "ondemand":
		b.WriteString("manual = yes\n")
		notes = append(notes, fmt.Sprintf(
			"line %d: `ondemand` starts only when runlevel %q is requested. Written with "+
				"`manual = yes`; start it from whatever should trigger it, or give it a "+
				"start-on-* activation directive.", e.line, e.levels))
	}

	if e.action == "askfirst" {
		notes = append(notes, fmt.Sprintf(
			"line %d: `askfirst` waits for Enter before spawning, to keep a console "+
				"quiet until someone wants it. slinit has no equivalent, so %s was "+
				"written as a plain respawn and will start immediately — add "+
				"`manual = yes` if that console should stay idle.", e.line, name))
	}

	if e.action == "sysinit" {
		notes = append(notes, fmt.Sprintf(
			"line %d: `sysinit` runs before everything else. %s has no dependencies; make "+
				"slinit's system-init depend on it so the ordering survives.", e.line, name))
	} else {
		b.WriteString("depends-on: system-init\n")
	}

	// The first column is where the two dialects genuinely disagree.
	//
	// sysvinit writes it into utmp as the entry id, and slinit has
	// directives for exactly that, so a converted getty keeps showing up
	// correctly in who(1) and last(1).
	//
	// busybox instead treats it as the tty to run the command on
	// (`.*TTY` becomes `/dev/TTY` in its parser), which is what slinit's
	// tty-path does: open the device, wire it as stdin/stdout/stderr, and
	// make the child a session leader with it as controlling terminal.
	switch d {
	case dialectBusybox:
		if e.id != "" {
			fmt.Fprintf(&b, "tty-path = %s\n", devPath(e.id))
		}
	default:
		if e.id != "" {
			fmt.Fprintf(&b, "inittab-id = %s\n", e.id)
		}
		if isGetty && tty != "" {
			fmt.Fprintf(&b, "inittab-line = %s\n", tty)
		}
	}

	return name, b.String(), notes
}

// serviceName derives a filename. The inittab id is the natural source
// but it is only 1-4 characters and means nothing to a reader, so a
// recognisable getty gets named after its tty instead.
func serviceName(e entry) string {
	if tty, ok := gettyTTY(e.process); ok && tty != "" {
		return "getty-" + sanitise(tty)
	}
	if base := commandBase(e.process); base != "" {
		if e.id != "" {
			return sanitise(base) + "-" + sanitise(e.id)
		}
		return sanitise(base)
	}
	return "inittab-" + sanitise(e.id)
}

// gettyTTY reports whether a command is a getty and on which tty.
// agetty, getty, mingetty and fbgetty all take the tty as a positional
// argument, but not always the first one, so the recognisable shape is
// "an argument that names a tty".
func gettyTTY(process string) (string, bool) {
	fields := strings.Fields(process)
	if len(fields) == 0 {
		return "", false
	}
	base := filepath.Base(fields[0])
	if !strings.HasSuffix(base, "getty") {
		return "", false
	}
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "-") {
			continue
		}
		if strings.HasPrefix(f, "tty") || strings.HasPrefix(f, "console") ||
			strings.HasPrefix(f, "ttyS") || strings.HasPrefix(f, "hvc") {
			return f, true
		}
	}
	// A getty whose tty we could not identify is still a getty.
	return "", true
}

func commandBase(process string) string {
	fields := strings.Fields(process)
	if len(fields) == 0 {
		return ""
	}
	return filepath.Base(fields[0])
}

// sanitise keeps a service name to what a filename and slinit's own
// name grammar accept.
func sanitise(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "entry"
	}
	return out
}

// splitLevels turns the runlevels column into individual level names.
// Levels 0 and 6 are halt and reboot; a service "in" them is something
// that runs while the system goes down, which slinit expresses with
// stop-command rather than membership, so they are left out.
func splitLevels(levels string) []string {
	var out []string
	for _, r := range levels {
		switch r {
		case '0', '6':
			continue
		case ' ', '\t':
			continue
		default:
			out = append(out, strings.ToLower(string(r)))
		}
	}
	return out
}

func defaultLevelName(levels string) string {
	l := strings.TrimSpace(levels)
	if l == "" {
		return "default"
	}
	return strings.ToLower(l)
}

func powerHookArg(action string) string {
	switch action {
	case "powerokwait":
		return "`ok`"
	case "powerfailnow":
		return "`low`"
	default: // powerwait, powerfail
		return "`failing`"
	}
}

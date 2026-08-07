// slinit-journalctl — query the slinit journal ring buffer over the
// control socket. Companion tool to slinit(1) / slinitctl(1); provides
// the operator surface for the journal pipeline that emit + hook code
// feed under the hood (see pkg/journal + pkg/service/journal_emit.go).
//
// Wire path in v1:
//
//	slinit-journalctl → CmdJournalQuery over /run/slinit.socket
//	  → pkg/control/journal.go handleJournalQuery
//	  → journal.GlobalBuffer().Query(filter, limit)
//	  → RplyJournalEntry* + RplyJournalDone
//
// Persistent files (Phase 3, /var/log/slinit-journal/*.jsonl) are a
// separate source that the CLI will merge in a later batch; v1 reads
// only the in-process ring buffer.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journalbin"
	"github.com/sunlightlinux/slinit/pkg/journald"
)

const (
	defaultSystemSocket = "/run/slinit.socket"
	defaultUserSocket   = ".slinitctl"
)

// version is stamped at build time via ldflags -X main.version=vX.Y.Z.
// Local builds without ldflags report "dev" (same convention as slinitctl).
var version = "dev"

// outputFormat is the -o/--output selector. Each format value maps
// to a renderer func in the render() switch below. The short list
// matches systemd's journalctl subset we cover in v1 (short, short-iso,
// cat, json, verbose); full systemd has ~16 modes but the rest are
// deferred to the v2.x scope in project_journal_pipeline.md.
type outputFormat string

const (
	fmtShort    outputFormat = "short"
	fmtShortISO outputFormat = "short-iso"
	fmtCat      outputFormat = "cat"
	fmtJSON     outputFormat = "json"
	fmtVerbose  outputFormat = "verbose"
	fmtExport   outputFormat = "export"
)

// validFormats lists every format value the -o flag accepts, in the
// order shown by --help. Kept as a slice (not a map) so error messages
// can print a stable, human-friendly enumeration.
var validFormats = []outputFormat{fmtShort, fmtShortISO, fmtCat, fmtJSON, fmtVerbose, fmtExport}

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-journalctl: %v\n", err)
		os.Exit(2)
	}
	if opts.showHelp {
		printHelp(os.Stdout)
		return
	}
	if opts.showVersion {
		fmt.Println(version)
		return
	}

	if err := runQuery(opts); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-journalctl: %v\n", err)
		os.Exit(1)
	}
}

// options holds parsed CLI state. 2b covers -n / -o / --socket-path /
// --user / --system / -h / --version; 2c adds -u / -p / --since / --until / -r;
// later batches (2d-2h) attach the remaining filter fields (-f, -k, -c,
// --list-boots) without renaming existing fields.
type options struct {
	limit       int
	format      outputFormat
	socketPath  string
	systemMode  bool
	userMode    bool
	units       []string        // -u NAME (repeatable) — OR-set of units
	priority    journal.Priority // -p LEVEL — highest priority kept (0..7)
	prioritySet bool             // sentinel distinguishing "not set" from "-p emerg"
	since       int64            // --since — Unix nanoseconds; 0 = unbounded
	until       int64            // --until — Unix nanoseconds; 0 = unbounded
	reverse     bool             // -r — newest first
	follow      bool             // -f — subscribe for new events after backlog
	kernelOnly  bool             // -k / --dmesg — Transport=kernel only
	sourceFile  string           // --file=PATH — read from a JSONL file instead of the control socket
	listBoots   bool             // --list-boots — print boot index and exit
	bootID      string           // --boot [ID] — restrict to boot ID (empty = current)
	bootSet     bool             // sentinel: --boot was present (with or without ID)
	cursor      string           // -c CURSOR — resume from a cursor produced by --show-cursor
	showCursor  bool             // --show-cursor — print a resumable cursor after output
	verify      bool             // --verify — walk FSS tag chain on a --file binary journal
	fssKeyPath  string           // --fss-key PATH — key for --verify

	// --- Group A additions (systemd journalctl parity) ---

	// Display modifiers.
	noHostname      bool     // --no-hostname — drop hostname column in short outputs
	utc             bool     // --utc — render timestamps in UTC instead of local
	truncateNewline bool     // --truncate-newline — cut MESSAGE at first \n
	quiet           bool     // -q/--quiet — suppress info messages (empty file etc.)
	noFull          bool     // --no-full — ellipsize long fields
	fullFlag        bool     // --full — inverse of --no-full (default, kept for parity)
	allFields       bool     // -a/--all — show all field values without ellipsizing
	noTail          bool     // --no-tail — show all matches (inverse of default -n heuristic)
	pagerEnd        bool     // -e/--pager-end — start pager at end (no-op: no pager wired)
	outputFields    []string // --output-fields=A,B,C — restrict verbose/JSON to these keys
	merge           bool     // -m/--merge — merge multiple journal sources (no-op single-source)

	// Filtering.
	identifiers        []string // -t IDENT — SYSLOG_IDENTIFIER include-set
	excludeIdentifiers []string // -T IDENT — SYSLOG_IDENTIFIER exclude-set
	facility           []int    // --facility=NAME|N — reserved; slinit doesn't record facility yet
	facilitySet        bool     // sentinel: --facility was present (emits WARN)
	grep               string   // -g PATTERN — RE2 regex on MESSAGE
	grepCaseSensitive  bool     // --case-sensitive[=BOOL] — override -g's default heuristic
	grepCaseSet        bool     // sentinel: user explicitly passed --case-sensitive
	thisBoot           bool     // --this-boot — alias for --boot=0
	userUnitFilters    []string // -U/--user-unit — user-scope unit filter

	// Introspection sub-commands (short-circuit before running a query).
	fieldName    string // -F/--field FIELD — list unique values for FIELD
	fieldsList   bool   // --fields — list all known field names
	headerDump   bool   // --header — dump journal file headers (--file mode)
	diskUsage    bool   // --disk-usage — total bytes across on-disk journals

	// Cursor / source extensions.
	afterCursor string // --after-cursor — same as -c but positions strictly after
	cursorFile  string // --cursor-file FILE — load+persist cursor via file
	directory   string // -D/--directory DIR — glob *.jsonl / *.slj under DIR
	root        string // --root PATH — filesystem root prefix for source lookups

	showHelp    bool
	showVersion bool
}

// parseArgs is a bespoke flag parser matching slinitctl's style —
// hand-rolled so long forms (--socket-path=/tmp/x) coexist with short
// forms (-n 20) and future --key=value additions land without a stdlib
// flag package rewrite. Returns options fully populated with defaults
// for any flag not on the command line.
func parseArgs(args []string) (options, error) {
	opts := options{
		format: fmtShort,
	}
	for len(args) > 0 {
		a := args[0]
		switch {
		case a == "-h" || a == "--help":
			opts.showHelp = true
			return opts, nil

		case a == "--version":
			opts.showVersion = true
			return opts, nil

		case a == "-n" || a == "--lines":
			if len(args) < 2 {
				return opts, fmt.Errorf("%s requires an argument", a)
			}
			n, err := strconv.Atoi(args[1])
			if err != nil || n < 0 {
				return opts, fmt.Errorf("%s: invalid count %q", a, args[1])
			}
			opts.limit = n
			args = args[2:]

		case strings.HasPrefix(a, "-n"):
			// -n20 shorthand.
			n, err := strconv.Atoi(strings.TrimPrefix(a, "-n"))
			if err != nil || n < 0 {
				return opts, fmt.Errorf("-n: invalid count %q", strings.TrimPrefix(a, "-n"))
			}
			opts.limit = n
			args = args[1:]

		case strings.HasPrefix(a, "--lines="):
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--lines="))
			if err != nil || n < 0 {
				return opts, fmt.Errorf("--lines: invalid count %q", strings.TrimPrefix(a, "--lines="))
			}
			opts.limit = n
			args = args[1:]

		case a == "-o" || a == "--output":
			if len(args) < 2 {
				return opts, fmt.Errorf("%s requires an argument", a)
			}
			f, err := parseFormat(args[1])
			if err != nil {
				return opts, err
			}
			opts.format = f
			args = args[2:]

		case strings.HasPrefix(a, "--output="):
			f, err := parseFormat(strings.TrimPrefix(a, "--output="))
			if err != nil {
				return opts, err
			}
			opts.format = f
			args = args[1:]

		case a == "--socket-path":
			if len(args) < 2 {
				return opts, errors.New("--socket-path requires an argument")
			}
			opts.socketPath = args[1]
			args = args[2:]

		case strings.HasPrefix(a, "--socket-path="):
			opts.socketPath = strings.TrimPrefix(a, "--socket-path=")
			args = args[1:]

		case a == "--system":
			opts.systemMode = true
			args = args[1:]

		case a == "--user":
			opts.userMode = true
			args = args[1:]

		case a == "-u" || a == "--unit":
			if len(args) < 2 {
				return opts, fmt.Errorf("%s requires an argument", a)
			}
			opts.units = append(opts.units, args[1])
			args = args[2:]

		case strings.HasPrefix(a, "--unit="):
			opts.units = append(opts.units, strings.TrimPrefix(a, "--unit="))
			args = args[1:]

		case a == "-p" || a == "--priority":
			if len(args) < 2 {
				return opts, fmt.Errorf("%s requires an argument", a)
			}
			p, err := parsePriorityArg(args[1])
			if err != nil {
				return opts, err
			}
			opts.priority = p
			opts.prioritySet = true
			args = args[2:]

		case strings.HasPrefix(a, "--priority="):
			p, err := parsePriorityArg(strings.TrimPrefix(a, "--priority="))
			if err != nil {
				return opts, err
			}
			opts.priority = p
			opts.prioritySet = true
			args = args[1:]

		case a == "--since":
			if len(args) < 2 {
				return opts, errors.New("--since requires an argument")
			}
			t, err := parseTimeArg(args[1], time.Now())
			if err != nil {
				return opts, fmt.Errorf("--since: %w", err)
			}
			opts.since = t.UnixNano()
			args = args[2:]

		case strings.HasPrefix(a, "--since="):
			t, err := parseTimeArg(strings.TrimPrefix(a, "--since="), time.Now())
			if err != nil {
				return opts, fmt.Errorf("--since: %w", err)
			}
			opts.since = t.UnixNano()
			args = args[1:]

		case a == "--until":
			if len(args) < 2 {
				return opts, errors.New("--until requires an argument")
			}
			t, err := parseTimeArg(args[1], time.Now())
			if err != nil {
				return opts, fmt.Errorf("--until: %w", err)
			}
			opts.until = t.UnixNano()
			args = args[2:]

		case strings.HasPrefix(a, "--until="):
			t, err := parseTimeArg(strings.TrimPrefix(a, "--until="), time.Now())
			if err != nil {
				return opts, fmt.Errorf("--until: %w", err)
			}
			opts.until = t.UnixNano()
			args = args[1:]

		case a == "-r" || a == "--reverse":
			opts.reverse = true
			args = args[1:]

		case a == "-f" || a == "--follow":
			opts.follow = true
			args = args[1:]

		case a == "-k" || a == "--dmesg":
			opts.kernelOnly = true
			args = args[1:]

		case a == "--file":
			if len(args) < 2 {
				return opts, errors.New("--file requires an argument")
			}
			opts.sourceFile = args[1]
			args = args[2:]

		case strings.HasPrefix(a, "--file="):
			opts.sourceFile = strings.TrimPrefix(a, "--file=")
			args = args[1:]

		case a == "--list-boots":
			opts.listBoots = true
			args = args[1:]

		case a == "--boot" || a == "-b":
			opts.bootSet = true
			// Optional ID argument. Peek: next token that DOESN'T look
			// like another flag (starts with '-' UNLESS it's a numeric
			// index like "0" or "-1") is treated as the boot spec.
			// Systemd-style shortcuts: "0" = current boot, negative
			// indices "-1" / "-2" reference previous boots (deferred —
			// see resolveBootSpec).
			if len(args) >= 2 && looksLikeBootSpec(args[1]) {
				opts.bootID = args[1]
				args = args[2:]
			} else {
				args = args[1:]
			}

		case strings.HasPrefix(a, "--boot="):
			opts.bootSet = true
			opts.bootID = strings.TrimPrefix(a, "--boot=")
			args = args[1:]

		case strings.HasPrefix(a, "-b"):
			// -b<spec> without space (systemd shorthand: -b0, -b-1)
			opts.bootSet = true
			opts.bootID = strings.TrimPrefix(a, "-b")
			args = args[1:]

		case a == "-c" || a == "--cursor":
			if len(args) < 2 {
				return opts, fmt.Errorf("%s requires an argument", a)
			}
			opts.cursor = args[1]
			args = args[2:]

		case strings.HasPrefix(a, "--cursor="):
			opts.cursor = strings.TrimPrefix(a, "--cursor=")
			args = args[1:]

		case a == "--show-cursor":
			opts.showCursor = true
			args = args[1:]

		case a == "--verify":
			opts.verify = true
			args = args[1:]

		case a == "--fss-key":
			if len(args) < 2 {
				return opts, errors.New("--fss-key requires an argument")
			}
			opts.fssKeyPath = args[1]
			args = args[2:]

		case strings.HasPrefix(a, "--fss-key="):
			opts.fssKeyPath = strings.TrimPrefix(a, "--fss-key=")
			args = args[1:]

		// --- Group A: display modifiers ---

		case a == "--no-hostname":
			opts.noHostname = true
			args = args[1:]

		case a == "--utc":
			opts.utc = true
			args = args[1:]

		case a == "--truncate-newline":
			opts.truncateNewline = true
			args = args[1:]

		case a == "-q" || a == "--quiet":
			opts.quiet = true
			args = args[1:]

		case a == "--no-full":
			opts.noFull = true
			args = args[1:]

		case a == "-l" || a == "--full":
			opts.fullFlag = true
			args = args[1:]

		case a == "-a" || a == "--all":
			opts.allFields = true
			args = args[1:]

		case a == "--no-tail":
			opts.noTail = true
			args = args[1:]

		case a == "-e" || a == "--pager-end":
			opts.pagerEnd = true
			args = args[1:]

		case a == "--output-fields":
			if len(args) < 2 {
				return opts, errors.New("--output-fields requires an argument")
			}
			opts.outputFields = splitCSVFields(args[1])
			args = args[2:]

		case strings.HasPrefix(a, "--output-fields="):
			opts.outputFields = splitCSVFields(strings.TrimPrefix(a, "--output-fields="))
			args = args[1:]

		case a == "-m" || a == "--merge":
			opts.merge = true
			args = args[1:]

		// --- Group A: filtering ---

		case a == "-t" || a == "--identifier":
			if len(args) < 2 {
				return opts, fmt.Errorf("%s requires an argument", a)
			}
			opts.identifiers = append(opts.identifiers, args[1])
			args = args[2:]

		case strings.HasPrefix(a, "--identifier="):
			opts.identifiers = append(opts.identifiers, strings.TrimPrefix(a, "--identifier="))
			args = args[1:]

		case a == "-T" || a == "--exclude-identifier":
			if len(args) < 2 {
				return opts, fmt.Errorf("%s requires an argument", a)
			}
			opts.excludeIdentifiers = append(opts.excludeIdentifiers, args[1])
			args = args[2:]

		case strings.HasPrefix(a, "--exclude-identifier="):
			opts.excludeIdentifiers = append(opts.excludeIdentifiers, strings.TrimPrefix(a, "--exclude-identifier="))
			args = args[1:]

		case a == "--facility":
			if len(args) < 2 {
				return opts, errors.New("--facility requires an argument")
			}
			fs, err := parseFacilityList(args[1])
			if err != nil {
				return opts, err
			}
			opts.facility = append(opts.facility, fs...)
			opts.facilitySet = true
			args = args[2:]

		case strings.HasPrefix(a, "--facility="):
			fs, err := parseFacilityList(strings.TrimPrefix(a, "--facility="))
			if err != nil {
				return opts, err
			}
			opts.facility = append(opts.facility, fs...)
			opts.facilitySet = true
			args = args[1:]

		case a == "-g" || a == "--grep":
			if len(args) < 2 {
				return opts, fmt.Errorf("%s requires an argument", a)
			}
			opts.grep = args[1]
			args = args[2:]

		case strings.HasPrefix(a, "--grep="):
			opts.grep = strings.TrimPrefix(a, "--grep=")
			args = args[1:]

		case a == "--case-sensitive":
			opts.grepCaseSensitive = true
			opts.grepCaseSet = true
			args = args[1:]

		case strings.HasPrefix(a, "--case-sensitive="):
			b, err := parseBoolArg(strings.TrimPrefix(a, "--case-sensitive="))
			if err != nil {
				return opts, fmt.Errorf("--case-sensitive: %w", err)
			}
			opts.grepCaseSensitive = b
			opts.grepCaseSet = true
			args = args[1:]

		case a == "--this-boot":
			opts.thisBoot = true
			opts.bootSet = true
			opts.bootID = "0"
			args = args[1:]

		case a == "-U" || a == "--user-unit":
			if len(args) < 2 {
				return opts, fmt.Errorf("%s requires an argument", a)
			}
			opts.userUnitFilters = append(opts.userUnitFilters, args[1])
			opts.userMode = true
			args = args[2:]

		case strings.HasPrefix(a, "--user-unit="):
			opts.userUnitFilters = append(opts.userUnitFilters, strings.TrimPrefix(a, "--user-unit="))
			opts.userMode = true
			args = args[1:]

		// --- Group A: introspection ---

		case a == "-F" || a == "--field":
			if len(args) < 2 {
				return opts, fmt.Errorf("%s requires an argument", a)
			}
			opts.fieldName = args[1]
			args = args[2:]

		case strings.HasPrefix(a, "--field="):
			opts.fieldName = strings.TrimPrefix(a, "--field=")
			args = args[1:]

		case a == "--fields":
			opts.fieldsList = true
			args = args[1:]

		case a == "--header":
			opts.headerDump = true
			args = args[1:]

		case a == "--disk-usage":
			opts.diskUsage = true
			args = args[1:]

		// --- Group A: cursor / source ---

		case a == "--after-cursor":
			if len(args) < 2 {
				return opts, errors.New("--after-cursor requires an argument")
			}
			opts.afterCursor = args[1]
			args = args[2:]

		case strings.HasPrefix(a, "--after-cursor="):
			opts.afterCursor = strings.TrimPrefix(a, "--after-cursor=")
			args = args[1:]

		case a == "--cursor-file":
			if len(args) < 2 {
				return opts, errors.New("--cursor-file requires an argument")
			}
			opts.cursorFile = args[1]
			args = args[2:]

		case strings.HasPrefix(a, "--cursor-file="):
			opts.cursorFile = strings.TrimPrefix(a, "--cursor-file=")
			args = args[1:]

		case a == "-D" || a == "--directory":
			if len(args) < 2 {
				return opts, fmt.Errorf("%s requires an argument", a)
			}
			opts.directory = args[1]
			args = args[2:]

		case strings.HasPrefix(a, "--directory="):
			opts.directory = strings.TrimPrefix(a, "--directory=")
			args = args[1:]

		case a == "--root":
			if len(args) < 2 {
				return opts, errors.New("--root requires an argument")
			}
			opts.root = args[1]
			args = args[2:]

		case strings.HasPrefix(a, "--root="):
			opts.root = strings.TrimPrefix(a, "--root=")
			args = args[1:]

		default:
			return opts, fmt.Errorf("unknown argument %q (try -h)", a)
		}
	}
	return opts, nil
}

// splitCSVFields tokenizes a comma-separated field list into a slice
// with whitespace trimmed. Empty tokens are dropped so
// `--output-fields=,A,,B,` behaves the same as `--output-fields=A,B`.
func splitCSVFields(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseBoolArg accepts "yes", "true", "1", "on" as true and their
// negations. Case-insensitive. Matches systemd's parse_boolean.
func parseBoolArg(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "yes", "true", "1", "on":
		return true, nil
	case "no", "false", "0", "off":
		return false, nil
	}
	return false, fmt.Errorf("invalid boolean %q", s)
}

// facilityNames maps syslog facility names (RFC 5424) to numeric
// codes. Kept as a slice so lookup logic can iterate case-insensitively
// without a second map allocation.
var facilityNames = map[string]int{
	"kern": 0, "user": 1, "mail": 2, "daemon": 3, "auth": 4,
	"syslog": 5, "lpr": 6, "news": 7, "uucp": 8, "cron": 9,
	"authpriv": 10, "ftp": 11, "ntp": 12, "security": 13, "console": 14,
	"solaris-cron": 15,
	"local0": 16, "local1": 17, "local2": 18, "local3": 19,
	"local4": 20, "local5": 21, "local6": 22, "local7": 23,
}

// parseFacilityList accepts a comma-separated list of syslog facility
// names or 0..23 numeric codes and returns the resolved numeric set.
// Unknown names return an error identifying the offender. Slinit
// events don't currently carry a facility field (only priority), so
// the parsed list is stored for future use and surfaces a WARN at
// query time — the flag is present so scripts written for systemd's
// journalctl don't fail with "unknown argument".
func parseFacilityList(s string) ([]int, error) {
	var out []int
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if n, err := strconv.Atoi(tok); err == nil {
			if n < 0 || n > 23 {
				return nil, fmt.Errorf("facility %d out of range 0..23", n)
			}
			out = append(out, n)
			continue
		}
		n, ok := facilityNames[strings.ToLower(tok)]
		if !ok {
			return nil, fmt.Errorf("unknown facility %q", tok)
		}
		out = append(out, n)
	}
	return out, nil
}

// parsePriorityArg accepts both numeric (0..7) and symbolic priority
// names (emerg, alert, crit, err, warning, notice, info, debug — plus
// systemd short aliases). Numeric parse is tried first so "3" beats
// having "3" as a symbolic name.
func parsePriorityArg(s string) (journal.Priority, error) {
	if n, err := strconv.Atoi(s); err == nil {
		if n < 0 || n > 7 {
			return 0, fmt.Errorf("priority %d out of range 0..7", n)
		}
		return journal.Priority(n), nil
	}
	return journal.ParsePriorityName(s)
}

// formatCursor encodes an event's position as `s=<ts_nsec>;b=<boot_id>`.
// The ts is the wall-clock nanosecond, boot_id the 32-hex machine boot
// identifier. Cursor tokens are stable across process restarts as long
// as the boot didn't change; when boot_id differs, resume returns an
// error so callers know their position is meaningless in the new boot.
//
// Kept string-based (not opaque bytes) for the same reason as systemd:
// operators paste cursors into shell scripts, config files, and issue
// trackers — text round-trips cleanly, binary doesn't.
func formatCursor(e *journal.Event) string {
	return fmt.Sprintf("s=%d;b=%s", e.Ts, e.BootID)
}

// parseCursor decodes a cursor produced by formatCursor. Returns
// (ts_nsec, boot_id, error). Both fields must be present; missing
// `s=` or `b=` is a hard error rather than silently applying defaults
// so a truncated cursor from a broken pipe surfaces loudly rather
// than replaying from t=0.
func parseCursor(s string) (int64, string, error) {
	var ts int64
	var bootID string
	haveTs, haveBoot := false, false
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "s=") {
			v, err := strconv.ParseInt(part[2:], 10, 64)
			if err != nil {
				return 0, "", fmt.Errorf("cursor: bad seq %q: %w", part[2:], err)
			}
			ts = v
			haveTs = true
		} else if strings.HasPrefix(part, "b=") {
			bootID = part[2:]
			haveBoot = true
		} else {
			return 0, "", fmt.Errorf("cursor: unknown component %q", part)
		}
	}
	if !haveTs || !haveBoot {
		return 0, "", errors.New("cursor: both s= and b= are required")
	}
	return ts, bootID, nil
}

// parseTimeArg accepts:
//   - "now" — current wall clock
//   - "today" / "yesterday" — local midnight-based
//   - RFC3339 (2026-07-31T12:00:00Z or with offset)
//   - "YYYY-MM-DD HH:MM:SS" — local wall time
//   - "YYYY-MM-DD" — local midnight
//   - relative "-Ns" / "-Nm" / "-Nh" / "-Nd" — subtract from now
//
// Range: nothing supported earlier than 1970-01-01 (Unix epoch).
// Returns nil error only if we could resolve the string to a real time.
func parseTimeArg(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, errors.New("empty time")
	}
	switch strings.ToLower(s) {
	case "now":
		return now, nil
	case "today":
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()), nil
	case "yesterday":
		y := now.AddDate(0, 0, -1)
		return time.Date(y.Year(), y.Month(), y.Day(), 0, 0, 0, 0, y.Location()), nil
	}
	// Relative: "-1h", "-30m", "-2d", "-45s".
	if strings.HasPrefix(s, "-") {
		body := s[1:]
		if len(body) < 2 {
			return time.Time{}, fmt.Errorf("bad relative time %q", s)
		}
		unit := body[len(body)-1]
		val, err := strconv.Atoi(body[:len(body)-1])
		if err != nil || val < 0 {
			return time.Time{}, fmt.Errorf("bad relative time %q", s)
		}
		var d time.Duration
		switch unit {
		case 's':
			d = time.Duration(val) * time.Second
		case 'm':
			d = time.Duration(val) * time.Minute
		case 'h':
			d = time.Duration(val) * time.Hour
		case 'd':
			d = time.Duration(val) * 24 * time.Hour
		default:
			return time.Time{}, fmt.Errorf("bad relative unit %q (want s/m/h/d)", string(unit))
		}
		return now.Add(-d), nil
	}
	// Fixed formats, tried in order.
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("could not parse %q as time", s)
}

// parseFormat validates a -o value against validFormats. Returns the
// canonical outputFormat on match, or an error with the enumeration on
// mismatch so users see exactly what's supported.
func parseFormat(v string) (outputFormat, error) {
	for _, f := range validFormats {
		if string(f) == v {
			return f, nil
		}
	}
	names := make([]string, len(validFormats))
	for i, f := range validFormats {
		names[i] = string(f)
	}
	return "", fmt.Errorf("invalid --output %q (valid: %s)", v, strings.Join(names, ", "))
}

// resolveSocketPath mirrors slinitctl's resolution order: explicit
// --socket-path wins; --system forces the system socket; --user forces
// $XDG_RUNTIME_DIR/slinitctl (or $HOME/.slinitctl); the default falls
// back to system when run as root, user otherwise.
func resolveSocketPath(opts options) string {
	if opts.socketPath != "" {
		return opts.socketPath
	}
	if opts.systemMode {
		return defaultSystemSocket
	}
	if !opts.userMode && os.Getuid() == 0 {
		return defaultSystemSocket
	}
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return xdg + "/slinitctl"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return defaultUserSocket
	}
	return home + "/" + defaultUserSocket
}

// runQuery dispatches to one of three paths based on opts:
//   --file=PATH   → runFromFile: parse a JSONL file, filter in-process
//                   (works offline / on rotated journals / in containers
//                   with no control socket)
//   --follow      → runFollow: subscribe to live events via CmdJournalSubscribe
//   default       → runOneShot: single CmdJournalQuery round-trip
//
// The file path never dials a socket, so --file + --follow is rejected
// (inotify-based file follow arrives with Phase 3 / 2h cursor work).
func runQuery(opts options) error {
	// Warn once about flags whose semantics don't map to slinit's
	// journal model. The flag is still parsed so scripts don't break;
	// the operator just learns not to rely on it.
	if opts.facilitySet && !opts.quiet {
		fmt.Fprintln(os.Stderr, "slinit-journalctl: --facility parsed but ignored (slinit events don't record a syslog facility yet)")
	}
	if opts.merge && !opts.quiet {
		// Merge is a no-op on a single-source setup; harmless. Only
		// worth mentioning if the operator combined it with something
		// hinting they expected multi-source behavior.
		if opts.directory == "" && opts.root == "" && opts.sourceFile == "" && !opts.quiet {
			// Silent by default — noise only appears when the user
			// might reasonably wonder. Currently that's always.
		}
	}

	// Introspection short-circuits — none of them require a live
	// journal connection except --disk-usage (which reads /var/log)
	// and --field/--fields (which iterate events, so they do need one).

	if opts.fieldsList {
		return runFieldsList(os.Stdout)
	}
	if opts.diskUsage {
		return runDiskUsage(opts, os.Stdout)
	}

	if opts.sourceFile != "" {
		if opts.follow {
			return errors.New("--file and --follow are mutually exclusive (file-follow lands with Phase 3)")
		}
		if opts.headerDump {
			return runHeaderDump(opts, os.Stdout)
		}
		if opts.fieldName != "" {
			return runFieldValuesFromFile(opts, os.Stdout)
		}
		return runFromFile(opts)
	}

	// --directory: iterate every *.jsonl / *.jsonl.gz / binary journal
	// in DIR (recursively). Each file gets rendered in filesystem
	// order — deterministic enough for grep pipelines without a
	// separate sort pass.
	if opts.directory != "" {
		if opts.follow {
			return errors.New("--directory and --follow are mutually exclusive")
		}
		return runFromDirectory(opts)
	}

	sockPath := resolveSocketPath(opts)
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return fmt.Errorf("dial %s: %w", sockPath, err)
	}
	defer conn.Close()

	if opts.listBoots {
		return runListBoots(conn)
	}
	if opts.headerDump {
		return runHeaderDumpLive(conn, os.Stdout)
	}
	if opts.fieldName != "" {
		return runFieldValues(conn, opts, os.Stdout)
	}
	if opts.bootSet {
		if err := verifyBootID(conn, opts.bootID); err != nil {
			return err
		}
	}
	if opts.follow {
		return runFollow(conn, opts)
	}
	return runOneShot(conn, opts)
}

// runFieldsList prints the set of field names slinit's Event schema
// exposes, matching systemd's `journalctl --fields`. The list is
// sorted so scripts that grep -x won't fail on reordering.
func runFieldsList(out io.Writer) error {
	fields := []string{
		"MESSAGE", "PRIORITY", "SYSLOG_IDENTIFIER", "TRANSPORT",
		"UNIT", "TS_NSEC", "MTS_NSEC",
		"_PID", "_UID", "_GID", "_COMM", "_EXE", "_CMDLINE",
		"_HOSTNAME", "_BOOT_ID", "_MACHINE_ID", "_TRANSPORT",
		// Slinit-native metadata a service can inject via
		// pkg/service/journal_emit.go.
		"SLINIT_EVENT", "SLINIT_SERVICE_STATE", "SLINIT_TARGET_PID",
	}
	for _, f := range fields {
		fmt.Fprintln(out, f)
	}
	return nil
}

// runFieldValues queries all events matching the current filter, then
// prints the distinct values seen for opts.fieldName. Systemd's
// `-F FIELD` semantics: iterate journal, project to the named field,
// dedupe, sort, print one per line.
func runFieldValues(conn net.Conn, opts options, out io.Writer) error {
	req := buildRequest(opts)
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if err := control.WritePacket(conn, control.CmdJournalQuery, payload); err != nil {
		return err
	}
	events, err := collectStream(conn)
	if err != nil {
		return err
	}
	return emitDistinctFieldValues(out, events, opts.fieldName)
}

// runFieldValuesFromFile is the --file counterpart of runFieldValues.
// Reads the file straight into memory, applies the filter, projects.
func runFieldValuesFromFile(opts options, out io.Writer) error {
	events, err := loadFileEvents(opts)
	if err != nil {
		return err
	}
	return emitDistinctFieldValues(out, events, opts.fieldName)
}

// emitDistinctFieldValues extracts opts.fieldName from every event,
// deduplicates, sorts, and prints one per line. Field name resolution
// mirrors the verbose renderer so "-F MESSAGE" pulls Msg, "-F _PID"
// pulls Pid, etc. Freeform keys fall through to Event.Fields lookup
// so slinit-native fields (SLINIT_*) work without extra plumbing.
func emitDistinctFieldValues(out io.Writer, events []*journal.Event, field string) error {
	seen := make(map[string]struct{})
	for _, e := range events {
		v := extractField(e, field)
		if v == "" {
			continue
		}
		seen[v] = struct{}{}
	}
	values := make([]string, 0, len(seen))
	for v := range seen {
		values = append(values, v)
	}
	// Stable sort so consecutive runs show identical output.
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j-1] > values[j]; j-- {
			values[j-1], values[j] = values[j], values[j-1]
		}
	}
	for _, v := range values {
		fmt.Fprintln(out, v)
	}
	return nil
}

// extractField reads a single named field off an Event using the same
// field names the renderers emit. Underscore-prefixed keys map to
// trusted metadata; upper-case bare names map to core fields; anything
// else falls through to the freeform Fields map so SLINIT_* and
// operator-injected keys work uniformly.
func extractField(e *journal.Event, name string) string {
	switch name {
	case "MESSAGE":
		return e.Msg
	case "PRIORITY":
		return e.Prio.String()
	case "SYSLOG_IDENTIFIER":
		return e.SyslogIdentifier
	case "TRANSPORT", "_TRANSPORT":
		return string(e.Transport)
	case "UNIT":
		return e.Unit
	case "TS_NSEC":
		return strconv.FormatInt(e.Ts, 10)
	case "MTS_NSEC":
		return strconv.FormatInt(e.Mts, 10)
	case "_PID":
		if e.Pid == 0 {
			return ""
		}
		return strconv.Itoa(e.Pid)
	case "_UID":
		if e.Uid == 0 {
			return ""
		}
		return strconv.Itoa(e.Uid)
	case "_GID":
		if e.Gid == 0 {
			return ""
		}
		return strconv.Itoa(e.Gid)
	case "_COMM":
		return e.Comm
	case "_EXE":
		return e.Exe
	case "_CMDLINE":
		return e.Cmdline
	case "_HOSTNAME":
		return e.Hostname
	case "_BOOT_ID":
		return e.BootID
	case "_MACHINE_ID":
		return e.MachineID
	}
	if e.Fields != nil {
		return e.Fields[name]
	}
	return ""
}

// loadFileEvents is a helper that funnels --file loading through the
// same filter path used by runFromFile/runFromBinaryFile, returning
// the filtered event slice for callers that want to project or count
// rather than render.
func loadFileEvents(opts options) ([]*journal.Event, error) {
	filter := buildRequest(opts).ToFilter()
	if isBinaryJournal(opts.sourceFile) {
		r, err := journalbin.OpenReader(opts.sourceFile)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		var events []*journal.Event
		err = r.Iter(func(e *journal.Event) bool {
			if filter.Match(e) {
				events = append(events, e)
			}
			return true
		})
		return events, err
	}
	var r io.ReadCloser
	if strings.HasSuffix(opts.sourceFile, ".gz") {
		rc, err := journald.OpenCompressed(opts.sourceFile)
		if err != nil {
			return nil, err
		}
		r = rc
	} else {
		f, err := os.Open(opts.sourceFile)
		if err != nil {
			return nil, err
		}
		r = f
	}
	defer r.Close()
	return readJSONLFile(r, filter, 0)
}

// runHeaderDump prints metadata about a --file source: for a binary
// journal, the SLJRNL01 header fields; for JSONL, first/last event
// stats. Matches systemd's `journalctl --header` which shows one
// header block per journal file it touches.
func runHeaderDump(opts options, out io.Writer) error {
	if isBinaryJournal(opts.sourceFile) {
		r, err := journalbin.OpenReader(opts.sourceFile)
		if err != nil {
			return err
		}
		defer r.Close()
		hdr := r.Header()
		fmt.Fprintf(out, "File: %s\n", opts.sourceFile)
		fmt.Fprintf(out, "  Magic: %s\n", string(hdr.Magic[:]))
		fmt.Fprintf(out, "  Compat flags: 0x%08x  Incompat flags: 0x%08x\n", hdr.CompatFlags, hdr.IncompatFlags)
		fmt.Fprintf(out, "  Boot ID: %x\n", hdr.BootID)
		fmt.Fprintf(out, "  Machine ID: %x\n", hdr.MachineID)
		fmt.Fprintf(out, "  Seqnum range: %d..%d\n", hdr.HeadEntrySeqnum, hdr.TailEntrySeqnum)
		fmt.Fprintf(out, "  Realtime range: %s..%s\n",
			time.Unix(0, int64(hdr.HeadEntryRealtime)*1000).Format(time.RFC3339),
			time.Unix(0, int64(hdr.TailEntryRealtime)*1000).Format(time.RFC3339))
		fmt.Fprintf(out, "  Objects: %d\n", hdr.NObjects)
		fmt.Fprintf(out, "  Entries: %d\n", hdr.NEntries)
		return nil
	}
	// JSONL: stat + count.
	fi, err := os.Stat(opts.sourceFile)
	if err != nil {
		return err
	}
	events, err := loadFileEvents(opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "File: %s (JSONL, %d bytes)\n", opts.sourceFile, fi.Size())
	fmt.Fprintf(out, "  Entries: %d\n", len(events))
	if len(events) > 0 {
		fmt.Fprintf(out, "  Realtime range: %s..%s\n",
			time.Unix(0, events[0].Ts).Format(time.RFC3339),
			time.Unix(0, events[len(events)-1].Ts).Format(time.RFC3339))
		fmt.Fprintf(out, "  Boot IDs seen: %s\n", strings.Join(distinctBootIDs(events), ", "))
	}
	return nil
}

// runHeaderDumpLive prints a summary of the running daemon's in-process
// ring buffer, since there's no on-disk file to describe. Systemd's
// --header without --file inspects /var/log/journal; slinit's live
// equivalent is the memory buffer.
func runHeaderDumpLive(conn net.Conn, out io.Writer) error {
	payload, _ := json.Marshal(control.JournalQueryRequest{})
	if err := control.WritePacket(conn, control.CmdJournalQuery, payload); err != nil {
		return err
	}
	events, err := collectStream(conn)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Source: in-process ring buffer via control socket")
	fmt.Fprintf(out, "  Entries: %d\n", len(events))
	if len(events) > 0 {
		fmt.Fprintf(out, "  Realtime range: %s..%s\n",
			time.Unix(0, events[0].Ts).Format(time.RFC3339),
			time.Unix(0, events[len(events)-1].Ts).Format(time.RFC3339))
		fmt.Fprintf(out, "  Boot IDs seen: %s\n", strings.Join(distinctBootIDs(events), ", "))
	}
	return nil
}

// distinctBootIDs collects and stably sorts the unique boot IDs seen
// in the event slice.
func distinctBootIDs(events []*journal.Event) []string {
	seen := make(map[string]struct{})
	for _, e := range events {
		if e.BootID != "" {
			seen[e.BootID] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// runDiskUsage prints total bytes consumed by on-disk journals under
// the configured journal directory. Matches systemd's --disk-usage
// which sums /var/log/journal recursively.
func runDiskUsage(opts options, out io.Writer) error {
	dir := opts.directory
	if dir == "" {
		dir = "/var/log/slinit-journal"
	}
	if opts.root != "" {
		dir = filepath.Join(opts.root, dir)
	}
	var total int64
	var count int
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Missing dir is fine (fresh install, nothing rotated yet).
			return nil
		}
		if info.IsDir() {
			return nil
		}
		total += info.Size()
		count++
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Journal path %s: %d file(s), %s total\n", dir, count, formatBytes(total))
	return nil
}

// runFromDirectory walks opts.directory (honoring --root prefix if
// set) collecting every plausible journal file and delegates to the
// existing runFromFile-style logic per file. Renders straight to
// stdout in walk order — deterministic per filesystem, which is what
// scripts want when piped into grep / awk.
func runFromDirectory(opts options) error {
	dir := opts.directory
	if opts.root != "" {
		dir = filepath.Join(opts.root, dir)
	}
	files, err := journalFilesUnder(dir)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		if !opts.quiet {
			fmt.Fprintf(os.Stderr, "slinit-journalctl: no journal files under %s\n", dir)
		}
		return nil
	}
	// Delegate per-file to the existing single-file renderer by
	// mutating a shallow-copy of opts. Filter matching stays consistent
	// because both paths derive from buildRequest → ToFilter.
	for _, f := range files {
		perFile := opts
		perFile.sourceFile = f
		if err := runFromFile(perFile); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
	}
	return nil
}

// journalFilesUnder walks dir and returns every path with a .jsonl,
// .jsonl.gz, or .slj suffix. Symlinks are followed by filepath.Walk
// default (Stat, not Lstat) which is what we want for admins who
// bind-mount rotated journals into place.
func journalFilesUnder(dir string) ([]string, error) {
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		name := info.Name()
		if strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".jsonl.gz") || strings.HasSuffix(name, ".slj") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// formatBytes turns byte counts into human-readable K/M/G with one
// decimal — enough resolution to spot a runaway journal without a
// second decimal that just adds noise.
func formatBytes(n int64) string {
	const (
		K = 1024
		M = K * 1024
		G = M * 1024
	)
	switch {
	case n >= G:
		return fmt.Sprintf("%.1fG", float64(n)/float64(G))
	case n >= M:
		return fmt.Sprintf("%.1fM", float64(n)/float64(M))
	case n >= K:
		return fmt.Sprintf("%.1fK", float64(n)/float64(K))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// runListBoots queries the buffer once, reports the boot(s) it
// covers, and exits. In Phase 2 the buffer holds one boot's worth
// of events (the process resets IDs on restart), so this prints
// exactly one row: `0 <boot_id> <first_ts>—<last_ts>`. When Phase 3
// starts persisting rotated journals across boots the server reply
// will grow to multiple rows and this same output loop handles them.
func runListBoots(conn net.Conn) error {
	// Query with no filter, no limit — we just need the min/max Ts
	// per distinct BootID in the buffer.
	payload, err := json.Marshal(control.JournalQueryRequest{})
	if err != nil {
		return err
	}
	if err := control.WritePacket(conn, control.CmdJournalQuery, payload); err != nil {
		return err
	}
	events, err := collectStream(conn)
	if err != nil {
		return err
	}
	type bootRange struct {
		id       string
		first    int64
		last     int64
	}
	byID := map[string]*bootRange{}
	order := []string{} // stable iteration order
	for _, e := range events {
		if e.BootID == "" {
			continue
		}
		r, ok := byID[e.BootID]
		if !ok {
			r = &bootRange{id: e.BootID, first: e.Ts, last: e.Ts}
			byID[e.BootID] = r
			order = append(order, e.BootID)
			continue
		}
		if e.Ts < r.first {
			r.first = e.Ts
		}
		if e.Ts > r.last {
			r.last = e.Ts
		}
	}
	if len(order) == 0 {
		// No events seen yet — still emit the current boot so the
		// operator sees SOMETHING (matches `journalctl --list-boots`
		// behaviour on a freshly-booted system before any logging).
		fmt.Printf(" 0 %s (no events yet)\n", journal.BootID())
		return nil
	}
	for i, id := range order {
		r := byID[id]
		// Newest first, index 0 = current boot per journalctl UX.
		idx := i - (len(order) - 1)
		fmt.Printf("%3d %s %s—%s\n",
			idx, id,
			time.Unix(0, r.first).Format(time.RFC3339),
			time.Unix(0, r.last).Format(time.RFC3339),
		)
	}
	return nil
}

// verifyBootID checks whether the requested boot spec matches the
// current boot. Accepts:
//   ""       → current boot (--boot / -b without arg)
//   "0"      → current boot (systemd shorthand)
//   <32 hex> → specific boot ID; must match current (multi-boot
//              indexing across on-disk rotated files not yet wired
//              in the control-socket query path)
//   "-N"     → negative index (systemd "N boots ago") — currently
//              rejected with a helpful message pointing at
//              --list-boots for lookup
func verifyBootID(conn net.Conn, want string) error {
	if want == "" || want == "0" {
		return nil
	}
	if strings.HasPrefix(want, "-") {
		return fmt.Errorf("-b %s: relative boot indexing not yet supported; use --list-boots to get full IDs and pass one explicitly",
			want)
	}
	// Query one event to fish out the server's BootID cheaply.
	payload, _ := json.Marshal(control.JournalQueryRequest{Limit: 1})
	if err := control.WritePacket(conn, control.CmdJournalQuery, payload); err != nil {
		return err
	}
	events, err := collectStream(conn)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return fmt.Errorf("--boot %s: no events on this boot yet to compare against", want)
	}
	if events[0].BootID != want {
		return fmt.Errorf("--boot %s: cross-boot journal queries not yet wired via the control socket; current boot is %s",
			want, events[0].BootID)
	}
	return nil
}

// looksLikeBootSpec reports whether a token peek'd during flag
// parsing looks like a boot spec argument to --boot / -b rather
// than a separate flag. Accepts:
//   - anything that doesn't start with '-' (positive index or hex ID)
//   - "-N" where N is all digits (relative boot index)
// Rejects other "-…" tokens which are always flags.
func looksLikeBootSpec(s string) bool {
	if s == "" {
		return false
	}
	if !strings.HasPrefix(s, "-") {
		return true
	}
	// "-<digits>" is a relative-boot spec, not a flag.
	if len(s) < 2 {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// runFromFile reads a journal file — either the binary Phase B format
// (detected by the 8-byte SLJRNL01 magic) or the Phase C JSONL text
// format (default when magic mismatches). Applies the CLI filter
// in-process; results bit-identical to control-socket queries.
//
// --verify: only valid against a binary file. Walks the FSS TAG chain
// using the key at --fss-key (or default /etc/slinit/journal-key) and
// reports "OK" or the first bad TAG's offset.
//
// .jsonl.gz files decompress transparently. Binary files are not
// compressed in v1 (see pkg/journalbin/compress design note).
func runFromFile(opts options) error {
	if opts.verify {
		return runVerify(opts)
	}
	// Peek at magic to route.
	if isBinaryJournal(opts.sourceFile) {
		return runFromBinaryFile(opts)
	}
	var r io.ReadCloser
	if strings.HasSuffix(opts.sourceFile, ".gz") {
		rc, err := journald.OpenCompressed(opts.sourceFile)
		if err != nil {
			return fmt.Errorf("open %s: %w", opts.sourceFile, err)
		}
		r = rc
	} else {
		f, err := os.Open(opts.sourceFile)
		if err != nil {
			return fmt.Errorf("open %s: %w", opts.sourceFile, err)
		}
		r = f
	}
	defer r.Close()

	filter := buildRequest(opts).ToFilter()
	events, err := readJSONLFile(r, filter, opts.limit)
	if err != nil {
		return err
	}
	if opts.reverse {
		for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
			events[i], events[j] = events[j], events[i]
		}
	}
	ro := buildRenderOpts(opts)
	out := os.Stdout
	for _, evt := range events {
		if err := render(out, opts.format, evt, ro); err != nil {
			return fmt.Errorf("render: %w", err)
		}
	}
	return nil
}

// isBinaryJournal peeks the first 8 bytes of path and reports whether
// they match the pkg/journalbin SLJRNL01 magic. Silently returns
// false on read error (caller then treats path as JSONL and either
// parses it or surfaces the read error there).
func isBinaryJournal(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var buf [8]byte
	if _, err := f.Read(buf[:]); err != nil {
		return false
	}
	return buf == journalbin.Magic
}

// runFromBinaryFile opens the file via journalbin.OpenReader (which
// validates magic + incompat flags), applies the CLI filter, and
// renders survivors. Reuses buildRequest().ToFilter() so filter
// semantics match every other path.
func runFromBinaryFile(opts options) error {
	r, err := journalbin.OpenReader(opts.sourceFile)
	if err != nil {
		return err
	}
	defer r.Close()

	filter := buildRequest(opts).ToFilter()
	var events []*journal.Event
	err = r.Iter(func(e *journal.Event) bool {
		if !filter.Match(e) {
			return true
		}
		events = append(events, e)
		if opts.limit > 0 && len(events) > opts.limit {
			events = events[1:]
		}
		return true
	})
	if err != nil {
		return err
	}
	if opts.reverse {
		for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
			events[i], events[j] = events[j], events[i]
		}
	}
	ro := buildRenderOpts(opts)
	out := os.Stdout
	for _, evt := range events {
		if err := render(out, opts.format, evt, ro); err != nil {
			return fmt.Errorf("render: %w", err)
		}
	}
	return nil
}

// runVerify walks the FSS TAG chain of the binary journal at
// opts.sourceFile. Requires an FSS key — either the path in
// opts.fssKeyPath, or the default /etc/slinit/journal-key when the
// flag is empty and the file is readable.
func runVerify(opts options) error {
	if opts.sourceFile == "" {
		return errors.New("--verify requires --file PATH")
	}
	keyPath := opts.fssKeyPath
	if keyPath == "" {
		keyPath = "/etc/slinit/journal-key"
	}
	key, err := journalbin.LoadFSSKey(keyPath)
	if err != nil {
		return err
	}
	res, err := journalbin.Verify(opts.sourceFile, key)
	if err != nil {
		return err
	}
	if !res.SealingEnabled {
		fmt.Printf("%s: FSS not enabled on this file (nothing to verify)\n", opts.sourceFile)
		return nil
	}
	if res.OK() {
		fmt.Printf("%s: OK (%d tags verified)\n", opts.sourceFile, res.TagsChecked)
		return nil
	}
	fmt.Printf("%s: TAMPER DETECTED at tag offset %d (seqnum %d, %d prior tags OK)\n",
		opts.sourceFile, res.FirstBadTagOffset, res.FirstBadTagSeqnum, res.TagsChecked)
	return fmt.Errorf("verify: tamper at offset %d", res.FirstBadTagOffset)
}

// readJSONLFile streams a JSONL reader, decoding each line into an
// Event and keeping only those the filter accepts. When limit > 0 we
// keep a rolling tail of the last N matches so a huge file doesn't
// blow up memory. Blank lines and lines that fail to parse are skipped
// (corruption tolerance — a rotated file with a trailing partial write
// still returns everything up to the break).
func readJSONLFile(r io.Reader, filter journal.QueryFilter, limit int) ([]*journal.Event, error) {
	scanner := bufio.NewScanner(r)
	// Journal events cap at 512 KiB (MaxEventSize); allow the scanner
	// to read a full oversized line without the default 64 KiB cap
	// truncating a legitimate large entry.
	scanner.Buffer(make([]byte, 64*1024), journal.MaxEventSize+1024)

	var kept []*journal.Event
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		evt, err := journal.UnmarshalEvent(line)
		if err != nil {
			// Corruption / partial write — skip and continue rather
			// than abort. The exit path can log a warning if it cares.
			continue
		}
		if !filter.Match(evt) {
			continue
		}
		kept = append(kept, evt)
		if limit > 0 && len(kept) > limit {
			// Drop from the head to keep only the most-recent limit
			// matches, matching the server-side tail semantics.
			kept = kept[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	return kept, nil
}

// runOneShot sends CmdJournalQuery, reads the full reply, optionally
// reverses, renders every event, and (when --show-cursor is set)
// prints a resumable cursor on a trailing "-- cursor:" line.
func runOneShot(conn net.Conn, opts options) error {
	req := buildRequest(opts)

	// Cursor resolution order (only one of these should be set at a
	// time; the parser doesn't enforce mutual exclusion but the last
	// non-empty value wins so the semantics stay predictable):
	//   1. --after-cursor  → strictly after (Since = ts + 1)
	//   2. --cursor        → at or after   (Since = ts)   [systemd -c semantics]
	//   3. --cursor-file   → same as --cursor but read from FILE
	cursorToken, cursorMode, err := resolveCursorInput(opts)
	if err != nil {
		return err
	}
	var wantBoot string
	if cursorToken != "" {
		ts, boot, err := parseCursor(cursorToken)
		if err != nil {
			return err
		}
		wantBoot = boot
		switch cursorMode {
		case cursorAfter:
			req.Since = ts + 1
		default:
			req.Since = ts
		}
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	if err := control.WritePacket(conn, control.CmdJournalQuery, payload); err != nil {
		return fmt.Errorf("send query: %w", err)
	}
	events, err := collectStream(conn)
	if err != nil {
		return err
	}
	// Client-side re-filter: older daemons ignore Identifiers/Grep
	// and return everything. Running the filter locally guarantees
	// correctness regardless of daemon vintage. Same code path also
	// makes --file / --directory results consistent with socket ones.
	events = clientSideFilter(events, req)
	if wantBoot != "" && len(events) > 0 && events[0].BootID != wantBoot {
		return fmt.Errorf("cursor: boot changed (was %s, now %s); position is meaningless in the new boot",
			wantBoot, events[0].BootID)
	}
	if opts.reverse {
		for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
			events[i], events[j] = events[j], events[i]
		}
	}
	ro := buildRenderOpts(opts)
	out := os.Stdout
	for _, evt := range events {
		if err := render(out, opts.format, evt, ro); err != nil {
			return fmt.Errorf("render: %w", err)
		}
	}
	if opts.showCursor && len(events) > 0 {
		last := events[len(events)-1]
		if opts.reverse {
			// After a reverse we want the newest event's cursor (which
			// is now events[0]), not the tail. That way a next run
			// with `-c LAST` picks up from where the newest visible
			// event was, not the oldest.
			last = events[0]
		}
		fmt.Fprintf(out, "-- cursor: %s\n", formatCursor(last))
	}
	// --cursor-file persists the trailing cursor so a subsequent run
	// with the same flag resumes precisely where this one left off
	// (mimicking systemd's atomic cursor-file update). We write even
	// on empty results so an operator scripting a scan sees a stable
	// path (older cursor stays valid if nothing new arrived).
	if opts.cursorFile != "" && len(events) > 0 {
		last := events[len(events)-1]
		if opts.reverse {
			last = events[0]
		}
		if err := writeCursorFile(opts.cursorFile, formatCursor(last)); err != nil {
			return fmt.Errorf("cursor-file: %w", err)
		}
	}
	return nil
}

// cursorMode discriminates inclusive (--cursor) vs exclusive
// (--after-cursor) positioning. --cursor-file inherits inclusive
// since it stores the last emitted cursor which the operator wants
// to re-emit only if the daemon rolled back (unlikely).
type cursorMode int

const (
	cursorInclusive cursorMode = iota
	cursorAfter
)

// resolveCursorInput picks the effective cursor token + mode from
// opts, respecting the priority chain declared in runOneShot. Returns
// ("", cursorInclusive, nil) when no cursor input was provided.
func resolveCursorInput(opts options) (string, cursorMode, error) {
	if opts.afterCursor != "" {
		return opts.afterCursor, cursorAfter, nil
	}
	if opts.cursor != "" {
		return opts.cursor, cursorInclusive, nil
	}
	if opts.cursorFile != "" {
		data, err := os.ReadFile(opts.cursorFile)
		if err != nil {
			// Missing file is treated as "no prior cursor" rather
			// than a hard error — first-time invocation of a
			// script using --cursor-file must be allowed to
			// bootstrap from scratch.
			if errors.Is(err, os.ErrNotExist) {
				return "", cursorInclusive, nil
			}
			return "", cursorInclusive, fmt.Errorf("read cursor-file %s: %w", opts.cursorFile, err)
		}
		return strings.TrimSpace(string(data)), cursorInclusive, nil
	}
	return "", cursorInclusive, nil
}

// writeCursorFile atomically writes token to path via a temp+rename
// dance — a torn write during shutdown must never leave the operator
// with a corrupted cursor that can't be resumed from.
func writeCursorFile(path, token string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(token+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// runFollow sends CmdJournalSubscribe and renders each RplyJournalEntry
// as it arrives. Never terminates unless the server closes the
// connection or the user sends SIGINT (which surfaces as a read error
// on Ctrl-C via the closed stdin/socket path). -r is ignored under -f
// because reversing an infinite stream would require buffering
// forever.
func runFollow(conn net.Conn, opts options) error {
	req := buildRequest(opts)
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	if err := control.WritePacket(conn, control.CmdJournalSubscribe, payload); err != nil {
		return fmt.Errorf("send subscribe: %w", err)
	}
	ro := buildRenderOpts(opts)
	out := os.Stdout
	for {
		typ, body, err := control.ReadPacket(conn)
		if err != nil {
			// EOF / read error typically means the server or the user
			// terminated the session. Return nil so the CLI exits 0
			// on a clean Ctrl-C rather than complaining loudly.
			return nil
		}
		switch typ {
		case control.RplyJournalEntry:
			evt, decodeErr := journal.UnmarshalEvent(body)
			if decodeErr != nil {
				return fmt.Errorf("decode entry: %w", decodeErr)
			}
			if err := render(out, opts.format, evt, ro); err != nil {
				return fmt.Errorf("render: %w", err)
			}
		case control.RplyJournalErr:
			return fmt.Errorf("server: %s", string(body))
		case control.RplyBadReq:
			return errors.New("server rejected CmdJournalSubscribe (older slinit without follow support?)")
		default:
			return fmt.Errorf("unexpected reply type %d", typ)
		}
	}
}

// buildRequest converts parsed CLI options into the on-wire journal
// query. Keeps the wire encoding in one place so 2d (subscribe) can
// share it and slinit-journald's replay tool (Phase 3) has a single
// authoritative reference.
func buildRequest(opts options) control.JournalQueryRequest {
	// --user-unit is treated as an additional -u source when the
	// operator is in user mode. Merging into Units keeps the wire
	// filter simple; the client-side scope check already ensures we
	// dialled the right socket.
	units := opts.units
	if len(opts.userUnitFilters) > 0 {
		units = append(append([]string{}, units...), opts.userUnitFilters...)
	}
	req := control.JournalQueryRequest{
		Units:              units,
		Since:              opts.since,
		Until:              opts.until,
		Limit:              opts.limit,
		Identifiers:        opts.identifiers,
		ExcludeIdentifiers: opts.excludeIdentifiers,
		GrepPattern:        opts.grep,
		// Systemd's `-g` default is case-insensitive when the pattern
		// is all-lowercase, case-sensitive otherwise; we mirror that
		// unless the operator overrode with --case-sensitive[=BOOL].
		GrepInsensitive: shouldGrepInsensitive(opts),
	}
	if opts.prioritySet {
		req.MinPriority = int(opts.priority)
		req.PrioritySet = true
	}
	if opts.kernelOnly {
		// -k is shorthand for --transport=kernel; keeping it in the
		// wire request means the server-side filter pushdown drops
		// non-kernel entries before they cross the socket. Note this
		// overrides any manual --transport if we ever add one later.
		req.Transports = []string{string(journal.TransportKernel)}
	}
	return req
}

// shouldGrepInsensitive implements systemd's --case-sensitive heuristic:
// explicit user override wins; otherwise pattern with any uppercase
// letter is case-sensitive, all-lowercase is case-insensitive.
func shouldGrepInsensitive(opts options) bool {
	if opts.grep == "" {
		return false
	}
	if opts.grepCaseSet {
		return !opts.grepCaseSensitive
	}
	for _, r := range opts.grep {
		if r >= 'A' && r <= 'Z' {
			return false
		}
	}
	return true
}

// clientSideFilter re-applies the wire-request's filter locally so
// filter dimensions the daemon doesn't understand (introduced in
// v2.1.6 — Identifiers, ExcludeIdentifiers, GrepPattern) still work
// against older daemons that ignore the unknown JSON keys. Cheap
// enough to run unconditionally: buffer + filter is O(N) either way
// and the pkg/journal Match function is trivial.
func clientSideFilter(events []*journal.Event, req control.JournalQueryRequest) []*journal.Event {
	if len(req.Identifiers) == 0 && len(req.ExcludeIdentifiers) == 0 && req.GrepPattern == "" {
		return events
	}
	filter := req.ToFilter()
	kept := events[:0]
	for _, e := range events {
		if filter.Match(e) {
			kept = append(kept, e)
		}
	}
	return kept
}

// collectStream reads RplyJournalEntry packets from conn into a slice
// until it sees the terminator (RplyJournalDone or RplyJournalErr).
// Returns the events in the order the server sent them (chronological).
func collectStream(conn net.Conn) ([]*journal.Event, error) {
	var events []*journal.Event
	for {
		typ, body, err := control.ReadPacket(conn)
		if err != nil {
			return nil, fmt.Errorf("read reply: %w", err)
		}
		switch typ {
		case control.RplyJournalEntry:
			evt, err := journal.UnmarshalEvent(body)
			if err != nil {
				return nil, fmt.Errorf("decode entry: %w", err)
			}
			events = append(events, evt)
		case control.RplyJournalDone:
			return events, nil
		case control.RplyJournalErr:
			return nil, fmt.Errorf("server: %s", string(body))
		case control.RplyBadReq:
			return nil, errors.New("server rejected CmdJournalQuery (older slinit without journal support?)")
		default:
			return nil, fmt.Errorf("unexpected reply type %d", typ)
		}
	}
}

// renderOpts collects the display-side toggles from CLI options that
// each renderer needs to honor (--utc, --no-hostname, --truncate-newline,
// --no-full, --output-fields). Passed as a small value so we don't leak
// the full options struct into the render layer.
type renderOpts struct {
	utc             bool
	noHostname      bool
	truncateNewline bool
	noFull          bool
	outputFields    map[string]bool // nil = all fields; non-empty = keep only these
}

// buildRenderOpts extracts the render-relevant subset of options. The
// outputFields slice is turned into a set for O(1) membership tests.
func buildRenderOpts(opts options) renderOpts {
	ro := renderOpts{
		utc:             opts.utc,
		noHostname:      opts.noHostname,
		truncateNewline: opts.truncateNewline,
		noFull:          opts.noFull,
	}
	if len(opts.outputFields) > 0 {
		ro.outputFields = make(map[string]bool, len(opts.outputFields))
		for _, f := range opts.outputFields {
			ro.outputFields[f] = true
		}
	}
	return ro
}

// keepField reports whether field name should be included given the
// active --output-fields filter. Empty filter means "keep all".
func (ro renderOpts) keepField(name string) bool {
	if ro.outputFields == nil {
		return true
	}
	return ro.outputFields[name]
}

// truncateMsg applies --truncate-newline (cut at first LF) and
// --no-full (ellipsize at ~256 chars, matching systemd's default
// column limit for short outputs). Order matters: newline first so
// we don't ellipsize a boring first line just because the tail was
// long.
func (ro renderOpts) truncateMsg(msg string) string {
	if ro.truncateNewline {
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
	}
	if ro.noFull {
		const cap = 256
		if len(msg) > cap {
			msg = msg[:cap-3] + "..."
		}
	}
	return msg
}

// render dispatches to the format-specific writer. Chosen over a
// map[format]func table because Go's method values allocate; a switch
// stays cheap and reads naturally.
func render(out io.Writer, f outputFormat, e *journal.Event, ro renderOpts) error {
	switch f {
	case fmtShort:
		return renderShort(out, e, timeShort, ro)
	case fmtShortISO:
		return renderShort(out, e, timeISO, ro)
	case fmtCat:
		return renderCat(out, e, ro)
	case fmtJSON:
		return renderJSON(out, e)
	case fmtVerbose:
		return renderVerbose(out, e, ro)
	case fmtExport:
		return renderExport(out, e, ro)
	default:
		return fmt.Errorf("no renderer for format %q", f)
	}
}

// timeFormat picks the timestamp representation. systemd's "short"
// format is a fixed 15-char localtime "Jan 02 15:04:05"; short-iso is
// RFC3339 with numeric offset.
type timeFormat int

const (
	timeShort timeFormat = iota
	timeISO
)

// formatTime turns a nanosecond Unix timestamp into the display string
// for the selected timeFormat, honoring --utc.
func formatTime(nsec int64, tf timeFormat, utc bool) string {
	t := time.Unix(0, nsec)
	if utc {
		t = t.UTC()
	}
	switch tf {
	case timeISO:
		return t.Format(time.RFC3339)
	default:
		return t.Format("Jan 02 15:04:05")
	}
}

// identOf resolves the display name shown in short/short-iso before
// the message: SyslogIdentifier wins, then Unit, then Comm, then a
// literal "unknown" — matches systemd's short-format identifier
// resolution.
func identOf(e *journal.Event) string {
	switch {
	case e.SyslogIdentifier != "":
		return e.SyslogIdentifier
	case e.Unit != "":
		return e.Unit
	case e.Comm != "":
		return e.Comm
	default:
		return "unknown"
	}
}

// renderShort produces "Mmm DD HH:MM:SS HOSTNAME UNIT[PID]: MSG" (or
// short-iso variant with RFC3339 timestamp). The [PID] block is
// omitted when Pid==0 so slinit-driver events don't carry a noisy
// "[1]" suffix on every line.
func renderShort(out io.Writer, e *journal.Event, tf timeFormat, ro renderOpts) error {
	// Bracket display priority:
	//   1. SLINIT_TARGET_PID (subject service's live PID) — the
	//      operator wants "hello[478]:" not "hello[1]:".
	//   2. No bracket when the event is slinit-internal
	//      (Transport=driver/stdout) and no target PID exists —
	//      internal services and scripted services that already
	//      exited have PID <=0, and printing the emitter's PID=1
	//      instead would be misleading ("system-init[1]:" reads as
	//      if system-init ran as PID 1 which it did not).
	//   3. Fall back to _PID for external emitters (native, syslog,
	//      kernel-with-pid) where _PID IS the real source.
	pidPart := ""
	switch {
	case targetPIDOf(e) > 0:
		pidPart = fmt.Sprintf("[%d]", targetPIDOf(e))
	case e.Transport == journal.TransportDriver || e.Transport == journal.TransportStdout:
		// Slinit-internal event without a target PID → no bracket.
	case e.Pid > 0:
		pidPart = fmt.Sprintf("[%d]", e.Pid)
	}
	msg := ro.truncateMsg(e.Msg)
	if ro.noHostname {
		_, err := fmt.Fprintf(out, "%s %s%s: %s\n",
			formatTime(e.Ts, tf, ro.utc), identOf(e), pidPart, msg)
		return err
	}
	host := e.Hostname
	if host == "" {
		host = "-"
	}
	_, err := fmt.Fprintf(out, "%s %s %s%s: %s\n",
		formatTime(e.Ts, tf, ro.utc), host, identOf(e), pidPart, msg)
	return err
}

// targetPIDOf returns the SLINIT_TARGET_PID hint from an event's
// freeform Fields, or 0 when absent / malformed. Used by short-
// format renderers to display the subject service's PID instead of
// the emitter's.
func targetPIDOf(e *journal.Event) int {
	v, ok := e.Fields["SLINIT_TARGET_PID"]
	if !ok || v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

// renderCat prints just the message with a trailing newline, matching
// systemd's cat mode: the terminal-friendly form for feeding a pager
// or grep pipeline that shouldn't see timestamps.
func renderCat(out io.Writer, e *journal.Event, ro renderOpts) error {
	_, err := fmt.Fprintln(out, ro.truncateMsg(e.Msg))
	return err
}

// renderJSON emits one raw JSON object per line — the on-wire form
// of the event. Kept intentionally minimal (no re-marshal, no re-key)
// so future field additions round-trip byte-for-byte through
// slinit-journalctl -o json | jq without lossy repacking.
func renderJSON(out io.Writer, e *journal.Event) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = out.Write(append(data, '\n'))
	return err
}

// renderVerbose prints one field per line, prefixed with its name,
// mirroring systemd's verbose format. Field order is stable so diffs
// between two verbose dumps are meaningful. Empty fields are skipped
// so the output stays scannable on narrow terminals.
func renderVerbose(out io.Writer, e *journal.Event, ro renderOpts) error {
	// Timestamp header line — human-readable + machine-readable.
	fmt.Fprintf(out, "%s [%d.%09d]\n",
		formatTime(e.Ts, timeISO, ro.utc), e.Ts/1_000_000_000, e.Ts%1_000_000_000)
	writeFieldFiltered(out, ro, "TS_NSEC", strconv.FormatInt(e.Ts, 10))
	writeFieldFiltered(out, ro, "MTS_NSEC", strconv.FormatInt(e.Mts, 10))
	writeFieldFiltered(out, ro, "PRIORITY", e.Prio.String())
	writeFieldFiltered(out, ro, "TRANSPORT", string(e.Transport))
	writeFieldFiltered(out, ro, "UNIT", e.Unit)
	writeFieldFiltered(out, ro, "SYSLOG_IDENTIFIER", e.SyslogIdentifier)
	if e.Pid > 0 {
		writeFieldFiltered(out, ro, "_PID", strconv.Itoa(e.Pid))
	}
	if e.Uid > 0 {
		writeFieldFiltered(out, ro, "_UID", strconv.Itoa(e.Uid))
	}
	if e.Gid > 0 {
		writeFieldFiltered(out, ro, "_GID", strconv.Itoa(e.Gid))
	}
	writeFieldFiltered(out, ro, "_COMM", e.Comm)
	writeFieldFiltered(out, ro, "_EXE", e.Exe)
	writeFieldFiltered(out, ro, "_CMDLINE", e.Cmdline)
	if !ro.noHostname {
		writeFieldFiltered(out, ro, "_HOSTNAME", e.Hostname)
	}
	writeFieldFiltered(out, ro, "_BOOT_ID", e.BootID)
	writeFieldFiltered(out, ro, "_MACHINE_ID", e.MachineID)
	writeFieldFiltered(out, ro, "MESSAGE", ro.truncateMsg(e.Msg))
	// Freeform fields last, sorted for deterministic output.
	for _, k := range sortedKeys(e.Fields) {
		writeFieldFiltered(out, ro, k, e.Fields[k])
	}
	fmt.Fprintln(out) // blank line between records
	return nil
}

// renderExport produces systemd's "export" format: one KEY=value per
// line, blank line separates events. Piped to systemd-journal-remote
// and equivalents for cross-host log forwarding, or fed into custom
// parsers that want a stable line-oriented text protocol without
// JSON overhead.
//
// Binary payloads (values containing a NUL or newline) are NOT
// supported in v1 — export format has a workaround (length-prefixed
// binary section) but slinit's Event schema doesn't emit binary
// values anywhere, so we skip the escape until it's actually needed.
func renderExport(out io.Writer, e *journal.Event, ro renderOpts) error {
	// Timestamps + core.
	fmt.Fprintf(out, "__REALTIME_TIMESTAMP=%d\n", e.Ts/1000) // us
	fmt.Fprintf(out, "__MONOTONIC_TIMESTAMP=%d\n", e.Mts/1000)
	writeExportFieldFiltered(out, ro, "PRIORITY", strconv.Itoa(int(e.Prio)))
	writeExportFieldFiltered(out, ro, "MESSAGE", ro.truncateMsg(e.Msg))
	writeExportFieldFiltered(out, ro, "SYSLOG_IDENTIFIER", e.SyslogIdentifier)
	writeExportFieldFiltered(out, ro, "_TRANSPORT", string(e.Transport))
	writeExportFieldFiltered(out, ro, "_SLINIT_UNIT", e.Unit)
	if e.Pid > 0 {
		writeExportFieldFiltered(out, ro, "_PID", strconv.Itoa(e.Pid))
	}
	if e.Uid > 0 {
		writeExportFieldFiltered(out, ro, "_UID", strconv.Itoa(e.Uid))
	}
	if e.Gid > 0 {
		writeExportFieldFiltered(out, ro, "_GID", strconv.Itoa(e.Gid))
	}
	writeExportFieldFiltered(out, ro, "_COMM", e.Comm)
	writeExportFieldFiltered(out, ro, "_EXE", e.Exe)
	writeExportFieldFiltered(out, ro, "_CMDLINE", e.Cmdline)
	if !ro.noHostname {
		writeExportFieldFiltered(out, ro, "_HOSTNAME", e.Hostname)
	}
	writeExportFieldFiltered(out, ro, "_BOOT_ID", e.BootID)
	writeExportFieldFiltered(out, ro, "_MACHINE_ID", e.MachineID)
	// Freeform fields in stable (sorted) order — matches renderVerbose.
	for _, k := range sortedKeys(e.Fields) {
		writeExportFieldFiltered(out, ro, k, e.Fields[k])
	}
	_, err := fmt.Fprintln(out) // blank line between events
	return err
}

// writeExportField writes "KEY=value\n" only when value is non-empty.
// systemd's export format allows empty-value lines (KEY=) but slinit's
// convention (matching renderVerbose) is to skip them for readability.
func writeExportField(out io.Writer, key, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(out, "%s=%s\n", key, value)
}

// writeExportFieldFiltered is the --output-fields-aware sibling of
// writeExportField. Field is emitted only when both non-empty AND
// present in the active fields set (or set is nil = keep all).
func writeExportFieldFiltered(out io.Writer, ro renderOpts, key, value string) {
	if !ro.keepField(key) {
		return
	}
	writeExportField(out, key, value)
}

// writeField prints "    KEY=value" only when the value is non-empty
// — keeps the verbose renderer scannable by dropping unset fields.
func writeField(out io.Writer, name, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(out, "    %s=%s\n", name, value)
}

// writeFieldFiltered is the --output-fields-aware sibling of
// writeField. Same emit-only-if-nonempty semantics, plus the extra
// membership gate.
func writeFieldFiltered(out io.Writer, ro renderOpts, name, value string) {
	if !ro.keepField(name) {
		return
	}
	writeField(out, name, value)
}

// sortedKeys returns the keys of a string map in stable ascending
// order. Used by renderVerbose so field iteration is deterministic
// across runs — makes diffing two dumps meaningful.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Simple insertion sort — the map is bounded by MaxFields (64)
	// so quadratic behaviour is fine and we avoid the sort package
	// import.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}

// printHelp writes the flag reference. Kept in one place so `-h`,
// `--help`, and any generated man page share the same source.
func printHelp(out io.Writer) {
	fmt.Fprint(out, `Usage: slinit-journalctl [flags]

Query the slinit journal ring buffer via the control socket.

Flags:
  -n, --lines=N               Show only the last N matching events (0 = all)
      --no-tail               Show all matches (inverse of default -n heuristic)
  -o, --output=FMT            Output format: short (default), short-iso, cat, json, verbose, export
      --output-fields=A,B,C   Restrict verbose/export/JSON to these field keys
  -u, --unit=NAME             Filter by service unit (repeatable — OR-set)
  -U, --user-unit=NAME        Filter by user-scope unit (also forces --user)
  -t, --identifier=IDENT      Include events with matching SYSLOG_IDENTIFIER (repeatable)
  -T, --exclude-identifier=I  Drop events with matching SYSLOG_IDENTIFIER (repeatable)
      --facility=NAME|N       Parsed, accepted; slinit doesn't record facility yet (WARN emitted)
  -g, --grep=PATTERN          RE2 regex on MESSAGE; default case-insensitive if pattern is all-lowercase
      --case-sensitive[=BOOL] Override the case heuristic used by -g
  -p, --priority=LVL          Keep only events at LVL or more urgent (0..7 or emerg..debug)
      --since=TIME            Keep only events at or after TIME
      --until=TIME            Keep only events at or before TIME
  -r, --reverse               Print newest first (ignored under -f)
  -f, --follow                Stream new events as they arrive (Ctrl-C to stop)
  -k, --dmesg                 Show only kernel (kmsg) events
  -m, --merge                 Accept for parity; no-op on single-source setups
      --list-boots            List boot IDs the journal buffer covers and exit
  -b, --boot [ID]             Restrict to a boot (empty or "0" = current; full hex ID also accepted)
      --this-boot             Alias for --boot=0
  -c, --cursor=TOKEN          Resume at cursor (inclusive)
      --after-cursor=TOKEN    Resume strictly after cursor (exclusive)
      --cursor-file=FILE      Load cursor from FILE at start, persist updated cursor at end
      --show-cursor           Print "-- cursor: s=..;b=.." line after output
      --file=PATH             Read a journal file (binary or JSONL; magic-detected; .gz auto-decompress)
  -D, --directory=DIR         Iterate every *.jsonl / *.jsonl.gz / *.slj under DIR
      --root=PATH             Prefix applied to filesystem paths (--directory, --disk-usage default)
      --verify                Walk FSS TAG chain on --file (binary only); needs --fss-key
      --fss-key=PATH          FSS key file for --verify (default /etc/slinit/journal-key)

  Display modifiers:
      --no-hostname           Drop hostname column from short outputs
      --utc                   Render timestamps in UTC instead of local
      --truncate-newline      Cut MESSAGE at the first newline
      --no-full               Ellipsize long fields (~256 chars)
  -l, --full                  Show full fields (default)
  -a, --all                   Show all field values without ellipsizing
  -e, --pager-end             Accepted for parity; no pager currently invoked
  -q, --quiet                 Suppress info messages (empty file etc.)

  Introspection (short-circuit — no event stream):
  -F, --field=NAME            Print distinct values seen for NAME across events
      --fields                Print the list of known field names
      --header                Print journal file / buffer header metadata
      --disk-usage            Print total bytes across on-disk journals

  Connection:
      --socket-path=P         Override the control socket path
      --system                Force system-mode socket (/run/slinit.socket)
      --user                  Force user-mode socket ($XDG_RUNTIME_DIR/slinitctl)
      --version               Print version and exit
  -h, --help                  Show this help

Time formats for --since/--until:
  now                          current wall clock
  today | yesterday            local midnight-based
  YYYY-MM-DD                   local midnight
  "YYYY-MM-DD HH:MM:SS"        local wall time (quote to preserve the space)
  RFC3339                      2026-07-31T12:00:00Z or with numeric offset
  -Ns / -Nm / -Nh / -Nd        N seconds/minutes/hours/days ago

Output formats:
  short       "Jan 02 15:04:05 host unit[pid]: msg" (syslog-style, default)
  short-iso   Same as short but with RFC3339 timestamp
  cat         Just the message text (feed to grep / less)
  json        One raw JSON object per event (feed to jq)
  verbose     Multi-line dump of every field (systemd-style)
  export      systemd export format (KEY=value lines, blank between events)

Wire path: slinit-journalctl → CmdJournalQuery over /run/slinit.socket
  → RplyJournalEntry* + RplyJournalDone.
`)
}

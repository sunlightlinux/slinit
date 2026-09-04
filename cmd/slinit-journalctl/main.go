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
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/catalog"
	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/dissect"
	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journalbin"
	"github.com/sunlightlinux/slinit/pkg/journald"
	"github.com/sunlightlinux/slinit/pkg/machine"
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
	fmtShort           outputFormat = "short"
	fmtShortPrecise    outputFormat = "short-precise"
	fmtShortISO        outputFormat = "short-iso"
	fmtShortISOPrecise outputFormat = "short-iso-precise"
	fmtShortFull       outputFormat = "short-full"
	fmtShortMonotonic  outputFormat = "short-monotonic"
	fmtShortUnix       outputFormat = "short-unix"
	fmtCat             outputFormat = "cat"
	fmtWithUnit        outputFormat = "with-unit"
	fmtJSON            outputFormat = "json"
	fmtJSONPretty      outputFormat = "json-pretty"
	fmtJSONSSE         outputFormat = "json-sse"
	fmtJSONSeq         outputFormat = "json-seq"
	fmtVerbose         outputFormat = "verbose"
	fmtExport          outputFormat = "export"
)

// validFormats lists every format value the -o flag accepts, in the
// order shown by --help. Kept as a slice (not a map) so error messages
// can print a stable, human-friendly enumeration.
var validFormats = []outputFormat{
	fmtShort, fmtShortPrecise, fmtShortISO, fmtShortISOPrecise,
	fmtShortFull, fmtShortMonotonic, fmtShortUnix,
	fmtCat, fmtWithUnit,
	fmtJSON, fmtJSONPretty, fmtJSONSSE, fmtJSONSeq,
	fmtVerbose, fmtExport,
}

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

	// --- Group B: maintenance ---

	sync        bool          // --sync — force daemon fsync via SIGUSR1
	rotate      bool          // --rotate — force daemon rotation via SIGUSR2
	vacuumSize  int64         // --vacuum-size=SIZE — prune to at most SIZE bytes
	vacuumFiles int           // --vacuum-files=N — keep at most N archived files
	vacuumTime  time.Duration // --vacuum-time=TIME — drop files older than TIME
	vacuumSet   bool          // sentinel — any of the three vacuum flags was passed
	pidFile     string        // --pid-file PATH — override /run/slinit-journald.pid

	// --- Group C: FSS ---

	setupKeys    bool          // --setup-keys — mint fresh FSS key + print verification token
	verifyKey    string        // --verify-key=KEY — inline verification token (alternative to --fss-key)
	fssInterval  time.Duration // --interval=DUR — epoch duration for --setup-keys
	force        bool          // --force — overwrite existing --setup-keys output
	syncOnExit   bool          // --synchronize-on-exit — accepted for parity; we always fsync on Close

	// --- Sprint 2: volatile ⇄ persistent switching ---

	flush              bool // --flush — signal SIGRTMIN+0: migrate volatile → persistent
	relinquishVar      bool // --relinquish-var — SIGRTMIN+1: close persistent, reopen at volatile
	smartRelinquishVar bool // --smart-relinquish-var — only relinquish when /var is a separate mount

	// --- Sprint 3: journal namespaces ---

	namespace      string // --namespace=NS — filter + route to NS daemon
	listNamespaces bool   // --list-namespaces — enumerate namespaces from filesystem

	// --- Sprint 4: disk image dissection ---

	image       string // --image=PATH — losetup + mount + read journal from image
	imagePolicy string // --image-policy=POLICY — parsed via pkg/dissect.ParsePolicy

	// --- Group D: catalog ---

	catalog       bool // -x/--catalog — augment MESSAGE with catalog text
	dumpCatalog   bool // --dump-catalog
	updateCatalog bool // --update-catalog
	listCatalog   bool // --list-catalog

	// --- Group E: invocation ---

	invocation           string // --invocation=UUID — filter events by SLINIT_INVOCATION_ID
	latestInvocation     bool   // -I — resolve to the latest invocation ID for -u UNIT at run time
	listInvocations      bool   // --list-invocations — list invocations for --unit
	machineTarget        string // -M --machine=CONTAINER — accepted for parity, WARN-and-skip

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

		case a == "-S" || a == "--since":
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

		case a == "-i" || a == "--file":
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

		case a == "-W" || a == "--no-hostname":
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

		case a == "-N" || a == "--fields":
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

		// --- Group B: maintenance ---

		case a == "--sync":
			opts.sync = true
			args = args[1:]

		case a == "--rotate":
			opts.rotate = true
			args = args[1:]

		case a == "--vacuum-size":
			if len(args) < 2 {
				return opts, errors.New("--vacuum-size requires an argument")
			}
			n, err := parseSizeArg(args[1])
			if err != nil {
				return opts, fmt.Errorf("--vacuum-size: %w", err)
			}
			opts.vacuumSize = n
			opts.vacuumSet = true
			args = args[2:]

		case strings.HasPrefix(a, "--vacuum-size="):
			n, err := parseSizeArg(strings.TrimPrefix(a, "--vacuum-size="))
			if err != nil {
				return opts, fmt.Errorf("--vacuum-size: %w", err)
			}
			opts.vacuumSize = n
			opts.vacuumSet = true
			args = args[1:]

		case a == "--vacuum-files":
			if len(args) < 2 {
				return opts, errors.New("--vacuum-files requires an argument")
			}
			n, err := strconv.Atoi(args[1])
			if err != nil || n < 0 {
				return opts, fmt.Errorf("--vacuum-files: invalid count %q", args[1])
			}
			opts.vacuumFiles = n
			opts.vacuumSet = true
			args = args[2:]

		case strings.HasPrefix(a, "--vacuum-files="):
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--vacuum-files="))
			if err != nil || n < 0 {
				return opts, fmt.Errorf("--vacuum-files: invalid count %q", strings.TrimPrefix(a, "--vacuum-files="))
			}
			opts.vacuumFiles = n
			opts.vacuumSet = true
			args = args[1:]

		case a == "--vacuum-time":
			if len(args) < 2 {
				return opts, errors.New("--vacuum-time requires an argument")
			}
			d, err := parseDurationArg(args[1])
			if err != nil {
				return opts, fmt.Errorf("--vacuum-time: %w", err)
			}
			opts.vacuumTime = d
			opts.vacuumSet = true
			args = args[2:]

		case strings.HasPrefix(a, "--vacuum-time="):
			d, err := parseDurationArg(strings.TrimPrefix(a, "--vacuum-time="))
			if err != nil {
				return opts, fmt.Errorf("--vacuum-time: %w", err)
			}
			opts.vacuumTime = d
			opts.vacuumSet = true
			args = args[1:]

		case a == "--pid-file":
			if len(args) < 2 {
				return opts, errors.New("--pid-file requires an argument")
			}
			opts.pidFile = args[1]
			args = args[2:]

		case strings.HasPrefix(a, "--pid-file="):
			opts.pidFile = strings.TrimPrefix(a, "--pid-file=")
			args = args[1:]

		// --- Group C: FSS ---

		case a == "--setup-keys":
			opts.setupKeys = true
			args = args[1:]

		case a == "--verify-key":
			if len(args) < 2 {
				return opts, errors.New("--verify-key requires an argument")
			}
			opts.verifyKey = args[1]
			args = args[2:]

		case strings.HasPrefix(a, "--verify-key="):
			opts.verifyKey = strings.TrimPrefix(a, "--verify-key=")
			args = args[1:]

		case a == "--interval":
			if len(args) < 2 {
				return opts, errors.New("--interval requires an argument")
			}
			d, err := parseDurationArg(args[1])
			if err != nil {
				return opts, fmt.Errorf("--interval: %w", err)
			}
			opts.fssInterval = d
			args = args[2:]

		case strings.HasPrefix(a, "--interval="):
			d, err := parseDurationArg(strings.TrimPrefix(a, "--interval="))
			if err != nil {
				return opts, fmt.Errorf("--interval: %w", err)
			}
			opts.fssInterval = d
			args = args[1:]

		case a == "--force":
			opts.force = true
			args = args[1:]

		case a == "--synchronize-on-exit":
			// Accepted for systemd parity. Slinit already fsyncs the
			// active sink on Close (see FileSink.Close / BinarySink.Close)
			// so there's nothing extra to configure at exit time.
			opts.syncOnExit = true
			args = args[1:]

		case strings.HasPrefix(a, "--synchronize-on-exit="):
			b, err := parseBoolArg(strings.TrimPrefix(a, "--synchronize-on-exit="))
			if err != nil {
				return opts, fmt.Errorf("--synchronize-on-exit: %w", err)
			}
			opts.syncOnExit = b
			args = args[1:]

		// --- Sprint 2: volatile ⇄ persistent switching ---

		case a == "--flush":
			opts.flush = true
			args = args[1:]

		case a == "--relinquish-var":
			opts.relinquishVar = true
			args = args[1:]

		case a == "--smart-relinquish-var":
			opts.smartRelinquishVar = true
			args = args[1:]

		// --- Sprint 3: journal namespaces ---

		case a == "--namespace":
			if len(args) < 2 {
				return opts, errors.New("--namespace requires an argument")
			}
			opts.namespace = args[1]
			args = args[2:]

		case strings.HasPrefix(a, "--namespace="):
			opts.namespace = strings.TrimPrefix(a, "--namespace=")
			args = args[1:]

		case a == "--list-namespaces":
			opts.listNamespaces = true
			args = args[1:]

		// --- Sprint 4: disk image dissection ---

		case a == "--image":
			if len(args) < 2 {
				return opts, errors.New("--image requires an argument")
			}
			opts.image = args[1]
			args = args[2:]

		case strings.HasPrefix(a, "--image="):
			opts.image = strings.TrimPrefix(a, "--image=")
			args = args[1:]

		case a == "--image-policy":
			if len(args) < 2 {
				return opts, errors.New("--image-policy requires an argument")
			}
			opts.imagePolicy = args[1]
			args = args[2:]

		case strings.HasPrefix(a, "--image-policy="):
			opts.imagePolicy = strings.TrimPrefix(a, "--image-policy=")
			args = args[1:]

		// --- Group D: catalog ---

		case a == "-x" || a == "--catalog":
			opts.catalog = true
			args = args[1:]

		case a == "--dump-catalog":
			opts.dumpCatalog = true
			args = args[1:]

		case a == "--update-catalog":
			opts.updateCatalog = true
			args = args[1:]

		case a == "--list-catalog":
			opts.listCatalog = true
			args = args[1:]

		// --- Group E: invocation ---

		case a == "--invocation":
			if len(args) < 2 {
				return opts, errors.New("--invocation requires an argument")
			}
			opts.invocation = args[1]
			args = args[2:]

		case strings.HasPrefix(a, "--invocation="):
			opts.invocation = strings.TrimPrefix(a, "--invocation=")
			args = args[1:]

		case a == "--list-invocations":
			opts.listInvocations = true
			args = args[1:]

		case a == "-I":
			// systemd: "-I" is the shortcut for "the latest
			// invocation of the specified -u UNIT". Resolved at run
			// time (see runQuery) — needs a unit + event gather.
			opts.latestInvocation = true
			args = args[1:]

		case a == "--no-pager":
			// systemd invokes $PAGER for long output; slinit never
			// does — accept silently so scripts ported from systemd
			// keep working without a WARN spew.
			args = args[1:]

		case a == "-M" || a == "--machine":
			if len(args) < 2 {
				return opts, fmt.Errorf("%s requires an argument", a)
			}
			opts.machineTarget = args[1]
			args = args[2:]

		case strings.HasPrefix(a, "--machine="):
			opts.machineTarget = strings.TrimPrefix(a, "--machine=")
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

// parseSizeArg accepts systemd-style byte sizes: bare integers are
// bytes, "K"/"M"/"G"/"T" suffixes use base-1024. Case-insensitive
// suffix. Matches sd_parse_size for the common cases (skips the
// "1234M56K" mixed form which admins rarely use in practice).
func parseSizeArg(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty size")
	}
	// Strip optional trailing "B"/"b" first (bytes marker).
	if strings.HasSuffix(s, "B") || strings.HasSuffix(s, "b") {
		s = s[:len(s)-1]
	}
	// Strip optional "i" (KiB/MiB style) that may sit between the
	// unit letter and the "B" we just removed.
	if strings.HasSuffix(s, "i") || strings.HasSuffix(s, "I") {
		s = s[:len(s)-1]
	}
	if s == "" {
		return 0, errors.New("empty size")
	}
	mult := int64(1)
	last := s[len(s)-1]
	switch last {
	case 'K', 'k':
		mult = 1024
		s = s[:len(s)-1]
	case 'M', 'm':
		mult = 1024 * 1024
		s = s[:len(s)-1]
	case 'G', 'g':
		mult = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	case 'T', 't':
		mult = 1024 * 1024 * 1024 * 1024
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad size %q: %w", s, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("size must be non-negative, got %d", n)
	}
	return n * mult, nil
}

// parseDurationArg accepts systemd-style time spans: `5s`, `30m`,
// `2h`, `3d`, `4w`, `6M` (30-day month approx), `1y` (365-day year).
// Also accepts Go-native forms (`1h30m`) via time.ParseDuration
// fallback.
func parseDurationArg(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty duration")
	}
	// Try Go-native first — covers `1h30m`, `250ms`, etc.
	if d, err := time.ParseDuration(s); err == nil && d >= 0 {
		return d, nil
	}
	// Systemd tokens: <number><unit>.
	unit := s[len(s)-1]
	body := s[:len(s)-1]
	n, err := strconv.ParseInt(body, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad duration %q", s)
	}
	if n < 0 {
		return 0, fmt.Errorf("duration must be non-negative, got %d", n)
	}
	var mult time.Duration
	switch unit {
	case 's':
		mult = time.Second
	case 'm':
		mult = time.Minute
	case 'h':
		mult = time.Hour
	case 'd':
		mult = 24 * time.Hour
	case 'w':
		mult = 7 * 24 * time.Hour
	case 'M':
		mult = 30 * 24 * time.Hour
	case 'y':
		mult = 365 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("bad duration unit %q (want s/m/h/d/w/M/y)", string(unit))
	}
	return time.Duration(n) * mult, nil
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
	if opts.machineTarget != "" {
		// -M CONTAINER routes the query into the container's own
		// journal via pkg/machine's file registry. slinit does not
		// ship a D-Bus machined equivalent — the registry is
		// /run/slinit/machines/<name> files that slinit-nspawn
		// (or `slinit-machinectl register`) writes. Resolution:
		//   registry file → container PID → container rootfs
		//   → /proc/PID/root/run/slinit/events.sock (host-visible)
		// If the container has --file, --directory, --root or
		// --image set, the operator's explicit source wins — -M is
		// silently downgraded to a WARN so the query still runs.
		if opts.sourceFile != "" || opts.directory != "" || opts.root != "" || opts.image != "" {
			if !opts.quiet {
				fmt.Fprintf(os.Stderr,
					"slinit-journalctl: -M %q ignored — explicit source (--file/--directory/--root/--image) takes precedence\n",
					opts.machineTarget)
			}
		} else {
			m, err := machine.Lookup(opts.machineTarget)
			if err != nil {
				return fmt.Errorf("-M %s: %w", opts.machineTarget, err)
			}
			if m == nil {
				return fmt.Errorf("-M %s: no such machine registered under %s", opts.machineTarget, machine.Dir())
			}
			if !machine.Alive(m.PID) {
				return fmt.Errorf("-M %s: registered PID %d is not alive (stale registration?)", opts.machineTarget, m.PID)
			}
			// Prefer control-socket dial: the container's slinit
			// itself binds /run/slinit.socket during PID-1 setup, so
			// this works even when the optional slinit-journald
			// daemon isn't running inside the container. Fall back
			// to persistent journal directory iteration only when
			// the control socket is unreachable.
			//
			// Always use /proc/PID/root for runtime paths — the
			// container's /run is a private tmpfs mounted inside
			// its namespace and does NOT reflect back to the bind-
			// mount source recorded in m.Root. m.Root is only
			// meaningful for on-disk files (journal dir under
			// /var/log/, which lives in the rootfs proper).
			sockPath := fmt.Sprintf("/proc/%d/root%s", m.PID, machine.ControlSockPath)
			if _, statErr := os.Stat(sockPath); statErr == nil {
				opts.socketPath = sockPath
			} else {
				// No live socket → walk the persistent journal
				// directory instead. Falls out through --directory
				// path, which already handles rotated JSONL + FSS.
				files, listErr := m.ListJournalFiles()
				if listErr != nil {
					return fmt.Errorf("-M %s: no live socket at %s and journal listing failed: %w", opts.machineTarget, sockPath, listErr)
				}
				if len(files) == 0 {
					return fmt.Errorf("-M %s: no live socket at %s and no persistent journal files under container rootfs", opts.machineTarget, sockPath)
				}
				// Point --directory at the first journal dir we found
				// so runFromDirectory picks up every rotated file.
				opts.directory = filepath.Dir(files[0])
			}
		}
	}
	if opts.latestInvocation {
		// -I resolves at run time: find the latest invocation ID
		// recorded for the requested unit, then continue query with
		// opts.invocation set. Requires -u UNIT (matches systemd).
		if len(opts.units) == 0 && len(opts.userUnitFilters) == 0 {
			return errors.New("-I requires -u UNIT")
		}
		if opts.invocation != "" {
			return errors.New("-I and --invocation are mutually exclusive")
		}
		id, err := resolveLatestInvocation(opts)
		if err != nil {
			return err
		}
		if id == "" {
			if !opts.quiet {
				fmt.Fprintln(os.Stderr, "slinit-journalctl: -I: no recorded invocations for this unit")
			}
			return nil
		}
		opts.invocation = id
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

	// Introspection + maintenance short-circuits. None of them stream
	// events; they operate on the daemon (--sync/--rotate) or directly
	// on the filesystem (--vacuum-*, --disk-usage, --fields).

	if opts.fieldsList {
		return runFieldsList(os.Stdout)
	}
	if opts.diskUsage {
		return runDiskUsage(opts, os.Stdout)
	}
	if opts.sync {
		return runSync(opts)
	}
	if opts.rotate {
		return runRotate(opts)
	}
	if opts.vacuumSet {
		return runVacuum(opts)
	}
	if opts.flush {
		return runFlush(opts)
	}
	if opts.relinquishVar || opts.smartRelinquishVar {
		return runRelinquishVar(opts)
	}
	if opts.listNamespaces {
		return runListNamespaces(os.Stdout)
	}
	if opts.image != "" {
		return runFromImage(opts)
	}

	// Group C — FSS key management. --setup-keys is a one-shot;
	// --verify-key feeds into the --verify path (handled in runFromFile).
	if opts.setupKeys {
		return runSetupKeys(opts, os.Stdout)
	}

	// Group D — catalog. --dump/--update/--list are short-circuits;
	// --catalog augments rendered output (handled in the render layer).
	if opts.dumpCatalog {
		return runDumpCatalog(opts, os.Stdout)
	}
	if opts.updateCatalog {
		return runUpdateCatalog(opts, os.Stdout)
	}
	if opts.listCatalog {
		return runListCatalog(opts, os.Stdout)
	}

	// Group E — invocation list is a short-circuit (project + dedupe).
	if opts.listInvocations {
		return runListInvocationsShortCircuit(opts)
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
		// Resolve any relative form (-N, +N) to a concrete boot-id
		// via the aggregator that --list-boots uses (ring buffer +
		// on-disk journals). "" / "0" stay as-is; a concrete 32-hex
		// ID passes through unchanged. Sets opts.bootID + when the
		// resolved boot is NOT current, sets opts.directory so we
		// route through the on-disk read path.
		if err := resolveBootSpec(conn, &opts); err != nil {
			return err
		}
		// Cross-boot: delegate to runFromDirectory. The buildRequest
		// filter (via ToFilter) picks up opts.bootID so only the
		// target boot's events surface even if the file has more.
		if opts.directory != "" {
			if opts.follow {
				return errors.New("-b <past-boot> and --follow are mutually exclusive (past boots don't emit new events)")
			}
			return runFromDirectory(opts)
		}
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
	dir := journalDir(opts)
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

// defaultPidFile returns opts.pidFile if the operator set it,
// otherwise the daemon's write-side default. Kept in one place so a
// future move (e.g. XDG runtime dir for user-mode journald) has a
// single edit site.
func defaultPidFile(opts options) string {
	if opts.pidFile != "" {
		return opts.pidFile
	}
	return "/run/slinit-journald.pid"
}

// readDaemonPID reads and validates the PID file. Returns a helpful
// error when missing or malformed so the operator knows whether the
// daemon just isn't running or the file got corrupted.
func readDaemonPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, fmt.Errorf("no daemon running (pid file %s not present)", path)
		}
		return 0, fmt.Errorf("read pid file %s: %w", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("pid file %s: bad contents %q", path, string(data))
	}
	// Sanity check that the PID actually exists.
	if err := syscall.Kill(pid, 0); err != nil {
		return 0, fmt.Errorf("pid %d in %s does not exist (stale pid file after crash?)", pid, path)
	}
	return pid, nil
}

// runSync sends SIGUSR1 to slinit-journald which fsyncs its current
// sink. If no daemon is running we walk the journal directory and
// fsync every JSONL/binary file directly — safe operation since
// nothing else writes to them concurrently in that scenario.
func runSync(opts options) error {
	pid, err := readDaemonPID(defaultPidFile(opts))
	if err != nil {
		// No daemon path — fsync files directly.
		return syncJournalDir(opts)
	}
	if err := syscall.Kill(pid, syscall.SIGUSR1); err != nil {
		return fmt.Errorf("send SIGUSR1 to %d: %w", pid, err)
	}
	if !opts.quiet {
		fmt.Fprintf(os.Stdout, "slinit-journald (pid %d): SIGUSR1 sent for flush\n", pid)
	}
	return nil
}

// syncJournalDir is the daemon-less --sync path: open every journal
// file in the configured dir and fsync each one. Slow on large
// directories but rare — operators typically call --sync during
// pre-shutdown or migration flows, not per-request.
func syncJournalDir(opts options) error {
	dir := journalDir(opts)
	// Missing directory is a benign no-op — a fresh system without
	// persistent journals yet still shouldn't fail its shutdown
	// script when it optimistically calls --sync.
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if !opts.quiet {
			fmt.Fprintf(os.Stdout, "no journal files under %s (nothing to sync)\n", dir)
		}
		return nil
	}
	files, err := journalFilesUnder(dir)
	if err != nil {
		return err
	}
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return fmt.Errorf("open %s: %w", f, err)
		}
		if err := fh.Sync(); err != nil {
			fh.Close()
			return fmt.Errorf("fsync %s: %w", f, err)
		}
		fh.Close()
	}
	if !opts.quiet {
		fmt.Fprintf(os.Stdout, "fsynced %d file(s) under %s (no daemon running)\n", len(files), dir)
	}
	return nil
}

// runRotate sends SIGUSR2 to slinit-journald which closes the active
// file, renames it with a nanosecond suffix, and opens a fresh one.
// Requires a running daemon — file-level rotation would race with
// journald's own writes, so we refuse rather than corrupt state.
func runRotate(opts options) error {
	pid, err := readDaemonPID(defaultPidFile(opts))
	if err != nil {
		return fmt.Errorf("--rotate requires slinit-journald running: %w", err)
	}
	if err := syscall.Kill(pid, syscall.SIGUSR2); err != nil {
		return fmt.Errorf("send SIGUSR2 to %d: %w", pid, err)
	}
	if !opts.quiet {
		fmt.Fprintf(os.Stdout, "slinit-journald (pid %d): SIGUSR2 sent for rotate\n", pid)
	}
	return nil
}

// runVacuum runs journald.Vacuum in-process, excluding the current
// (active) journal file so we never race a live daemon into corrupting
// its own writes. Reports the count of pruned files so scripts can
// gate on non-zero cleanup.
func runVacuum(opts options) error {
	dir := journalDir(opts)
	// Missing dir treated as "no files to vacuum" — same shutdown-
	// script rationale as --sync above.
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if !opts.quiet {
			fmt.Fprintf(os.Stdout, "no journal directory at %s (nothing to vacuum)\n", dir)
		}
		return nil
	}
	vopts := journald.VacuumOptions{
		MaxFiles:     opts.vacuumFiles,
		MaxTotalSize: opts.vacuumSize,
		MaxAge:       opts.vacuumTime,
		Suffixes:     []string{".jsonl", ".jsonl.gz", ".journal"},
	}
	exclude := currentJournalFiles(dir)
	removed, err := journald.Vacuum(dir, vopts, exclude...)
	if err != nil {
		return fmt.Errorf("vacuum %s: %w", dir, err)
	}
	if !opts.quiet {
		fmt.Fprintf(os.Stdout, "vacuum %s: removed %d file(s)\n", dir, removed)
	}
	return nil
}

// journalDir picks the journal directory the maintenance ops act on.
// --directory wins; otherwise the daemon's default, optionally
// prefixed by --root.
func journalDir(opts options) string {
	dir := opts.directory
	if dir == "" {
		dir = "/var/log/slinit-journal"
	}
	if opts.root != "" {
		dir = filepath.Join(opts.root, dir)
	}
	return dir
}

// currentJournalFiles enumerates the "live" file names under dir that
// vacuum must NOT delete: today's <YYYY-MM-DD>.jsonl and .journal (the
// convention both FileSink and BinarySink use for their current
// writer). Returned paths are absolute so Vacuum's exclusion check
// (path-equal) matches without further normalization.
func currentJournalFiles(dir string) []string {
	t := time.Now().UTC()
	base := fmt.Sprintf("%04d-%02d-%02d", t.Year(), int(t.Month()), t.Day())
	return []string{
		filepath.Join(dir, base+".jsonl"),
		filepath.Join(dir, base+".journal"),
	}
}

// --- Sprint 4: disk image dissection ---

// runFromImage attaches a disk image via pkg/dissect, then reruns
// the query as a --directory scan over the mounted journal path.
// Detach always runs — even on error paths — so a broken image
// doesn't leak a loop device.
func runFromImage(opts options) error {
	policy, err := dissect.ParsePolicy(opts.imagePolicy)
	if err != nil {
		return fmt.Errorf("--image-policy: %w", err)
	}
	_, journalDir, detach, err := dissect.Attach(opts.image, policy)
	if err != nil {
		return err
	}
	defer func() {
		if err := detach(); err != nil {
			fmt.Fprintf(os.Stderr, "slinit-journalctl: WARN detach %s: %v\n", opts.image, err)
		}
	}()
	if !opts.quiet {
		fmt.Fprintf(os.Stderr, "slinit-journalctl: --image %s → mounted, journal at %s\n", opts.image, journalDir)
	}
	// Rewrite opts as a --directory query pointing at the mounted
	// journal, then re-enter the standard runFromDirectory path.
	imgOpts := opts
	imgOpts.image = ""
	imgOpts.directory = journalDir
	return runFromDirectory(imgOpts)
}

// --- Sprint 3: journal namespaces ---

// runListNamespaces enumerates namespaces by scanning the standard
// slinit-journald directory-naming convention (`<base>.NS` for both
// persistent /var/log and volatile /run). Prints one namespace per
// line, sorted, deduped across primary + volatile paths. The default
// namespace (unnamed) is implied by any bare `<base>` dir but not
// listed since it's always present.
func runListNamespaces(out io.Writer) error {
	seen := map[string]struct{}{}
	scan := func(parent, prefix string) {
		entries, err := os.ReadDir(parent)
		if err != nil {
			return
		}
		for _, ent := range entries {
			if !ent.IsDir() {
				continue
			}
			name := ent.Name()
			if !strings.HasPrefix(name, prefix+".") {
				continue
			}
			ns := strings.TrimPrefix(name, prefix+".")
			if ns == "" {
				continue
			}
			seen[ns] = struct{}{}
		}
	}
	scan("/var/log", "slinit-journal")
	scan("/run", "slinit-journal")
	if len(seen) == 0 {
		fmt.Fprintln(out, "(no namespaces present — only the default namespace is active)")
		return nil
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	// Stable sort with the tiny bubble the rest of this file uses.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j-1] > names[j]; j-- {
			names[j-1], names[j] = names[j], names[j-1]
		}
	}
	for _, n := range names {
		fmt.Fprintln(out, n)
	}
	return nil
}

// --- Sprint 2: volatile ⇄ persistent switching ---

// defaultAdminSocket matches slinit-journald's DefaultAdminSocket
// constant. Kept in sync manually — both are Linux-only.
const defaultAdminSocket = "/run/slinit-journald.ctl"

// sendAdminCommand writes cmd as a single datagram to the daemon's
// admin socket. Fire-and-forget: the daemon logs errors to its own
// stderr but doesn't ack per command (matches SIGUSR1/2 fire-and-
// forget semantics).
func sendAdminCommand(cmd string) error {
	addr, err := net.ResolveUnixAddr("unixgram", defaultAdminSocket)
	if err != nil {
		return err
	}
	conn, err := net.DialUnix("unixgram", nil, addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w (is slinit-journald running with --admin-socket?)", defaultAdminSocket, err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return fmt.Errorf("write %q: %w", cmd, err)
	}
	return nil
}

// runFlush asks slinit-journald to migrate volatile journal files
// to the persistent primary and switch the active sink over. No-op
// when the daemon is already writing to the primary. Requires a
// running daemon — the sink-swap step is the whole point.
func runFlush(opts options) error {
	if err := sendAdminCommand("flush"); err != nil {
		return fmt.Errorf("--flush: %w", err)
	}
	if !opts.quiet {
		fmt.Fprintln(os.Stdout, "slinit-journald: flush command sent")
	}
	return nil
}

// runRelinquishVar asks the daemon to close the persistent sink and
// reopen at the volatile fallback. Under --smart-relinquish-var the
// /var mount check runs first: no-op when /var isn't a separate
// mountpoint (in which case there's nothing to un-pin before umount).
func runRelinquishVar(opts options) error {
	cmd := "relinquish-var"
	if opts.smartRelinquishVar && !opts.relinquishVar {
		sep, err := isSeparateVarMount()
		if err != nil {
			return fmt.Errorf("--smart-relinquish-var: probe /var: %w", err)
		}
		if !sep {
			if !opts.quiet {
				fmt.Fprintln(os.Stdout, "smart-relinquish-var: /var is not a separate mountpoint, nothing to relinquish")
			}
			return nil
		}
		cmd = "smart-relinquish"
	}
	if err := sendAdminCommand(cmd); err != nil {
		return fmt.Errorf("--relinquish-var: %w", err)
	}
	if !opts.quiet {
		fmt.Fprintf(os.Stdout, "slinit-journald: %s command sent\n", cmd)
	}
	return nil
}

// isSeparateVarMount parses /proc/self/mountinfo looking for a
// mount point whose target is exactly "/var". Returns true if such
// a line exists — meaning /var lives on a filesystem distinct from
// the root fs and the daemon needs to release it before umount.
func isSeparateVarMount() (bool, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		// mountinfo format: id parentID major:minor root mount-point ...
		// We want field 5 (0-indexed 4).
		fields := strings.Fields(line)
		if len(fields) >= 5 && fields[4] == "/var" {
			return true, nil
		}
	}
	return false, nil
}

// --- Group C: FSS key management ---

// runSetupKeys mints a fresh FSS sealing key and saves it to the
// configured path (--fss-key or default /etc/slinit/journal-key),
// then prints the base64-encoded seed as the verification token the
// operator will paste into `--verify-key=...` on remote hosts.
// Matches systemd's `journalctl --setup-keys` UX: separate
// sealing-key file kept private, verification token printed for
// out-of-band distribution.
func runSetupKeys(opts options, out io.Writer) error {
	path := opts.fssKeyPath
	if path == "" {
		path = "/etc/slinit/journal-key"
	}
	// Safety gate: refuse to clobber an existing key file (which would
	// invalidate every TAG chain that already used it) unless the
	// operator opts in with --force. Matches systemd's `journalctl
	// --setup-keys` behaviour of erroring on re-run without --force.
	if !opts.force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists — pass --force to overwrite (previously-sealed journals become unverifiable)", path)
		}
	}
	interval := opts.fssInterval
	intervalUsec := int64(interval / time.Microsecond)
	if intervalUsec <= 0 {
		intervalUsec = journalbin.DefaultFSSEpochUsec
	}
	startUsec := time.Now().UnixMicro()
	key, err := journalbin.NewFSSKey(startUsec, intervalUsec)
	if err != nil {
		return fmt.Errorf("mint FSS key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := journalbin.SaveFSSKey(path, key); err != nil {
		return err
	}
	if !opts.quiet {
		fmt.Fprintf(out, "FSS key saved to %s (interval %s)\n", path, time.Duration(intervalUsec)*time.Microsecond)
		fmt.Fprintf(out, "Verification key (share this out-of-band; keep %s private):\n\n  %s\n\n",
			path, key.Seed)
		fmt.Fprintf(out, "Use on the verifier host as:\n  slinit-journalctl --verify --verify-key=%s --file=<journal>\n",
			key.Seed)
	}
	return nil
}

// loadFSSKeyForVerify resolves the FSS key for a --verify run,
// preferring an inline --verify-key over a --fss-key file. Returns an
// in-memory FSSKey either way so the callers stay uniform.
func loadFSSKeyForVerify(opts options) (*journalbin.FSSKey, error) {
	if opts.verifyKey != "" {
		// Reconstitute a minimal FSSKey from just the seed — enough
		// for Verify() since it only needs Seed + StartUsec/IntervalUsec
		// to re-derive per-epoch keys.  StartUsec/IntervalUsec are
		// read from the journal file's own header, so we can leave
		// them zero here and let Verify pull the metadata from disk.
		return &journalbin.FSSKey{
			Seed:         opts.verifyKey,
			IntervalUsec: journalbin.DefaultFSSEpochUsec,
		}, nil
	}
	path := opts.fssKeyPath
	if path == "" {
		path = "/etc/slinit/journal-key"
	}
	return journalbin.LoadFSSKey(path)
}

// --- Group D: catalog ---

// runDumpCatalog prints every entry in the compiled catalog cache in
// a systemd-compatible text layout so operators can diff / grep. Uses
// the pkg/catalog default paths (compiled cache under /var/lib/slinit).
func runDumpCatalog(opts options, out io.Writer) error {
	c, err := loadCatalog(opts)
	if err != nil {
		return err
	}
	c.Dump(out)
	return nil
}

// runUpdateCatalog re-scans catalog source directories and rebuilds
// the compiled cache. Idempotent — safe to run on every boot or after
// package install / removal.
func runUpdateCatalog(opts options, out io.Writer) error {
	c, err := catalog.LoadDirs(catalogSourceDirs(opts)...)
	if err != nil {
		return err
	}
	cachePath := catalogCachePath(opts)
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return err
	}
	if err := c.SaveCompiled(cachePath); err != nil {
		return err
	}
	if !opts.quiet {
		fmt.Fprintf(out, "catalog updated: %d entries -> %s\n", c.Len(), cachePath)
	}
	return nil
}

// runListCatalog prints one MESSAGE_ID per line, sorted.
func runListCatalog(opts options, out io.Writer) error {
	c, err := loadCatalog(opts)
	if err != nil {
		return err
	}
	for _, id := range c.SortedIDs() {
		fmt.Fprintln(out, id)
	}
	return nil
}

// loadCatalog tries the compiled cache first; falls back to fresh
// scan of source dirs if the cache is missing (fresh install) so
// --dump/--list work even before a --update-catalog run.
func loadCatalog(opts options) (*catalog.Catalog, error) {
	if c, err := catalog.LoadCompiled(catalogCachePath(opts)); err == nil {
		return c, nil
	}
	return catalog.LoadDirs(catalogSourceDirs(opts)...)
}

// catalogSourceDirs enumerates the directories scanned by
// --update-catalog. Matches systemd's convention plus a slinit-native
// dir. Order-sensitive: later entries win on MESSAGE_ID collision.
func catalogSourceDirs(opts options) []string {
	dirs := []string{
		"/usr/share/slinit-catalog",
		"/usr/lib/slinit/catalog",
		"/usr/lib/systemd/catalog", // reuse existing systemd catalogs
	}
	if opts.root != "" {
		for i, d := range dirs {
			dirs[i] = filepath.Join(opts.root, d)
		}
	}
	return dirs
}

// catalogCachePath is the compiled binary catalog location. --root
// prefix applied so image-installer flows write into the target
// filesystem, not the host.
func catalogCachePath(opts options) string {
	path := "/var/lib/slinit/catalog/catalog.compiled"
	if opts.root != "" {
		path = filepath.Join(opts.root, path)
	}
	return path
}

// --- Group E: invocation ---

// runListInvocationsShortCircuit queries the buffer restricted to
// opts.units, projects to (SLINIT_INVOCATION_ID, first-seen ts),
// dedupes, sorts by timestamp, and prints one row per invocation.
// Matches systemd's `journalctl --list-invocations -u UNIT` output
// shape (ID + newest/oldest timestamp).
func runListInvocationsShortCircuit(opts options) error {
	if len(opts.units) == 0 && len(opts.userUnitFilters) == 0 {
		return errors.New("--list-invocations requires -u UNIT")
	}
	events, err := gatherEvents(opts)
	if err != nil {
		return err
	}
	type inv struct {
		id        string
		first     int64
		last      int64
	}
	seen := map[string]*inv{}
	var order []string
	for _, e := range events {
		id := extractField(e, "SLINIT_INVOCATION_ID")
		if id == "" {
			continue
		}
		rec, ok := seen[id]
		if !ok {
			rec = &inv{id: id, first: e.Ts, last: e.Ts}
			seen[id] = rec
			order = append(order, id)
			continue
		}
		if e.Ts < rec.first {
			rec.first = e.Ts
		}
		if e.Ts > rec.last {
			rec.last = e.Ts
		}
	}
	if len(order) == 0 {
		fmt.Fprintln(os.Stdout, "(no invocations recorded for this unit — is the daemon emitting SLINIT_INVOCATION_ID?)")
		return nil
	}
	for _, id := range order {
		rec := seen[id]
		fmt.Fprintf(os.Stdout, "%s  %s..%s\n",
			id,
			time.Unix(0, rec.first).Format(time.RFC3339),
			time.Unix(0, rec.last).Format(time.RFC3339),
		)
	}
	return nil
}

// resolveLatestInvocation returns the SLINIT_INVOCATION_ID with the
// highest observed timestamp for the units in opts. Used by -I to
// pin the query to the latest invocation. Returns "" when no
// invocations are recorded (caller decides whether that's a soft
// no-op or an error).
func resolveLatestInvocation(opts options) (string, error) {
	events, err := gatherEvents(opts)
	if err != nil {
		return "", err
	}
	var bestID string
	var bestTs int64
	for _, e := range events {
		id := extractField(e, "SLINIT_INVOCATION_ID")
		if id == "" {
			continue
		}
		if bestID == "" || e.Ts > bestTs {
			bestID = id
			bestTs = e.Ts
		}
	}
	return bestID, nil
}

// gatherEvents pulls events for a normal query — used by
// --list-invocations and any future Group E extension that wants to
// aggregate over events without duplicating the socket/file logic.
func gatherEvents(opts options) ([]*journal.Event, error) {
	if opts.sourceFile != "" {
		return loadFileEvents(opts)
	}
	sockPath := resolveSocketPath(opts)
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", sockPath, err)
	}
	defer conn.Close()
	req := buildRequest(opts)
	req.Limit = 0 // pull everything; caller aggregates
	payload, _ := json.Marshal(req)
	if err := control.WritePacket(conn, control.CmdJournalQuery, payload); err != nil {
		return nil, err
	}
	events, err := collectStream(conn)
	if err != nil {
		return nil, err
	}
	return clientSideFilter(events, req), nil
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

// bootRange is the min/max ts window per BootID surfaced by
// --list-boots. Shared across sources so aggregate can merge the
// ring buffer's view with the on-disk journals'.
type bootRange struct {
	id    string
	first int64
	last  int64
}

// aggregateBoot updates byID with e's contribution — extends an
// existing window or creates a fresh one. Empty BootID entries are
// dropped (nothing useful to plot on the timeline).
func aggregateBoot(byID map[string]*bootRange, e *journal.Event) {
	if e.BootID == "" {
		return
	}
	r, ok := byID[e.BootID]
	if !ok {
		byID[e.BootID] = &bootRange{id: e.BootID, first: e.Ts, last: e.Ts}
		return
	}
	if e.Ts < r.first {
		r.first = e.Ts
	}
	if e.Ts > r.last {
		r.last = e.Ts
	}
}

// listBootsJournalDirs lists the standard on-disk paths --list-boots
// scans in addition to the live control-socket ring buffer.
// Persistent dir first (survives hard reboot on a real host), then
// the volatile /run fallback (dinit-philosophy: journald is optional,
// on a container without persistent journal /run is the only place
// events land).
var listBootsJournalDirs = []string{
	"/var/log/slinit-journal",
	"/run/slinit-journal",
}

// runListBoots aggregates BootID→timespan from every source
// slinit-journalctl can see: the live ring buffer via the control
// socket, then every .journal (binary Phase B) and .jsonl / .jsonl.gz
// (Phase C) file under the standard journal directories. Newest boot
// prints as index 0, older ones as -1 / -2 / ... matching systemd's
// UX. A cross-source event that shares a BootID is deduplicated by
// the aggregator — the same boot showing up in both the buffer and
// on disk collapses into one row, with the timespan spanning the
// widest bounds seen.
func runListBoots(conn net.Conn) error {
	byID := map[string]*bootRange{}

	// 1. Ring buffer — always available (control socket dial
	// already succeeded to reach this point).
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
	for _, e := range events {
		aggregateBoot(byID, e)
	}

	// 2. On-disk journals — walk each standard dir. Missing dirs
	// are not an error (a container without journald has none);
	// only report an error when the dir exists but a file inside
	// is malformed (would silently swallow real corruption
	// otherwise).
	for _, dir := range listBootsJournalDirs {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		if err := aggregateBootsFromDir(dir, byID); err != nil {
			return fmt.Errorf("list-boots: %s: %w", dir, err)
		}
	}

	if len(byID) == 0 {
		// No events seen yet — still emit the current boot so the
		// operator sees SOMETHING (matches `journalctl --list-boots`
		// behaviour on a freshly-booted system before any logging).
		fmt.Printf(" 0 %s (no events yet)\n", journal.BootID())
		return nil
	}

	// 3. Sort by first_ts ascending so newest boot prints last —
	// but with a NEGATIVE index so `journalctl --list-boots`
	// convention holds (0 = current, -1 = previous, ...).
	sorted := make([]*bootRange, 0, len(byID))
	for _, r := range byID {
		sorted = append(sorted, r)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].first < sorted[j].first })

	for i, r := range sorted {
		idx := i - (len(sorted) - 1)
		fmt.Printf("%3d %s %s—%s\n",
			idx, r.id,
			time.Unix(0, r.first).Format(time.RFC3339),
			time.Unix(0, r.last).Format(time.RFC3339),
		)
	}
	return nil
}

// aggregateBootsFromDir walks every journal file under dir and folds
// its BootID→timespan contribution into byID. Handles both
// binary (.journal via journalbin.MultiReader) and JSONL (.jsonl /
// .jsonl.gz via readJSONLFile with a null filter).
//
// Truncation tolerance: an unclean shutdown mid-write leaves the
// tail ENTRY_ARRAY of the active .journal file pointing past
// end-of-file, so mr.Iter surfaces `EOF` from `read entry-array
// hdr at N`. That's a normal recovery scenario — the operator
// wants to SEE which boots the journal captured up to that point,
// not have `--list-boots` refuse to work. Absorb read-past-EOF
// (and its io.ErrUnexpectedEOF sibling for short JSONL reads) as
// "reached end of usable data on this file" and continue.
// Genuine unreadable-file errors (permissions, bad magic) still
// surface — those aren't tail-truncation, they're broken files.
func aggregateBootsFromDir(dir string, byID map[string]*bootRange) error {
	// Binary path: journalbin.OpenDir + Iter walks every .journal
	// file in one call.
	if mr, err := journalbin.OpenDir(dir); err == nil {
		if len(mr.Readers()) > 0 {
			err := mr.Iter(func(e *journal.Event) bool {
				aggregateBoot(byID, e)
				return true
			})
			mr.Close()
			if err != nil && !isTruncationErr(err) {
				return err
			}
		} else {
			mr.Close()
		}
	}
	// JSONL path: enumerate .jsonl / .jsonl.gz separately, read
	// each end-to-end. Same shape as loadFileEvents but with a
	// no-op filter.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		name := ent.Name()
		isJSONL := strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".jsonl.gz")
		if !isJSONL {
			continue
		}
		path := filepath.Join(dir, name)
		var rc io.ReadCloser
		if strings.HasSuffix(name, ".gz") {
			rc, err = journald.OpenCompressed(path)
			if err != nil {
				return err
			}
		} else {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			rc = f
		}
		events, err := readJSONLFile(rc, journal.QueryFilter{MinPriority: -1}, 0)
		rc.Close()
		if err != nil && !isTruncationErr(err) {
			return err
		}
		for _, e := range events {
			aggregateBoot(byID, e)
		}
	}
	return nil
}

// isTruncationErr returns true for the read-past-end shapes that
// signal an unclean-shutdown tail rather than an unreadable file.
// EOF is the binary walker's "next offset points beyond file end";
// io.ErrUnexpectedEOF is the JSONL parser's "line ends mid-record".
// Neither means the earlier records are unreadable — they're just
// telling us where the usable data ends.
func isTruncationErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	// journalbin wraps EOF into descriptive strings like
	// "journalbin: read entry-array hdr at 1844720: EOF" — the
	// errors.Is chain catches that via %w. The string-contains
	// fallback below covers the (historical) `fmt.Errorf` wraps
	// that didn't preserve the sentinel.
	msg := err.Error()
	return strings.Contains(msg, ": EOF") || strings.Contains(msg, "unexpected EOF")
}

// resolveBootSpec rewrites opts.bootID from any of the systemd-parity
// spec forms into a concrete 32-hex boot-id, and sets opts.directory
// to the target boot's on-disk journal directory when that boot is
// not the current one. Called before verifyBootID.
//
// Spec forms:
//
//	""       → current boot (no rewrite)
//	"0"      → current boot (no rewrite)
//	<32hex>  → specific boot-id (pre-resolved; no rewrite)
//	"-N"     → N-th previous boot from the ordered set the aggregator
//	           sees (ring buffer + on-disk journals). "-1" = the boot
//	           just before current, "-2" = the one before that, etc.
//	"+N"     → not implemented (only meaningful on merged multi-machine
//	           setups; returns error)
//
// The resolved ID is looked up against the same aggregation used by
// --list-boots, so an operator seeing "-1" in --list-boots output
// gets exactly that boot's events when they pass `-b -1`.
func resolveBootSpec(conn net.Conn, opts *options) error {
	spec := opts.bootID
	// Fast paths: current-boot shorthands and pre-resolved hex.
	if spec == "" || spec == "0" {
		return nil
	}
	if len(spec) == 32 && isAllHex(spec) {
		// Concrete boot-id — route via directory in case it's not
		// current (verifyBootID will confirm or the directory read
		// takes over).
		opts.directory = firstExistingJournalDir()
		return nil
	}
	// Relative index?
	if strings.HasPrefix(spec, "-") || strings.HasPrefix(spec, "+") {
		n, err := strconv.Atoi(spec)
		if err != nil {
			return fmt.Errorf("-b %s: not a valid relative index (want -N)", spec)
		}
		if n > 0 {
			return fmt.Errorf("-b +%d: future-boot indexing not supported (only meaningful on merged multi-machine setups)", n)
		}
		byID, err := aggregateAllBoots(conn)
		if err != nil {
			return fmt.Errorf("-b %s: %w", spec, err)
		}
		if len(byID) == 0 {
			return fmt.Errorf("-b %s: no boots recorded yet", spec)
		}
		// Sort by first_ts ascending, then index from newest.
		// idx 0 = newest, -1 = second-newest, etc.
		sorted := make([]*bootRange, 0, len(byID))
		for _, r := range byID {
			sorted = append(sorted, r)
		}
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].first < sorted[j].first })
		wantIdx := len(sorted) - 1 + n // -1 → len-2, -2 → len-3, ...
		if wantIdx < 0 || wantIdx >= len(sorted) {
			return fmt.Errorf("-b %s: only %d boots recorded; requested index out of range", spec, len(sorted))
		}
		opts.bootID = sorted[wantIdx].id
		opts.directory = firstExistingJournalDir()
		return nil
	}
	return fmt.Errorf("-b %s: not a recognised spec (expected empty, 0, -N, or 32-hex ID)", spec)
}

// aggregateAllBoots is the aggregator used by both --list-boots and
// -b -N resolution — pulls from ring buffer + on-disk journals into
// a single BootID→bootRange map.
func aggregateAllBoots(conn net.Conn) (map[string]*bootRange, error) {
	byID := map[string]*bootRange{}
	// Ring buffer via control socket.
	payload, err := json.Marshal(control.JournalQueryRequest{})
	if err == nil {
		if werr := control.WritePacket(conn, control.CmdJournalQuery, payload); werr == nil {
			if events, cerr := collectStream(conn); cerr == nil {
				for _, e := range events {
					aggregateBoot(byID, e)
				}
			}
		}
	}
	// On-disk directories.
	for _, dir := range listBootsJournalDirs {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		if err := aggregateBootsFromDir(dir, byID); err != nil {
			return nil, err
		}
	}
	return byID, nil
}

// firstExistingJournalDir returns the first on-disk journal directory
// that exists among listBootsJournalDirs, or "" when none exist.
// Used to auto-set opts.directory when -b <spec> targets a boot
// other than the current one.
func firstExistingJournalDir() string {
	for _, dir := range listBootsJournalDirs {
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
	}
	return ""
}

// bootIDForFilter returns s only when it's a valid 32-hex boot-id.
// Empty string / "0" / "-N" forms map to "" (no filter) — those are
// current-boot / relative-index shorthands and must not reach the
// wire as literal filters. resolveBootSpec has already converted any
// relative form to a concrete hex ID before this is called.
func bootIDForFilter(s string) string {
	if len(s) == 32 && isAllHex(s) {
		return s
	}
	return ""
}

// isAllHex reports whether s is all lowercase-hex chars — a
// pre-resolved boot-id from journalctl --list-boots output has this
// shape.
func isAllHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// verifyBootID checks whether the requested boot spec matches the
// current boot. Accepts:
//   ""       → current boot (--boot / -b without arg)
//   "0"      → current boot (systemd shorthand)
//   <32 hex> → specific boot ID; must match current OR fall through
//              to on-disk read via opts.directory when set by
//              resolveBootSpec
func verifyBootID(conn net.Conn, want string) error {
	if want == "" || want == "0" {
		return nil
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
	// Apply -n client-side when the daemon was told Limit=0 (see
	// wireLimitFor). Trims the most recent N matches, matching the
	// tail semantics the daemon would have applied itself.
	if req.Limit == 0 && opts.limit > 0 && len(events) > opts.limit {
		events = events[len(events)-opts.limit:]
	}
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
	// Limit handling: when the request carries client-side filters
	// (Identifiers/ExcludeIdentifiers/GrepPattern) we deliberately
	// send the server Limit=0 so it returns the full match set. The
	// client then filters + applies the operator's -n. Sending Limit
	// to a daemon that doesn't understand the new filter fields
	// would trim BEFORE the filter runs, and -n 5 on `-t sshd` would
	// silently return zero events. See wireLimitFor.
	req := control.JournalQueryRequest{
		Units:              units,
		Since:              opts.since,
		Until:              opts.until,
		Limit:              wireLimitFor(opts),
		Identifiers:        opts.identifiers,
		ExcludeIdentifiers: opts.excludeIdentifiers,
		GrepPattern:        opts.grep,
		// Systemd's `-g` default is case-insensitive when the pattern
		// is all-lowercase, case-sensitive otherwise; we mirror that
		// unless the operator overrode with --case-sensitive[=BOOL].
		GrepInsensitive: shouldGrepInsensitive(opts),
		InvocationID:    opts.invocation,
		Namespace:       opts.namespace,
		// Only a resolved 32-hex boot-id becomes a filter. "" and
		// "0" are current-boot shorthands — passing them as a
		// filter would drop every event because no Event.BootID
		// equals them. resolveBootSpec has already rewritten -N
		// forms to concrete hex before this call.
		BootID: bootIDForFilter(opts.bootID),
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

// wireLimitFor returns the Limit value to send to the daemon. When
// the client is going to re-filter locally (any Group A filter is
// set) we send 0 (== "no cap") so the daemon returns the complete
// match set before we trim. Otherwise the daemon's -n trimming is
// authoritative.
func wireLimitFor(opts options) int {
	if len(opts.identifiers) > 0 || len(opts.excludeIdentifiers) > 0 ||
		opts.grep != "" || opts.invocation != "" || opts.namespace != "" {
		return 0
	}
	return opts.limit
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
	if len(req.Identifiers) == 0 && len(req.ExcludeIdentifiers) == 0 &&
		req.GrepPattern == "" && req.InvocationID == "" && req.Namespace == "" {
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
	catalog         *catalog.Catalog // non-nil when -x/--catalog is on; augments short output
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
	if opts.catalog {
		if c, err := loadCatalog(opts); err == nil {
			ro.catalog = c
		}
		// Silent on load failure — --catalog augmentation is a
		// convenience; a missing/broken catalog file shouldn't sink
		// the whole query.
	}
	return ro
}

// catalogAugment returns the catalog body for e's MESSAGE_ID, or
// "" if -x is off or the ID isn't found. Called from short renderers
// to append the human-readable explanation on a second line.
func (ro renderOpts) catalogAugment(e *journal.Event) string {
	if ro.catalog == nil || e.Fields == nil {
		return ""
	}
	id := e.Fields["MESSAGE_ID"]
	if id == "" {
		return ""
	}
	entry := ro.catalog.Lookup(id)
	if entry == nil {
		return ""
	}
	return entry.Body
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
	case fmtShortPrecise:
		return renderShort(out, e, timeShortPrecise, ro)
	case fmtShortISO:
		return renderShort(out, e, timeISO, ro)
	case fmtShortISOPrecise:
		return renderShort(out, e, timeISOPrecise, ro)
	case fmtShortFull:
		return renderShort(out, e, timeFull, ro)
	case fmtShortMonotonic:
		// Monotonic clock reads e.Mts, not e.Ts. Route through a
		// dedicated path that swaps the timestamp source.
		return renderShortMonotonic(out, e, ro)
	case fmtShortUnix:
		return renderShort(out, e, timeUnix, ro)
	case fmtCat:
		return renderCat(out, e, ro)
	case fmtWithUnit:
		return renderWithUnit(out, e, ro)
	case fmtJSON:
		return renderJSON(out, e)
	case fmtJSONPretty:
		return renderJSONPretty(out, e)
	case fmtJSONSSE:
		return renderJSONSSE(out, e)
	case fmtJSONSeq:
		return renderJSONSeq(out, e)
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
// RFC3339 with numeric offset. The -precise / -full / -monotonic /
// -unix variants tune the same slot for higher precision, weekday
// visibility, or a different clock reference altogether.
type timeFormat int

const (
	timeShort           timeFormat = iota // "Jan 02 15:04:05"
	timeShortPrecise                      // "Jan 02 15:04:05.uuuuuu"
	timeISO                               // RFC3339: "2026-09-03T22:53:17+02:00"
	timeISOPrecise                        // RFC3339 + microseconds
	timeFull                              // "Wed 2026-09-03 22:53:17 CEST"
	timeMonotonic                         // "[    5.123456]" seconds since boot
	timeUnix                              // "1234567890.123456"
)

// formatTime turns a nanosecond Unix timestamp into the display string
// for the selected timeFormat, honoring --utc. mts is used only by
// timeMonotonic (which reads e.Mts, not e.Ts).
func formatTime(nsec int64, tf timeFormat, utc bool) string {
	t := time.Unix(0, nsec)
	if utc {
		t = t.UTC()
	}
	switch tf {
	case timeShortPrecise:
		return t.Format("Jan 02 15:04:05.000000")
	case timeISO:
		return t.Format(time.RFC3339)
	case timeISOPrecise:
		return t.Format("2006-01-02T15:04:05.000000Z07:00")
	case timeFull:
		// systemd's short-full: "Wed 2026-09-03 22:53:17 CEST"
		return t.Format("Mon 2006-01-02 15:04:05 MST")
	case timeUnix:
		// Unix seconds with 6-digit microsecond precision.
		return fmt.Sprintf("%d.%06d", nsec/int64(time.Second), (nsec%int64(time.Second))/int64(time.Microsecond))
	case timeMonotonic:
		// Same shape as dmesg / kernel log: "[    S.uuuuuu]".
		secs := nsec / int64(time.Second)
		micros := (nsec % int64(time.Second)) / int64(time.Microsecond)
		return fmt.Sprintf("[%5d.%06d]", secs, micros)
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
		if _, err := fmt.Fprintf(out, "%s %s%s: %s\n",
			formatTime(e.Ts, tf, ro.utc), identOf(e), pidPart, msg); err != nil {
			return err
		}
		writeCatalogAugment(out, ro, e)
		return nil
	}
	host := e.Hostname
	if host == "" {
		host = "-"
	}
	if _, err := fmt.Fprintf(out, "%s %s %s%s: %s\n",
		formatTime(e.Ts, tf, ro.utc), host, identOf(e), pidPart, msg); err != nil {
		return err
	}
	writeCatalogAugment(out, ro, e)
	return nil
}

// writeCatalogAugment prints the catalog body under the event line
// when -x is set and a matching entry exists. Indent each body line
// so it visually parents to the event above. Matches systemd's
// two-space indent + `-- Subject:` header block.
func writeCatalogAugment(out io.Writer, ro renderOpts, e *journal.Event) {
	body := ro.catalogAugment(e)
	if body == "" {
		return
	}
	for _, line := range strings.Split(body, "\n") {
		fmt.Fprintf(out, "  %s\n", line)
	}
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

// renderShortMonotonic prints "[    S.uuuuuu] IDENT[PID]: MSG" — the
// short format with a monotonic-clock timestamp instead of wall-clock.
// Reads e.Mts (monotonic ns since boot), not e.Ts. Useful when the
// wall clock is unreliable (VMs without RTC, containers with fake
// time) but boot-relative ordering is meaningful.
func renderShortMonotonic(out io.Writer, e *journal.Event, ro renderOpts) error {
	// Reuse renderShort's identifier + bracket logic by constructing
	// a fake event with e.Ts replaced by e.Mts. Cheaper than
	// duplicating the whole renderShort body just to swap one field.
	shadow := *e
	shadow.Ts = e.Mts
	return renderShort(out, &shadow, timeMonotonic, ro)
}

// renderWithUnit produces short-format output with an explicit "unit:"
// prefix so multi-unit dumps stay unambiguous when a single log line
// could belong to any of several services. Mirrors systemd's
// with-unit format.
func renderWithUnit(out io.Writer, e *journal.Event, ro renderOpts) error {
	unit := e.Unit
	if unit == "" {
		unit = identOf(e)
	}
	// Emit the same identifying prefix short would produce, then
	// suffix it with "unit=NAME:" for disambiguation.
	fmt.Fprintf(out, "%s unit=%s ", formatTime(e.Ts, timeShort, ro.utc), unit)
	// Now delegate the message + identifier/pid rendering to a
	// pared-down inline path (can't call renderShort without
	// double-timestamping).
	ident := identOf(e)
	pid := shortDisplayPID(e)
	if pid > 0 {
		fmt.Fprintf(out, "%s[%d]: ", ident, pid)
	} else {
		fmt.Fprintf(out, "%s: ", ident)
	}
	msg := ro.truncateMsg(e.Msg)
	if _, err := fmt.Fprintln(out, msg); err != nil {
		return err
	}
	return nil
}

// renderJSONPretty is renderJSON with json.MarshalIndent — two-space
// indent, one field per line. Matches systemd's json-pretty layout
// closely enough that jq / diffing tools produce identical results.
func renderJSONPretty(out io.Writer, e *journal.Event) error {
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	_, err = out.Write(append(data, '\n'))
	return err
}

// renderJSONSSE emits Server-Sent Events framing: each event becomes
// one "data: <json>\n\n" record. HTTP/SSE consumers (browser
// EventSource, kubernetes-style watch endpoints) parse this directly.
// The exact framing is defined by the W3C SSE spec — a single data:
// line per JSON, followed by a blank line as the record delimiter.
func renderJSONSSE(out io.Writer, e *journal.Event) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "data: %s\n\n", data)
	return err
}

// renderJSONSeq emits RFC 7464 JSON Text Sequences: each record is
// framed by a leading RS (0x1E) byte and a trailing LF. This is the
// wire format `curl --output-format=json-seq` and jq --seq speak, so
// operators piping slinit-journalctl output into those tools get a
// non-ambiguous stream even when a JSON value contains embedded
// newlines.
func renderJSONSeq(out io.Writer, e *journal.Event) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := out.Write([]byte{0x1e}); err != nil {
		return err
	}
	_, err = out.Write(append(data, '\n'))
	return err
}

// shortDisplayPID replicates renderShort's PID selection so
// renderWithUnit stays byte-identical in the [PID] slot.
func shortDisplayPID(e *journal.Event) int {
	if v, ok := e.Fields["SLINIT_TARGET_PID"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return e.Pid
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
  -o, --output=FMT            Output format:
                              short (default), short-precise, short-iso,
                              short-iso-precise, short-full, short-monotonic,
                              short-unix, cat, with-unit,
                              json, json-pretty, json-sse, json-seq,
                              verbose, export
      --output-fields=A,B,C   Restrict verbose/export/JSON to these field keys
  -u, --unit=NAME             Filter by service unit (repeatable — OR-set)
  -U, --user-unit=NAME        Filter by user-scope unit (also forces --user)
  -t, --identifier=IDENT      Include events with matching SYSLOG_IDENTIFIER (repeatable)
  -T, --exclude-identifier=I  Drop events with matching SYSLOG_IDENTIFIER (repeatable)
      --facility=NAME|N       Parsed, accepted; slinit doesn't record facility yet (WARN emitted)
  -g, --grep=PATTERN          RE2 regex on MESSAGE; default case-insensitive if pattern is all-lowercase
      --case-sensitive[=BOOL] Override the case heuristic used by -g
  -p, --priority=LVL          Keep only events at LVL or more urgent (0..7 or emerg..debug)
  -S, --since=TIME            Keep only events at or after TIME
  -U, --until=TIME            Keep only events at or before TIME (also --user-unit above)
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
  -i, --file=PATH             Read a journal file (binary or JSONL; magic-detected; .gz auto-decompress)
  -D, --directory=DIR         Iterate every *.jsonl / *.jsonl.gz / *.slj under DIR
      --root=PATH             Prefix applied to filesystem paths (--directory, --disk-usage default)
      --verify                Walk FSS TAG chain on --file (binary only); needs --fss-key
      --fss-key=PATH          FSS key file for --verify (default /etc/slinit/journal-key)

  Display modifiers:
  -W, --no-hostname           Drop hostname column from short outputs
      --utc                   Render timestamps in UTC instead of local
      --truncate-newline      Cut MESSAGE at the first newline
      --no-full               Ellipsize long fields (~256 chars)
  -l, --full                  Show full fields (default)
  -a, --all                   Show all field values without ellipsizing
  -e, --pager-end             Accepted for parity; no pager currently invoked
      --no-pager              Accepted for parity; slinit never invokes a pager
  -q, --quiet                 Suppress info messages (empty file etc.)

  Introspection (short-circuit — no event stream):
  -F, --field=NAME            Print distinct values seen for NAME across events
  -N, --fields                Print the list of known field names
      --header                Print journal file / buffer header metadata
      --disk-usage            Print total bytes across on-disk journals
  -I                          Query only the latest invocation of -u UNIT
                              (mutually exclusive with --invocation=ID)

  Machine target (systemd-nspawn integration):
  -M, --machine=CONTAINER     Accepted for parity; slinit has no nspawn
                              integration — WARN emitted, query hits host
                              journal.

  Maintenance (short-circuit — talks to slinit-journald via signals):
      --sync                  Force fsync via SIGUSR1 to daemon
                              (falls back to walking journal dir if no daemon)
      --rotate                Force rotation via SIGUSR2 (daemon required)
      --vacuum-size=SIZE      Prune rotated files until total on-disk ≤ SIZE
                              (bytes; K/M/G/T suffix accepted)
      --vacuum-files=N        Keep only the most recent N archived files
      --vacuum-time=TIME      Drop files older than TIME (s/m/h/d/w/M/y)
      --pid-file=PATH         Override /run/slinit-journald.pid lookup path
      --flush                 Migrate volatile /run journal → persistent
                              /var (via admin socket). Daemon required.
      --relinquish-var        Close persistent sink, reopen at volatile —
                              call before umount /var.
      --smart-relinquish-var  --relinquish-var only when /var is a separate
                              mountpoint.

  Journal namespaces:
      --namespace=NS          Filter events by their Namespace tag; when
                              set, wireLimit switches to client-side -n so
                              small limits still surface matching events
      --list-namespaces       List namespaces detected via
                              /var/log/slinit-journal.* and
                              /run/slinit-journal.* dirs

  Disk image dissection (requires root):
      --image=PATH            Attach the image via losetup(8), mount ro,
                              locate the journal dir (var/log/slinit-
                              journal or run/slinit-journal), query it,
                              detach on exit
      --image-policy=POLICY   loose (default) | strict | full colon-
                              separated systemd form. strict refuses
                              LUKS/LVM/verity partitions

  FSS (Forward-Secure Sealing):
      --setup-keys            Mint a fresh sealing key; save to --fss-key
                              (default /etc/slinit/journal-key); print the
                              verification token for out-of-band sharing
      --force                 Allow --setup-keys to overwrite an existing
                              key file (invalidates prior TAG chains)
      --verify-key=TOKEN      Inline verification token (alternative to
                              --fss-key file)
      --interval=DUR          Epoch duration for --setup-keys (default 15m)
      --synchronize-on-exit[=BOOL]
                              Accepted for parity with systemd; slinit
                              always fsyncs on Close so this is a no-op

  Catalog:
  -x, --catalog               Augment MESSAGE with catalog entry text
                              (matches on the event's MESSAGE_ID field)
      --dump-catalog          Dump all catalog entries as text
      --list-catalog          List every catalog MESSAGE_ID, sorted
      --update-catalog        Rescan /usr/share/slinit-catalog + friends,
                              rebuild the compiled cache

  Invocation tracking:
      --invocation=UUID       Filter events by SLINIT_INVOCATION_ID
      --list-invocations      With -u UNIT: list every invocation seen,
                              one row per (id + first..last timestamp)

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

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

		default:
			return opts, fmt.Errorf("unknown argument %q (try -h)", a)
		}
	}
	return opts, nil
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
	if opts.sourceFile != "" {
		if opts.follow {
			return errors.New("--file and --follow are mutually exclusive (file-follow lands with Phase 3)")
		}
		return runFromFile(opts)
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
	out := os.Stdout
	for _, evt := range events {
		if err := render(out, opts.format, evt); err != nil {
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
	out := os.Stdout
	for _, evt := range events {
		if err := render(out, opts.format, evt); err != nil {
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
	// --cursor overrides --since and adds a boot_id sanity check on
	// the reply. If the boot changed since the cursor was issued we
	// bail rather than replaying stale data.
	var wantBoot string
	if opts.cursor != "" {
		ts, boot, err := parseCursor(opts.cursor)
		if err != nil {
			return err
		}
		req.Since = ts + 1 // strictly after the cursor position
		wantBoot = boot
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
	if wantBoot != "" && len(events) > 0 && events[0].BootID != wantBoot {
		return fmt.Errorf("cursor: boot changed (was %s, now %s); position is meaningless in the new boot",
			wantBoot, events[0].BootID)
	}
	if opts.reverse {
		for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
			events[i], events[j] = events[j], events[i]
		}
	}
	out := os.Stdout
	for _, evt := range events {
		if err := render(out, opts.format, evt); err != nil {
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
	return nil
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
			if err := render(out, opts.format, evt); err != nil {
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
	req := control.JournalQueryRequest{
		Units: opts.units,
		Since: opts.since,
		Until: opts.until,
		Limit: opts.limit,
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

// render dispatches to the format-specific writer. Chosen over a
// map[format]func table because Go's method values allocate; a switch
// stays cheap and reads naturally.
func render(out io.Writer, f outputFormat, e *journal.Event) error {
	switch f {
	case fmtShort:
		return renderShort(out, e, timeShort)
	case fmtShortISO:
		return renderShort(out, e, timeISO)
	case fmtCat:
		return renderCat(out, e)
	case fmtJSON:
		return renderJSON(out, e)
	case fmtVerbose:
		return renderVerbose(out, e)
	case fmtExport:
		return renderExport(out, e)
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
// for the selected timeFormat.
func formatTime(nsec int64, tf timeFormat) string {
	t := time.Unix(0, nsec)
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
func renderShort(out io.Writer, e *journal.Event, tf timeFormat) error {
	host := e.Hostname
	if host == "" {
		host = "-"
	}
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
	_, err := fmt.Fprintf(out, "%s %s %s%s: %s\n",
		formatTime(e.Ts, tf), host, identOf(e), pidPart, e.Msg)
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
func renderCat(out io.Writer, e *journal.Event) error {
	_, err := fmt.Fprintln(out, e.Msg)
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
func renderVerbose(out io.Writer, e *journal.Event) error {
	// Timestamp header line — human-readable + machine-readable.
	fmt.Fprintf(out, "%s [%d.%09d]\n",
		formatTime(e.Ts, timeISO), e.Ts/1_000_000_000, e.Ts%1_000_000_000)
	writeField(out, "TS_NSEC", strconv.FormatInt(e.Ts, 10))
	writeField(out, "MTS_NSEC", strconv.FormatInt(e.Mts, 10))
	writeField(out, "PRIORITY", e.Prio.String())
	writeField(out, "TRANSPORT", string(e.Transport))
	writeField(out, "UNIT", e.Unit)
	writeField(out, "SYSLOG_IDENTIFIER", e.SyslogIdentifier)
	if e.Pid > 0 {
		writeField(out, "_PID", strconv.Itoa(e.Pid))
	}
	if e.Uid > 0 {
		writeField(out, "_UID", strconv.Itoa(e.Uid))
	}
	if e.Gid > 0 {
		writeField(out, "_GID", strconv.Itoa(e.Gid))
	}
	writeField(out, "_COMM", e.Comm)
	writeField(out, "_EXE", e.Exe)
	writeField(out, "_CMDLINE", e.Cmdline)
	writeField(out, "_HOSTNAME", e.Hostname)
	writeField(out, "_BOOT_ID", e.BootID)
	writeField(out, "_MACHINE_ID", e.MachineID)
	writeField(out, "MESSAGE", e.Msg)
	// Freeform fields last, sorted for deterministic output.
	for _, k := range sortedKeys(e.Fields) {
		writeField(out, k, e.Fields[k])
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
func renderExport(out io.Writer, e *journal.Event) error {
	// Timestamps + core.
	fmt.Fprintf(out, "__REALTIME_TIMESTAMP=%d\n", e.Ts/1000) // us
	fmt.Fprintf(out, "__MONOTONIC_TIMESTAMP=%d\n", e.Mts/1000)
	writeExportField(out, "PRIORITY", strconv.Itoa(int(e.Prio)))
	writeExportField(out, "MESSAGE", e.Msg)
	writeExportField(out, "SYSLOG_IDENTIFIER", e.SyslogIdentifier)
	writeExportField(out, "_TRANSPORT", string(e.Transport))
	writeExportField(out, "_SLINIT_UNIT", e.Unit)
	if e.Pid > 0 {
		writeExportField(out, "_PID", strconv.Itoa(e.Pid))
	}
	if e.Uid > 0 {
		writeExportField(out, "_UID", strconv.Itoa(e.Uid))
	}
	if e.Gid > 0 {
		writeExportField(out, "_GID", strconv.Itoa(e.Gid))
	}
	writeExportField(out, "_COMM", e.Comm)
	writeExportField(out, "_EXE", e.Exe)
	writeExportField(out, "_CMDLINE", e.Cmdline)
	writeExportField(out, "_HOSTNAME", e.Hostname)
	writeExportField(out, "_BOOT_ID", e.BootID)
	writeExportField(out, "_MACHINE_ID", e.MachineID)
	// Freeform fields in stable (sorted) order — matches renderVerbose.
	for _, k := range sortedKeys(e.Fields) {
		writeExportField(out, k, e.Fields[k])
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

// writeField prints "    KEY=value" only when the value is non-empty
// — keeps the verbose renderer scannable by dropping unset fields.
func writeField(out io.Writer, name, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(out, "    %s=%s\n", name, value)
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
  -n, --lines=N        Show only the last N matching events (0 = all)
  -o, --output=FMT     Output format: short (default), short-iso, cat, json, verbose
  -u, --unit=NAME      Filter by service unit (repeatable — OR-set)
  -p, --priority=LVL   Keep only events at LVL or more urgent (0..7 or emerg..debug)
      --since=TIME     Keep only events at or after TIME
      --until=TIME     Keep only events at or before TIME
  -r, --reverse        Print newest first (ignored under -f)
  -f, --follow         Stream new events as they arrive (Ctrl-C to stop)
  -k, --dmesg          Show only kernel (kmsg) events
      --list-boots     List boot IDs the journal buffer covers and exit
  -b, --boot [ID]      Restrict to a boot (empty or "0" = current; full hex ID also accepted;
                       relative "-N" and cross-boot control-socket query not yet wired)
  -c, --cursor=TOKEN   Resume from a cursor produced by --show-cursor
      --show-cursor    Print a "-- cursor: s=..;b=.." line after output
      --file=PATH      Read a journal file directly (binary or JSONL; magic-detected; .gz auto-decompress)
      --verify         Walk FSS TAG chain on --file (binary only); needs --fss-key
      --fss-key=PATH   FSS key file for --verify (default /etc/slinit/journal-key)
      --socket-path=P  Override the control socket path
      --system         Force system-mode socket (/run/slinit.socket)
      --user           Force user-mode socket ($XDG_RUNTIME_DIR/slinitctl)
      --version        Print version and exit
  -h, --help           Show this help

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

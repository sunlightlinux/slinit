// Package einfo produces OpenRC-flavoured status output — the
// familiar `* Doing thing ...` messages with `[ ok ]` / `[ !! ]`
// right-aligned markers that init.d scripts emit via einfo(1),
// ebegin(1), eend(1), ewarn(1), eerror(1), and friends.
//
// The package is stateless: each call reads config from the process
// environment (EINFO_QUIET, EINFO_VERBOSE, EINFO_COLOR, EINFO_INDENT)
// so the caller can compose with the surrounding shell script's
// state without threading a config struct through every helper.
package einfo

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Level marks how a message should be styled.
type Level int

const (
	LevelInfo Level = iota
	LevelWarn
	LevelError
)

// ColorSet is the ANSI escape triplet used by a run. Empty strings
// mean "colour disabled" — either because EINFO_COLOR=no, the target
// stream is not a TTY, or the user's TERM is dumb.
type ColorSet struct {
	Good    string
	Warn    string
	Bad     string
	Hilite  string
	Bracket string
	Normal  string
}

// Reset (Normal) is aliased on the ColorSet zero value too so
// callers do not have to test each field.
func (c ColorSet) Reset() string { return c.Normal }

// Quiet reports whether EINFO_QUIET is truthy. Every Emit / Begin /
// End helper honours it by returning early.
func Quiet() bool { return isTruthy(os.Getenv("EINFO_QUIET")) }

// Verbose reports whether EINFO_VERBOSE is truthy. The `v*` applet
// variants only print when Verbose() is true.
func Verbose() bool { return isTruthy(os.Getenv("EINFO_VERBOSE")) }

// Indent returns the current EINFO_INDENT value as a whitespace
// string. init.d shells maintain the counter themselves (the CLI
// tool cannot mutate its parent's environment), but everything
// downstream honours whatever it finds.
func Indent() string {
	n, err := strconv.Atoi(os.Getenv("EINFO_INDENT"))
	if err != nil || n <= 0 {
		return ""
	}
	if n > 40 {
		n = 40
	}
	return strings.Repeat(" ", n)
}

// ColorsFor returns the escape set appropriate for w. Colours are
// suppressed when EINFO_COLOR=no, when w is not a *os.File attached
// to a TTY, or when TERM is "dumb"/empty. Callers pass w so a
// mid-pipeline redirect gets sensible output.
func ColorsFor(w io.Writer) ColorSet {
	if !colorEnabled(w) {
		return ColorSet{}
	}
	// Values taken verbatim from OpenRC libeinfo — same escape
	// bytes so operator eyes trained on OpenRC still match ours.
	return ColorSet{
		Good:    "\033[32;01m",
		Warn:    "\033[33;01m",
		Bad:     "\033[31;01m",
		Hilite:  "\033[36;01m",
		Bracket: "\033[34;01m",
		Normal:  "\033[0m",
	}
}

func colorEnabled(w io.Writer) bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("EINFO_COLOR")), "no") {
		return false
	}
	if t := strings.ToLower(os.Getenv("TERM")); t == "" || t == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	// Only enable when we hit a character device (i.e. a real terminal).
	return fi.Mode()&os.ModeCharDevice != 0
}

// Emit prints a single line prefixed with the standard " *" marker.
// verbose=true means "gate on EINFO_VERBOSE" (matches the v* applets);
// newline=false skips the trailing newline (the n* applets).
func Emit(w io.Writer, lv Level, verbose, newline bool, msg string) {
	if Quiet() {
		return
	}
	if verbose && !Verbose() {
		return
	}
	c := ColorsFor(w)
	prefix := c.Good
	switch lv {
	case LevelWarn:
		prefix = c.Warn
	case LevelError:
		prefix = c.Bad
	}
	tail := "\n"
	if !newline {
		tail = ""
	}
	fmt.Fprintf(w, "%s%s * %s%s%s",
		Indent(), prefix, c.Normal, msg, tail)
}

// Begin prints a "* Doing thing ..." line with no newline, laying
// the groundwork for a later End on the same visual row (End's
// carriage return moves the cursor back to overwrite the trailing
// "..." with a status marker).
func Begin(w io.Writer, verbose bool, msg string) {
	if Quiet() {
		return
	}
	if verbose && !Verbose() {
		return
	}
	c := ColorsFor(w)
	fmt.Fprintf(w, "%s%s * %s%s ...", Indent(), c.Good, c.Normal, msg)
}

// End prints the status marker right-aligned at column COLUMNS
// (defaults to 80). code=0 → green `[ ok ]`; non-zero → red `[ !! ]`.
// When msg is non-empty it is emitted as a warning/error line first,
// then the marker follows on a fresh line so the operator can see
// both. Returns the exit code the caller should propagate.
func End(w io.Writer, verbose bool, code int, msg string) int {
	if Quiet() {
		return code
	}
	if verbose && !Verbose() {
		return code
	}
	return endMarker(w, code, msg, false)
}

// EndWarn is like End but uses [ !! ] in yellow for non-zero — the
// ewend semantic (warning end, not a fatal error).
func EndWarn(w io.Writer, verbose bool, code int, msg string) int {
	if Quiet() {
		return code
	}
	if verbose && !Verbose() {
		return code
	}
	return endMarker(w, code, msg, true)
}

// endMarker is shared between End and EndWarn; warnColour swaps
// the bad colour for warn so the user's palette lines up with the
// underlying applet.
func endMarker(w io.Writer, code int, msg string, warnColour bool) int {
	c := ColorsFor(w)
	if msg != "" {
		if code == 0 {
			Emit(w, LevelInfo, false, true, msg)
		} else if warnColour {
			Emit(w, LevelWarn, false, true, msg)
		} else {
			Emit(w, LevelError, false, true, msg)
		}
	}
	cols := terminalColumns()
	// "  [ ok ]" — 8 visible chars total; right-align.
	marker := " ok "
	col := c.Good
	if code != 0 {
		if warnColour {
			col = c.Warn
		} else {
			col = c.Bad
		}
		marker = " !! "
	}
	// The carriage return brings us back to column 0 after Begin's
	// "* thing ..." so the marker overwrites the ellipsis tail.
	pad := cols - 8
	if pad < 0 {
		pad = 0
	}
	fmt.Fprintf(w, "\r%s%s[%s%s%s%s]%s\n",
		strings.Repeat(" ", pad),
		c.Bracket, col, marker, c.Bracket, c.Normal, c.Normal)
	return code
}

func terminalColumns() int {
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n > 0 {
		return n
	}
	return 80
}

// EvalColors returns a shell-friendly `KEY='value'` block naming
// each colour so operator scripts can `eval $(eval_ecolors)`.
func EvalColors(c ColorSet) string {
	return fmt.Sprintf("GOOD='%s'\nWARN='%s'\nBAD='%s'\nHILITE='%s'\nBRACKET='%s'\nNORMAL='%s'\n",
		c.Good, c.Warn, c.Bad, c.Hilite, c.Bracket, c.Normal)
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "y", "yes", "true", "on":
		return true
	}
	return false
}

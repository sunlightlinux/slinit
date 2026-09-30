package recovery

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/sunlightlinux/slinit/pkg/einfo"
)

// Box geometry. The interior is the total minus the two `|` margins and
// the space either side of the content.
const (
	defaultBoxWidth = 62 // the historical fixed size, and the fallback
	minBoxWidth     = 44 // narrower than this and the action rows wrap
	maxBoxWidth     = 100
)

// box renders the rescue prompts. All three — load failure, boot
// collapse and the Ctrl-B debugger — share it so they keep looking like
// siblings, and so a fix to the framing lands in all three at once.
//
// It carries the console's real width and the palette rather than
// consulting the environment per line: the detection happens once, in
// newBox, and a test constructing one over a bytes.Buffer deterministically
// gets the 62-column plain-text rendering the golden assertions expect.
type box struct {
	w     io.Writer
	total int            // full line width, both margins included
	pal   einfo.ColorSet // zero value when colour is off
	rich  bool           // safe to emit screen-control sequences
}

// newBox sizes and styles a box for w.
//
// Width comes from the kernel's record of the terminal size, not from
// asking the terminal: see ttyColumns.
func newBox(w io.Writer) *box {
	b := &box{w: w, total: defaultBoxWidth}
	if f, ok := w.(*os.File); ok {
		if cols := ttyColumns(f); cols > 0 {
			b.total = clampInt(cols, minBoxWidth, maxBoxWidth)
		}
	}
	if b.rich = consoleIsCapable(w); b.rich {
		b.pal = rescuePalette
	}
	return b
}

// rescuePalette holds the same escape bytes pkg/einfo emits, which are
// OpenRC's, so an operator's eyes do not have to learn a second palette
// for the one screen that appears when things have already gone wrong.
// Copied rather than fetched through einfo.ColorsFor, because that
// applies einfo's capability gate and consoleIsCapable has already made
// that decision on different and, for PID 1, correct grounds.
var rescuePalette = einfo.ColorSet{
	Good:   "\033[32;01m",
	Warn:   "\033[33;01m",
	Bad:    "\033[31;01m",
	Hilite: "\033[36;01m",
	Normal: "\033[0m",
}

// consoleIsCapable decides whether it is safe to emit colour and a
// screen clear.
//
// einfo's own gate cannot be reused verbatim here, and the difference
// matters: it treats an empty TERM as "not a terminal", which is right
// for a CLI invoked from an init.d script, and wrong for PID 1. The
// kernel hands PID 1 no environment, so TERM is normally unset at the
// exact moment the rescue menu is the only thing on screen — deferring
// to that rule would have made the colour unreachable in production
// while still passing every test on a developer's terminal.
//
// So: a character device is a real terminal and /dev/console on Linux
// understands ANSI whether or not anyone named it in TERM. An explicit
// TERM=dumb is still honoured, because that is someone saying so, and
// EINFO_COLOR=no turns this off together with the rest of slinit's
// output.
func consoleIsCapable(w io.Writer) bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("EINFO_COLOR")), "no") {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb") {
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
	return fi.Mode()&os.ModeCharDevice != 0
}

func (b *box) inner() int { return b.total - 4 }

func (b *box) bar() string { return "+" + strings.Repeat("=", b.total-2) + "+" }

// header opens a box, clearing the screen first when the terminal can
// take it.
func (b *box) header(title string) {
	b.clear()
	fmt.Fprintf(b.w, "\n%s\n", b.bar())
	b.styled(b.pal.Hilite, title)
}

// styled writes one content row. The text is truncated and padded to the
// interior width first, then wrapped in colour — the escape sequences go
// inside the margins, so the frame stays one colour whatever the content
// is, and the padding arithmetic never has to measure around them.
func (b *box) styled(colour, text string) {
	text = padTo(truncTo(text, b.inner()), b.inner())
	if colour == "" {
		fmt.Fprintf(b.w, "| %s |\n", text)
		return
	}
	fmt.Fprintf(b.w, "| %s%s%s |\n", colour, text, b.pal.Normal)
}

func (b *box) line(format string, args ...interface{}) {
	b.styled("", fmt.Sprintf(format, args...))
}

// bad is for what went wrong. On a rescue screen the error is the reason
// the operator is reading at all, and it used to be typographically
// identical to the hints.
func (b *box) bad(format string, args ...interface{}) {
	b.styled(b.pal.Bad, fmt.Sprintf(format, args...))
}

// action is for a row the operator can press.
func (b *box) action(format string, args ...interface{}) {
	b.styled(b.pal.Good, fmt.Sprintf(format, args...))
}

func (b *box) blank() { b.line("") }

// footer writes the countdown row, the closing bar and the prompt. verb
// is what the timeout actually does — "reboot" for the failure menus,
// "continue" for the debugger — so the two never disagree.
func (b *box) footer(verb string, timeout time.Duration) {
	b.styled(b.pal.Warn, fmt.Sprintf("Auto-%s in %2ds if no input.", verb, int(timeout.Seconds())))
	fmt.Fprintf(b.w, "%s\n> ", b.bar())
}

// countdown rewrites the prompt line in place once a second so the
// screen looks alive rather than hung. Padded to the box width, which is
// what clears the previous tick and any log line that slipped out before
// the boot console was paused.
func (b *box) countdown(verb string, secs int) {
	fmt.Fprintf(b.w, "\r%s\r> %sAuto-%s in %2ds if no input… (press any key)%s\r> ",
		strings.Repeat(" ", b.total), b.pal.Warn, verb, secs, b.pal.Normal)
}

// clearPrompt wipes the countdown line once input arrives or the wait
// ends, so whatever prints next starts from a clean line.
func (b *box) clearPrompt() {
	fmt.Fprintf(b.w, "\r%s\r", strings.Repeat(" ", b.total))
}

// clear puts the menu at the top of an empty screen.
//
// On a failed boot the console is already full of kernel and service
// output, so the box appeared at the bottom and any late log line pushed
// it out of view — the operator then had to scroll a serial console to
// find the prompt they were supposed to answer.
//
// ESC[H and ESC[2J are write-only. That matters here specifically:
// flushInput exists because terminals answer *queries* (cursor position,
// device attributes) and those answers arrive as input, which a
// single-keypress menu reads as a choice. Anything that probes the
// terminal is therefore off limits in this package, which is also why
// the width comes from an ioctl.
func (b *box) clear() {
	if !b.rich {
		return
	}
	fmt.Fprint(b.w, "\033[H\033[2J")
}

// --- display-width helpers ---
//
// Everything below measures text in terminal columns rather than bytes.
// The previous renderer used len() and sliced byte-wise, so a service
// name or error message containing anything outside ASCII moved the
// box's right margin — and since truncation appended a three-byte
// ellipsis, the box broke precisely when a line was long enough to need
// shortening. The em dash in the titles did it unconditionally.

// runeWidth is a deliberately small model: zero for combining marks and
// format characters, two for the CJK/Hangul/fullwidth blocks, one for
// everything else. Enough to keep the frame square for the Latin,
// Cyrillic and Greek text that service names and error strings actually
// carry, plus the em dash and ellipsis this package emits itself,
// without vendoring a full east-asian-width table into PID 1.
func runeWidth(r rune) int {
	if r == 0 || unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf) {
		return 0
	}
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0xA4CF, // CJK radicals through Yi
		r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK compatibility ideographs
		r >= 0xFE30 && r <= 0xFE6F, // CJK compatibility forms
		r >= 0xFF00 && r <= 0xFF60, // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x20000 && r <= 0x3FFFD: // CJK extension B onward
		return 2
	}
	return 1
}

// dispWidth measures s in terminal columns. s must be plain text; box
// content is coloured only after it has been padded, so no escape
// sequence ever reaches here.
func dispWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// cutToWidth returns the longest prefix of s that fits in cols columns,
// always on a rune boundary.
func cutToWidth(s string, cols int) string {
	w := 0
	for i, r := range s {
		rw := runeWidth(r)
		if w+rw > cols {
			return s[:i]
		}
		w += rw
	}
	return s
}

// truncTo shortens s to at most cols columns, marking the cut with an
// ellipsis. Below three columns there is no room for a marker and the
// text is cut hard, which is the threshold the previous byte-based
// version used.
func truncTo(s string, cols int) string {
	if cols <= 0 {
		return ""
	}
	if dispWidth(s) <= cols {
		return s
	}
	if cols < 3 {
		return cutToWidth(s, cols)
	}
	return cutToWidth(s, cols-1) + "…"
}

// padTo right-pads s with spaces to cols columns.
func padTo(s string, cols int) string {
	if d := cols - dispWidth(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// truncString is kept as the column-correct replacement for the
// byte-slicing helper the debugger used for service names.
func truncString(s string, n int) string { return truncTo(s, n) }

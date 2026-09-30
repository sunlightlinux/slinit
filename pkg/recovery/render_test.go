package recovery

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

// boxRows returns the framed content rows of a rendered menu.
func boxRows(out string) []string {
	var rows []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "|") {
			rows = append(rows, line)
		}
	}
	return rows
}

// The defect this file exists for: every row has to be exactly as wide
// as the frame, whatever is in it. The previous renderer measured with
// len(), so any multi-byte character moved the right margin — the em
// dash in the titles did it on every single menu, unconditionally.
func TestBoxRowsAreSquareWithMultibyteContent(t *testing.T) {
	var buf bytes.Buffer
	b := newBox(&buf)
	renderDebugMenu(b, StatusSnapshot{
		Elapsed: 4200 * time.Millisecond,
		InProgress: []ServiceInfo{
			{Name: "ascii-name", State: "STARTING", Note: "4.2s"},
			{Name: "nume-cu-diacritice-țăîâș-lung", State: "STARTING", Note: "9.9s"},
			{Name: "日本語のサービス名", State: "STARTING"},
		},
		RecentErrors: []string{
			"plain ascii error",
			"eroare cu diacritice: dependență lipsă în configurație — nu pornește",
		},
	}, 60*time.Second)

	rows := boxRows(buf.String())
	if len(rows) < 8 {
		t.Fatalf("expected a populated menu, got %d rows", len(rows))
	}
	for _, r := range rows {
		if w := dispWidth(r); w != defaultBoxWidth {
			t.Errorf("row is %d columns, want %d: %q", w, defaultBoxWidth, r)
		}
	}

	// And the bars must match the rows, or the box has a step in it.
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(line, "+") && dispWidth(line) != defaultBoxWidth {
			t.Errorf("bar is %d columns, want %d", dispWidth(line), defaultBoxWidth)
		}
	}
}

func TestBoxWidthFollowsTerminal(t *testing.T) {
	for _, tc := range []struct{ cols, want int }{
		{0, defaultBoxWidth}, // unknown size keeps the historical width
		{80, 80},             //
		{132, maxBoxWidth},   // clamped: wider frames are unreadable
		{20, minBoxWidth},    // clamped: narrower and the action rows wrap
	} {
		var buf bytes.Buffer
		b := newBox(&buf)
		if tc.cols > 0 {
			b.total = clampInt(tc.cols, minBoxWidth, maxBoxWidth)
		}
		if b.total != tc.want {
			t.Errorf("cols=%d: width %d, want %d", tc.cols, b.total, tc.want)
		}
		b.header("title")
		b.line("content")
		b.footer("reboot", time.Second)
		for _, r := range boxRows(buf.String()) {
			if w := dispWidth(r); w != tc.want {
				t.Errorf("cols=%d: row is %d columns, want %d: %q", tc.cols, w, tc.want, r)
			}
		}
	}
}

// A bytes.Buffer is not a tty, so nothing may emit colour or screen
// control. This is what keeps the golden substring assertions in the
// other test files meaningful, and what keeps a redirected console
// readable.
func TestBoxIsPlainWhenNotATerminal(t *testing.T) {
	var buf bytes.Buffer
	b := newBox(&buf)
	if b.rich {
		t.Error("a buffer must not be treated as a capable terminal")
	}
	b.header("slinit: BOOT FAILURE — cannot continue")
	b.bad("  something broke")
	b.action("  [r]  reboot now")
	b.footer("reboot", 60*time.Second)
	b.countdown("reboot", 42)

	if strings.Contains(buf.String(), "\033") {
		t.Errorf("escape sequence leaked into non-tty output: %q", buf.String())
	}
}

// Colour has to go inside the margins: the frame stays one colour and,
// more importantly, the padding is computed on the plain text so the
// escapes cannot be mistaken for content.
func TestBoxColourStaysInsideTheFrame(t *testing.T) {
	var buf bytes.Buffer
	b := newBox(&buf)
	b.pal.Bad = "\033[31;01m"
	b.pal.Normal = "\033[0m"
	b.bad("boom")

	got := buf.String()
	if !strings.HasPrefix(got, "| \033[31;01m") {
		t.Errorf("colour should start after the left margin, got %q", got)
	}
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), "\033[0m |") {
		t.Errorf("colour should end before the right margin, got %q", got)
	}
	// Strip the escapes and the row must be exactly frame-width.
	plain := strings.NewReplacer("\033[31;01m", "", "\033[0m", "").Replace(strings.TrimRight(got, "\n"))
	if w := dispWidth(plain); w != defaultBoxWidth {
		t.Errorf("row is %d columns once stripped, want %d: %q", w, defaultBoxWidth, plain)
	}
}

func TestDispWidth(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"—", 1},     // em dash: one column, three bytes
		{"…", 1},     // ellipsis: the marker that broke the box
		{"țăîâș", 5}, // Latin Extended
		{"日本", 4},    // wide: two columns each
		{"á", 1},    // combining acute contributes nothing
	}
	for _, c := range cases {
		if got := dispWidth(c.in); got != c.want {
			t.Errorf("dispWidth(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestTruncToNeverSplitsARune(t *testing.T) {
	// Cutting mid-sequence would emit a replacement character or worse.
	for cols := 1; cols <= 12; cols++ {
		got := truncTo("日本語のサービス", cols)
		if !utf8Valid(got) {
			t.Errorf("cols=%d produced invalid UTF-8: %q", cols, got)
		}
		if w := dispWidth(got); w > cols {
			t.Errorf("cols=%d: result is %d columns: %q", cols, w, got)
		}
	}
	for cols := 1; cols <= 12; cols++ {
		got := truncTo("eroare-țăîâș-lungă", cols)
		if !utf8Valid(got) {
			t.Errorf("cols=%d produced invalid UTF-8: %q", cols, got)
		}
		if w := dispWidth(got); w > cols {
			t.Errorf("cols=%d: result is %d columns: %q", cols, w, got)
		}
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func TestPadToUsesColumnsNotBytes(t *testing.T) {
	// Three bytes, one column: padding has to add 9 spaces, not 7.
	if got := padTo("—", 10); dispWidth(got) != 10 {
		t.Errorf("padTo(em dash, 10) is %d columns: %q", dispWidth(got), got)
	}
	// Already at or over width: left alone.
	if got := padTo("abcdefghij", 10); got != "abcdefghij" {
		t.Errorf("padTo at exact width changed the string: %q", got)
	}
	if got := padTo("abcdefghijkl", 10); got != "abcdefghijkl" {
		t.Errorf("padTo must not truncate: %q", got)
	}
}

// The kernel gives PID 1 no environment, so TERM is normally unset at
// the exact moment the rescue menu is the only thing on screen. A gate
// that reads an empty TERM as "not a terminal" — which is the right rule
// for a CLI invoked from an init.d script — would make the colour
// unreachable in production while still passing on a developer's
// terminal, so it is asserted here rather than left to chance.
func TestConsoleCapabilityIgnoresUnsetTERM(t *testing.T) {
	tty, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no %s: %v", os.DevNull, err)
	}
	defer tty.Close()
	if fi, err := tty.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		t.Skip("/dev/null is not reporting as a character device here")
	}

	t.Setenv("EINFO_COLOR", "")
	t.Setenv("TERM", "")
	if !consoleIsCapable(tty) {
		t.Error("an unset TERM on a character device must still allow colour")
	}

	// An explicit dumb terminal is someone saying so, and is honoured.
	t.Setenv("TERM", "dumb")
	if consoleIsCapable(tty) {
		t.Error("TERM=dumb must disable colour")
	}

	// And the house-wide switch turns it off with the rest of slinit's
	// output rather than needing its own knob.
	t.Setenv("TERM", "linux")
	t.Setenv("EINFO_COLOR", "no")
	if consoleIsCapable(tty) {
		t.Error("EINFO_COLOR=no must disable colour")
	}

	// A plain file is never a terminal, whatever TERM says.
	t.Setenv("EINFO_COLOR", "")
	f, err := os.CreateTemp(t.TempDir(), "notatty")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if consoleIsCapable(f) {
		t.Error("a regular file must not be treated as a terminal")
	}
}

package einfo

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// clearEnv wipes every einfo env var so a test starts from a known
// baseline. Also disables colours (TERM=dumb) so the assertions do
// not have to filter escape sequences.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"EINFO_QUIET", "EINFO_VERBOSE", "EINFO_COLOR",
		"EINFO_INDENT", "COLUMNS",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("TERM", "dumb")
}

func TestEmitInfoWritesMarker(t *testing.T) {
	clearEnv(t)
	var buf bytes.Buffer
	Emit(&buf, LevelInfo, false, true, "hello")
	got := buf.String()
	if !strings.Contains(got, " * hello") || !strings.HasSuffix(got, "\n") {
		t.Errorf("got %q", got)
	}
}

func TestEmitNoNewline(t *testing.T) {
	clearEnv(t)
	var buf bytes.Buffer
	Emit(&buf, LevelInfo, false, false, "no-nl")
	if strings.HasSuffix(buf.String(), "\n") {
		t.Errorf("unexpected trailing newline: %q", buf.String())
	}
}

func TestEmitQuietSuppresses(t *testing.T) {
	clearEnv(t)
	t.Setenv("EINFO_QUIET", "yes")
	var buf bytes.Buffer
	Emit(&buf, LevelInfo, false, true, "hidden")
	if buf.Len() != 0 {
		t.Errorf("quiet leaked: %q", buf.String())
	}
}

func TestEmitVerboseGatesOnEnv(t *testing.T) {
	clearEnv(t)
	var buf bytes.Buffer
	Emit(&buf, LevelInfo, true, true, "silent")
	if buf.Len() != 0 {
		t.Errorf("verbose without EINFO_VERBOSE leaked: %q", buf.String())
	}
	buf.Reset()
	t.Setenv("EINFO_VERBOSE", "yes")
	Emit(&buf, LevelInfo, true, true, "loud")
	if !strings.Contains(buf.String(), "loud") {
		t.Errorf("verbose with env didn't fire: %q", buf.String())
	}
}

func TestEmitIndent(t *testing.T) {
	clearEnv(t)
	t.Setenv("EINFO_INDENT", "4")
	var buf bytes.Buffer
	Emit(&buf, LevelInfo, false, true, "in")
	// Four leading spaces before the " *" marker.
	if !strings.HasPrefix(buf.String(), "     * in") {
		t.Errorf("indent missing: %q", buf.String())
	}
}

func TestBeginNoNewline(t *testing.T) {
	clearEnv(t)
	var buf bytes.Buffer
	Begin(&buf, false, "starting")
	if strings.HasSuffix(buf.String(), "\n") {
		t.Errorf("Begin should not end with newline: %q", buf.String())
	}
	if !strings.Contains(buf.String(), " * starting ...") {
		t.Errorf("Begin format: %q", buf.String())
	}
}

func TestEndOKMarker(t *testing.T) {
	clearEnv(t)
	t.Setenv("COLUMNS", "20")
	var buf bytes.Buffer
	rc := End(&buf, false, 0, "")
	if rc != 0 {
		t.Errorf("rc=%d", rc)
	}
	if !strings.Contains(buf.String(), "[ ok ]") {
		t.Errorf("ok marker missing: %q", buf.String())
	}
}

func TestEndFailMarker(t *testing.T) {
	clearEnv(t)
	t.Setenv("COLUMNS", "20")
	var buf bytes.Buffer
	rc := End(&buf, false, 1, "")
	if rc != 1 {
		t.Errorf("rc=%d", rc)
	}
	if !strings.Contains(buf.String(), "[ !! ]") {
		t.Errorf("fail marker missing: %q", buf.String())
	}
}

func TestEndPrintsMessageBeforeMarker(t *testing.T) {
	clearEnv(t)
	var buf bytes.Buffer
	End(&buf, false, 1, "something went wrong")
	out := buf.String()
	msgIdx := strings.Index(out, "something went wrong")
	markerIdx := strings.Index(out, "[ !! ]")
	if msgIdx < 0 || markerIdx < 0 {
		t.Fatalf("missing pieces:\n%s", out)
	}
	if msgIdx >= markerIdx {
		t.Errorf("message must appear before marker:\n%s", out)
	}
}

func TestEvalColors(t *testing.T) {
	c := ColorSet{Good: "G", Warn: "W", Bad: "B", Hilite: "H", Bracket: "K", Normal: "N"}
	got := EvalColors(c)
	for _, want := range []string{"GOOD='G'", "WARN='W'", "BAD='B'",
		"HILITE='H'", "BRACKET='K'", "NORMAL='N'"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestColorsForRespectsEINFO_COLOR(t *testing.T) {
	clearEnv(t)
	t.Setenv("EINFO_COLOR", "no")
	t.Setenv("TERM", "xterm-256color")
	// Even with a real TERM, EINFO_COLOR=no disables.
	c := ColorsFor(os.Stdout)
	if c.Good != "" {
		t.Errorf("EINFO_COLOR=no should suppress: %+v", c)
	}
}

func TestColorsForBytesBuffer(t *testing.T) {
	// A non-*os.File writer never gets colour.
	var buf bytes.Buffer
	t.Setenv("EINFO_COLOR", "yes")
	t.Setenv("TERM", "xterm")
	if c := ColorsFor(&buf); c.Good != "" {
		t.Errorf("bytes.Buffer should not carry colour: %+v", c)
	}
}

func TestQuietVerboseHelpers(t *testing.T) {
	clearEnv(t)
	if Quiet() {
		t.Error("Quiet true on empty env")
	}
	t.Setenv("EINFO_QUIET", "on")
	if !Quiet() {
		t.Error("Quiet false when env=on")
	}
	clearEnv(t)
	if Verbose() {
		t.Error("Verbose true on empty env")
	}
	t.Setenv("EINFO_VERBOSE", "1")
	if !Verbose() {
		t.Error("Verbose false when env=1")
	}
}

func TestIndentClampsAndTolerates(t *testing.T) {
	clearEnv(t)
	if Indent() != "" {
		t.Errorf("empty EINFO_INDENT should yield empty prefix")
	}
	t.Setenv("EINFO_INDENT", "bogus")
	if Indent() != "" {
		t.Errorf("bogus should yield empty")
	}
	t.Setenv("EINFO_INDENT", "-5")
	if Indent() != "" {
		t.Errorf("negative should yield empty")
	}
	t.Setenv("EINFO_INDENT", "100")
	if len(Indent()) != 40 {
		t.Errorf("clamp to 40 failed: %d", len(Indent()))
	}
}

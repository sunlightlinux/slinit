package config

import (
	"strings"
	"testing"
)

// TestParseScriptBlock verifies a "script ... end script" block becomes the
// command via /bin/sh -c with the body preserved verbatim.
func TestParseScriptBlock(t *testing.T) {
	input := `type = process
script
echo starting
exec /usr/bin/myapp --flag
end script
`
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !desc.ScriptBlock {
		t.Error("expected ScriptBlock to be true")
	}
	want := []string{"/bin/sh", "-c", "echo starting\nexec /usr/bin/myapp --flag"}
	if len(desc.Command) != 3 || desc.Command[0] != want[0] ||
		desc.Command[1] != want[1] || desc.Command[2] != want[2] {
		t.Fatalf("expected %q, got %q", want, desc.Command)
	}
}

// TestParseScriptBlockPreservesIndentation confirms the body is taken
// verbatim, including leading whitespace (shell here-doc style).
func TestParseScriptBlockPreservesIndentation(t *testing.T) {
	input := "type = process\nscript\n  if true; then\n    echo nested\n  fi\nend script\n"
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	got := desc.Command[2]
	want := "  if true; then\n    echo nested\n  fi"
	if got != want {
		t.Fatalf("expected verbatim body %q, got %q", want, got)
	}
}

// TestParseScriptBlockConflictsWithPriorCommand verifies a script block after
// a command setting is rejected.
func TestParseScriptBlockConflictsWithPriorCommand(t *testing.T) {
	input := `type = process
command = /bin/true
script
echo hi
end script
`
	_, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err == nil || !strings.Contains(err.Error(), "script block conflicts with command") {
		t.Fatalf("expected conflict error, got %v", err)
	}
}

// TestParseCommandConflictsWithPriorScriptBlock verifies a command setting
// after a script block is rejected (the reverse direction).
func TestParseCommandConflictsWithPriorScriptBlock(t *testing.T) {
	input := `type = process
script
echo hi
end script
command = /bin/true
`
	_, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err == nil || !strings.Contains(err.Error(), "command conflicts with script block") {
		t.Fatalf("expected conflict error, got %v", err)
	}
}

// TestParseScriptBlockUnterminated verifies a missing "end script" is fatal.
func TestParseScriptBlockUnterminated(t *testing.T) {
	input := `type = process
script
echo hi
`
	_, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err == nil || !strings.Contains(err.Error(), "unterminated script block") {
		t.Fatalf("expected unterminated error, got %v", err)
	}
}

// TestParseScriptBlockEmpty verifies an empty block is accepted and yields an
// empty -c argument (no panic / off-by-one).
func TestParseScriptBlockEmpty(t *testing.T) {
	input := "type = process\nscript\nend script\n"
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(desc.Command) != 3 || desc.Command[2] != "" {
		t.Fatalf("expected empty -c body, got %q", desc.Command)
	}
}

// TestParseScriptBlockTemplateArg verifies $1 substitution still applies
// inside a script block (same load-time expansion as the command setting).
func TestParseScriptBlockTemplateArg(t *testing.T) {
	input := "type = process\nscript\nexec /usr/bin/worker $1\nend script\n"
	desc, err := ParseWithArg(strings.NewReader(input), "worker@tty1", "test-file", "tty1")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if desc.Command[2] != "exec /usr/bin/worker tty1" {
		t.Fatalf("expected $1 expanded, got %q", desc.Command[2])
	}
}

// TestParseScriptBlockDollarEscape verifies $$ collapses to a literal $ (same
// rule as the command setting), so runtime shell variables must use $$.
func TestParseScriptBlockDollarEscape(t *testing.T) {
	input := "type = process\nscript\necho $$HOME\nend script\n"
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if desc.Command[2] != "echo $HOME" {
		t.Fatalf("expected $$ -> $ escape, got %q", desc.Command[2])
	}
}

// TestParseScriptBlockIndentedKeyword verifies the opener/closer are matched
// even when indented (leading whitespace is trimmed for the markers only).
func TestParseScriptBlockIndentedKeyword(t *testing.T) {
	input := "type = process\n  script\necho hi\n  end script\n"
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if desc.Command[2] != "echo hi" {
		t.Fatalf("expected body, got %q", desc.Command[2])
	}
}

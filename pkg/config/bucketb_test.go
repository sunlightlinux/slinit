package config

import (
	"strings"
	"testing"
)

// TestParseBucketBDirectives verifies each legacy-safe niche directive
// round-trips through the parser. Runtime behaviour is covered by the
// runner-side tests (bucketb.go's applyBucketB path) and the utmp
// package's own tests.
func TestParseBucketBDirectives(t *testing.T) {
	input := `
type = process
command = /bin/true
coredump-filter = 0x33
timer-slack-nsec = 100000
memory-ksm = yes
remove-ipc = yes
ignore-sigpipe = no
personality = x86
utmp-mode = login
`
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.CoredumpFilter != "0x33" {
		t.Errorf("coredump-filter = %q, want 0x33", desc.CoredumpFilter)
	}
	if desc.TimerSlackNsec != 100000 {
		t.Errorf("timer-slack-nsec = %d, want 100000", desc.TimerSlackNsec)
	}
	if !desc.MemoryKSM {
		t.Error("memory-ksm should be true")
	}
	if !desc.RemoveIPC {
		t.Error("remove-ipc should be true")
	}
	if desc.IgnoreSIGPIPE == nil || *desc.IgnoreSIGPIPE {
		t.Errorf("ignore-sigpipe should be explicit false, got %v", desc.IgnoreSIGPIPE)
	}
	if desc.Personality != "x86" {
		t.Errorf("personality = %q, want x86", desc.Personality)
	}
	if desc.UtmpMode != "login" {
		t.Errorf("utmp-mode = %q, want login", desc.UtmpMode)
	}
}

// TestParseBucketBRejectsBadValues catches invalid enum values so a
// typo surfaces immediately rather than being silently ignored.
func TestParseBucketBRejectsBadValues(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"personality bogus",
			"type = process\ncommand = /bin/true\npersonality = z80\n"},
		{"utmp-mode bogus",
			"type = process\ncommand = /bin/true\nutmp-mode = bogus\n"},
		{"timer-slack-nsec negative",
			"type = process\ncommand = /bin/true\ntimer-slack-nsec = -1\n"},
	}
	for _, tc := range cases {
		if _, err := Parse(strings.NewReader(tc.body), "svc", "test-file"); err == nil {
			t.Errorf("%s: expected parse error, got nil", tc.name)
		}
	}
}

// TestParseIgnoreSigpipeUnset: without the directive, IgnoreSIGPIPE
// pointer stays nil — the loader interprets that as "use the default"
// (which is yes). Explicit yes/no set the pointer so the runner can
// distinguish opt-in from opt-out.
func TestParseIgnoreSigpipeUnset(t *testing.T) {
	desc, err := Parse(strings.NewReader("type = process\ncommand = /bin/true\n"), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.IgnoreSIGPIPE != nil {
		t.Errorf("unset ignore-sigpipe should keep pointer nil, got %v", *desc.IgnoreSIGPIPE)
	}
}

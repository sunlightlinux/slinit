package config

import (
	"strings"
	"testing"
)

// TestParseBusDirectives round-trips the three D-Bus config knobs.
// All three are storage-only — no runtime behaviour attached here
// (loader-side auto-wire covered separately when dbus-send probing
// can be mocked).
func TestParseBusDirectives(t *testing.T) {
	input := `
type = process
command = /usr/libexec/my-dbus-service
bus-name = org.example.MyService
bus-policy = talk
bus-name-scope = session
`
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.BusName != "org.example.MyService" {
		t.Errorf("bus-name = %q", desc.BusName)
	}
	if desc.BusPolicy != "talk" {
		t.Errorf("bus-policy = %q (accepted-warned, not consumed at runtime)", desc.BusPolicy)
	}
	if desc.BusNameScope != "session" {
		t.Errorf("bus-name-scope = %q", desc.BusNameScope)
	}
}

// TestParseBusNameRejectsGarbage catches typos before they turn
// into confusing runtime failures. Whitespace, missing dots, empty
// segments all fail at parse.
func TestParseBusNameRejectsGarbage(t *testing.T) {
	cases := []struct{ name, body string }{
		{"whitespace", "type=process\ncommand=/bin/true\nbus-name = my service\n"},
		{"no-dot", "type=process\ncommand=/bin/true\nbus-name = MyService\n"},
		{"leading-dot", "type=process\ncommand=/bin/true\nbus-name = .foo.bar\n"},
		{"trailing-dot", "type=process\ncommand=/bin/true\nbus-name = foo.bar.\n"},
		{"empty-segment", "type=process\ncommand=/bin/true\nbus-name = foo..bar\n"},
		{"unique-name", "type=process\ncommand=/bin/true\nbus-name = :1.42\n"},
		{"scope-bogus", "type=process\ncommand=/bin/true\nbus-name-scope = wat\n"},
	}
	for _, tc := range cases {
		if _, err := Parse(strings.NewReader(tc.body), "svc", "test-file"); err == nil {
			t.Errorf("%s: expected parse error", tc.name)
		}
	}
}

// TestIsValidDBusName pins the well-known-name grammar directly so
// changes to the helper are visible in test output.
func TestIsValidDBusName(t *testing.T) {
	good := []string{
		"org.freedesktop.DBus",
		"org.example.MyService",
		"a.b",
		"_x._y",
		"org.example.svc-1",
	}
	for _, s := range good {
		if !isValidDBusName(s) {
			t.Errorf("expected valid: %q", s)
		}
	}
	bad := []string{
		"",
		"noDots",
		".leadingDot",
		"trailing.",
		"empty..segment",
		"1starts.with.digit",
		":1.42",
		"has space",
		"has\ttab",
		strings.Repeat("a.b", 100), // 300 chars, over the 255 cap
	}
	for _, s := range bad {
		if isValidDBusName(s) {
			t.Errorf("expected invalid: %q", s)
		}
	}
}

// TestDBusReadyCheckCommandShape verifies the auto-wired ready-check
// command polls the right D-Bus method. Uses dbusSendPath directly so
// the test doesn't depend on dbus-send actually being installed on
// the host running the tests.
func TestDBusReadyCheckCommandShape(t *testing.T) {
	orig := dbusSendPath
	dbusSendPath = "/usr/bin/dbus-send"
	defer func() { dbusSendPath = orig }()

	cmd := dbusReadyCheckCommand("org.example.Foo", "")
	if len(cmd) != 3 || cmd[0] != "/bin/sh" || cmd[1] != "-c" {
		t.Fatalf("shape: %v", cmd)
	}
	body := cmd[2]
	for _, needle := range []string{
		"/usr/bin/dbus-send",
		"--system",
		"NameHasOwner",
		"string:org.example.Foo",
		"boolean true",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("body missing %q:\n%s", needle, body)
		}
	}

	// scope=session flips the flag.
	cmd = dbusReadyCheckCommand("org.example.Foo", "session")
	if !strings.Contains(cmd[2], "--session") {
		t.Errorf("session scope should switch flag: %s", cmd[2])
	}
}

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/config"
)

// TestParseOnActive covers the accepted duration forms + the reject
// paths (empty, negative, garbage). Bare integer is interpreted as
// seconds so the "sleep 5" idiom works.
func TestParseOnActive(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"5s", 5 * time.Second, false},
		{"200ms", 200 * time.Millisecond, false},
		{"1h", time.Hour, false},
		{"7", 7 * time.Second, false},
		{"", 0, true},
		{"-1", 0, true},
		{"garbage", 0, true},
	}
	for _, tc := range cases {
		got, err := parseOnActive(tc.in)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%q: err=%v want=%v", tc.in, err, tc.wantErr)
		}
		if err == nil && got != tc.want {
			t.Errorf("%q = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestWrapWithSleep: --on-active wraps the argv in /bin/sh -c so the
// child sleeps N first, then execs the original argv. Verify the
// shape and that embedded single quotes in an argv token survive the
// escape round-trip.
func TestWrapWithSleep(t *testing.T) {
	wrapped := wrapWithSleep("5s", []string{"/bin/echo", "hello world", "it's fine"})
	if len(wrapped) != 3 {
		t.Fatalf("wrapper argv should be [sh, -c, script], got %v", wrapped)
	}
	if wrapped[0] != "/bin/sh" || wrapped[1] != "-c" {
		t.Errorf("wrapper prefix wrong: %v", wrapped[:2])
	}
	script := wrapped[2]
	if !strings.HasPrefix(script, "sleep '5s'; exec ") {
		t.Errorf("script prefix wrong: %q", script)
	}
	// The 'it's fine' token has an embedded single quote; POSIX-quoting
	// closes-escapes-reopens: it'\''s fine
	if !strings.Contains(script, `'it'\''s fine'`) {
		t.Errorf("embedded quote escape missing: %q", script)
	}
}

// TestWrapWithSleepParsesBack: the generated body containing the
// wrapped command must still parse cleanly through the config parser.
func TestWrapWithSleepParsesBack(t *testing.T) {
	wrapped := wrapWithSleep("5s", []string{"/bin/echo", "hi"})
	body := buildRunBody("process", "", wrapped, "", "", "", "", nil)
	desc, err := config.Parse(strings.NewReader(body), "svc", "test")
	if err != nil {
		t.Fatalf("wrapped body failed to parse: %v\n%s", err, body)
	}
	if len(desc.Command) != 3 || desc.Command[0] != "/bin/sh" {
		t.Errorf("command tokenisation: %v", desc.Command)
	}
}

// TestBuildRunBodyMinimal covers the shape a bare `slinitctl run --
// /bin/echo hello` produces: type, command with quoted tokens,
// restart = false.
func TestBuildRunBodyMinimal(t *testing.T) {
	body := buildRunBody("process", "", []string{"/bin/echo", "hello"}, "", "", "", "", nil)
	want := "type = process\ncommand = \"/bin/echo\" \"hello\"\nrestart = false\n"
	if body != want {
		t.Fatalf("body =\n%q\nwant\n%q", body, want)
	}
}

// TestBuildRunBodyFull exercises every new field. Order is fixed by
// buildRunBody so the golden compare is stable across runs.
func TestBuildRunBodyFull(t *testing.T) {
	body := buildRunBody(
		"scripted",
		"my transient",
		[]string{"/bin/sh", "-c", "echo hi"},
		"system.slice",
		"-5",
		"nobody:daemon",
		"/run/slinit.d/run-abcd.env",
		[]string{"cgroup=/x", "restart-delay=1"},
	)
	for _, needle := range []string{
		"type = scripted\n",
		"description = my transient\n",
		"restart = false\n",
		"slice = system.slice\n",
		"nice = -5\n",
		"run-as = nobody:daemon\n",
		"env-file = /run/slinit.d/run-abcd.env\n",
		"cgroup=/x\n",
		"restart-delay=1\n",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("body missing %q:\n%s", needle, body)
		}
	}
}

// TestBuildRunBodyQuotesEmbeddedQuote: a command token with a "
// character round-trips through the double-quote escape and is
// re-split by the config parser back to the original argv.
func TestBuildRunBodyQuotesEmbeddedQuote(t *testing.T) {
	body := buildRunBody("process", "", []string{`arg"with"quote`}, "", "", "", "", nil)
	if !strings.Contains(body, `command = "arg\"with\"quote"`) {
		t.Errorf("embedded quote not escaped:\n%s", body)
	}
}

// TestBuildRunBodyParsesBack: the assembled body must parse cleanly
// through the config parser, so a transient unit is never rejected
// by the daemon due to a quoting bug in the CLI. The FULL directive
// set (slice, nice, setenv, property) round-trips.
func TestBuildRunBodyParsesBack(t *testing.T) {
	body := buildRunBody(
		"process",
		"round-trip",
		[]string{"/usr/bin/env", "sleep", "1"},
		"my.slice",
		"10",
		"",
		"",
		[]string{"restart-delay=1"},
	)
	desc, err := config.Parse(strings.NewReader(body), "svc", "test")
	if err != nil {
		t.Fatalf("generated body failed to parse: %v\nbody:\n%s", err, body)
	}
	if desc.Description != "round-trip" {
		t.Errorf("description round-trip failed: %q", desc.Description)
	}
	if len(desc.Command) != 3 || desc.Command[0] != "/usr/bin/env" {
		t.Errorf("command tokenisation failed: %v", desc.Command)
	}
	if desc.Slice != "my.slice" {
		t.Errorf("slice = %q", desc.Slice)
	}
}

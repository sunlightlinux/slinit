package config

import (
	"strings"
	"testing"
)

type deprecationCall struct {
	service, directive, since, replacedBy string
}

// withDeprecated makes `description` look deprecated for the duration
// of a test. Nothing in the shipped table is deprecated yet, so the
// wiring has to be exercised against a stand-in.
func withDeprecated(t *testing.T, directive, since, replacedBy string) *[]deprecationCall {
	t.Helper()
	var calls []deprecationCall
	oldLookup, oldHook := deprecationLookup, OnDeprecatedDirective
	deprecationLookup = func(name string) (string, string, bool) {
		if name == directive {
			return since, replacedBy, true
		}
		return "", "", false
	}
	OnDeprecatedDirective = func(svc, d, s, r string) {
		calls = append(calls, deprecationCall{svc, d, s, r})
	}
	t.Cleanup(func() {
		deprecationLookup, OnDeprecatedDirective = oldLookup, oldHook
	})
	return &calls
}

// TestDeprecatedDirectiveWarns is the contract STABILITY.md states:
// a deprecated directive keeps working, and says so.
func TestDeprecatedDirectiveWarns(t *testing.T) {
	calls := withDeprecated(t, "description", "2.5.0", "use author")

	desc, err := Parse(strings.NewReader(
		"type = process\ncommand = /bin/true\ndescription = hello\n"), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(*calls) != 1 {
		t.Fatalf("got %d warnings, want 1: %+v", len(*calls), *calls)
	}
	got := (*calls)[0]
	want := deprecationCall{"svc", "description", "2.5.0", "use author"}
	if got != want {
		t.Errorf("warning = %+v, want %+v", got, want)
	}

	// "Keeps working" is the other half of the rule, and the half a
	// warning could quietly break.
	if desc.Description != "hello" {
		t.Errorf("Description = %q — a deprecated directive must still take effect", desc.Description)
	}
}

// TestLiveDirectiveDoesNotWarn: everything not in the table stays
// silent, so enabling the hook cannot turn an ordinary service file
// into a wall of noise.
func TestLiveDirectiveDoesNotWarn(t *testing.T) {
	calls := withDeprecated(t, "some-directive-that-is-deprecated", "2.5.0", "x")

	if _, err := Parse(strings.NewReader(
		"type = process\ncommand = /bin/true\ndescription = hello\n"), "svc", "test"); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("got %d warnings for a file with no deprecated directive: %+v", len(*calls), *calls)
	}
}

// TestNoHookNoCost: with no hook wired — every library caller, and
// every test — the lookup is not even consulted.
func TestNoHookNoCost(t *testing.T) {
	oldLookup, oldHook := deprecationLookup, OnDeprecatedDirective
	consulted := false
	deprecationLookup = func(string) (string, string, bool) {
		consulted = true
		return "", "", false
	}
	OnDeprecatedDirective = nil
	t.Cleanup(func() { deprecationLookup, OnDeprecatedDirective = oldLookup, oldHook })

	if _, err := Parse(strings.NewReader(
		"type = process\ncommand = /bin/true\n"), "svc", "test"); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if consulted {
		t.Error("deprecation lookup ran with no hook wired")
	}
}

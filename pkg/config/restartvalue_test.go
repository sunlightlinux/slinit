package config

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestParseRestartOnSuccess(t *testing.T) {
	desc, err := Parse(strings.NewReader(
		"type = process\ncommand = /bin/true\nrestart = on-success\n"), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if desc.AutoRestart != service.RestartOnSuccess {
		t.Errorf("AutoRestart = %v, want on-success", desc.AutoRestart)
	}
}

func TestParseRestartValuesStillAccepted(t *testing.T) {
	for value, want := range map[string]service.AutoRestartMode{
		"yes":        service.RestartAlways,
		"true":       service.RestartAlways,
		"no":         service.RestartNever,
		"false":      service.RestartNever,
		"on-failure": service.RestartOnFailure,
		"on-success": service.RestartOnSuccess,
	} {
		desc, err := Parse(strings.NewReader(
			"type = process\ncommand = /bin/true\nrestart = "+value+"\n"), "svc", "f")
		if err != nil {
			t.Fatalf("restart = %s: %v", value, err)
		}
		if desc.AutoRestart != want {
			t.Errorf("restart = %s gave %v, want %v", value, desc.AutoRestart, want)
		}
	}
}

// The error message must list the new value, or an operator who
// mistypes it is told the wrong set of options.
func TestParseRestartInvalidListsOnSuccess(t *testing.T) {
	_, err := Parse(strings.NewReader(
		"type = process\ncommand = /bin/true\nrestart = sometimes\n"), "svc", "f")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "on-success") {
		t.Errorf("error does not mention on-success: %v", err)
	}
}

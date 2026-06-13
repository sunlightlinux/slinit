package config

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestParseFailureSuccessActionAndRebootArg(t *testing.T) {
	input := `
type = process
command = /bin/true
failure-action = reboot
success-action = poweroff
reboot-argument = kexec=1
`
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.FailureAction != service.ActionReboot {
		t.Errorf("FailureAction: got %v want reboot", desc.FailureAction)
	}
	if desc.SuccessAction != service.ActionPoweroff {
		t.Errorf("SuccessAction: got %v want poweroff", desc.SuccessAction)
	}
	if desc.RebootArgument != "kexec=1" {
		t.Errorf("RebootArgument: got %q want %q", desc.RebootArgument, "kexec=1")
	}
}

func TestParseUnknownActionRejected(t *testing.T) {
	input := "type = process\ncommand = /bin/true\nfailure-action = explode\n"
	if _, err := Parse(strings.NewReader(input), "svc", "test"); err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestParseActionDefaultsAreNone(t *testing.T) {
	input := "type = process\ncommand = /bin/true\n"
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.FailureAction != service.ActionNone || desc.SuccessAction != service.ActionNone {
		t.Errorf("defaults should be ActionNone, got failure=%v success=%v",
			desc.FailureAction, desc.SuccessAction)
	}
}

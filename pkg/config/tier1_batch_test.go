package config

import (
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// TestParseRefuseManualStart verifies the parser wires both yes/no
// forms into ServiceDescription.
func TestParseRefuseManualStart(t *testing.T) {
	desc, err := parseServiceContent(`
type = process
command = /bin/true
refuse-manual-start = yes
refuse-manual-stop = no
`, "")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !desc.RefuseManualStart {
		t.Error("refuse-manual-start = yes did not stick")
	}
	if desc.RefuseManualStop {
		t.Error("refuse-manual-stop = no unexpectedly true")
	}
}

// TestParseStopWhenUnneeded matches the parse for the sibling flag.
func TestParseStopWhenUnneeded(t *testing.T) {
	desc, err := parseServiceContent(`
type = process
command = /bin/true
stop-when-unneeded = yes
`, "")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !desc.StopWhenUnneeded {
		t.Error("stop-when-unneeded = yes did not stick")
	}
}

// TestParseRestartRandomizedDelay covers both duration and negative
// rejection.
func TestParseRestartRandomizedDelay(t *testing.T) {
	desc, err := parseServiceContent(`
type = process
command = /bin/true
restart-randomized-delay = 250ms
`, "")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.RestartRandomizedDelay != 250*time.Millisecond {
		t.Errorf("got %v, want 250ms", desc.RestartRandomizedDelay)
	}

	if _, err := parseServiceContent(`
type = process
command = /bin/true
restart-randomized-delay = -1s
`, ""); err == nil || !strings.Contains(err.Error(), "restart-randomized-delay") {
		t.Errorf("negative value should be rejected, got err=%v", err)
	}
}

// TestParseStartLimitAction covers all recognised action names + a
// bogus value that must be rejected.
func TestParseStartLimitAction(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want service.SystemAction
	}{
		{"none", service.ActionNone},
		{"reboot", service.ActionReboot},
		{"poweroff", service.ActionPoweroff},
		{"halt", service.ActionHalt},
	} {
		desc, err := parseServiceContent(`
type = process
command = /bin/true
start-limit-action = `+tc.val+`
`, "")
		if err != nil {
			t.Errorf("%s: parse: %v", tc.val, err)
			continue
		}
		if desc.StartLimitAction != tc.want {
			t.Errorf("%s: got %v, want %v", tc.val, desc.StartLimitAction, tc.want)
		}
	}
	if _, err := parseServiceContent(`
type = process
command = /bin/true
start-limit-action = flopulate
`, ""); err == nil {
		t.Error("unknown action must be rejected")
	}
}

// parseServiceContent is a small test helper that feeds a body through
// the parser without needing a file on disk.
func parseServiceContent(body, name string) (*ServiceDescription, error) {
	if name == "" {
		name = "tier1-batch-test"
	}
	return ParseWithArg(strings.NewReader(body), name, "", "")
}

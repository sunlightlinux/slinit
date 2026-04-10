package config

import (
	"strings"
	"testing"
)

func TestParseExtraCommand(t *testing.T) {
	input := `type = process
command = /bin/app
extra-command = checkconfig /usr/bin/app --check
`
	desc, err := Parse(strings.NewReader(input), "app", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmd, ok := desc.ExtraCommands["checkconfig"]
	if !ok {
		t.Fatal("expected ExtraCommands[checkconfig]")
	}
	if len(cmd) != 2 || cmd[0] != "/usr/bin/app" || cmd[1] != "--check" {
		t.Errorf("ExtraCommands[checkconfig] = %v", cmd)
	}
}

func TestParseExtraStartedCommand(t *testing.T) {
	input := `type = process
command = /bin/app
extra-started-command = rotate /usr/bin/app --rotate-logs
`
	desc, err := Parse(strings.NewReader(input), "app", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmd, ok := desc.ExtraStartedCommands["rotate"]
	if !ok {
		t.Fatal("expected ExtraStartedCommands[rotate]")
	}
	if len(cmd) != 2 || cmd[0] != "/usr/bin/app" || cmd[1] != "--rotate-logs" {
		t.Errorf("ExtraStartedCommands[rotate] = %v", cmd)
	}
}

func TestParseMultipleExtraCommands(t *testing.T) {
	input := `type = process
command = /bin/app
extra-command = checkconfig /usr/bin/app --check
extra-command = validate /usr/bin/app --validate
extra-started-command = reload /usr/bin/app --reload
`
	desc, err := Parse(strings.NewReader(input), "app", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(desc.ExtraCommands) != 2 {
		t.Errorf("expected 2 ExtraCommands, got %d", len(desc.ExtraCommands))
	}
	if _, ok := desc.ExtraCommands["checkconfig"]; !ok {
		t.Error("missing checkconfig")
	}
	if _, ok := desc.ExtraCommands["validate"]; !ok {
		t.Error("missing validate")
	}
	if len(desc.ExtraStartedCommands) != 1 {
		t.Errorf("expected 1 ExtraStartedCommands, got %d", len(desc.ExtraStartedCommands))
	}
}

func TestParseExtraCommandRequiresCommand(t *testing.T) {
	input := `type = process
command = /bin/app
extra-command = justname
`
	_, err := Parse(strings.NewReader(input), "app", "test-file")
	if err == nil {
		t.Fatal("expected error for extra-command with only action name")
	}
	if !strings.Contains(err.Error(), "requires an action name and a command") {
		t.Errorf("unexpected error: %v", err)
	}
}

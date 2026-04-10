package config

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestParseOutputLogger(t *testing.T) {
	input := `type = process
command = /bin/app
output-logger = logger -t myapp
`
	desc, err := Parse(strings.NewReader(input), "app", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.LogType != service.LogToCommand {
		t.Errorf("LogType = %v, want LogToCommand", desc.LogType)
	}
	if len(desc.OutputLogger) != 3 {
		t.Fatalf("OutputLogger = %v, want [logger -t myapp]", desc.OutputLogger)
	}
	if desc.OutputLogger[0] != "logger" || desc.OutputLogger[1] != "-t" || desc.OutputLogger[2] != "myapp" {
		t.Errorf("OutputLogger = %v", desc.OutputLogger)
	}
}

func TestParseErrorLogger(t *testing.T) {
	input := `type = process
command = /bin/app
output-logger = logger -t app-out
error-logger = logger -t app-err
`
	desc, err := Parse(strings.NewReader(input), "app", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.LogType != service.LogToCommand {
		t.Errorf("LogType = %v, want LogToCommand", desc.LogType)
	}
	if len(desc.ErrorLogger) != 3 || desc.ErrorLogger[2] != "app-err" {
		t.Errorf("ErrorLogger = %v, want [logger -t app-err]", desc.ErrorLogger)
	}
}

func TestParseOutputLoggerPlusEqual(t *testing.T) {
	input := `type = process
command = /bin/app
output-logger = logger
output-logger += -t myapp
`
	desc, err := Parse(strings.NewReader(input), "app", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// += appends to existing command
	if len(desc.OutputLogger) != 3 {
		t.Fatalf("OutputLogger = %v, want [logger -t myapp]", desc.OutputLogger)
	}
}

func TestParseOutputLoggerExplicitLogType(t *testing.T) {
	input := `type = process
command = /bin/app
log-type = command
output-logger = logger -t myapp
`
	desc, err := Parse(strings.NewReader(input), "app", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.LogType != service.LogToCommand {
		t.Errorf("LogType = %v, want LogToCommand", desc.LogType)
	}
}

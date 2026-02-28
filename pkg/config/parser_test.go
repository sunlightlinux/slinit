package config

import (
	"os"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestParseBasicService(t *testing.T) {
	input := `
# This is a comment
type = internal
description = A test service
`
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if desc.Name != "test" {
		t.Errorf("expected name 'test', got '%s'", desc.Name)
	}
	if desc.Type != service.TypeInternal {
		t.Errorf("expected type Internal, got %v", desc.Type)
	}
	if desc.Description != "A test service" {
		t.Errorf("expected description 'A test service', got '%s'", desc.Description)
	}
}

func TestParseProcessService(t *testing.T) {
	input := `
type = process
command = /usr/bin/myservice --flag
stop-command = /usr/bin/myservice --stop
working-dir = /var/lib/myservice
restart = on-failure
stop-timeout = 30
start-timeout = 60
term-signal = SIGTERM
`
	desc, err := Parse(strings.NewReader(input), "myservice", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if desc.Type != service.TypeProcess {
		t.Errorf("expected type Process, got %v", desc.Type)
	}
	if len(desc.Command) != 2 {
		t.Fatalf("expected 2 command parts, got %d: %v", len(desc.Command), desc.Command)
	}
	if desc.Command[0] != "/usr/bin/myservice" {
		t.Errorf("expected command[0] '/usr/bin/myservice', got '%s'", desc.Command[0])
	}
	if desc.Command[1] != "--flag" {
		t.Errorf("expected command[1] '--flag', got '%s'", desc.Command[1])
	}
	if desc.AutoRestart != service.RestartOnFailure {
		t.Errorf("expected RestartOnFailure, got %v", desc.AutoRestart)
	}
	if desc.StopTimeout.Seconds() != 30 {
		t.Errorf("expected stop timeout 30s, got %v", desc.StopTimeout)
	}
	if desc.StartTimeout.Seconds() != 60 {
		t.Errorf("expected start timeout 60s, got %v", desc.StartTimeout)
	}
	if desc.WorkingDir != "/var/lib/myservice" {
		t.Errorf("expected working dir '/var/lib/myservice', got '%s'", desc.WorkingDir)
	}
}

func TestParseDependencies(t *testing.T) {
	input := `
type = process
command = /usr/bin/myservice
depends-on: network
depends-on: syslog
waits-for: dbus
depends-ms: mount-fs
before: shutdown
after: early-boot
`
	desc, err := Parse(strings.NewReader(input), "myservice", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(desc.DependsOn) != 2 {
		t.Errorf("expected 2 depends-on, got %d", len(desc.DependsOn))
	}
	if desc.DependsOn[0] != "network" {
		t.Errorf("expected depends-on[0] 'network', got '%s'", desc.DependsOn[0])
	}
	if desc.DependsOn[1] != "syslog" {
		t.Errorf("expected depends-on[1] 'syslog', got '%s'", desc.DependsOn[1])
	}
	if len(desc.WaitsFor) != 1 || desc.WaitsFor[0] != "dbus" {
		t.Errorf("expected waits-for ['dbus'], got %v", desc.WaitsFor)
	}
	if len(desc.DependsMS) != 1 || desc.DependsMS[0] != "mount-fs" {
		t.Errorf("expected depends-ms ['mount-fs'], got %v", desc.DependsMS)
	}
	if len(desc.Before) != 1 || desc.Before[0] != "shutdown" {
		t.Errorf("expected before ['shutdown'], got %v", desc.Before)
	}
	if len(desc.After) != 1 || desc.After[0] != "early-boot" {
		t.Errorf("expected after ['early-boot'], got %v", desc.After)
	}
}

func TestParseOptions(t *testing.T) {
	input := `
type = process
command = /usr/bin/myservice
options = runs-on-console signal-process-only
`
	desc, err := Parse(strings.NewReader(input), "myservice", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !desc.Flags.RunsOnConsole {
		t.Error("expected RunsOnConsole to be true")
	}
	if !desc.Flags.SignalProcessOnly {
		t.Error("expected SignalProcessOnly to be true")
	}
	if desc.Flags.AlwaysChain {
		t.Error("expected AlwaysChain to be false")
	}
}

func TestParseOptionsAppend(t *testing.T) {
	input := `
type = process
command = /usr/bin/myservice
options = runs-on-console
options += always-chain
`
	desc, err := Parse(strings.NewReader(input), "myservice", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !desc.Flags.RunsOnConsole {
		t.Error("expected RunsOnConsole to be true")
	}
	if !desc.Flags.AlwaysChain {
		t.Error("expected AlwaysChain to be true")
	}
}

func TestParseUnknownSetting(t *testing.T) {
	input := `
type = process
command = /usr/bin/myservice
unknown-setting = value
`
	_, err := Parse(strings.NewReader(input), "myservice", "test-file")
	if err == nil {
		t.Fatal("expected error for unknown setting")
	}
	if !strings.Contains(err.Error(), "unknown setting") {
		t.Errorf("expected 'unknown setting' error, got: %v", err)
	}
}

func TestParseInvalidOperator(t *testing.T) {
	input := `
type = process
command = /usr/bin/myservice
depends-on = syslog
`
	_, err := Parse(strings.NewReader(input), "myservice", "test-file")
	if err == nil {
		t.Fatal("expected error for invalid operator")
	}
	if !strings.Contains(err.Error(), "invalid operator") {
		t.Errorf("expected 'invalid operator' error, got: %v", err)
	}
}

func TestParseQuotedCommand(t *testing.T) {
	input := `
type = process
command = /usr/bin/myservice "hello world" --flag
`
	desc, err := Parse(strings.NewReader(input), "myservice", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(desc.Command) != 3 {
		t.Fatalf("expected 3 parts, got %d: %v", len(desc.Command), desc.Command)
	}
	if desc.Command[1] != "hello world" {
		t.Errorf("expected command[1] 'hello world', got '%s'", desc.Command[1])
	}
}

func TestParseSignal(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{`type = process
command = /bin/true
term-signal = SIGTERM`, ""},
		{`type = process
command = /bin/true
term-signal = HUP`, ""},
		{`type = process
command = /bin/true
term-signal = 15`, ""},
	}

	for _, tt := range tests {
		_, err := Parse(strings.NewReader(tt.input), "test", "test-file")
		if tt.expected == "" && err != nil {
			t.Errorf("unexpected error for input %q: %v", tt.input, err)
		}
	}
}

func TestParseBoolValues(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{`type = process
command = /bin/true
smooth-recovery = yes`, false},
		{`type = process
command = /bin/true
smooth-recovery = true`, false},
		{`type = process
command = /bin/true
smooth-recovery = no`, false},
		{`type = process
command = /bin/true
smooth-recovery = invalid`, true},
	}

	for _, tt := range tests {
		_, err := Parse(strings.NewReader(tt.input), "test", "test-file")
		if (err != nil) != tt.wantErr {
			t.Errorf("Parse(%q): error = %v, wantErr = %v", tt.input, err, tt.wantErr)
		}
	}
}

func TestSplitCommand(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"/usr/bin/foo", []string{"/usr/bin/foo"}},
		{"/usr/bin/foo bar baz", []string{"/usr/bin/foo", "bar", "baz"}},
		{`/usr/bin/foo "hello world"`, []string{"/usr/bin/foo", "hello world"}},
		{`/usr/bin/foo 'hello world'`, []string{"/usr/bin/foo", "hello world"}},
		{`/usr/bin/foo hello\ world`, []string{"/usr/bin/foo", "hello world"}},
		{"", nil},
	}

	for _, tt := range tests {
		result := splitCommand(tt.input)
		if len(result) != len(tt.expected) {
			t.Errorf("splitCommand(%q): got %v, expected %v", tt.input, result, tt.expected)
			continue
		}
		for i := range result {
			if result[i] != tt.expected[i] {
				t.Errorf("splitCommand(%q)[%d]: got %q, expected %q", tt.input, i, result[i], tt.expected[i])
			}
		}
	}
}

func TestParseNice(t *testing.T) {
	input := `type = process
command = /bin/true
nice = 10
`
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if desc.Nice == nil || *desc.Nice != 10 {
		t.Errorf("Nice: got %v, expected 10", desc.Nice)
	}
}

func TestParseNiceInvalid(t *testing.T) {
	input := `type = process
command = /bin/true
nice = 25
`
	_, err := Parse(strings.NewReader(input), "test", "test-file")
	if err == nil {
		t.Fatal("expected error for nice=25")
	}
}

func TestParseOOMScoreAdj(t *testing.T) {
	input := `type = process
command = /bin/true
oom-score-adj = -500
`
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if desc.OOMScoreAdj == nil || *desc.OOMScoreAdj != -500 {
		t.Errorf("OOMScoreAdj: got %v, expected -500", desc.OOMScoreAdj)
	}
}

func TestParseIOPrioConfig(t *testing.T) {
	input := `type = process
command = /bin/true
ioprio = be:4
`
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if desc.IOPrio != "be:4" {
		t.Errorf("IOPrio: got %q, expected \"be:4\"", desc.IOPrio)
	}
}

func TestParseCgroup(t *testing.T) {
	input := `type = process
command = /bin/true
cgroup = /sys/fs/cgroup/myservice
`
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if desc.CgroupPath != "/sys/fs/cgroup/myservice" {
		t.Errorf("CgroupPath: got %q, expected \"/sys/fs/cgroup/myservice\"", desc.CgroupPath)
	}
}

func TestParseRlimit(t *testing.T) {
	input := `type = process
command = /bin/true
rlimit-nofile = 1024:4096
rlimit-core = unlimited
`
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if desc.RlimitNofile == nil || desc.RlimitNofile[0] != 1024 || desc.RlimitNofile[1] != 4096 {
		t.Errorf("RlimitNofile: got %v, expected [1024, 4096]", desc.RlimitNofile)
	}
	maxVal := uint64(^uint64(0))
	if desc.RlimitCore == nil || desc.RlimitCore[0] != maxVal || desc.RlimitCore[1] != maxVal {
		t.Errorf("RlimitCore: got %v, expected [unlimited, unlimited]", desc.RlimitCore)
	}
}

func TestParseNoNewPrivs(t *testing.T) {
	input := `type = process
command = /bin/true
options = no-new-privs
`
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if !desc.NoNewPrivs {
		t.Error("NoNewPrivs: expected true")
	}
}

func TestParseUnmaskIntr(t *testing.T) {
	input := `type = process
command = /bin/sh
options = runs-on-console unmask-intr
`
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if !desc.Flags.RunsOnConsole {
		t.Error("RunsOnConsole: expected true")
	}
	if !desc.Flags.UnmaskIntr {
		t.Error("UnmaskIntr: expected true")
	}
}

func TestParseStartsRWFS(t *testing.T) {
	input := `type = scripted
command = /bin/mount-rw
options = starts-rwfs
`
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if !desc.Flags.RWReady {
		t.Error("RWReady: expected true")
	}
}

func TestParseStartsLog(t *testing.T) {
	input := `type = process
command = /usr/sbin/syslogd
options = starts-log
`
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if !desc.Flags.LogReady {
		t.Error("LogReady: expected true")
	}
}

func TestParseCapabilities(t *testing.T) {
	input := `type = process
command = /bin/true
capabilities = cap_net_bind_service,cap_sys_admin
`
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if desc.Capabilities != "cap_net_bind_service,cap_sys_admin" {
		t.Errorf("Capabilities: got %q", desc.Capabilities)
	}
}

func TestParseSecurebits(t *testing.T) {
	input := `type = process
command = /bin/true
securebits = noroot keep-caps
`
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if desc.Securebits != "noroot keep-caps" {
		t.Errorf("Securebits: got %q", desc.Securebits)
	}
}

func TestExpandEnvVars(t *testing.T) {
	// Set test environment variables
	os.Setenv("SLINIT_TEST_VAR", "hello")
	os.Setenv("SLINIT_TEST_DIR", "/opt/myapp")
	defer os.Unsetenv("SLINIT_TEST_VAR")
	defer os.Unsetenv("SLINIT_TEST_DIR")

	tests := []struct {
		input    string
		expected string
	}{
		// No variables
		{"no vars here", "no vars here"},
		{"", ""},

		// Simple $VAR
		{"$SLINIT_TEST_VAR", "hello"},
		{"prefix-$SLINIT_TEST_VAR-suffix", "prefix-hello-suffix"},
		{"$SLINIT_TEST_DIR/bin/app", "/opt/myapp/bin/app"},

		// Braced ${VAR}
		{"${SLINIT_TEST_VAR}", "hello"},
		{"${SLINIT_TEST_VAR}World", "helloWorld"},
		{"${SLINIT_TEST_DIR}/bin", "/opt/myapp/bin"},

		// Escaped $$
		{"$$", "$"},
		{"cost: $$5", "cost: $5"},
		{"$$SLINIT_TEST_VAR", "$SLINIT_TEST_VAR"},

		// Unset variable → empty
		{"$SLINIT_UNSET_VAR", ""},
		{"${SLINIT_UNSET_VAR}", ""},

		// Multiple variables
		{"$SLINIT_TEST_DIR/$SLINIT_TEST_VAR", "/opt/myapp/hello"},

		// Trailing dollar
		{"path$", "path$"},

		// Dollar followed by non-var char
		{"$!foo", "$!foo"},

		// Unclosed brace
		{"${SLINIT_TEST_VAR", "${SLINIT_TEST_VAR"},
	}

	for _, tt := range tests {
		got := expandEnvVars(tt.input)
		if got != tt.expected {
			t.Errorf("expandEnvVars(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestExpandEnvVarsInParsedFields(t *testing.T) {
	os.Setenv("SLINIT_APP_DIR", "/opt/myapp")
	os.Setenv("SLINIT_LOG_DIR", "/var/log")
	defer os.Unsetenv("SLINIT_APP_DIR")
	defer os.Unsetenv("SLINIT_LOG_DIR")

	input := `type = process
command = $SLINIT_APP_DIR/bin/server --port 8080
stop-command = $SLINIT_APP_DIR/bin/server --stop
working-dir = ${SLINIT_APP_DIR}
logfile = $SLINIT_LOG_DIR/myapp.log
pid-file = $SLINIT_APP_DIR/run/app.pid
socket-listen = $SLINIT_APP_DIR/run/app.sock
env-file = ${SLINIT_APP_DIR}/env
`
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if desc.Command[0] != "/opt/myapp/bin/server" {
		t.Errorf("Command[0]: got %q, want %q", desc.Command[0], "/opt/myapp/bin/server")
	}
	if desc.StopCommand[0] != "/opt/myapp/bin/server" {
		t.Errorf("StopCommand[0]: got %q, want %q", desc.StopCommand[0], "/opt/myapp/bin/server")
	}
	if desc.WorkingDir != "/opt/myapp" {
		t.Errorf("WorkingDir: got %q, want %q", desc.WorkingDir, "/opt/myapp")
	}
	if desc.LogFile != "/var/log/myapp.log" {
		t.Errorf("LogFile: got %q, want %q", desc.LogFile, "/var/log/myapp.log")
	}
	if desc.PIDFile != "/opt/myapp/run/app.pid" {
		t.Errorf("PIDFile: got %q, want %q", desc.PIDFile, "/opt/myapp/run/app.pid")
	}
	if desc.SocketPath != "/opt/myapp/run/app.sock" {
		t.Errorf("SocketPath: got %q, want %q", desc.SocketPath, "/opt/myapp/run/app.sock")
	}
	if desc.EnvFile != "/opt/myapp/env" {
		t.Errorf("EnvFile: got %q, want %q", desc.EnvFile, "/opt/myapp/env")
	}
}

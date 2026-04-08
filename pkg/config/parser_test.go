package config

import (
	"os"
	"strings"
	"testing"
	"time"

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

func TestParseCPUAffinity(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []uint
	}{
		{"single", "cpu-affinity = 0\n", []uint{0}},
		{"list spaces", "cpu-affinity = 0 1 2 3\n", []uint{0, 1, 2, 3}},
		{"list commas", "cpu-affinity = 0,2,4\n", []uint{0, 2, 4}},
		{"range", "cpu-affinity = 0-3\n", []uint{0, 1, 2, 3}},
		{"mixed", "cpu-affinity = 0-2 8-9\n", []uint{0, 1, 2, 8, 9}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := "type = process\ncommand = /bin/true\n" + tt.input
			desc, err := Parse(strings.NewReader(input), "test", "test-file")
			if err != nil {
				t.Fatalf("Parse failed: %v", err)
			}
			if len(desc.CPUAffinity) != len(tt.want) {
				t.Fatalf("CPUAffinity: got %v, want %v", desc.CPUAffinity, tt.want)
			}
			for i, c := range desc.CPUAffinity {
				if c != tt.want[i] {
					t.Errorf("CPUAffinity[%d]: got %d, want %d", i, c, tt.want[i])
				}
			}
		})
	}
}

func TestParseCPUAffinityInvalid(t *testing.T) {
	input := "type = process\ncommand = /bin/true\ncpu-affinity = abc\n"
	_, err := Parse(strings.NewReader(input), "test", "test-file")
	if err == nil {
		t.Fatal("expected error for invalid cpu-affinity")
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
		got := expandEnvVars(tt.input, nil)
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

func TestExpandEnvVarsDefault(t *testing.T) {
	// Test ${VAR:-default} and ${VAR:+alt} operators
	os.Setenv("SLINIT_TEST_SET", "value123")
	os.Unsetenv("SLINIT_TEST_UNSET")
	os.Setenv("SLINIT_TEST_EMPTY", "")
	defer os.Unsetenv("SLINIT_TEST_SET")
	defer os.Unsetenv("SLINIT_TEST_EMPTY")

	tests := []struct {
		input    string
		expected string
	}{
		// ${VAR:-default} — use default if unset or empty
		{"${SLINIT_TEST_SET:-fallback}", "value123"},
		{"${SLINIT_TEST_UNSET:-fallback}", "fallback"},
		{"${SLINIT_TEST_EMPTY:-fallback}", "fallback"},
		{"prefix-${SLINIT_TEST_UNSET:-/default/path}-suffix", "prefix-/default/path-suffix"},

		// ${VAR:+alt} — use alt if set and non-empty
		{"${SLINIT_TEST_SET:+alt}", "alt"},
		{"${SLINIT_TEST_UNSET:+alt}", ""},
		{"${SLINIT_TEST_EMPTY:+alt}", ""},
		{"pre-${SLINIT_TEST_SET:+YES}-post", "pre-YES-post"},
		{"pre-${SLINIT_TEST_UNSET:+YES}-post", "pre--post"},

		// Combined with plain vars
		{"$SLINIT_TEST_SET-${SLINIT_TEST_UNSET:-default}", "value123-default"},
	}

	for _, tt := range tests {
		got := expandEnvVars(tt.input, nil)
		if got != tt.expected {
			t.Errorf("expandEnvVars(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestExpandEnvVarsNonColonOp(t *testing.T) {
	// Test ${VAR-default} and ${VAR+alt} (without colon) — check unset only, not empty
	os.Setenv("SLINIT_TEST_SET", "value123")
	os.Unsetenv("SLINIT_TEST_UNSET")
	os.Setenv("SLINIT_TEST_EMPTY", "")
	defer os.Unsetenv("SLINIT_TEST_SET")
	defer os.Unsetenv("SLINIT_TEST_EMPTY")

	tests := []struct {
		input    string
		expected string
	}{
		// ${VAR-default} — use default only if unset (empty is OK)
		{"${SLINIT_TEST_SET-fallback}", "value123"},
		{"${SLINIT_TEST_UNSET-fallback}", "fallback"},
		{"${SLINIT_TEST_EMPTY-fallback}", ""},           // empty is set, so no fallback
		{"pre-${SLINIT_TEST_UNSET-/path}-suf", "pre-/path-suf"},

		// ${VAR+alt} — use alt if set (even if empty)
		{"${SLINIT_TEST_SET+alt}", "alt"},
		{"${SLINIT_TEST_UNSET+alt}", ""},                // unset → no alt
		{"${SLINIT_TEST_EMPTY+alt}", "alt"},             // empty but set → alt
		{"pre-${SLINIT_TEST_EMPTY+YES}-post", "pre-YES-post"},
		{"pre-${SLINIT_TEST_UNSET+YES}-post", "pre--post"},

		// Mix colon and non-colon in same string
		{"${SLINIT_TEST_EMPTY:-C}/${SLINIT_TEST_EMPTY-N}", "C/"},
		{"${SLINIT_TEST_EMPTY:+C}/${SLINIT_TEST_EMPTY+N}", "/N"},
	}

	for _, tt := range tests {
		got := expandEnvVars(tt.input, nil)
		if got != tt.expected {
			t.Errorf("expandEnvVars(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestExpandEnvVarsNonColonServiceArg(t *testing.T) {
	// Test ${1-default} and ${1+alt} with service argument
	arg := "myarg"

	tests := []struct {
		input    string
		arg      *string
		expected string
	}{
		{"${1-fallback}", &arg, "myarg"},
		{"${1-fallback}", nil, "fallback"},
		{"${1+alt}", &arg, "alt"},
		{"${1+alt}", nil, ""},
	}

	for _, tt := range tests {
		got := expandEnvVars(tt.input, tt.arg)
		if got != tt.expected {
			t.Errorf("expandEnvVars(%q, arg=%v) = %q, want %q", tt.input, tt.arg, got, tt.expected)
		}
	}
}

func TestCommandPlusEqual(t *testing.T) {
	input := `type = process
command = /usr/bin/myapp
command += --verbose
command += --config /etc/myapp.conf
`
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	expected := []string{"/usr/bin/myapp", "--verbose", "--config", "/etc/myapp.conf"}
	if len(desc.Command) != len(expected) {
		t.Fatalf("Command length: got %d, want %d; command=%v", len(desc.Command), len(expected), desc.Command)
	}
	for i, want := range expected {
		if desc.Command[i] != want {
			t.Errorf("Command[%d]: got %q, want %q", i, desc.Command[i], want)
		}
	}
}

func TestStopCommandPlusEqual(t *testing.T) {
	input := `type = process
command = /usr/bin/myapp
stop-command = /usr/bin/myapp --stop
stop-command += --graceful
`
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	expected := []string{"/usr/bin/myapp", "--stop", "--graceful"}
	if len(desc.StopCommand) != len(expected) {
		t.Fatalf("StopCommand length: got %d, want %d; cmd=%v", len(desc.StopCommand), len(expected), desc.StopCommand)
	}
	for i, want := range expected {
		if desc.StopCommand[i] != want {
			t.Errorf("StopCommand[%d]: got %q, want %q", i, desc.StopCommand[i], want)
		}
	}
}

func TestCommandPlusEqualReplace(t *testing.T) {
	// Verify that = after += replaces everything
	input := `type = process
command = /usr/bin/old
command += --flag
command = /usr/bin/new
`
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(desc.Command) != 1 || desc.Command[0] != "/usr/bin/new" {
		t.Errorf("Command after replace: got %v, want [/usr/bin/new]", desc.Command)
	}
}

func TestLoadOptionsExportPasswdVars(t *testing.T) {
	input := `type = process
command = /usr/bin/app
load-options = export-passwd-vars
`
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if !desc.ExportPasswdVars {
		t.Error("ExportPasswdVars should be true")
	}
	if desc.ExportServiceName {
		t.Error("ExportServiceName should be false")
	}
}

func TestLoadOptionsExportServiceName(t *testing.T) {
	input := `type = process
command = /usr/bin/app
load-options = export-service-name
`
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if !desc.ExportServiceName {
		t.Error("ExportServiceName should be true")
	}
	if desc.ExportPasswdVars {
		t.Error("ExportPasswdVars should be false")
	}
}

func TestLoadOptionsMultiple(t *testing.T) {
	input := `type = process
command = /usr/bin/app
load-options = export-passwd-vars export-service-name sub-vars
`
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if !desc.ExportPasswdVars {
		t.Error("ExportPasswdVars should be true")
	}
	if !desc.ExportServiceName {
		t.Error("ExportServiceName should be true")
	}
}

func TestParseInittabSettings(t *testing.T) {
	input := `type = process
command = /sbin/getty 38400 tty1
inittab-id = 1
inittab-line = tty1
`
	desc, err := Parse(strings.NewReader(input), "getty", "test-file")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if desc.InittabID != "1" {
		t.Errorf("InittabID: got %q, want %q", desc.InittabID, "1")
	}
	if desc.InittabLine != "tty1" {
		t.Errorf("InittabLine: got %q, want %q", desc.InittabLine, "tty1")
	}
}

// --- @include / @include-opt tests ---

func TestIncludeBasic(t *testing.T) {
	dir := t.TempDir()

	// Write included fragment
	fragPath := dir + "/common.conf"
	os.WriteFile(fragPath, []byte("description = included desc\n"), 0644)

	// Write main service file
	mainPath := dir + "/test-svc"
	mainContent := "type = internal\n@include " + fragPath + "\n"
	os.WriteFile(mainPath, []byte(mainContent), 0644)

	f, _ := os.Open(mainPath)
	defer f.Close()

	desc, err := Parse(f, "test-svc", mainPath)
	if err != nil {
		t.Fatalf("parse with include failed: %v", err)
	}
	if desc.Type != service.TypeInternal {
		t.Errorf("expected type Internal, got %v", desc.Type)
	}
	if desc.Description != "included desc" {
		t.Errorf("expected description 'included desc', got %q", desc.Description)
	}
}

func TestIncludeRelativePath(t *testing.T) {
	dir := t.TempDir()

	// Write fragment in same directory
	os.WriteFile(dir+"/extra.conf", []byte("description = relative include\n"), 0644)

	mainPath := dir + "/test-svc"
	mainContent := "type = internal\n@include extra.conf\n"
	os.WriteFile(mainPath, []byte(mainContent), 0644)

	f, _ := os.Open(mainPath)
	defer f.Close()

	desc, err := Parse(f, "test-svc", mainPath)
	if err != nil {
		t.Fatalf("parse with relative include failed: %v", err)
	}
	if desc.Description != "relative include" {
		t.Errorf("expected 'relative include', got %q", desc.Description)
	}
}

func TestIncludeOptMissing(t *testing.T) {
	dir := t.TempDir()

	mainPath := dir + "/test-svc"
	mainContent := "type = internal\n@include-opt /nonexistent/file.conf\ndescription = still works\n"
	os.WriteFile(mainPath, []byte(mainContent), 0644)

	f, _ := os.Open(mainPath)
	defer f.Close()

	desc, err := Parse(f, "test-svc", mainPath)
	if err != nil {
		t.Fatalf("parse with include-opt of missing file should not fail: %v", err)
	}
	if desc.Description != "still works" {
		t.Errorf("expected 'still works', got %q", desc.Description)
	}
}

func TestIncludeMissingFails(t *testing.T) {
	dir := t.TempDir()

	mainPath := dir + "/test-svc"
	mainContent := "type = internal\n@include /nonexistent/file.conf\n"
	os.WriteFile(mainPath, []byte(mainContent), 0644)

	f, _ := os.Open(mainPath)
	defer f.Close()

	_, err := Parse(f, "test-svc", mainPath)
	if err == nil {
		t.Fatal("expected error for missing include file, got nil")
	}
}

func TestIncludeNested(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(dir+"/level2.conf", []byte("description = deep\n"), 0644)
	os.WriteFile(dir+"/level1.conf", []byte("@include "+dir+"/level2.conf\n"), 0644)

	mainPath := dir + "/test-svc"
	mainContent := "type = internal\n@include " + dir + "/level1.conf\n"
	os.WriteFile(mainPath, []byte(mainContent), 0644)

	f, _ := os.Open(mainPath)
	defer f.Close()

	desc, err := Parse(f, "test-svc", mainPath)
	if err != nil {
		t.Fatalf("nested include failed: %v", err)
	}
	if desc.Description != "deep" {
		t.Errorf("expected 'deep', got %q", desc.Description)
	}
}

func TestIncludeDepthLimit(t *testing.T) {
	dir := t.TempDir()

	// Create chain: file0 -> file1 -> ... -> file11 (exceeds maxIncludeDepth=10)
	for i := 11; i > 0; i-- {
		next := dir + "/" + strings.Replace("fileN", "N", string(rune('a'+i)), 1) + ".conf"
		content := "@include " + next + "\n"
		if i == 11 {
			content = "description = unreachable\n"
		}
		os.WriteFile(dir+"/"+strings.Replace("fileN", "N", string(rune('a'+i-1)), 1)+".conf", []byte(content), 0644)
	}
	os.WriteFile(dir+"/filel.conf", []byte("description = unreachable\n"), 0644)

	// Simpler approach: create a circular include
	os.WriteFile(dir+"/circ-a.conf", []byte("@include "+dir+"/circ-b.conf\n"), 0644)
	os.WriteFile(dir+"/circ-b.conf", []byte("@include "+dir+"/circ-a.conf\n"), 0644)

	mainPath := dir + "/test-svc"
	mainContent := "type = internal\n@include " + dir + "/circ-a.conf\n"
	os.WriteFile(mainPath, []byte(mainContent), 0644)

	f, _ := os.Open(mainPath)
	defer f.Close()

	_, err := Parse(f, "test-svc", mainPath)
	if err == nil {
		t.Fatal("expected error for circular include, got nil")
	}
	if !strings.Contains(err.Error(), "nesting depth") {
		t.Errorf("expected depth limit error, got: %v", err)
	}
}

func TestIncludeOverride(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(dir+"/override.conf", []byte("description = overridden\n"), 0644)

	mainPath := dir + "/test-svc"
	mainContent := "type = internal\ndescription = original\n@include " + dir + "/override.conf\n"
	os.WriteFile(mainPath, []byte(mainContent), 0644)

	f, _ := os.Open(mainPath)
	defer f.Close()

	desc, err := Parse(f, "test-svc", mainPath)
	if err != nil {
		t.Fatalf("include override failed: %v", err)
	}
	if desc.Description != "overridden" {
		t.Errorf("expected 'overridden', got %q", desc.Description)
	}
}

func TestIncludeAdditive(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(dir+"/deps.conf", []byte("depends-on: extra-dep\n"), 0644)

	mainPath := dir + "/test-svc"
	mainContent := "type = internal\ndepends-on: main-dep\n@include " + dir + "/deps.conf\n"
	os.WriteFile(mainPath, []byte(mainContent), 0644)

	f, _ := os.Open(mainPath)
	defer f.Close()

	desc, err := Parse(f, "test-svc", mainPath)
	if err != nil {
		t.Fatalf("include additive failed: %v", err)
	}
	if len(desc.DependsOn) != 2 {
		t.Fatalf("expected 2 deps, got %d: %v", len(desc.DependsOn), desc.DependsOn)
	}
	if desc.DependsOn[0] != "main-dep" || desc.DependsOn[1] != "extra-dep" {
		t.Errorf("unexpected deps: %v", desc.DependsOn)
	}
}

func TestUnknownDirective(t *testing.T) {
	input := "type = internal\n@unknown stuff\n"
	_, err := Parse(strings.NewReader(input), "test", "test-file")
	if err == nil {
		t.Fatal("expected error for unknown directive")
	}
}

func TestWordSplitExpansion(t *testing.T) {
	os.Setenv("WSPLIT_ARGS", "arg1 arg2  arg3")
	os.Setenv("WSPLIT_EMPTY", "")
	os.Setenv("WSPLIT_SINGLE", "one")
	defer os.Unsetenv("WSPLIT_ARGS")
	defer os.Unsetenv("WSPLIT_EMPTY")
	defer os.Unsetenv("WSPLIT_SINGLE")

	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			"basic word-split",
			"type = process\ncommand = /bin/test $/WSPLIT_ARGS\n",
			[]string{"/bin/test", "arg1", "arg2", "arg3"},
		},
		{
			"word-split with braces",
			"type = process\ncommand = /bin/test $/{WSPLIT_ARGS}\n",
			[]string{"/bin/test", "arg1", "arg2", "arg3"},
		},
		{
			"word-split empty collapses",
			"type = process\ncommand = /bin/test $/WSPLIT_EMPTY foo\n",
			[]string{"/bin/test", "foo"},
		},
		{
			"word-split single value",
			"type = process\ncommand = /bin/test $/WSPLIT_SINGLE\n",
			[]string{"/bin/test", "one"},
		},
		{
			"non-split preserves spaces as one arg",
			"type = process\ncommand = /bin/test \"$WSPLIT_ARGS\"\n",
			[]string{"/bin/test", "arg1 arg2  arg3"},
		},
		{
			"mixed split and non-split",
			"type = process\ncommand = /bin/test prefix$/WSPLIT_ARGS suffix\n",
			[]string{"/bin/test", "prefixarg1", "arg2", "arg3", "suffix"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desc, err := Parse(strings.NewReader(tt.input), "test", "test-file")
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			if len(desc.Command) != len(tt.expected) {
				t.Fatalf("command args: got %v, want %v", desc.Command, tt.expected)
			}
			for i, want := range tt.expected {
				if desc.Command[i] != want {
					t.Errorf("arg[%d]: got %q, want %q", i, desc.Command[i], want)
				}
			}
		})
	}
}

func TestMetaDirectiveIgnored(t *testing.T) {
	input := "type = internal\n@meta enable-via foo\n@meta\ncommand = /bin/true\n"
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("@meta should be silently ignored: %v", err)
	}
	if desc.Type != service.TypeInternal {
		t.Fatalf("expected internal, got %v", desc.Type)
	}
	// enable-via should be parsed
	if desc.EnableVia != "foo" {
		t.Fatalf("expected EnableVia='foo', got %q", desc.EnableVia)
	}
}

func TestMetaEnableViaEmpty(t *testing.T) {
	input := "type = internal\n@meta unknown-directive bar\ncommand = /bin/true\n"
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("unknown @meta should be silently ignored: %v", err)
	}
	if desc.EnableVia != "" {
		t.Fatalf("expected empty EnableVia, got %q", desc.EnableVia)
	}
}

func TestServiceArgSubstitution(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		arg      string
		expected []string // expected Command
	}{
		{
			"basic $1 substitution",
			"type = process\ncommand = /bin/echo $1\n",
			"hello",
			[]string{"/bin/echo", "hello"},
		},
		{
			"${1} substitution",
			"type = process\ncommand = /bin/echo ${1}\n",
			"world",
			[]string{"/bin/echo", "world"},
		},
		{
			"$1 in middle of word",
			"type = process\ncommand = /bin/test prefix$1suffix\n",
			"mid",
			[]string{"/bin/test", "prefixmidsuffix"},
		},
		{
			"${1:-default} with arg",
			"type = process\ncommand = /bin/echo ${1:-fallback}\n",
			"actual",
			[]string{"/bin/echo", "actual"},
		},
		{
			"${1:-default} without arg value (empty)",
			"type = process\ncommand = /bin/echo ${1:-fallback}\n",
			"",
			[]string{"/bin/echo", "fallback"},
		},
		{
			"${1:+alt} with arg",
			"type = process\ncommand = /bin/echo ${1:+present}\n",
			"something",
			[]string{"/bin/echo", "present"},
		},
		{
			"${1:+alt} with empty arg",
			"type = process\ncommand = /bin/echo ${1:+present}\n",
			"",
			[]string{"/bin/echo"},
		},
		{
			"$/1 word-split",
			"type = process\ncommand = /bin/test $/1\n",
			"arg1 arg2 arg3",
			[]string{"/bin/test", "arg1", "arg2", "arg3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desc, err := ParseWithArg(strings.NewReader(tt.input), "test@"+tt.arg, "test-file", tt.arg)
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			if len(desc.Command) != len(tt.expected) {
				t.Fatalf("command: got %v, want %v", desc.Command, tt.expected)
			}
			for i, want := range tt.expected {
				if desc.Command[i] != want {
					t.Errorf("arg[%d]: got %q, want %q", i, desc.Command[i], want)
				}
			}
		})
	}
}

func TestServiceArgInDependencies(t *testing.T) {
	input := "type = process\ncommand = /bin/true\ndepends-on : base-$1\nwaits-for : opt-${1}\n"
	desc, err := ParseWithArg(strings.NewReader(input), "svc@myarg", "test-file", "myarg")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(desc.DependsOn) != 1 || desc.DependsOn[0] != "base-myarg" {
		t.Errorf("depends-on: got %v, want [base-myarg]", desc.DependsOn)
	}
	if len(desc.WaitsFor) != 1 || desc.WaitsFor[0] != "opt-myarg" {
		t.Errorf("waits-for: got %v, want [opt-myarg]", desc.WaitsFor)
	}
}

func TestServiceArgNoArgNil(t *testing.T) {
	// Without arg, $1 expands to empty string
	input := "type = process\ncommand = /bin/echo $1\n"
	desc, err := Parse(strings.NewReader(input), "test", "test-file")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(desc.Command) != 1 || desc.Command[0] != "/bin/echo" {
		t.Errorf("command: got %v, want [/bin/echo]", desc.Command)
	}
}

func TestTemplateLoaderNameSplit(t *testing.T) {
	// Test that findAndParse splits name@arg and looks for base name file
	dir := t.TempDir()
	// Create a template service file with base name "mysvc"
	content := "type = process\ncommand = /bin/run $1\n"
	if err := os.WriteFile(dir+"/mysvc", []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	set := service.NewServiceSet(nil)
	loader := NewDirLoader(set, []string{dir})

	desc, _, err := loader.findAndParse("mysvc@instance1")
	if err != nil {
		t.Fatalf("findAndParse failed: %v", err)
	}
	if desc.Name != "mysvc@instance1" {
		t.Errorf("name: got %q, want 'mysvc@instance1'", desc.Name)
	}
	if len(desc.Command) != 2 || desc.Command[1] != "instance1" {
		t.Errorf("command: got %v, want [/bin/run instance1]", desc.Command)
	}
}

func TestParseCronSettings(t *testing.T) {
	input := `
type = process
command = /bin/mydaemon
cron-command = /usr/bin/cleanup --all
cron-interval = 5m
cron-delay = 30s
cron-on-error = stop
`
	desc, err := Parse(strings.NewReader(input), "cron-svc", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(desc.CronCommand) != 2 || desc.CronCommand[0] != "/usr/bin/cleanup" {
		t.Errorf("CronCommand = %v, want [/usr/bin/cleanup --all]", desc.CronCommand)
	}
	if desc.CronInterval.Minutes() != 5 {
		t.Errorf("CronInterval = %v, want 5m", desc.CronInterval)
	}
	if desc.CronDelay.Seconds() != 30 {
		t.Errorf("CronDelay = %v, want 30s", desc.CronDelay)
	}
	if desc.CronOnError != "stop" {
		t.Errorf("CronOnError = %q, want %q", desc.CronOnError, "stop")
	}
}

func TestParseCronIntervalSeconds(t *testing.T) {
	input := `
type = process
command = /bin/mydaemon
cron-command = /usr/bin/task
cron-interval = 300
`
	desc, err := Parse(strings.NewReader(input), "cron-sec", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc.CronInterval.Seconds() != 300 {
		t.Errorf("CronInterval = %v, want 300s", desc.CronInterval)
	}
}

func TestParseCronOnErrorInvalid(t *testing.T) {
	input := `
type = process
command = /bin/mydaemon
cron-on-error = restart
`
	_, err := Parse(strings.NewReader(input), "bad-cron", "test-file")
	if err == nil {
		t.Fatal("expected error for invalid cron-on-error")
	}
}

func TestParseCronCommandAppend(t *testing.T) {
	input := `
type = process
command = /bin/mydaemon
cron-command = /usr/bin/task
cron-command += --verbose
`
	desc, err := Parse(strings.NewReader(input), "append-cron", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(desc.CronCommand) != 2 || desc.CronCommand[1] != "--verbose" {
		t.Errorf("CronCommand = %v, want [/usr/bin/task --verbose]", desc.CronCommand)
	}
}

func TestParseVTTYSettings(t *testing.T) {
	input := `type = process
command = /bin/myapp
vtty = true
vtty-scrollback = 131072
`
	desc, err := Parse(strings.NewReader(input), "vtty-svc", "test")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !desc.VTTYEnabled {
		t.Error("expected VTTYEnabled = true")
	}
	if desc.VTTYScrollback != 131072 {
		t.Errorf("expected VTTYScrollback = 131072, got %d", desc.VTTYScrollback)
	}
}

func TestParseVTTYDefault(t *testing.T) {
	input := `type = process
command = /bin/myapp
vtty = true
`
	desc, err := Parse(strings.NewReader(input), "vtty-def", "test")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !desc.VTTYEnabled {
		t.Error("expected VTTYEnabled = true")
	}
	if desc.VTTYScrollback != 0 {
		t.Errorf("expected default VTTYScrollback = 0 (uses built-in default), got %d", desc.VTTYScrollback)
	}
}

func TestParseNamespaceAll(t *testing.T) {
	input := `type = process
command = /bin/isolated
namespace-pid = true
namespace-mount = yes
namespace-net = true
namespace-uts = yes
namespace-ipc = true
namespace-user = yes
namespace-cgroup = true
`
	desc, err := Parse(strings.NewReader(input), "ns-all", "test")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	checks := []struct {
		name string
		got  bool
	}{
		{"NamespacePID", desc.NamespacePID},
		{"NamespaceMount", desc.NamespaceMount},
		{"NamespaceNet", desc.NamespaceNet},
		{"NamespaceUTS", desc.NamespaceUTS},
		{"NamespaceIPC", desc.NamespaceIPC},
		{"NamespaceUser", desc.NamespaceUser},
		{"NamespaceCgroup", desc.NamespaceCgroup},
	}
	for _, c := range checks {
		if !c.got {
			t.Errorf("expected %s = true", c.name)
		}
	}
}

func TestParseNamespacePartial(t *testing.T) {
	input := `type = process
command = /bin/app
namespace-pid = true
namespace-mount = true
`
	desc, err := Parse(strings.NewReader(input), "ns-partial", "test")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !desc.NamespacePID {
		t.Error("expected NamespacePID = true")
	}
	if !desc.NamespaceMount {
		t.Error("expected NamespaceMount = true")
	}
	if desc.NamespaceNet {
		t.Error("expected NamespaceNet = false")
	}
	if desc.NamespaceUser {
		t.Error("expected NamespaceUser = false")
	}
}

func TestParseNamespaceDisabled(t *testing.T) {
	input := `type = process
command = /bin/app
namespace-pid = false
`
	desc, err := Parse(strings.NewReader(input), "ns-off", "test")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if desc.NamespacePID {
		t.Error("expected NamespacePID = false")
	}
}

func TestParseIDMappingValid(t *testing.T) {
	m, err := ParseIDMapping("0:1000:65536")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ContainerID != 0 || m.HostID != 1000 || m.Size != 65536 {
		t.Errorf("got %+v, want {0 1000 65536}", m)
	}
}

func TestParseIDMappingSpaces(t *testing.T) {
	m, err := ParseIDMapping(" 0 : 500 : 1 ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ContainerID != 0 || m.HostID != 500 || m.Size != 1 {
		t.Errorf("got %+v", m)
	}
}

func TestParseIDMappingInvalid(t *testing.T) {
	tests := []string{
		"",
		"0:1000",
		"0:1000:0",      // size must be > 0
		"-1:1000:1",     // negative container id
		"abc:1000:1",    // non-numeric
		"0:1000:1:extra", // too many parts (SplitN limits to 3, so "1:extra" fails Atoi)
	}
	for _, s := range tests {
		if _, err := ParseIDMapping(s); err == nil {
			t.Errorf("ParseIDMapping(%q) should fail", s)
		}
	}
}

func TestParseNamespaceUidGidMap(t *testing.T) {
	input := `type = process
command = /bin/app
namespace-user = true
namespace-uid-map = 0:1000:65536
namespace-gid-map = 0:1000:65536
`
	desc, err := Parse(strings.NewReader(input), "ns-map", "test")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(desc.NamespaceUidMap) != 1 {
		t.Fatalf("expected 1 uid map, got %d", len(desc.NamespaceUidMap))
	}
	if desc.NamespaceUidMap[0].HostID != 1000 {
		t.Errorf("uid map host id = %d, want 1000", desc.NamespaceUidMap[0].HostID)
	}
	if len(desc.NamespaceGidMap) != 1 {
		t.Fatalf("expected 1 gid map, got %d", len(desc.NamespaceGidMap))
	}
	if desc.NamespaceGidMap[0].Size != 65536 {
		t.Errorf("gid map size = %d, want 65536", desc.NamespaceGidMap[0].Size)
	}
}

func TestParseNamespaceUidMapAppend(t *testing.T) {
	input := `type = process
command = /bin/app
namespace-user = true
namespace-uid-map = 0:1000:1
namespace-uid-map += 1:2000:100
`
	desc, err := Parse(strings.NewReader(input), "ns-map-multi", "test")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(desc.NamespaceUidMap) != 2 {
		t.Fatalf("expected 2 uid maps, got %d", len(desc.NamespaceUidMap))
	}
	if desc.NamespaceUidMap[0].ContainerID != 0 || desc.NamespaceUidMap[0].HostID != 1000 {
		t.Errorf("first map: %+v", desc.NamespaceUidMap[0])
	}
	if desc.NamespaceUidMap[1].ContainerID != 1 || desc.NamespaceUidMap[1].HostID != 2000 {
		t.Errorf("second map: %+v", desc.NamespaceUidMap[1])
	}
}

func TestParseKeyword(t *testing.T) {
	input := `type = process
command = /bin/app
keyword -docker -lxc -podman
`
	desc, err := Parse(strings.NewReader(input), "kw-svc", "test")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(desc.Keywords) != 3 {
		t.Fatalf("expected 3 keywords, got %d: %v", len(desc.Keywords), desc.Keywords)
	}
	want := []string{"-docker", "-lxc", "-podman"}
	for i, kw := range want {
		if desc.Keywords[i] != kw {
			t.Errorf("keyword[%d] = %q, want %q", i, desc.Keywords[i], kw)
		}
	}
}

func TestParseKeywordMultipleLines(t *testing.T) {
	input := `type = process
command = /bin/app
keyword -docker -lxc
keyword -wsl -xenu
`
	desc, err := Parse(strings.NewReader(input), "kw-multi", "test")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(desc.Keywords) != 4 {
		t.Fatalf("expected 4 keywords, got %d: %v", len(desc.Keywords), desc.Keywords)
	}
}

func TestParseKeywordWithEquals(t *testing.T) {
	input := `type = process
command = /bin/app
keyword = -docker -wsl
`
	desc, err := Parse(strings.NewReader(input), "kw-eq", "test")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(desc.Keywords) != 2 {
		t.Fatalf("expected 2 keywords, got %d: %v", len(desc.Keywords), desc.Keywords)
	}
	if desc.Keywords[0] != "-docker" || desc.Keywords[1] != "-wsl" {
		t.Errorf("unexpected keywords: %v", desc.Keywords)
	}
}

func TestParseKeywordEmpty(t *testing.T) {
	input := `type = process
command = /bin/app
`
	desc, err := Parse(strings.NewReader(input), "no-kw", "test")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(desc.Keywords) != 0 {
		t.Errorf("expected 0 keywords, got %d", len(desc.Keywords))
	}
}

func TestParseHealthCheckSettings(t *testing.T) {
	input := `type = process
command = /usr/bin/myapp
healthcheck-command = /usr/bin/curl -sf http://localhost:8080/health
healthcheck-interval = 30
healthcheck-delay = 10
healthcheck-max-failures = 5
unhealthy-command = /usr/local/bin/notify-unhealthy
`
	desc, err := Parse(strings.NewReader(input), "hc-svc", "test")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(desc.HealthCheckCommand) == 0 {
		t.Fatal("expected healthcheck-command to be set")
	}
	if desc.HealthCheckCommand[0] != "/usr/bin/curl" {
		t.Errorf("healthcheck-command[0] = %q, want /usr/bin/curl", desc.HealthCheckCommand[0])
	}
	if desc.HealthCheckInterval != 30*time.Second {
		t.Errorf("healthcheck-interval = %v, want 30s", desc.HealthCheckInterval)
	}
	if desc.HealthCheckDelay != 10*time.Second {
		t.Errorf("healthcheck-delay = %v, want 10s", desc.HealthCheckDelay)
	}
	if desc.HealthCheckMaxFail != 5 {
		t.Errorf("healthcheck-max-failures = %d, want 5", desc.HealthCheckMaxFail)
	}
	if len(desc.UnhealthyCommand) == 0 || desc.UnhealthyCommand[0] != "/usr/local/bin/notify-unhealthy" {
		t.Errorf("unhealthy-command = %v", desc.UnhealthyCommand)
	}
}

func TestParseHealthCheckDuration(t *testing.T) {
	input := `type = process
command = /bin/app
healthcheck-command = /bin/true
healthcheck-interval = 5s
healthcheck-delay = 1m
`
	desc, err := Parse(strings.NewReader(input), "hc-dur", "test")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if desc.HealthCheckInterval != 5*time.Second {
		t.Errorf("interval = %v, want 5s", desc.HealthCheckInterval)
	}
	if desc.HealthCheckDelay != time.Minute {
		t.Errorf("delay = %v, want 1m", desc.HealthCheckDelay)
	}
}

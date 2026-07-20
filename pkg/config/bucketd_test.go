package config

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// TestParseBucketDDirectives covers every new env + credential + fd
// pipeline directive. Runtime behaviour lives in the record and
// runner-side tests; this is a wiring round-trip.
func TestParseBucketDDirectives(t *testing.T) {
	input := `
type = process
command = /bin/true
pass-environment = HOME PATH USER
unset-environment = TERM
exec-search-path = /opt/bin:/usr/bin
standard-input-text = hello
open-file = /var/log/foo.log:foo-log:append,graceful
import-credential = mycred.*
notify-access = main
guess-main-pid = yes
`
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !desc.PassEnvSet || len(desc.PassEnvironment) != 3 {
		t.Errorf("pass-environment: set=%v len=%d", desc.PassEnvSet, len(desc.PassEnvironment))
	}
	if len(desc.UnsetEnvironment) != 1 || desc.UnsetEnvironment[0] != "TERM" {
		t.Errorf("unset-environment = %v", desc.UnsetEnvironment)
	}
	if desc.ExecSearchPath != "/opt/bin:/usr/bin" {
		t.Errorf("exec-search-path = %q", desc.ExecSearchPath)
	}
	if !desc.StandardInputSet || string(desc.StandardInput) != "hello" {
		t.Errorf("standard-input-text set=%v data=%q", desc.StandardInputSet, desc.StandardInput)
	}
	if len(desc.OpenFiles) != 1 {
		t.Fatalf("open-file len = %d", len(desc.OpenFiles))
	}
	f := desc.OpenFiles[0]
	if f.Path != "/var/log/foo.log" || f.FDName != "foo-log" || f.Options != "append,graceful" {
		t.Errorf("open-file spec = %+v", f)
	}
	if len(desc.ImportCredentials) != 1 || desc.ImportCredentials[0] != "mycred.*" {
		t.Errorf("import-credential = %v", desc.ImportCredentials)
	}
	if !desc.NotifyAccessSet || desc.NotifyAccess != service.NotifyAccessMain {
		t.Errorf("notify-access set=%v val=%v", desc.NotifyAccessSet, desc.NotifyAccess)
	}
	if !desc.GuessMainPID {
		t.Errorf("guess-main-pid should be true")
	}
}

// TestStandardInputData decodes a base64 payload including a NUL byte
// so we know the pipeline preserves arbitrary bytes.
func TestStandardInputData(t *testing.T) {
	input := `
type = process
command = /bin/true
standard-input-data = aGVsbG8AYnl0ZXM=
`
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := "hello\x00bytes"
	if string(desc.StandardInput) != want {
		t.Errorf("standard-input-data = %q, want %q", desc.StandardInput, want)
	}
}

// TestParseOpenFileMinimal: bare PATH form defaults FDName to
// basename(PATH) and leaves Options empty.
func TestParseOpenFileMinimal(t *testing.T) {
	spec, err := parseOpenFile("/etc/hosts")
	if err != nil {
		t.Fatalf("parseOpenFile: %v", err)
	}
	if spec.Path != "/etc/hosts" || spec.FDName != "hosts" || spec.Options != "" {
		t.Errorf("minimal spec = %+v", spec)
	}
}

// TestParseOpenFileRejectsRelative refuses non-absolute paths so a
// config typo is caught at parse time rather than turning into a
// silent openat(AT_FDCWD, "foo") inside the daemon at start time.
func TestParseOpenFileRejectsRelative(t *testing.T) {
	if _, err := parseOpenFile("etc/hosts"); err == nil {
		t.Errorf("relative path should be rejected")
	}
}

// TestParsePassEnvironmentAppend covers the += semantics — most other
// list-form directives use the same code path, so one test carries
// enough regression protection.
func TestParsePassEnvironmentAppend(t *testing.T) {
	input := `
type = process
command = /bin/true
pass-environment = HOME
pass-environment += PATH
pass-environment += USER
`
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(desc.PassEnvironment) != 3 {
		t.Fatalf("want 3 entries, got %d: %v", len(desc.PassEnvironment), desc.PassEnvironment)
	}
}

// TestParseNotifyAccessBogus catches enum typos.
func TestParseNotifyAccessBogus(t *testing.T) {
	input := "type = process\ncommand = /bin/true\nnotify-access = bogus\n"
	if _, err := Parse(strings.NewReader(input), "svc", "test-file"); err == nil {
		t.Errorf("expected parse error for bogus notify-access")
	}
}

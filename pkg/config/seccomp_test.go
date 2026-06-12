package config

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// TestParseSeccompFilter exercises the four stanzas in a realistic
// shape: group expansion, deny-mode prefix, arch list, error action.
func TestParseSeccompFilter(t *testing.T) {
	input := `type = process
command = /usr/bin/svc
system-call-filter = @system-service
system-call-filter += write read
system-call-architectures = native
system-call-error-number = EPERM
system-call-log = ptrace
`
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(desc.SystemCallFilter) < 3 {
		t.Fatalf("SystemCallFilter too small: %v", desc.SystemCallFilter)
	}
	// The list preserves the raw items (parser does not pre-expand
	// groups — the runner does). First entry must be the group token.
	if desc.SystemCallFilter[0] != "@system-service" {
		t.Errorf("first item = %q, want @system-service", desc.SystemCallFilter[0])
	}
	if desc.SystemCallErrorNumber != "EPERM" {
		t.Errorf("SystemCallErrorNumber = %q", desc.SystemCallErrorNumber)
	}
	wantArchs := []string{"native"}
	if !equalStrings(desc.SystemCallArchitectures, wantArchs) {
		t.Errorf("SystemCallArchitectures = %v, want %v",
			desc.SystemCallArchitectures, wantArchs)
	}
	wantLog := []string{"ptrace"}
	if !equalStrings(desc.SystemCallLog, wantLog) {
		t.Errorf("SystemCallLog = %v, want %v", desc.SystemCallLog, wantLog)
	}
}

// TestParseSeccompDenyPrefix verifies '~' on the first item is
// accepted and that mid-list '~' is rejected.
func TestParseSeccompDenyPrefix(t *testing.T) {
	good := `type = process
command = /bin/true
system-call-filter = ~ptrace
`
	if _, err := Parse(strings.NewReader(good), "svc", "test-file"); err != nil {
		t.Fatalf("leading ~ rejected unexpectedly: %v", err)
	}
	bad := `type = process
command = /bin/true
system-call-filter = read ~ptrace
`
	if _, err := Parse(strings.NewReader(bad), "svc", "test-file"); err == nil {
		t.Fatal("mid-list ~ must be rejected")
	}
}

// TestParseSeccompUnknownSyscallRejected makes sure a typo is caught at
// parse time — silent dropping would leave the runtime filter with a
// gap the operator never asked for.
func TestParseSeccompUnknownSyscallRejected(t *testing.T) {
	input := `type = process
command = /bin/true
system-call-filter = not_a_syscall
`
	_, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err == nil || !strings.Contains(err.Error(), "unknown syscall") {
		t.Fatalf("expected unknown-syscall error, got %v", err)
	}
}

// TestParseSeccompUnknownGroupRejected catches @typo's similarly.
func TestParseSeccompUnknownGroupRejected(t *testing.T) {
	input := `type = process
command = /bin/true
system-call-filter = @bogus-group
`
	_, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err == nil || !strings.Contains(err.Error(), "unknown group") {
		t.Fatalf("expected unknown-group error, got %v", err)
	}
}

// TestParseSeccompBadAction rejects unparseable action values at parse
// time rather than producing a kernel-side error at boot.
func TestParseSeccompBadAction(t *testing.T) {
	input := `type = process
command = /bin/true
system-call-error-number = nopenope
`
	if _, err := Parse(strings.NewReader(input), "svc", "test-file"); err == nil {
		t.Fatal("expected error for invalid action")
	}
}

// TestSeccompFlowsToRecord checks the loader copies the seccomp fields
// onto the ServiceRecord and that SeccompActive trips correctly.
func TestSeccompFlowsToRecord(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "boxed-seccomp",
		"type = process\ncommand = /usr/bin/svc\n"+
			"system-call-filter = @system-service\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	svc, err := loader.LoadService("boxed-seccomp")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	rec := svc.Record()
	if !rec.SeccompActive() {
		t.Fatal("SeccompActive() should be true")
	}
	cfg := rec.Seccomp()
	if cfg.Filter[0] != "@system-service" {
		t.Errorf("Filter[0] = %q", cfg.Filter[0])
	}
}

package config

import (
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// TestParseGapBServiceDirectives verifies the parser accepts every new
// [Service] directive shipped in the Gap B batch and stores each value
// in the expected ServiceDescription field. Purely a wiring test — the
// runtime behaviour of these fields is covered by the service package's
// own gapb_test.go.
func TestParseGapBServiceDirectives(t *testing.T) {
	input := `
type = process
command = /bin/true
timeout-sec = 20
timeout-abort-sec = 3
timeout-start-failure-mode = kill
restart-max-delay = 90
runtime-randomized-extra = 15
restart-force-exit-status = 3 4 7
restart-mode = direct
exit-type = cgroup
exec-condition = /usr/bin/test -f /etc/proceed
`
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if desc.StartTimeout != 20*time.Second || desc.StopTimeout != 20*time.Second {
		t.Errorf("timeout-sec must set both start (%v) and stop (%v) to 20s",
			desc.StartTimeout, desc.StopTimeout)
	}
	if desc.TimeoutAbortSec != 3*time.Second {
		t.Errorf("timeout-abort-sec = %v, want 3s", desc.TimeoutAbortSec)
	}
	if desc.TimeoutStartFailureMode != service.TimeoutFailureKill {
		t.Errorf("timeout-start-failure-mode = %v, want kill", desc.TimeoutStartFailureMode)
	}
	if desc.RestartMaxDelay != 90*time.Second {
		t.Errorf("restart-max-delay = %v, want 90s", desc.RestartMaxDelay)
	}
	if desc.RuntimeRandomizedExtra != 15*time.Second {
		t.Errorf("runtime-randomized-extra = %v, want 15s", desc.RuntimeRandomizedExtra)
	}
	want := []int{3, 4, 7}
	if len(desc.RestartForceExitCodes) != len(want) {
		t.Fatalf("restart-force-exit-status len=%d, want %d",
			len(desc.RestartForceExitCodes), len(want))
	}
	for i, c := range want {
		if desc.RestartForceExitCodes[i] != c {
			t.Errorf("restart-force-exit-status[%d] = %d, want %d",
				i, desc.RestartForceExitCodes[i], c)
		}
	}
	if desc.RestartMode != service.RestartModeDirect {
		t.Errorf("restart-mode = %v, want direct", desc.RestartMode)
	}
	if desc.ExitType != service.ExitTypeCgroup {
		t.Errorf("exit-type = %v, want cgroup", desc.ExitType)
	}

	// exec-condition is stored as a synthetic Predicate; find it.
	var found *service.Predicate
	for i := range desc.Predicates {
		if desc.Predicates[i].Kind == service.PredExecCondition {
			found = &desc.Predicates[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("exec-condition predicate not populated: %v", desc.Predicates)
	}
	if found.Param != "/usr/bin/test -f /etc/proceed" {
		t.Errorf("exec-condition param = %q, want the raw shell command", found.Param)
	}
	if found.IsAssert {
		t.Errorf("exec-condition should not be an assert unless spelled assert-exec-condition")
	}
}

// TestParseAssertExecCondition ensures assert-exec-condition flips
// IsAssert while sharing the same predicate kind + param path.
func TestParseAssertExecCondition(t *testing.T) {
	input := `
type = process
command = /bin/true
assert-exec-condition = test -x /usr/local/bin/preflight
`
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(desc.Predicates) != 1 {
		t.Fatalf("expected 1 predicate, got %d", len(desc.Predicates))
	}
	p := desc.Predicates[0]
	if p.Kind != service.PredExecCondition {
		t.Errorf("kind = %v, want PredExecCondition", p.Kind)
	}
	if !p.IsAssert {
		t.Errorf("IsAssert should be true for assert-exec-condition")
	}
	if p.Param != "test -x /usr/local/bin/preflight" {
		t.Errorf("param = %q, want the raw shell command", p.Param)
	}
}

// TestParseGapBRejectsBadValues catches typos in enum-shaped
// directives so they surface as parse errors, not silent defaults.
func TestParseGapBRejectsBadValues(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"exit-type bogus", "type = process\ncommand = /bin/true\nexit-type = bogus\n"},
		{"restart-mode bogus", "type = process\ncommand = /bin/true\nrestart-mode = bogus\n"},
		{"timeout-start-failure-mode bogus",
			"type = process\ncommand = /bin/true\ntimeout-start-failure-mode = bogus\n"},
	}
	for _, tc := range cases {
		if _, err := Parse(strings.NewReader(tc.body), "svc", "test-file"); err == nil {
			t.Errorf("%s: expected parse error, got nil", tc.name)
		}
	}
}

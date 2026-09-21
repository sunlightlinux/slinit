// Regression tests derived from the systemd bug list on nosystemd.org.
// See pkg/journal/nosystemd_checklist_test.go for the rationale.
package config

import (
	"strings"
	"testing"
)

// nosystemd.org, "systemd does not respect system wide resource limits"
// (fredrikaverpil.github.io/blog/2016/04/27/systemd-services-and-
// resource-limits/): an admin sets limits in /etc/security/limits.conf,
// services silently ignore them because they are not PAM sessions, and
// nothing anywhere says so. The complaint is not that systemd has its
// own Limit* directives — it is the silence.
//
// slinit cannot honour limits.conf either, for the same structural
// reason: a service is not a login. What it can do is never be silent.
// Its own rlimit-* directives are applied (tests/functional/cases/
// 56-rlimits.sh proves that end-to-end against /proc/PID/limits), and a
// value it cannot make sense of is a load error rather than a shrug.
//
// This pins the second half: malformed rlimits must fail loudly.
func TestMalformedRlimitIsRejectedNotIgnored_resourceLimits(t *testing.T) {
	for _, tc := range []struct {
		directive string
		value     string
		why       string
	}{
		{"rlimit-nofile", "not-a-number", "non-numeric"},
		{"rlimit-nofile", "1024:", "missing hard limit"},
		{"rlimit-nofile", ":4096", "missing soft limit"},
		{"rlimit-core", "-5", "negative"},
		{"rlimit-data", "1024:512:256", "too many fields"},
		{"rlimit-as", "", "empty"},
	} {
		src := "type = process\ncommand = /bin/true\n" +
			tc.directive + " = " + tc.value + "\n"
		_, err := Parse(strings.NewReader(src), "rlimit-test", "test-file")
		if err == nil {
			t.Errorf("%s = %q (%s) was accepted silently — the whole point of "+
				"this case is that a limit slinit cannot apply must not be "+
				"dropped without a word", tc.directive, tc.value, tc.why)
			continue
		}
		if !strings.Contains(err.Error(), tc.directive) {
			t.Errorf("%s = %q rejected, but the error does not name the "+
				"directive: %v", tc.directive, tc.value, err)
		}
	}
}

// The companion: well-formed limits must actually be recorded, or the
// test above could be satisfied by rejecting everything.
func TestWellFormedRlimitsAreRecorded_resourceLimits(t *testing.T) {
	src := "type = process\ncommand = /bin/true\n" +
		"rlimit-nofile = 1024:4096\n" +
		"rlimit-core = 0\n"
	desc, err := Parse(strings.NewReader(src), "rlimit-test", "test-file")
	if err != nil {
		t.Fatalf("a well-formed rlimit set was rejected: %v", err)
	}
	if desc.RlimitNofile == nil {
		t.Error("rlimit-nofile parsed without error but was not recorded")
	}
	if desc.RlimitCore == nil {
		t.Error("rlimit-core = 0 was not recorded — a zero limit is a real " +
			"limit (no core dumps), not an absent one")
	}
}

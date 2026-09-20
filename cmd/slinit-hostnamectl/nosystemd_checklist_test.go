// Regression tests derived from the systemd bug list on nosystemd.org.
// See pkg/journal/nosystemd_checklist_test.go for the rationale.
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// systemd#6369: "hostnamed does not like fqdns with trailing dots".
// https://github.com/systemd/systemd/issues/6369
//
// slinit REJECTS a trailing dot too, and that is deliberate rather than
// an oversight: `foo.example.com.` is DNS presentation syntax for a
// root-anchored name, not a hostname. sethostname(2) takes an RFC 1123
// host name, which has no root label. Accepting the dot would mean
// either storing a name the kernel treats as ordinary text or silently
// rewriting what the operator typed.
//
// What this test actually guards is the part upstream got wrong in
// spirit: the rejection has to be a clean, explained refusal *before*
// anything is changed — not a partial apply and not a confusing error.
// If someone later decides to normalise the trailing dot away instead,
// that is a defensible change; it just has to be deliberate, and this
// test is where it gets noticed.
func TestTrailingDotHostnameIsRejectedCleanly_systemd6369(t *testing.T) {
	err := validateHostname("foo.example.com.")
	if err == nil {
		t.Fatal("a trailing-dot FQDN was accepted; if that is now intended, " +
			"update this test and say why in the commit")
	}
	if !strings.Contains(err.Error(), "cannot start or end with") {
		t.Errorf("rejection reason is unclear to an operator: %v", err)
	}

	// The same name without the root label is a perfectly good hostname
	// and must still be accepted — otherwise the rule above is just a
	// blanket ban on dots.
	if err := validateHostname("foo.example.com"); err != nil {
		t.Errorf("a plain FQDN was rejected: %v", err)
	}
}

// The property that matters more than the verdict: a name that fails
// validation must leave nothing behind. systemd's own hostnamed has had
// several half-apply bugs; slinit validates before it touches the
// kernel or /etc/hostname, and this pins that ordering.
func TestRejectedHostnameLeavesNoTrace_systemd6369(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hostname")

	saved := hostnamePath
	hostnamePath = path
	defer func() { hostnamePath = saved }()

	// Static scope only, so the test never calls sethostname(2) on the
	// machine running it.
	var out bytes.Buffer
	opts := options{args: []string{"foo.example.com."}, scope: scopeStatic}

	if err := runHostname(&out, opts); err == nil {
		t.Fatal("runHostname accepted a trailing-dot FQDN")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a rejected hostname was still written to %s", path)
	}
	if out.Len() != 0 {
		t.Errorf("a rejected hostname produced output: %q", out.String())
	}
}

// The neighbouring rules the same loop enforces, pinned so a rewrite of
// validateHostname cannot quietly widen what reaches sethostname(2).
func TestHostnameValidationBoundaries_systemd6369(t *testing.T) {
	for _, tc := range []struct {
		name   string
		accept bool
		why    string
	}{
		{"host", true, "plain label"},
		{"foo.example.com", true, "FQDN without root label"},
		{"a-b.c-d", true, "hyphens inside labels"},
		{"foo.example.com.", false, "trailing root label"},
		{".foo", false, "leading dot"},
		{"-foo", false, "leading hyphen"},
		{"foo-", false, "trailing hyphen"},
		{"foo..bar", false, "consecutive dots"},
		{"", false, "empty"},
		{"localhost", false, "would break loopback resolution"},
		{"localhost.localdomain", false, "localhost-prefixed"},
		{"foo bar", false, "space"},
		{"foo_bar", false, "underscore is not RFC 1123"},
		{strings.Repeat("a", 65), false, "over 64 characters"},
		{strings.Repeat("a", 64), true, "exactly 64 characters"},
	} {
		err := validateHostname(tc.name)
		if tc.accept && err != nil {
			t.Errorf("%s: %q rejected (%v), want accepted", tc.why, tc.name, err)
		}
		if !tc.accept && err == nil {
			t.Errorf("%s: %q accepted, want rejected", tc.why, tc.name)
		}
	}
}

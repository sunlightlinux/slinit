package config

import (
	"strings"
	"testing"
)

// TestParseBucketESELinuxSMACK covers the two shipped Bucket E
// directives (selinux-context + smack-process-label). Everything
// else in Bucket E is deferred per project memory rationale.
func TestParseBucketESELinuxSMACK(t *testing.T) {
	input := `
type = process
command = /bin/true
selinux-context = system_u:system_r:my_service_t:s0
smack-process-label = MyServiceLabel
`
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.SELinuxContext != "system_u:system_r:my_service_t:s0" {
		t.Errorf("selinux-context = %q", desc.SELinuxContext)
	}
	if desc.SMACKProcessLabel != "MyServiceLabel" {
		t.Errorf("smack-process-label = %q", desc.SMACKProcessLabel)
	}
}

// TestParseBucketERejectsEmpty pins the parser-side validation:
// empty values on either directive surface as errors so a
// misconfiguration doesn't silently disable a security intent.
func TestParseBucketERejectsEmpty(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"selinux empty",
			"type = process\ncommand = /bin/true\nselinux-context =\n"},
		{"smack empty",
			"type = process\ncommand = /bin/true\nsmack-process-label =\n"},
	}
	for _, tc := range cases {
		if _, err := Parse(strings.NewReader(tc.body), "svc", "test-file"); err == nil {
			t.Errorf("%s: expected parse error", tc.name)
		}
	}
}

package config

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestParseOOMPolicySetting(t *testing.T) {
	for _, c := range []struct {
		val  string
		want service.OOMPolicy
	}{
		{"continue", service.OOMContinue},
		{"stop", service.OOMStop},
		{"kill", service.OOMKill},
	} {
		input := "type = process\ncommand = /bin/true\noom-policy = " + c.val + "\n"
		desc, err := Parse(strings.NewReader(input), "svc", "test")
		if err != nil {
			t.Errorf("%q: parse: %v", c.val, err)
			continue
		}
		if desc.OOMPolicy != c.want {
			t.Errorf("%q: got %v want %v", c.val, desc.OOMPolicy, c.want)
		}
	}
}

func TestParseOOMPolicyRejectsUnknown(t *testing.T) {
	input := "type = process\ncommand = /bin/true\noom-policy = nuke\n"
	if _, err := Parse(strings.NewReader(input), "svc", "test"); err == nil {
		t.Fatal("expected error for unknown oom-policy")
	}
}

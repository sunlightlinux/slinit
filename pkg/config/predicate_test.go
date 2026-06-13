package config

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestParseConditionAndAssert(t *testing.T) {
	input := `
type = process
command = /bin/true
condition-path-exists = /etc/hostname
condition-path-exists = !/nope
condition-virtualization = kvm
assert-path-is-directory = /tmp
assert-kernel-command-line = quiet
`
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if got, want := len(desc.Predicates), 5; got != want {
		t.Fatalf("predicates: got %d want %d (%v)", got, want, desc.Predicates)
	}

	expectations := []struct {
		kind     service.PredicateKind
		param    string
		negate   bool
		isAssert bool
	}{
		{service.PredPathExists, "/etc/hostname", false, false},
		{service.PredPathExists, "/nope", true, false},
		{service.PredVirtualization, "kvm", false, false},
		{service.PredPathIsDirectory, "/tmp", false, true},
		{service.PredKernelCommandLine, "quiet", false, true},
	}
	for i, want := range expectations {
		got := desc.Predicates[i]
		if got.Kind != want.kind {
			t.Errorf("[%d] kind: got %v want %v", i, got.Kind, want.kind)
		}
		if got.Param != want.param {
			t.Errorf("[%d] param: got %q want %q", i, got.Param, want.param)
		}
		if got.Negate != want.negate {
			t.Errorf("[%d] negate: got %v want %v", i, got.Negate, want.negate)
		}
		if got.IsAssert != want.isAssert {
			t.Errorf("[%d] isAssert: got %v want %v", i, got.IsAssert, want.isAssert)
		}
	}
}

func TestParseUnknownConditionRejected(t *testing.T) {
	input := `
type = process
command = /bin/true
condition-no-such-kind = whatever
`
	_, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err == nil {
		t.Fatal("expected error for unknown condition kind")
	}
}

func TestParseNegationSpacingTolerant(t *testing.T) {
	cases := []string{
		"condition-virtualization = !kvm",
		"condition-virtualization = ! kvm",
		"condition-virtualization=!kvm",
	}
	for _, line := range cases {
		input := "type = process\ncommand = /bin/true\n" + line + "\n"
		desc, err := Parse(strings.NewReader(input), "svc", line)
		if err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		if len(desc.Predicates) != 1 {
			t.Fatalf("%q: predicates=%d", line, len(desc.Predicates))
		}
		p := desc.Predicates[0]
		if !p.Negate || p.Param != "kvm" {
			t.Errorf("%q: got param=%q negate=%v", line, p.Param, p.Negate)
		}
	}
}

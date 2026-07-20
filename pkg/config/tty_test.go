package config

import (
	"strings"
	"testing"
)

// TestParseTTYDirectives round-trips the whole TTY cluster.
func TestParseTTYDirectives(t *testing.T) {
	input := `
type = process
command = /sbin/agetty
tty-path = /dev/tty1
tty-columns = 132
tty-rows = 50
tty-vhangup = yes
tty-vt-disallocate = yes
tty-reset = yes
`
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.TTYPath != "/dev/tty1" {
		t.Errorf("tty-path = %q", desc.TTYPath)
	}
	if desc.TTYColumns != 132 {
		t.Errorf("tty-columns = %d, want 132", desc.TTYColumns)
	}
	if desc.TTYRows != 50 {
		t.Errorf("tty-rows = %d, want 50", desc.TTYRows)
	}
	if !desc.TTYVHangup || !desc.TTYVTDisallocate || !desc.TTYReset {
		t.Errorf("bool flags: vhangup=%v vt-disallocate=%v reset=%v",
			desc.TTYVHangup, desc.TTYVTDisallocate, desc.TTYReset)
	}
}

// TestParseTTYRejectsBadValues catches typos before they silently
// leave the TTY unconfigured. Relative paths, out-of-range winsize,
// non-integer winsize all surface as errors at parse time.
func TestParseTTYRejectsBadValues(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"relative tty-path",
			"type = process\ncommand = /bin/true\ntty-path = tty1\n"},
		{"non-integer tty-columns",
			"type = process\ncommand = /bin/true\ntty-columns = wide\n"},
		{"out-of-range tty-rows",
			"type = process\ncommand = /bin/true\ntty-rows = 100000\n"},
	}
	for _, tc := range cases {
		if _, err := Parse(strings.NewReader(tc.body), "svc", "test-file"); err == nil {
			t.Errorf("%s: expected parse error", tc.name)
		}
	}
}

package process

import (
	"reflect"
	"testing"
)

// TestNeedsRunnerWrapSandbox verifies that every sandbox field — alone
// and unset — drives the runner-wrap decision correctly. The runner is
// the only place sandbox mounts happen, so an unwrapped command would
// silently lose the requested isolation.
func TestNeedsRunnerWrapSandbox(t *testing.T) {
	cases := []struct {
		name string
		p    ExecParams
		want bool
	}{
		{"no sandbox", ExecParams{}, false},
		{"private-tmp", ExecParams{PrivateTmp: true}, true},
		{"protect-system=yes", ExecParams{ProtectSystem: "yes"}, true},
		{"read-only-paths", ExecParams{ReadOnlyPaths: []string{"/usr"}}, true},
		{"read-write-paths", ExecParams{ReadWritePaths: []string{"/var/lib/svc"}}, true},
	}
	for _, c := range cases {
		got := needsRunnerWrap(c.p)
		if got != c.want {
			t.Errorf("%s: needsRunnerWrap = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestWrapWithRunnerSandbox checks the wire format of the sandbox flags
// emitted to slinit-runner. The runner consumes them in this exact
// shape; getting the encoding wrong would silently disable isolation.
func TestWrapWithRunnerSandbox(t *testing.T) {
	p := ExecParams{
		Command:        []string{"/usr/bin/svc"},
		PrivateTmp:     true,
		ProtectSystem:  "strict",
		ReadOnlyPaths:  []string{"/usr/local", "/opt"},
		ReadWritePaths: []string{"/var/lib/svc"},
		RunnerPath:     "/sbin/slinit-runner",
	}
	got := wrapWithRunner(p)
	want := []string{
		"/sbin/slinit-runner",
		"--private-tmp",
		"--protect-system=strict",
		"--read-only-path=/usr/local",
		"--read-only-path=/opt",
		"--read-write-path=/var/lib/svc",
		"--",
		"/usr/bin/svc",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv mismatch:\n got %v\nwant %v", got, want)
	}
}

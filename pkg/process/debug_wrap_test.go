package process

import (
	"reflect"
	"testing"
)

// TestNeedsRunnerWrapDebug verifies the debug stop requires the runner
// wrap (SIGSTOP must be raised in the child, between fork and exec).
func TestNeedsRunnerWrapDebug(t *testing.T) {
	if !needsRunnerWrap(ExecParams{DebugStop: true}) {
		t.Error("debug stop should require the runner wrap")
	}
}

// TestWrapWithRunnerDebug verifies --debug is emitted, ordered after the
// apparmor flag and before the "--" separator.
func TestWrapWithRunnerDebug(t *testing.T) {
	p := ExecParams{
		Command:         []string{"/usr/bin/svc"},
		AppArmorProfile: "p",
		DebugStop:       true,
		RunnerPath:      "/sbin/slinit-runner",
	}
	got := wrapWithRunner(p)
	want := []string{
		"/sbin/slinit-runner",
		"--apparmor=p",
		"--debug",
		"--",
		"/usr/bin/svc",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv mismatch:\n got %v\nwant %v", got, want)
	}
}

// TestWrapWithRunnerDebugOnly verifies a debug-only service still wraps
// correctly.
func TestWrapWithRunnerDebugOnly(t *testing.T) {
	p := ExecParams{
		Command:    []string{"/bin/true"},
		DebugStop:  true,
		RunnerPath: "/sbin/slinit-runner",
	}
	got := wrapWithRunner(p)
	want := []string{"/sbin/slinit-runner", "--debug", "--", "/bin/true"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv mismatch:\n got %v\nwant %v", got, want)
	}
}

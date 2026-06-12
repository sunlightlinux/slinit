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
		// #3a MVP
		{"private-tmp", ExecParams{PrivateTmp: true}, true},
		{"protect-system=yes", ExecParams{ProtectSystem: "yes"}, true},
		{"read-only-paths", ExecParams{ReadOnlyPaths: []string{"/usr"}}, true},
		{"read-write-paths", ExecParams{ReadWritePaths: []string{"/var/lib/svc"}}, true},
		// #3b expansion
		{"protect-home=yes", ExecParams{ProtectHome: "yes"}, true},
		{"inaccessible-paths", ExecParams{InaccessiblePaths: []string{"/opt/secret"}}, true},
		{"protect-proc=invisible", ExecParams{ProtectProc: "invisible"}, true},
		{"proc-subset=pid", ExecParams{ProcSubset: "pid"}, true},
		{"bind-paths", ExecParams{BindPaths: []string{"/a:/b"}}, true},
		{"bind-ro-paths", ExecParams{BindReadOnlyPaths: []string{"/a:/b"}}, true},
		{"temporary-filesystem", ExecParams{TemporaryFileSystem: []string{"/run/svc"}}, true},
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

// TestNeedsRunnerWrapSeccomp verifies any seccomp field forces the
// runner wrap; install must happen in the same task that will become
// the service, so an unwrapped command would silently lose the filter.
func TestNeedsRunnerWrapSeccomp(t *testing.T) {
	cases := []struct {
		name string
		p    ExecParams
	}{
		{"filter", ExecParams{SeccompFilter: []string{"@system-service"}}},
		{"arch", ExecParams{SeccompArchitectures: []string{"native"}}},
		{"errno", ExecParams{SeccompErrorAction: "EPERM"}},
		{"log", ExecParams{SeccompLogFilter: []string{"ptrace"}}},
	}
	for _, c := range cases {
		if !needsRunnerWrap(c.p) {
			t.Errorf("%s: should require runner wrap", c.name)
		}
	}
}

// TestWrapWithRunnerSeccomp checks the wire format of the seccomp
// flags. The runner consumes them in this exact shape; getting the
// encoding wrong would silently disable the filter.
func TestWrapWithRunnerSeccomp(t *testing.T) {
	p := ExecParams{
		Command:              []string{"/usr/bin/svc"},
		SeccompFilter:        []string{"@system-service", "write"},
		SeccompArchitectures: []string{"native"},
		SeccompErrorAction:   "EPERM",
		SeccompLogFilter:     []string{"ptrace"},
		RunnerPath:           "/sbin/slinit-runner",
	}
	got := wrapWithRunner(p)
	want := []string{
		"/sbin/slinit-runner",
		"--syscall-filter=@system-service",
		"--syscall-filter=write",
		"--syscall-arch=native",
		"--syscall-action=EPERM",
		"--syscall-log=ptrace",
		"--",
		"/usr/bin/svc",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv mismatch:\n got %v\nwant %v", got, want)
	}
}

// TestNeedsRunnerWrapHardening verifies any Restrict*/Protect* knob
// triggers the wrap, since the protection is applied in-task in the
// runner.
func TestNeedsRunnerWrapHardening(t *testing.T) {
	cases := []struct {
		name string
		p    ExecParams
	}{
		{"kernel-tunables", ExecParams{ProtectKernelTunables: true}},
		{"kernel-modules", ExecParams{ProtectKernelModules: true}},
		{"kernel-logs", ExecParams{ProtectKernelLogs: true}},
		{"clock", ExecParams{ProtectClock: true}},
		{"control-groups", ExecParams{ProtectControlGroups: true}},
		{"hostname", ExecParams{ProtectHostname: true}},
		{"personality", ExecParams{LockPersonality: true}},
	}
	for _, c := range cases {
		if !needsRunnerWrap(c.p) {
			t.Errorf("%s: should require runner wrap", c.name)
		}
	}
}

// TestWrapWithRunnerHardening verifies the wire format. The runner
// reads these as bool flag.Bool flags; the names must match.
func TestWrapWithRunnerHardening(t *testing.T) {
	p := ExecParams{
		Command:               []string{"/usr/bin/svc"},
		ProtectKernelTunables: true,
		ProtectKernelModules:  true,
		ProtectKernelLogs:     true,
		ProtectClock:          true,
		ProtectControlGroups:  true,
		ProtectHostname:       true,
		LockPersonality:       true,
		RunnerPath:            "/sbin/slinit-runner",
	}
	got := wrapWithRunner(p)
	want := []string{
		"/sbin/slinit-runner",
		"--protect-kernel-tunables",
		"--protect-kernel-modules",
		"--protect-kernel-logs",
		"--protect-clock",
		"--protect-control-groups",
		"--protect-hostname",
		"--lock-personality",
		"--",
		"/usr/bin/svc",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv mismatch:\n got %v\nwant %v", got, want)
	}
}

// TestWrapWithRunnerSandboxExpansion checks the wire format of the #3b
// flags. The runner consumes them in this exact shape and order.
func TestWrapWithRunnerSandboxExpansion(t *testing.T) {
	p := ExecParams{
		Command:             []string{"/usr/bin/svc"},
		ProtectHome:         "tmpfs",
		InaccessiblePaths:   []string{"/opt/secret"},
		ProtectProc:         "invisible",
		ProcSubset:          "pid",
		BindPaths:           []string{"/var/data:/var/data"},
		BindReadOnlyPaths:   []string{"/etc/conf:/etc/conf"},
		TemporaryFileSystem: []string{"/run/svc:size=64m"},
		RunnerPath:          "/sbin/slinit-runner",
	}
	got := wrapWithRunner(p)
	want := []string{
		"/sbin/slinit-runner",
		"--protect-home=tmpfs",
		"--inaccessible-path=/opt/secret",
		"--protect-proc=invisible",
		"--proc-subset=pid",
		"--bind-path=/var/data:/var/data",
		"--bind-ro-path=/etc/conf:/etc/conf",
		"--tmpfs-path=/run/svc:size=64m",
		"--",
		"/usr/bin/svc",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv mismatch:\n got %v\nwant %v", got, want)
	}
}

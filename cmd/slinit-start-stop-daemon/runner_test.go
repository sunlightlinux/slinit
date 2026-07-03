package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnerWrapNoOpWhenClean(t *testing.T) {
	// Without hardening flags, no wrap should be applied.
	opts := Options{Mode: "start", Exec: "/bin/true"}
	_, _, wrapped, err := runnerWrapArgs(opts, "/bin/true", []string{"/bin/true"})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if wrapped {
		t.Errorf("wrap unexpectedly applied")
	}
}

func TestRunnerWrapErrorsWithoutBinary(t *testing.T) {
	// Force PATH to a scratch dir with no slinit-runner. Save/restore.
	dir := t.TempDir()
	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })
	os.Setenv("PATH", dir)

	opts := Options{Mode: "start", Exec: "/bin/true", NoNewPrivs: true}
	_, _, wrapped, err := runnerWrapArgs(opts, "/bin/true", []string{"/bin/true"})
	if wrapped {
		t.Errorf("wrap reported success without slinit-runner")
	}
	if err == nil || !strings.Contains(err.Error(), "slinit-runner not found") {
		t.Errorf("err=%v, want 'slinit-runner not found'", err)
	}
}

func TestRunnerWrapBuildsExpectedArgv(t *testing.T) {
	// Drop a fake slinit-runner into a temp PATH so locateRunner finds it.
	dir := t.TempDir()
	fake := filepath.Join(dir, "slinit-runner")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })
	os.Setenv("PATH", dir)

	opts := Options{
		Mode:         "start",
		Exec:         "/usr/sbin/foo",
		NoNewPrivs:   true,
		Capabilities: "cap_net_bind_service",
		Args:         []string{"-c", "/etc/foo.conf"},
	}
	binary, argv, wrapped, err := runnerWrapArgs(opts, "/usr/sbin/foo",
		[]string{"/usr/sbin/foo", "-c", "/etc/foo.conf"})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !wrapped {
		t.Fatalf("wrap not applied")
	}
	if binary != fake {
		t.Errorf("binary=%q, want %q", binary, fake)
	}
	// argv should be: [slinit-runner --no-new-privs --ambient-cap=N --bounding-cap=N -- /usr/sbin/foo -c /etc/foo.conf]
	if argv[0] != "slinit-runner" {
		t.Errorf("argv[0]=%q, want slinit-runner", argv[0])
	}
	if !contains(argv, "--no-new-privs") {
		t.Errorf("--no-new-privs missing: %v", argv)
	}
	if !containsPrefix(argv, "--ambient-cap=") {
		t.Errorf("--ambient-cap missing: %v", argv)
	}
	if !containsPrefix(argv, "--bounding-cap=") {
		t.Errorf("--bounding-cap missing: %v", argv)
	}
	sepIdx := indexOf(argv, "--")
	if sepIdx < 0 {
		t.Fatalf("`--` separator missing: %v", argv)
	}
	tail := argv[sepIdx+1:]
	if len(tail) < 3 || tail[0] != "/usr/sbin/foo" || tail[1] != "-c" || tail[2] != "/etc/foo.conf" {
		t.Errorf("tail=%v, want [/usr/sbin/foo -c /etc/foo.conf]", tail)
	}
}

func contains(argv []string, s string) bool {
	for _, a := range argv {
		if a == s {
			return true
		}
	}
	return false
}
func containsPrefix(argv []string, p string) bool {
	for _, a := range argv {
		if strings.HasPrefix(a, p) {
			return true
		}
	}
	return false
}
func indexOf(argv []string, s string) int {
	for i, a := range argv {
		if a == s {
			return i
		}
	}
	return -1
}

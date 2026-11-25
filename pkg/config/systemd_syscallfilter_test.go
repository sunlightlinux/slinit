package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/seccomp"
)

// Repeated SystemCallFilter= lines were emitted one per line and the
// parser appended them, so the common
//
//	SystemCallFilter=@system-service
//	SystemCallFilter=~@privileged @resources
//
// became "@system-service ~@privileged @resources", which seccomp.Build
// refuses (~ only on the first item): the unit loaded and its service
// could not start. systemd merges the lines: the second takes its
// syscalls out of the allow-list.
func TestSystemdSyscallFilterLinesMerge(t *testing.T) {
	build := func(unit string) *seccomp.Filter {
		t.Helper()
		desc, _ := convertWarns(t, "[Service]\nExecStart=/bin/true\n"+unit+"\n")
		f, err := seccomp.Build(desc.SystemCallFilter, seccomp.ModeAllow, nil, seccomp.ActionKill, nil)
		if err != nil {
			t.Fatalf("%q: %v (filter %q)", unit, err, desc.SystemCallFilter)
		}
		return f
	}

	f := build("SystemCallFilter=@system-service\nSystemCallFilter=~@privileged @resources")
	if f.Mode != seccomp.ModeAllow {
		t.Errorf("allow-list then ~ became mode %v", f.Mode)
	}
	for name, want := range map[string]bool{
		"read": true, "openat": true, "execve": true,
		"setuid": false, "chown": false, "setrlimit": false, "sched_setaffinity": false,
	} {
		if got := slices.Contains(f.Syscalls, name); got != want {
			t.Errorf("@system-service ~@privileged @resources: %s allowed = %v, want %v", name, got, want)
		}
	}

	// Two deny lines join into one deny-list.
	f = build("SystemCallFilter=~@mount\nSystemCallFilter=~@debug")
	if f.Mode != seccomp.ModeDeny || !slices.Contains(f.Syscalls, "mount") || !slices.Contains(f.Syscalls, "ptrace") {
		t.Errorf("~@mount + ~@debug: mode %v, syscalls %v", f.Mode, f.Syscalls)
	}

	// A deny-list then an allow line lets that syscall through again.
	f = build("SystemCallFilter=~@mount\nSystemCallFilter=mount")
	if f.Mode != seccomp.ModeDeny || slices.Contains(f.Syscalls, "mount") || !slices.Contains(f.Syscalls, "umount2") {
		t.Errorf("~@mount + mount: mode %v, syscalls %v", f.Mode, f.Syscalls)
	}

	// An empty line resets.
	desc, _ := convertWarns(t, "[Service]\nExecStart=/bin/true\nSystemCallFilter=~@mount\nSystemCallFilter=\nSystemCallFilter=@basic-io\n")
	if len(desc.SystemCallFilter) != 1 || desc.SystemCallFilter[0] != "@basic-io" {
		t.Errorf("reset then @basic-io: %q", desc.SystemCallFilter)
	}

	// Taking part of @default out of an allow-list cannot be written:
	// slinit's allow-list always admits @default.
	path := filepath.Join(t.TempDir(), "u.service")
	os.WriteFile(path, []byte("[Service]\nExecStart=/bin/true\nSystemCallFilter=@system-service\nSystemCallFilter=~@sandbox\n"), 0o644)
	if _, _, err := ConvertSystemdUnit(path); err == nil {
		t.Error("~@sandbox (seccomp is in @default) after an allow-list converted; want an error")
	}
}

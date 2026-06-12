package config

import (
	"strings"
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// TestParseHardeningAll exercises every Restrict*/Protect* stanza in
// one description and verifies the bool fields land on the right place.
func TestParseHardeningAll(t *testing.T) {
	input := `type = process
command = /usr/bin/svc
protect-kernel-tunables = yes
protect-kernel-modules = yes
protect-kernel-logs = yes
protect-clock = yes
protect-control-groups = yes
protect-hostname = yes
lock-personality = yes
`
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !desc.ProtectKernelTunables || !desc.ProtectKernelModules ||
		!desc.ProtectKernelLogs || !desc.ProtectClock ||
		!desc.ProtectControlGroups || !desc.ProtectHostname ||
		!desc.LockPersonality {
		t.Errorf("not all hardening flags set: %+v", desc)
	}
}

// TestParseHardeningRejectsJunk ensures a non-bool value fails the
// parse rather than silently leaving the knob off.
func TestParseHardeningRejectsJunk(t *testing.T) {
	input := "type = process\ncommand = /bin/true\nprotect-clock = maybe\n"
	if _, err := Parse(strings.NewReader(input), "svc", "test-file"); err == nil {
		t.Fatal("expected error for non-bool value")
	}
}

// TestHardeningFlowsToRecord checks the loader stores the config on
// the ServiceRecord and auto-implies CLONE_NEWNS for the mount-based
// knobs (kernel-tunables / control-groups / kernel-logs).
func TestHardeningFlowsToRecord(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "hardened",
		"type = process\ncommand = /usr/bin/svc\n"+
			"protect-kernel-tunables = yes\n"+
			"protect-control-groups = yes\n"+
			"protect-clock = yes\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	svc, err := loader.LoadService("hardened")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	rec := svc.Record()
	if !rec.HardeningActive() {
		t.Fatal("HardeningActive() should be true")
	}
	cfg := rec.Hardening()
	if !cfg.ProtectKernelTunables || !cfg.ProtectControlGroups || !cfg.ProtectClock {
		t.Errorf("Hardening fields missing: %+v", cfg)
	}
	if rec.Cloneflags()&syscall.CLONE_NEWNS == 0 {
		t.Errorf("CLONE_NEWNS not auto-implied (cloneflags=0x%x)", rec.Cloneflags())
	}
}

// TestHardeningSeccompOnlyDoesNotForceNS verifies that the pure
// seccomp knobs (no mount op) do NOT force a mount namespace.
// Creating a NS unnecessarily would impose mount setup overhead on
// services that only need syscall filtering.
func TestHardeningSeccompOnlyDoesNotForceNS(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "seccomp-only",
		"type = process\ncommand = /bin/true\n"+
			"protect-clock = yes\nprotect-hostname = yes\n"+
			"lock-personality = yes\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	svc, err := loader.LoadService("seccomp-only")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	rec := svc.Record()
	if !rec.HardeningActive() {
		t.Fatal("HardeningActive should be true")
	}
	if rec.Cloneflags()&syscall.CLONE_NEWNS != 0 {
		t.Errorf("CLONE_NEWNS should NOT be auto-implied for seccomp-only knobs (got 0x%x)",
			rec.Cloneflags())
	}
}

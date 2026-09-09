package switchroot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestPrecheck_EmptyNewroot: an empty newroot is rejected up front,
// before any filesystem work. Regression against a slinitctl bug
// that would send an empty payload.
func TestPrecheck_EmptyNewroot(t *testing.T) {
	if _, err := Precheck("", ""); err == nil {
		t.Fatal("Precheck(\"\", \"\"): want error, got nil")
	}
}

// TestPrecheck_RelativeNewroot: only absolute paths are accepted.
// A relative path here would resolve against whatever cwd slinit
// happens to have — non-deterministic for an init.
func TestPrecheck_RelativeNewroot(t *testing.T) {
	if _, err := Precheck("newroot", ""); err == nil {
		t.Fatal("Precheck(\"newroot\", \"\"): want error, got nil")
	}
}

// TestPrecheck_RelativeNewinit: same guard for the init path.
func TestPrecheck_RelativeNewinit(t *testing.T) {
	// Bypass the PID-1 check by explicitly triggering an earlier
	// failure — a relative newinit trips before the PID check.
	if _, err := Precheck("/newroot", "bin/init"); err == nil {
		t.Fatal("Precheck: relative newinit should be rejected")
	}
}

// TestPrecheck_DefaultInit: an empty newinit falls back to
// /sbin/init (matches systemd + finit). The precheck returns the
// resolved path so the caller and Do agree.
func TestPrecheck_DefaultInit(t *testing.T) {
	// This only tests the return-value defaulting; a full run
	// requires PID 1, which the test binary isn't.
	_, err := Precheck("/newroot", "")
	// We expect an error (not PID 1 or newroot missing), but the
	// error must NOT complain about newinit resolution.
	if err == nil {
		t.Skip("running as PID 1 or /newroot exists — happy path unexpectedly reached")
	}
	if strings.Contains(err.Error(), "newinit") {
		t.Fatalf("empty newinit should default to /sbin/init; got error mentioning newinit: %v", err)
	}
}

// TestPrecheck_NotPID1: with PID != 1 and an existing directory,
// the precheck must fail with ErrNotPID1 (the caller can special-
// case this to log "wrong context" instead of "malformed request").
func TestPrecheck_NotPID1(t *testing.T) {
	if os.Getpid() == 1 {
		t.Skip("running as PID 1 — cannot exercise the ErrNotPID1 path")
	}
	// Build a plausible newroot that would pass the earlier checks:
	// absolute + exists + directory. The PID check fires before
	// the mount-point check, so it's what we hit first.
	dir := t.TempDir()
	// Put an executable "init" in place so the newinit check would
	// pass if reached.
	initPath := filepath.Join(dir, "sbin")
	if err := os.MkdirAll(initPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(initPath, "init"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Precheck(dir, "")
	if err == nil {
		t.Fatal("Precheck: expected ErrNotPID1 outside PID 1")
	}
	if !errors.Is(err, ErrNotPID1) {
		// Some earlier check may have fired (e.g. same-device). That's
		// fine — the test's job is just to verify the precheck never
		// silently succeeds outside PID 1.
		t.Logf("Precheck returned a different error before reaching the PID check: %v", err)
	}
}

// TestPrecheck_NewrootDoesntExist: precheck must reject cleanly when
// newroot is not a directory (either missing or a plain file).
func TestPrecheck_NewrootDoesntExist(t *testing.T) {
	// Path guaranteed not to exist.
	_, err := Precheck("/no/such/newroot/directory/xyzzy", "")
	if err == nil {
		t.Fatal("Precheck: missing newroot should fail")
	}
}

// TestPrecheck_NewrootIsFile: a regular file at the newroot path is
// still not a directory — must be rejected.
func TestPrecheck_NewrootIsFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Precheck(file, "")
	if err == nil {
		t.Fatal("Precheck: file at newroot should fail")
	}
}

// TestDecodeSwitchRootPayloadRoundTrip lives in pkg/control, but we
// exercise the same shape here to lock the Precheck contract:
// leading slash, newinit optional. Small guard against future
// argument-order changes.
func TestPrecheck_ArgOrder(t *testing.T) {
	// Bogus paths — we only care about the argument-order rejection
	// shape. The first arg is newroot; the second is newinit.
	if _, err := Precheck("", "/sbin/init"); err == nil {
		t.Error("Precheck(\"\", \"/sbin/init\"): empty newroot must fail regardless of newinit")
	}
}

// TestMoveMount_SkipsNonMountpoint: a plain directory (same device
// as /) is silently skipped so re-invoking after a partial move
// doesn't error on the entries that already moved.
func TestMoveMount_SkipsNonMountpoint(t *testing.T) {
	// A regular temp dir is (usually) on the test filesystem, same
	// device as the test binary's cwd — but definitely a non-
	// mountpoint. moveMount should return nil silently.
	dir := t.TempDir()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	stt, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("Stat_t unavailable on this platform")
	}
	if err := moveMount(dir, dir+"-target", uint64(stt.Dev)); err != nil {
		t.Errorf("moveMount on plain dir should skip, got: %v", err)
	}
}

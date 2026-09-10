package hooks

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
)

// TestRunRcLocal_MissingBoth: no /etc/rc.local, no /etc/rc.local.d
// → silent no-op. Guards the zero-config default for hosts that
// never touched the legacy files.
func TestRunRcLocal_MissingBoth(t *testing.T) {
	dir := t.TempDir()
	orig1, orig2 := rcLocalPath, rcLocalDPath
	rcLocalPath = filepath.Join(dir, "no-rc.local")
	rcLocalDPath = filepath.Join(dir, "no-rc.local.d")
	defer func() { rcLocalPath, rcLocalDPath = orig1, orig2 }()

	logger := logging.New(logging.LevelDebug)
	ran, failed := RunRcLocal(logger)
	if ran != 0 || failed != 0 {
		t.Errorf("missing both → ran=%d failed=%d, want 0/0", ran, failed)
	}
}

// TestRunRcLocal_ExecutesMonolithic: /etc/rc.local exists +
// executable → runs. Marker file proves it did.
func TestRunRcLocal_ExecutesMonolithic(t *testing.T) {
	dir := t.TempDir()
	rc := filepath.Join(dir, "rc.local")
	marker := filepath.Join(dir, "rc-ran")
	script := "#!/bin/sh\ntouch " + marker + "\n"
	if err := os.WriteFile(rc, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	orig1, orig2 := rcLocalPath, rcLocalDPath
	rcLocalPath = rc
	rcLocalDPath = filepath.Join(dir, "no-d")
	defer func() { rcLocalPath, rcLocalDPath = orig1, orig2 }()

	logger := logging.New(logging.LevelDebug)
	ran, failed := RunRcLocal(logger)
	if ran != 1 || failed != 0 {
		t.Errorf("ran=%d failed=%d, want 1/0", ran, failed)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("rc.local marker not written: %v", err)
	}
}

// TestRunRcLocal_NonExecutableSkipped: rc.local exists but bit
// missing → silent skip. Matches Finit's contract; also protects
// against a checked-in-git rc.local that lost +x on a Windows edit.
func TestRunRcLocal_NonExecutableSkipped(t *testing.T) {
	dir := t.TempDir()
	rc := filepath.Join(dir, "rc.local")
	if err := os.WriteFile(rc, []byte("#!/bin/sh\ntrue\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig1, orig2 := rcLocalPath, rcLocalDPath
	rcLocalPath = rc
	rcLocalDPath = filepath.Join(dir, "no-d")
	defer func() { rcLocalPath, rcLocalDPath = orig1, orig2 }()

	logger := logging.New(logging.LevelDebug)
	ran, failed := RunRcLocal(logger)
	if ran != 0 || failed != 0 {
		t.Errorf("non-exec rc.local → ran=%d failed=%d, want 0/0", ran, failed)
	}
}

// TestRunRcLocal_DirOrderingBeforeMonolithic: /etc/rc.local.d/*
// runs BEFORE the monolithic /etc/rc.local, matching Debian's
// rc-local.service drop-in convention. Regression against a
// naive "monolithic first" flip.
func TestRunRcLocal_DirOrderingBeforeMonolithic(t *testing.T) {
	dir := t.TempDir()
	dDir := filepath.Join(dir, "rc.local.d")
	_ = os.MkdirAll(dDir, 0o755)
	monolithic := filepath.Join(dir, "rc.local")
	marker := filepath.Join(dir, "order")
	dropScript := "#!/bin/sh\necho drop >> " + marker + "\n"
	monoScript := "#!/bin/sh\necho mono >> " + marker + "\n"
	if err := os.WriteFile(filepath.Join(dDir, "10-first"), []byte(dropScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(monolithic, []byte(monoScript), 0o755); err != nil {
		t.Fatal(err)
	}
	orig1, orig2 := rcLocalPath, rcLocalDPath
	rcLocalPath = monolithic
	rcLocalDPath = dDir
	defer func() { rcLocalPath, rcLocalDPath = orig1, orig2 }()

	logger := logging.New(logging.LevelDebug)
	ran, _ := RunRcLocal(logger)
	if ran != 2 {
		t.Errorf("ran=%d, want 2 (drop-in + monolithic)", ran)
	}
	data, _ := os.ReadFile(marker)
	if string(data) != "drop\nmono\n" {
		t.Errorf("order = %q, want %q", string(data), "drop\nmono\n")
	}
}

// TestRunRcLocal_FailureCounted: rc.local exits non-zero → logged
// but doesn't abort. Best-effort contract matches hooks.Run.
func TestRunRcLocal_FailureCounted(t *testing.T) {
	dir := t.TempDir()
	rc := filepath.Join(dir, "rc.local")
	if err := os.WriteFile(rc, []byte("#!/bin/sh\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig1, orig2 := rcLocalPath, rcLocalDPath
	rcLocalPath = rc
	rcLocalDPath = filepath.Join(dir, "no-d")
	defer func() { rcLocalPath, rcLocalDPath = orig1, orig2 }()

	logger := logging.New(logging.LevelDebug)
	ran, failed := RunRcLocal(logger)
	if ran != 1 || failed != 1 {
		t.Errorf("failing rc.local → ran=%d failed=%d, want 1/1", ran, failed)
	}
}

// TestRunRcLocal_HookPointEnv: SLINIT_HOOK_POINT is set to
// "rc-local" so a shared script can branch cleanly on whether it
// was invoked via hooks.d or the legacy well-known path.
func TestRunRcLocal_HookPointEnv(t *testing.T) {
	dir := t.TempDir()
	rc := filepath.Join(dir, "rc.local")
	marker := filepath.Join(dir, "point")
	if err := os.WriteFile(rc,
		[]byte("#!/bin/sh\necho -n \"$SLINIT_HOOK_POINT\" > "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig1, orig2 := rcLocalPath, rcLocalDPath
	rcLocalPath = rc
	rcLocalDPath = filepath.Join(dir, "no-d")
	defer func() { rcLocalPath, rcLocalDPath = orig1, orig2 }()

	logger := logging.New(logging.LevelDebug)
	RunRcLocal(logger)
	data, _ := os.ReadFile(marker)
	if string(data) != "rc-local" {
		t.Errorf("SLINIT_HOOK_POINT = %q, want %q", string(data), "rc-local")
	}
}

// TestRunRcLocal_TimeoutKillsHang: hung script gets killed at the
// configured timeout, counted as failed, boot proceeds. Uses
// SetRcLocalTimeout so the test doesn't wait 5 minutes.
func TestRunRcLocal_TimeoutKillsHang(t *testing.T) {
	dir := t.TempDir()
	rc := filepath.Join(dir, "rc.local")
	if err := os.WriteFile(rc, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig1, orig2, origT := rcLocalPath, rcLocalDPath, rcLocalTimeout
	rcLocalPath = rc
	rcLocalDPath = filepath.Join(dir, "no-d")
	SetRcLocalTimeout(500 * time.Millisecond)
	defer func() {
		rcLocalPath, rcLocalDPath = orig1, orig2
		SetRcLocalTimeout(origT)
	}()

	logger := logging.New(logging.LevelDebug)
	start := time.Now()
	ran, failed := RunRcLocal(logger)
	elapsed := time.Since(start)
	if ran != 1 || failed != 1 {
		t.Errorf("ran=%d failed=%d, want 1/1", ran, failed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("elapsed=%v, timeout should have fired at 500ms", elapsed)
	}
}

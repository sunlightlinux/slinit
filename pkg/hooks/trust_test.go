package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
)

// writeMarkerScript writes an executable script at path that creates
// marker when it runs, then sets path's mode to mode.
func writeMarkerScript(t *testing.T, path, marker string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func ranScript(marker string) bool {
	_, err := os.Stat(marker)
	return err == nil
}

// Every hook runs as root, so a script anyone else can write is a way
// to run code as root. A group- or world-writable script used to run.
func TestRun_SkipsWritableScript(t *testing.T) {
	dir := t.TempDir()
	pointDir := filepath.Join(dir, "system-up")
	if err := os.MkdirAll(pointDir, 0o755); err != nil {
		t.Fatal(err)
	}
	SetHooksDir(dir)
	defer SetHooksDir("/etc/slinit/hooks.d")

	bad := filepath.Join(dir, "bad")
	good := filepath.Join(dir, "good")
	writeMarkerScript(t, filepath.Join(pointDir, "10-world"), bad, 0o757)
	writeMarkerScript(t, filepath.Join(pointDir, "20-group"), bad, 0o775)
	writeMarkerScript(t, filepath.Join(pointDir, "30-ok"), good, 0o755)

	ran, _ := Run("system-up", logging.New(logging.LevelDebug))
	if ranScript(bad) {
		t.Error("a group- or world-writable hook script ran")
	}
	if !ranScript(good) || ran != 1 {
		t.Errorf("ran=%d, good script ran=%v; want 1, true", ran, ranScript(good))
	}
}

// Whoever can write the hook-point directory can swap a script in it,
// so a writable directory runs nothing.
func TestRun_SkipsWritableDir(t *testing.T) {
	dir := t.TempDir()
	pointDir := filepath.Join(dir, "system-up")
	if err := os.MkdirAll(pointDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(pointDir, 0o777); err != nil {
		t.Fatal(err)
	}
	SetHooksDir(dir)
	defer SetHooksDir("/etc/slinit/hooks.d")

	marker := filepath.Join(dir, "ran")
	writeMarkerScript(t, filepath.Join(pointDir, "10-x"), marker, 0o755)

	if ran, _ := Run("system-up", logging.New(logging.LevelDebug)); ran != 0 || ranScript(marker) {
		t.Errorf("hook in a world-writable dir ran (ran=%d)", ran)
	}
}

// A symlinked hook-point directory is refused: the link's own mode is
// always 0777 and its target is not what was checked.
func TestRun_SkipsSymlinkedDir(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "system-up")); err != nil {
		t.Fatal(err)
	}
	SetHooksDir(dir)
	defer SetHooksDir("/etc/slinit/hooks.d")

	marker := filepath.Join(dir, "ran")
	writeMarkerScript(t, filepath.Join(target, "10-x"), marker, 0o755)

	if ran, _ := Run("system-up", logging.New(logging.LevelDebug)); ran != 0 || ranScript(marker) {
		t.Errorf("hook in a symlinked dir ran (ran=%d)", ran)
	}
}

// rc.local and rc.local.d run as root too: the same checks apply.
func TestRunRcLocal_SkipsWritable(t *testing.T) {
	dir := t.TempDir()
	orig1, orig2 := rcLocalPath, rcLocalDPath
	rcLocalPath = filepath.Join(dir, "rc.local")
	rcLocalDPath = filepath.Join(dir, "rc.local.d")
	defer func() { rcLocalPath, rcLocalDPath = orig1, orig2 }()
	if err := os.MkdirAll(rcLocalDPath, 0o755); err != nil {
		t.Fatal(err)
	}

	bad := filepath.Join(dir, "bad")
	good := filepath.Join(dir, "good")
	writeMarkerScript(t, rcLocalPath, bad, 0o777)
	writeMarkerScript(t, filepath.Join(rcLocalDPath, "10-world"), bad, 0o757)
	writeMarkerScript(t, filepath.Join(rcLocalDPath, "20-ok"), good, 0o755)

	logger := logging.New(logging.LevelDebug)
	ran, _ := RunRcLocal(logger)
	if ranScript(bad) {
		t.Error("a world-writable rc.local script ran")
	}
	if !ranScript(good) || ran != 1 {
		t.Errorf("ran=%d, good script ran=%v; want 1, true", ran, ranScript(good))
	}

	// A writable rc.local.d runs none of its drop-ins.
	os.Remove(good)
	if err := os.Chmod(rcLocalDPath, 0o775); err != nil {
		t.Fatal(err)
	}
	if ran, _ := RunRcLocal(logger); ran != 0 || ranScript(good) {
		t.Errorf("drop-in in a group-writable rc.local.d ran (ran=%d)", ran)
	}
}

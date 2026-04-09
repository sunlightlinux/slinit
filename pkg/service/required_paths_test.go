package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckRequiredPathsAllPresent verifies that CheckRequiredPaths returns
// nil when every configured path exists with the right type.
func TestCheckRequiredPathsAllPresent(t *testing.T) {
	dir := t.TempDir()
	goodDir := filepath.Join(dir, "sub")
	if err := os.Mkdir(goodDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	goodFile := filepath.Join(dir, "conf")
	if err := os.WriteFile(goodFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var rec ServiceRecord
	rec.SetRequiredPaths([]string{goodFile}, []string{goodDir})
	if err := rec.CheckRequiredPaths(); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

// TestCheckRequiredPathsMissingDir verifies the error message points at the
// missing directory path — operators must be able to grep the logs for the
// exact path that failed.
func TestCheckRequiredPathsMissingDir(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope")

	var rec ServiceRecord
	rec.SetRequiredPaths(nil, []string{missing})
	err := rec.CheckRequiredPaths()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error %q should mention %q", err, missing)
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error %q should say 'does not exist'", err)
	}
}

// TestCheckRequiredPathsMissingFile checks the file-missing branch.
func TestCheckRequiredPathsMissingFile(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.conf")

	var rec ServiceRecord
	rec.SetRequiredPaths([]string{missing}, nil)
	err := rec.CheckRequiredPaths()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error %q should mention %q", err, missing)
	}
}

// TestCheckRequiredPathsDirIsFile rejects a required_dirs entry that exists
// but is actually a regular file (type mismatch).
func TestCheckRequiredPathsDirIsFile(t *testing.T) {
	dir := t.TempDir()
	notADir := filepath.Join(dir, "oops")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var rec ServiceRecord
	rec.SetRequiredPaths(nil, []string{notADir})
	err := rec.CheckRequiredPaths()
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("expected 'not a directory' error, got %v", err)
	}
}

// TestCheckRequiredPathsFileIsDir rejects a required_files entry that is
// actually a directory.
func TestCheckRequiredPathsFileIsDir(t *testing.T) {
	dir := t.TempDir()
	notAFile := filepath.Join(dir, "actually-dir")
	if err := os.Mkdir(notAFile, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var rec ServiceRecord
	rec.SetRequiredPaths([]string{notAFile}, nil)
	err := rec.CheckRequiredPaths()
	if err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("expected 'is a directory' error, got %v", err)
	}
}

// TestCheckRequiredPathsUnreadable ensures a mode-0 file is flagged when the
// test doesn't run as root (root can read anything, skip in that case).
func TestCheckRequiredPathsUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file mode bits")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.WriteFile(locked, []byte("x"), 0o000); err != nil {
		t.Fatalf("write: %v", err)
	}

	var rec ServiceRecord
	rec.SetRequiredPaths([]string{locked}, nil)
	err := rec.CheckRequiredPaths()
	if err == nil || !strings.Contains(err.Error(), "not readable") {
		t.Errorf("expected 'not readable' error, got %v", err)
	}
}

// TestCheckRequiredPathsEmpty verifies an unconfigured record passes.
func TestCheckRequiredPathsEmpty(t *testing.T) {
	var rec ServiceRecord
	if err := rec.CheckRequiredPaths(); err != nil {
		t.Errorf("empty record should pass, got %v", err)
	}
}

// TestSetRequiredPathsClearsOnNil verifies that passing nil clears previously
// set paths — important for reload semantics where an operator may remove
// required_files from the config.
func TestSetRequiredPathsClearsOnNil(t *testing.T) {
	var rec ServiceRecord
	rec.SetRequiredPaths([]string{"/a"}, []string{"/b"})
	rec.SetRequiredPaths(nil, nil)
	if len(rec.RequiredFiles()) != 0 || len(rec.RequiredDirs()) != 0 {
		t.Errorf("expected cleared, got files=%v dirs=%v",
			rec.RequiredFiles(), rec.RequiredDirs())
	}
}

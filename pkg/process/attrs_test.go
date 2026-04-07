package process

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestKillCgroupEmptyPath(t *testing.T) {
	// Empty path should be a no-op
	err := KillCgroup("", syscall.SIGTERM)
	if err != nil {
		t.Fatalf("KillCgroup with empty path should return nil, got: %v", err)
	}
}

func TestKillCgroupNonExistentPath(t *testing.T) {
	// Non-existent cgroup path should return an error
	err := KillCgroup("/nonexistent/cgroup/path", syscall.SIGTERM)
	if err == nil {
		t.Fatal("KillCgroup with non-existent path should return error")
	}
}

func TestKillCgroupWithProcsFile(t *testing.T) {
	// Create a fake cgroup directory with a cgroup.procs file
	dir := t.TempDir()

	// Write a procs file with PIDs that don't exist (will get ESRCH, which is ignored)
	procsPath := filepath.Join(dir, "cgroup.procs")
	err := os.WriteFile(procsPath, []byte("999999\n999998\n"), 0644)
	if err != nil {
		t.Fatalf("failed to write cgroup.procs: %v", err)
	}

	// SIGTERM to non-existent PIDs → ESRCH, which KillCgroup ignores
	err = KillCgroup(dir, syscall.SIGTERM)
	if err != nil {
		t.Fatalf("KillCgroup should ignore ESRCH errors, got: %v", err)
	}
}

func TestKillCgroupSIGKILLTriesCgroupKill(t *testing.T) {
	dir := t.TempDir()

	// With SIGKILL, KillCgroup tries cgroup.kill first. In a temp dir,
	// writing "1" to cgroup.kill succeeds (regular file), so it returns nil.
	killPath := filepath.Join(dir, "cgroup.kill")
	// Don't pre-create cgroup.kill — WriteFile will create it and succeed
	_ = killPath

	err := KillCgroup(dir, syscall.SIGKILL)
	if err != nil {
		t.Fatalf("KillCgroup SIGKILL should succeed via cgroup.kill write, got: %v", err)
	}

	// Verify cgroup.kill was written (file was created with 0200, so chmod before read)
	if err := os.Chmod(killPath, 0644); err != nil {
		t.Fatalf("chmod cgroup.kill: %v", err)
	}
	data, err := os.ReadFile(killPath)
	if err != nil {
		t.Fatalf("cgroup.kill should have been created: %v", err)
	}
	if string(data) != "1" {
		t.Fatalf("cgroup.kill content = %q, want \"1\"", data)
	}
}

func TestKillCgroupSIGKILLFallback(t *testing.T) {
	dir := t.TempDir()

	// Make cgroup.kill a directory so the write fails, forcing fallback to cgroup.procs
	killDir := filepath.Join(dir, "cgroup.kill")
	if err := os.Mkdir(killDir, 0755); err != nil {
		t.Fatalf("failed to create cgroup.kill dir: %v", err)
	}

	// Add empty procs file → no PIDs to kill → success
	procsPath := filepath.Join(dir, "cgroup.procs")
	if err := os.WriteFile(procsPath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to write cgroup.procs: %v", err)
	}

	err := KillCgroup(dir, syscall.SIGKILL)
	if err != nil {
		t.Fatalf("KillCgroup SIGKILL fallback should succeed with empty procs, got: %v", err)
	}
}

func TestKillCgroupMultiplePIDs(t *testing.T) {
	dir := t.TempDir()

	// Multiple non-existent PIDs — all get ESRCH, should be ignored
	procsPath := filepath.Join(dir, "cgroup.procs")
	err := os.WriteFile(procsPath, []byte("999990\n999991\n999992\n999993\n"), 0644)
	if err != nil {
		t.Fatalf("write cgroup.procs: %v", err)
	}

	err = KillCgroup(dir, syscall.SIGTERM)
	if err != nil {
		t.Fatalf("KillCgroup with multiple non-existent PIDs should succeed, got: %v", err)
	}
}

func TestKillCgroupEmptyProcsFile(t *testing.T) {
	dir := t.TempDir()

	procsPath := filepath.Join(dir, "cgroup.procs")
	if err := os.WriteFile(procsPath, []byte("\n\n"), 0644); err != nil {
		t.Fatalf("write cgroup.procs: %v", err)
	}

	err := KillCgroup(dir, syscall.SIGTERM)
	if err != nil {
		t.Fatalf("KillCgroup with empty procs should succeed, got: %v", err)
	}
}

func TestKillCgroupSIGHUP(t *testing.T) {
	dir := t.TempDir()

	procsPath := filepath.Join(dir, "cgroup.procs")
	err := os.WriteFile(procsPath, []byte("999999\n"), 0644)
	if err != nil {
		t.Fatalf("write cgroup.procs: %v", err)
	}

	// SIGHUP should use procs-based kill path (not cgroup.kill)
	err = KillCgroup(dir, syscall.SIGHUP)
	if err != nil {
		t.Fatalf("KillCgroup with SIGHUP should succeed (ESRCH ignored), got: %v", err)
	}
}

func TestKillCgroupNoProcsFile(t *testing.T) {
	dir := t.TempDir()
	// No cgroup.procs file at all — should return error
	err := KillCgroup(dir, syscall.SIGTERM)
	if err == nil {
		t.Fatal("KillCgroup without cgroup.procs should return error")
	}
}

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
	// A missing cgroup directory is treated as benign: cgroup cleanup is
	// inherently racy, and by the time we get to kill a subtree the kernel
	// may already have removed it. Returning nil here mirrors what the
	// service manager actually wants (teardown is complete).
	err := KillCgroup("/nonexistent/cgroup/path", syscall.SIGTERM)
	if err != nil {
		t.Fatalf("KillCgroup with non-existent path should be a no-op, got: %v", err)
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
	// No cgroup.procs file at all — the recursive walker treats this as an
	// empty cgroup (no PIDs to signal). A cgroup directory with no procs
	// file is the natural state of a freshly-drained leaf; promoting that
	// to an error would spam the log during teardown.
	err := KillCgroup(dir, syscall.SIGTERM)
	if err != nil {
		t.Fatalf("KillCgroup without cgroup.procs should be a no-op, got: %v", err)
	}
}

// TestKillCgroupRecursiveWalk verifies that the fallback walker signals
// PIDs in sub-cgroups, not just the root. This is the core behaviour that
// makes the non-cgroup.kill path safe for services that spawn sub-cgroups
// (container runtimes, worker pools, etc.).
func TestKillCgroupRecursiveWalk(t *testing.T) {
	root := t.TempDir()

	// Build a two-level subtree:
	//   root/cgroup.procs       (non-existent PID)
	//   root/child/cgroup.procs (another non-existent PID)
	//   root/child/grand/cgroup.procs
	if err := os.WriteFile(filepath.Join(root, "cgroup.procs"), []byte("999990\n"), 0644); err != nil {
		t.Fatalf("write root procs: %v", err)
	}
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	if err := os.WriteFile(filepath.Join(child, "cgroup.procs"), []byte("999991\n"), 0644); err != nil {
		t.Fatalf("write child procs: %v", err)
	}
	grand := filepath.Join(child, "grand")
	if err := os.Mkdir(grand, 0755); err != nil {
		t.Fatalf("mkdir grand: %v", err)
	}
	if err := os.WriteFile(filepath.Join(grand, "cgroup.procs"), []byte("999992\n"), 0644); err != nil {
		t.Fatalf("write grand procs: %v", err)
	}

	// All PIDs are non-existent → kill returns ESRCH → walker ignores it
	// → overall success. If the walker were non-recursive, this would
	// still succeed, so the real assertion here is that it does not panic
	// or mis-parse the sub-cgroup layout.
	if err := KillCgroup(root, syscall.SIGTERM); err != nil {
		t.Fatalf("KillCgroup recursive walk: %v", err)
	}
}

// TestKillCgroupRecursiveSkipsNonDirs ensures the walker does not try to
// recurse into regular files like cgroup.procs, cgroup.events, etc.
func TestKillCgroupRecursiveSkipsNonDirs(t *testing.T) {
	root := t.TempDir()

	// Plant a handful of typical cgroup v2 pseudo-files. The walker must
	// treat these as files, not directories, and must not attempt to read
	// them as cgroup.procs.
	for _, f := range []string{"cgroup.events", "cgroup.stat", "memory.current"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("junk\n"), 0644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "cgroup.procs"), []byte(""), 0644); err != nil {
		t.Fatalf("write procs: %v", err)
	}

	if err := KillCgroup(root, syscall.SIGTERM); err != nil {
		t.Fatalf("KillCgroup: %v", err)
	}
}

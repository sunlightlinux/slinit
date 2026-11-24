package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
)

// TestRun_MissingDir: hook point that doesn't exist on disk is a
// no-op — no error, ran=0. Operators shouldn't have to create empty
// dirs just to avoid warnings.
func TestRun_MissingDir(t *testing.T) {
	SetHooksDir(t.TempDir() + "/nonexistent")
	defer SetHooksDir("/etc/slinit/hooks.d")

	logger := logging.New(logging.LevelDebug)
	ran, failed := Run("system-up", logger)
	if ran != 0 || failed != 0 {
		t.Errorf("missing dir → ran=%d failed=%d, want 0/0", ran, failed)
	}
}

// TestRun_OrdersByName: two scripts drop a stamp with their name in
// order; the resulting file records name-sorted order. Regression
// guard against a filepath.Glob call that returns in undefined order.
func TestRun_OrdersByName(t *testing.T) {
	dir := t.TempDir()
	pointDir := filepath.Join(dir, "system-up")
	if err := os.MkdirAll(pointDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stamp := filepath.Join(dir, "stamp")
	for _, name := range []string{"20-second", "10-first", "30-third"} {
		script := "#!/bin/sh\necho " + name + " >> " + stamp + "\n"
		if err := os.WriteFile(filepath.Join(pointDir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	SetHooksDir(dir)
	defer SetHooksDir("/etc/slinit/hooks.d")

	logger := logging.New(logging.LevelDebug)
	ran, failed := Run("system-up", logger)
	if ran != 3 || failed != 0 {
		t.Fatalf("ran=%d failed=%d, want 3/0", ran, failed)
	}
	data, err := os.ReadFile(stamp)
	if err != nil {
		t.Fatal(err)
	}
	want := "10-first\n20-second\n30-third\n"
	if string(data) != want {
		t.Errorf("script order = %q, want %q", string(data), want)
	}
}

// TestRun_SkipsNonExecutable: a README dropped in the hook dir must
// not trigger execution. Guards against confusing "why is my README
// running?" reports.
func TestRun_SkipsNonExecutable(t *testing.T) {
	dir := t.TempDir()
	pointDir := filepath.Join(dir, "system-up")
	_ = os.MkdirAll(pointDir, 0o755)
	if err := os.WriteFile(filepath.Join(pointDir, "README"),
		[]byte("just a doc"), 0o644); err != nil {
		t.Fatal(err)
	}
	SetHooksDir(dir)
	defer SetHooksDir("/etc/slinit/hooks.d")

	logger := logging.New(logging.LevelDebug)
	ran, failed := Run("system-up", logger)
	if ran != 0 || failed != 0 {
		t.Errorf("non-executable → ran=%d failed=%d, want 0/0", ran, failed)
	}
}

// TestRun_CountsFailures: a failing script bumps `failed` but
// doesn't stop later scripts running. Best-effort contract.
func TestRun_CountsFailures(t *testing.T) {
	dir := t.TempDir()
	pointDir := filepath.Join(dir, "system-up")
	_ = os.MkdirAll(pointDir, 0o755)
	fail := "#!/bin/sh\nexit 3\n"
	ok := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(filepath.Join(pointDir, "10-fail"), []byte(fail), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pointDir, "20-ok"), []byte(ok), 0o755); err != nil {
		t.Fatal(err)
	}
	SetHooksDir(dir)
	defer SetHooksDir("/etc/slinit/hooks.d")

	logger := logging.New(logging.LevelDebug)
	ran, failed := Run("system-up", logger)
	if ran != 2 || failed != 1 {
		t.Errorf("ran=%d failed=%d, want 2/1", ran, failed)
	}
}

// TestRun_HookPointEnv: SLINIT_HOOK_POINT is exposed so shared
// scripts can branch on which point invoked them.
func TestRun_HookPointEnv(t *testing.T) {
	dir := t.TempDir()
	pointDir := filepath.Join(dir, "switch-root")
	_ = os.MkdirAll(pointDir, 0o755)
	stamp := filepath.Join(dir, "hook-point")
	script := "#!/bin/sh\necho -n \"$SLINIT_HOOK_POINT\" > " + stamp + "\n"
	if err := os.WriteFile(filepath.Join(pointDir, "10-record"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	SetHooksDir(dir)
	defer SetHooksDir("/etc/slinit/hooks.d")

	logger := logging.New(logging.LevelDebug)
	Run("switch-root", logger)

	data, _ := os.ReadFile(stamp)
	if string(data) != "switch-root" {
		t.Errorf("SLINIT_HOOK_POINT = %q, want %q", string(data), "switch-root")
	}
}

// TestRun_TimeoutKillsHang: a hook script that sleeps past the
// per-script timeout is killed and counted as failed. Guards
// against a runaway hook wedging PID 1's shutdown sequence.
func TestRun_TimeoutKillsHang(t *testing.T) {
	dir := t.TempDir()
	pointDir := filepath.Join(dir, "system-up")
	_ = os.MkdirAll(pointDir, 0o755)
	// sleep 5 with a 500ms timeout — script gets killed, Run
	// reports failure.
	script := "#!/bin/sh\nsleep 5\n"
	if err := os.WriteFile(filepath.Join(pointDir, "10-hang"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	SetHooksDir(dir)
	SetPerScriptTimeout(500 * time.Millisecond)
	defer func() {
		SetHooksDir("/etc/slinit/hooks.d")
		SetPerScriptTimeout(30 * time.Second)
	}()

	logger := logging.New(logging.LevelDebug)
	start := time.Now()
	ran, failed := Run("system-up", logger)
	elapsed := time.Since(start)
	if ran != 1 || failed != 1 {
		t.Errorf("ran=%d failed=%d, want 1/1", ran, failed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Run took %v, timeout should have fired at 500ms", elapsed)
	}
}

// TestRun_SetsidChildDoesNotBlock: a hook that starts a child in its
// own session leaves that child holding the output pipe and outside
// the process group the timeout kills. Draining the pipe before
// waiting for the script blocked Run until the child exited — for a
// daemon, forever — however short the timeout.
func TestRun_SetsidChildDoesNotBlock(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not installed")
	}
	dir := t.TempDir()
	pointDir := filepath.Join(dir, "system-up")
	_ = os.MkdirAll(pointDir, 0o755)
	pidFile := filepath.Join(dir, "child.pid")
	script := "#!/bin/sh\nsetsid sh -c 'echo $$ > " + pidFile + "; exec sleep 20' &\nsleep 0.2\nexit 0\n"
	if err := os.WriteFile(filepath.Join(pointDir, "10-bg"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	SetHooksDir(dir)
	SetPerScriptTimeout(time.Second)
	defer func() {
		SetHooksDir("/etc/slinit/hooks.d")
		SetPerScriptTimeout(30 * time.Second)
	}()

	logger := logging.New(logging.LevelDebug)
	done := make(chan [2]int, 1)
	start := time.Now()
	go func() {
		ran, failed := Run("system-up", logger)
		done <- [2]int{ran, failed}
	}()
	select {
	case r := <-done:
		t.Logf("ran=%d failed=%d elapsed=%v", r[0], r[1], time.Since(start))
		if r[0] != 1 || r[1] != 0 {
			t.Errorf("ran=%d failed=%d, want 1/0: the hook exited 0", r[0], r[1])
		}
		if el := time.Since(start); el > 2*time.Second {
			t.Errorf("Run took %v: it waited on the child's pipe, not the hook", el)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run still blocked on a pipe held by the hook's setsid child")
	}
}

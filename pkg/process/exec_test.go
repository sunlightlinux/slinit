package process

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// --- Namespace / clone flags tests ---

func TestStartProcessWithUserNamespace(t *testing.T) {
	// CLONE_NEWUSER doesn't require CAP_SYS_ADMIN for unprivileged users
	params := ExecParams{
		Command:    []string{"/bin/id", "-u"},
		Cloneflags: syscall.CLONE_NEWUSER,
	}

	pid, ch, err := StartProcess(params)
	if err != nil {
		t.Skipf("user namespace not supported: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("expected positive PID, got %d", pid)
	}

	exit := <-ch
	if exit.ExecErr != nil {
		t.Skipf("exec failed in user ns: %v", exit.ExecErr)
	}
	// Process ran with default 1:1 UID mapping (ContainerID=0, HostID=current)
	if !exit.Status.Exited() {
		t.Errorf("process did not exit normally")
	}
}

func TestStartProcessWithCustomUidGidMappings(t *testing.T) {
	uid := os.Getuid()
	gid := os.Getgid()

	params := ExecParams{
		Command:    []string{"/bin/true"},
		Cloneflags: syscall.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: uid, Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: gid, Size: 1},
		},
	}

	pid, ch, err := StartProcess(params)
	if err != nil {
		t.Skipf("user namespace not supported: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("expected positive PID, got %d", pid)
	}

	exit := <-ch
	if exit.ExecErr != nil {
		t.Fatalf("exec failed: %v", exit.ExecErr)
	}
	if !exit.ExitedClean() {
		t.Errorf("process did not exit cleanly: %v", exit.Status)
	}
}

func TestStartProcessDefaultUidMappingWhenUserNS(t *testing.T) {
	// When CLONE_NEWUSER is set but no explicit mappings, default 1:1 should be used
	params := ExecParams{
		Command:    []string{"/bin/true"},
		Cloneflags: syscall.CLONE_NEWUSER,
		// No UidMappings/GidMappings — defaults should kick in
	}

	pid, ch, err := StartProcess(params)
	if err != nil {
		t.Skipf("user namespace not supported: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("expected positive PID, got %d", pid)
	}

	exit := <-ch
	if exit.ExecErr != nil {
		t.Fatalf("exec failed: %v", exit.ExecErr)
	}
	if !exit.ExitedClean() {
		t.Errorf("process did not exit cleanly")
	}
}

func TestStartProcessNoCloneflags(t *testing.T) {
	// Zero cloneflags should work normally
	params := ExecParams{
		Command: []string{"/bin/true"},
	}

	pid, ch, err := StartProcess(params)
	if err != nil {
		t.Fatalf("StartProcess failed: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("expected positive PID, got %d", pid)
	}

	exit := <-ch
	if !exit.ExitedClean() {
		t.Errorf("process did not exit cleanly")
	}
}

// --- Lock file tests ---

func TestStartProcessLockFile(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	params := ExecParams{
		Command:  []string{"/bin/true"},
		LockFile: lockPath,
	}

	pid, ch, err := StartProcess(params)
	if err != nil {
		t.Fatalf("StartProcess with lock-file failed: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("expected positive PID, got %d", pid)
	}

	exit := <-ch
	if !exit.ExitedClean() {
		t.Errorf("process with lock-file did not exit cleanly")
	}

	// Lock file should exist
	if _, err := os.Stat(lockPath); err != nil {
		t.Errorf("lock file should exist: %v", err)
	}
}

func TestStartProcessLockFileContention(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "contended.lock")

	// Hold the lock ourselves
	lockFD, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("open lock file: %v", err)
	}
	defer lockFD.Close()

	if err := syscall.Flock(int(lockFD.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock: %v", err)
	}

	// Try to start a process that needs the same lock — should fail
	params := ExecParams{
		Command:  []string{"/bin/true"},
		LockFile: lockPath,
	}

	_, _, err = StartProcess(params)
	if err == nil {
		t.Fatal("expected error when lock is held, got nil")
	}
}

// --- Chroot test ---

func TestStartProcessChroot(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("chroot requires root")
	}

	// Use /tmp as chroot target (it exists and has /bin/true in host)
	params := ExecParams{
		Command: []string{"/bin/true"},
		Chroot:  "/",
	}

	pid, ch, err := StartProcess(params)
	if err != nil {
		t.Fatalf("StartProcess with chroot failed: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("expected positive PID, got %d", pid)
	}

	exit := <-ch
	if !exit.ExitedClean() {
		t.Errorf("process with chroot did not exit cleanly")
	}
}

// --- Working directory test ---

func TestStartProcessWorkingDir(t *testing.T) {
	dir := t.TempDir()

	params := ExecParams{
		Command:    []string{"/bin/pwd"},
		WorkingDir: dir,
	}

	pid, ch, err := StartProcess(params)
	if err != nil {
		t.Fatalf("StartProcess with working dir failed: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("expected positive PID, got %d", pid)
	}

	exit := <-ch
	if !exit.ExitedClean() {
		t.Errorf("process with working dir did not exit cleanly")
	}
}

// --- New session test ---

func TestStartProcessNewSession(t *testing.T) {
	params := ExecParams{
		Command:    []string{"/bin/true"},
		NewSession: true,
	}

	pid, ch, err := StartProcess(params)
	if err != nil {
		t.Fatalf("StartProcess with new session failed: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("expected positive PID, got %d", pid)
	}

	exit := <-ch
	if !exit.ExitedClean() {
		t.Errorf("process with new session did not exit cleanly")
	}
}

// --- Environment test ---

func TestStartProcessEnv(t *testing.T) {
	params := ExecParams{
		Command: []string{"/bin/sh", "-c", "test \"$MY_TEST_VAR\" = hello"},
		Env:     []string{"MY_TEST_VAR=hello"},
	}

	pid, ch, err := StartProcess(params)
	if err != nil {
		t.Fatalf("StartProcess failed: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("expected positive PID, got %d", pid)
	}

	exit := <-ch
	if !exit.ExitedClean() {
		t.Errorf("env var check failed, exit status: %d", exit.Status.ExitStatus())
	}
}

// --- Signal handling test ---

func TestStartProcessSignalGroup(t *testing.T) {
	// Start a sleep process and kill its process group
	params := ExecParams{
		Command: []string{"/bin/sleep", "60"},
	}

	pid, ch, err := StartProcess(params)
	if err != nil {
		t.Fatalf("StartProcess failed: %v", err)
	}

	// Kill the process group
	syscall.Kill(-pid, syscall.SIGTERM)

	exit := <-ch
	if !exit.Signaled() {
		t.Errorf("expected signaled exit, got %v", exit.Status)
	}
}

// --- Empty command test ---

func TestStartProcessEmptyCommand(t *testing.T) {
	params := ExecParams{
		Command: []string{},
	}

	_, _, err := StartProcess(params)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestStartProcessNonexistentBinary(t *testing.T) {
	params := ExecParams{
		Command: []string{"/nonexistent/binary/path"},
	}

	_, _, err := StartProcess(params)
	if err == nil {
		t.Fatal("expected error for nonexistent binary")
	}
}

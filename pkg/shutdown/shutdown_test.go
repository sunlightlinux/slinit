package shutdown

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

func testLogger() *logging.Logger {
	return logging.New(logging.LevelError)
}

func TestKillAllProcesses(t *testing.T) {
	// Track syscall invocations
	var calls []struct {
		pid int
		sig syscall.Signal
	}

	origKill := killFunc
	killFunc = func(pid int, sig syscall.Signal) error {
		calls = append(calls, struct {
			pid int
			sig syscall.Signal
		}{pid, sig})
		return syscall.ESRCH // No processes to signal
	}
	defer func() { killFunc = origKill }()

	KillAllProcesses(testLogger())

	if len(calls) != 2 {
		t.Fatalf("Expected 2 kill calls, got %d", len(calls))
	}

	// First call: SIGTERM to all processes
	if calls[0].pid != -1 || calls[0].sig != syscall.SIGTERM {
		t.Fatalf("Expected kill(-1, SIGTERM), got kill(%d, %v)", calls[0].pid, calls[0].sig)
	}

	// Second call: SIGKILL to remaining processes
	if calls[1].pid != -1 || calls[1].sig != syscall.SIGKILL {
		t.Fatalf("Expected kill(-1, SIGKILL), got kill(%d, %v)", calls[1].pid, calls[1].sig)
	}
}

func TestRebootSystemHalt(t *testing.T) {
	var receivedCmd int
	origReboot := rebootFunc
	rebootFunc = func(cmd int) error {
		receivedCmd = cmd
		return nil
	}
	defer func() { rebootFunc = origReboot }()

	err := rebootSystem(service.ShutdownHalt)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if receivedCmd != syscall.LINUX_REBOOT_CMD_HALT {
		t.Fatalf("Expected HALT cmd, got %d", receivedCmd)
	}
}

func TestRebootSystemPoweroff(t *testing.T) {
	var receivedCmd int
	origReboot := rebootFunc
	rebootFunc = func(cmd int) error {
		receivedCmd = cmd
		return nil
	}
	defer func() { rebootFunc = origReboot }()

	err := rebootSystem(service.ShutdownPoweroff)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if receivedCmd != syscall.LINUX_REBOOT_CMD_POWER_OFF {
		t.Fatalf("Expected POWER_OFF cmd, got %d", receivedCmd)
	}
}

func TestRebootSystemReboot(t *testing.T) {
	var receivedCmd int
	origReboot := rebootFunc
	rebootFunc = func(cmd int) error {
		receivedCmd = cmd
		return nil
	}
	defer func() { rebootFunc = origReboot }()

	err := rebootSystem(service.ShutdownReboot)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if receivedCmd != syscall.LINUX_REBOOT_CMD_RESTART {
		t.Fatalf("Expected RESTART cmd, got %d", receivedCmd)
	}
}

func TestShutdownTypeMapping(t *testing.T) {
	origReboot := rebootFunc
	defer func() { rebootFunc = origReboot }()

	tests := []struct {
		shutType    service.ShutdownType
		expectedCmd int
	}{
		{service.ShutdownHalt, syscall.LINUX_REBOOT_CMD_HALT},
		{service.ShutdownPoweroff, syscall.LINUX_REBOOT_CMD_POWER_OFF},
		{service.ShutdownReboot, syscall.LINUX_REBOOT_CMD_RESTART},
		{service.ShutdownKexec, 0x45584543},                   // LINUX_REBOOT_CMD_KEXEC
		{service.ShutdownNone, syscall.LINUX_REBOOT_CMD_HALT}, // default fallback
	}

	for _, tt := range tests {
		var receivedCmd int
		rebootFunc = func(cmd int) error {
			receivedCmd = cmd
			return nil
		}

		err := rebootSystem(tt.shutType)
		if err != nil {
			t.Errorf("ShutdownType %s: unexpected error: %v", tt.shutType, err)
		}
		if receivedCmd != tt.expectedCmd {
			t.Errorf("ShutdownType %s: expected cmd %d, got %d", tt.shutType, tt.expectedCmd, receivedCmd)
		}
	}
}

func TestShutdownTypeArg(t *testing.T) {
	tests := []struct {
		st       service.ShutdownType
		expected string
	}{
		{service.ShutdownReboot, "reboot"},
		{service.ShutdownHalt, "halt"},
		{service.ShutdownPoweroff, "poweroff"},
		{service.ShutdownSoftReboot, "soft"},
		{service.ShutdownKexec, "kexec"},
		{service.ShutdownNone, "halt"},
	}

	for _, tt := range tests {
		got := shutdownTypeArg(tt.st)
		if got != tt.expected {
			t.Errorf("shutdownTypeArg(%s) = %q, want %q", tt.st, got, tt.expected)
		}
	}
}

func TestRunShutdownHookSuccess(t *testing.T) {
	// Create a temporary hook script that exits 0
	dir := t.TempDir()
	hookPath := filepath.Join(dir, "shutdown-hook")
	err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 0\n"), 0755)
	if err != nil {
		t.Fatalf("Failed to create hook: %v", err)
	}

	origPaths := shutdownHookPaths
	shutdownHookPaths = []string{hookPath}
	defer func() { shutdownHookPaths = origPaths }()

	logger := logging.New(logging.LevelDebug)
	result := runShutdownHook(service.ShutdownReboot, logger)
	if !result {
		t.Error("Expected hook to return true (success)")
	}
}

func TestRunShutdownHookFailure(t *testing.T) {
	// Create a hook that exits non-zero
	dir := t.TempDir()
	hookPath := filepath.Join(dir, "shutdown-hook")
	err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 1\n"), 0755)
	if err != nil {
		t.Fatalf("Failed to create hook: %v", err)
	}

	origPaths := shutdownHookPaths
	shutdownHookPaths = []string{hookPath}
	defer func() { shutdownHookPaths = origPaths }()

	logger := logging.New(logging.LevelDebug)
	result := runShutdownHook(service.ShutdownHalt, logger)
	if result {
		t.Error("Expected hook to return false (failure)")
	}
}

func TestRunShutdownHookNotFound(t *testing.T) {
	origPaths := shutdownHookPaths
	shutdownHookPaths = []string{"/nonexistent/path/shutdown-hook"}
	defer func() { shutdownHookPaths = origPaths }()

	logger := logging.New(logging.LevelDebug)
	result := runShutdownHook(service.ShutdownPoweroff, logger)
	if result {
		t.Error("Expected false when no hook found")
	}
}

func TestRunShutdownHookNotExecutable(t *testing.T) {
	// Create a hook that is NOT executable
	dir := t.TempDir()
	hookPath := filepath.Join(dir, "shutdown-hook")
	err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 0\n"), 0644)
	if err != nil {
		t.Fatalf("Failed to create hook: %v", err)
	}

	origPaths := shutdownHookPaths
	shutdownHookPaths = []string{hookPath}
	defer func() { shutdownHookPaths = origPaths }()

	logger := logging.New(logging.LevelDebug)
	result := runShutdownHook(service.ShutdownReboot, logger)
	if result {
		t.Error("Expected false when hook is not executable")
	}
}

func TestRunShutdownHookReceivesArg(t *testing.T) {
	// Create a hook that writes its argument to a file
	dir := t.TempDir()
	hookPath := filepath.Join(dir, "shutdown-hook")
	outFile := filepath.Join(dir, "arg.txt")
	script := "#!/bin/sh\necho \"$1\" > " + outFile + "\n"
	err := os.WriteFile(hookPath, []byte(script), 0755)
	if err != nil {
		t.Fatalf("Failed to create hook: %v", err)
	}

	origPaths := shutdownHookPaths
	shutdownHookPaths = []string{hookPath}
	defer func() { shutdownHookPaths = origPaths }()

	logger := logging.New(logging.LevelDebug)
	runShutdownHook(service.ShutdownPoweroff, logger)

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("Failed to read arg file: %v", err)
	}
	got := string(data)
	if got != "poweroff\n" {
		t.Errorf("Hook received arg %q, want %q", got, "poweroff\n")
	}
}

func TestExecuteWithHook(t *testing.T) {
	// Mock all syscalls to prevent real shutdown
	origKill := killFunc
	origSync := syncFunc
	origReboot := rebootFunc
	origHook := runHookFunc

	killFunc = func(pid int, sig syscall.Signal) error { return syscall.ESRCH }
	syncFunc = func() {}
	rebootFunc = func(cmd int) error { return nil }

	// Hook returns true → swapoff/umount should NOT be called
	hookCalled := false
	runHookFunc = func(st service.ShutdownType, l *logging.Logger) bool {
		hookCalled = true
		return true
	}

	defer func() {
		killFunc = origKill
		syncFunc = origSync
		rebootFunc = origReboot
		runHookFunc = origHook
	}()

	// Execute won't return (InfiniteHold) but rebootFunc returns nil
	// which leads to InfiniteHold - we can't test that, but we can
	// verify the hook was called by running in a goroutine with a timeout
	done := make(chan struct{})
	go func() {
		// Override rebootFunc to signal completion instead of holding
		rebootFunc = func(cmd int) error {
			close(done)
			// Block forever to prevent InfiniteHold from being reached
			select {}
		}
		Execute(service.ShutdownReboot, logging.New(logging.LevelError))
	}()

	<-done
	if !hookCalled {
		t.Error("Expected shutdown hook to be called")
	}
}

func TestSoftReboot(t *testing.T) {
	// Mock all syscall functions
	origKill := killFunc
	origSync := syncFunc
	origExec := execFunc

	killFunc = func(pid int, sig syscall.Signal) error { return syscall.ESRCH }
	syncFunc = func() {}

	var execCalled bool
	var execPath string
	execFunc = func(argv0 string, argv []string, envv []string) error {
		execCalled = true
		execPath = argv0
		return nil // In reality, Exec never returns on success
	}

	defer func() {
		killFunc = origKill
		syncFunc = origSync
		execFunc = origExec
	}()

	err := SoftReboot(testLogger())
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !execCalled {
		t.Fatal("Expected exec to be called")
	}
	if execPath == "" {
		t.Fatal("Expected non-empty exec path")
	}
}

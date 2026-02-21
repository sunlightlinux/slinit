package shutdown

import (
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

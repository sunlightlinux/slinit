package shutdown

import (
	"os"
	"os/exec"
	"testing"
)

// TestCrashRecoveryNoPanic verifies that CrashRecovery is a no-op when
// there is no panic — it returns normally without side effects.
func TestCrashRecoveryNoPanic(t *testing.T) {
	// Should not panic or call os.Exit
	func() {
		defer CrashRecovery(false, false)
	}()
}

// TestCrashRecoveryContainerMode verifies that a panic in container mode
// causes an exit with code 111. We use a subprocess to test os.Exit behavior.
func TestCrashRecoveryContainerMode(t *testing.T) {
	if os.Getenv("SLINIT_TEST_CRASH") == "container" {
		func() {
			defer CrashRecovery(false, true)
			panic("test crash")
		}()
		// Should not reach here
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestCrashRecoveryContainerMode")
	cmd.Env = append(os.Environ(), "SLINIT_TEST_CRASH=container")
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 111 {
		t.Errorf("exit code = %d, want 111", exitErr.ExitCode())
	}
}

// TestCrashRecoveryNonPID1 verifies that a panic in non-PID1 system mode
// causes an exit with code 111.
func TestCrashRecoveryNonPID1(t *testing.T) {
	if os.Getenv("SLINIT_TEST_CRASH") == "nonpid1" {
		func() {
			defer CrashRecovery(false, false)
			panic("test crash non-pid1")
		}()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestCrashRecoveryNonPID1")
	cmd.Env = append(os.Environ(), "SLINIT_TEST_CRASH=nonpid1")
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 111 {
		t.Errorf("exit code = %d, want 111", exitErr.ExitCode())
	}
}

// TestWriteConsoleFallback verifies that writeConsole does not panic
// when /dev/console is not available (falls back to stderr).
func TestWriteConsoleFallback(t *testing.T) {
	// On CI/test environments /dev/console may not exist —
	// writeConsole should silently fall back to stderr.
	writeConsole("test message\n")
}

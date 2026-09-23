package shutdown

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
)

// fdIdentity returns the device and inode fd currently points at, which is
// how these tests tell "fd 1 is the console again" from "fd 1 is still the
// catch-all pipe".
func fdIdentity(t *testing.T, fd int) (uint64, uint64) {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		t.Fatalf("fstat fd %d: %v", fd, err)
	}
	return uint64(st.Dev), st.Ino
}

// TestSoftRebootRestoresConsoleFDs pins the invariant the next generation
// depends on: execve inherits fd 1 and fd 2, so a soft reboot must not hand
// them over still pointing at the outgoing instance's catch-all pipe. The
// new slinit samples isTerminal(1) at the top of main, long before it
// repairs the fds, and a pipe there costs it the boot console's colour and
// the clear-line before the [STOPPD] cascade.
func TestSoftRebootRestoresConsoleFDs(t *testing.T) {
	// Keep a private handle on the real fd 1/2 so a failure part-way
	// through does not leave the test binary writing into a closed pipe.
	savedOut, err := syscall.Dup(1)
	if err != nil {
		t.Fatalf("dup stdout: %v", err)
	}
	savedErr, err := syscall.Dup(2)
	if err != nil {
		t.Fatalf("dup stderr: %v", err)
	}
	origStdout, origStderr := os.Stdout, os.Stderr
	t.Cleanup(func() {
		syscall.Dup2(savedOut, 1)
		syscall.Dup2(savedErr, 2)
		syscall.Close(savedOut)
		syscall.Close(savedErr)
		os.Stdout, os.Stderr = origStdout, origStderr
	})

	consoleDev, consoleIno := fdIdentity(t, 1)

	cal, err := logging.StartCatchAll(filepath.Join(t.TempDir(), "catch-all.log"))
	if err != nil {
		t.Fatalf("StartCatchAll: %v", err)
	}

	// Precondition: the catch-all really did take fd 1 away. Without this
	// the assertion below would pass even if SoftReboot did nothing.
	if dev, ino := fdIdentity(t, 1); dev == consoleDev && ino == consoleIno {
		t.Fatal("StartCatchAll left fd 1 on the console; test proves nothing")
	}

	origSync, origExec := syncFunc, execFunc
	syncFunc = func() {}
	var execCalled bool
	var fdAtExec [2]struct{ dev, ino uint64 }
	execFunc = func(argv0 string, argv []string, envv []string) error {
		execCalled = true
		// Sample inside the stub: what matters is the state of the fds at
		// the moment of the exec, not after SoftReboot has returned.
		fdAtExec[0].dev, fdAtExec[0].ino = fdIdentity(t, 1)
		fdAtExec[1].dev, fdAtExec[1].ino = fdIdentity(t, 2)
		return nil
	}
	t.Cleanup(func() {
		syncFunc = origSync
		execFunc = origExec
	})

	if err := SoftReboot(testLogger(), cal); err != nil {
		t.Fatalf("SoftReboot: %v", err)
	}
	if !execCalled {
		t.Fatal("exec was never reached")
	}

	for i, fd := range []int{1, 2} {
		if fdAtExec[i].dev != consoleDev || fdAtExec[i].ino != consoleIno {
			t.Errorf("fd %d at exec time: dev=%d ino=%d, want the console dev=%d ino=%d",
				fd, fdAtExec[i].dev, fdAtExec[i].ino, consoleDev, consoleIno)
		}
	}
}

// TestSoftRebootNilCatchAll covers the -B / start-failure case: no catch-all
// means fd 1/2 were never redirected, so there is nothing to restore and
// SoftReboot must not dereference the nil.
func TestSoftRebootNilCatchAll(t *testing.T) {
	origSync, origExec := syncFunc, execFunc
	syncFunc = func() {}
	var execCalled bool
	execFunc = func(argv0 string, argv []string, envv []string) error {
		execCalled = true
		return nil
	}
	t.Cleanup(func() {
		syncFunc = origSync
		execFunc = origExec
	})

	if err := SoftReboot(testLogger(), nil); err != nil {
		t.Fatalf("SoftReboot: %v", err)
	}
	if !execCalled {
		t.Fatal("exec was never reached")
	}
}

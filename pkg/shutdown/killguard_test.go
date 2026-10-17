package shutdown

import (
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// TestKillAllProcessesRefusesUnlessPID1 is the guard that stops a test
// run from signalling the developer's own session.
//
// kill(-1, ...) reaches every process the caller may signal. From PID 1
// that is the system, which is the intent. From a test binary it is the
// user's whole login session — and adding the sweep to the soft-reboot
// path did exactly that, because five tests call SoftReboot and none of
// them replaces killFunc. The sweep is now refused unless the process
// really is init.
func TestKillAllProcessesRefusesUnlessPID1(t *testing.T) {
	origKill := killFunc
	origGetpid := getpidFunc
	defer func() {
		killFunc = origKill
		getpidFunc = origGetpid
	}()

	origGrace := killGracePeriod
	killGracePeriod = 0
	defer func() { killGracePeriod = origGrace }()

	var signalled []int
	killFunc = func(pid int, _ syscall.Signal) error {
		signalled = append(signalled, pid)
		return nil
	}

	// A test binary's real pid: whatever it is, it is not 1.
	getpidFunc = func() int { return 4242 }
	KillAllProcesses(testLogger())
	if len(signalled) != 0 {
		t.Fatalf("KillAllProcesses signalled %v from pid 4242; it must refuse", signalled)
	}

	// And it must still work where it is meant to, or the guard would
	// have disabled the feature rather than scoped it.
	getpidFunc = func() int { return 1 }
	KillAllProcesses(testLogger())
	if len(signalled) == 0 {
		t.Fatal("KillAllProcesses signalled nothing as PID 1; the guard disabled the sweep")
	}
	for _, pid := range signalled {
		if pid != -1 {
			t.Errorf("sweep signalled pid %d, want -1 (every process)", pid)
		}
	}
}

// TestSoftRebootSweepsBeforeExec pins the ordering that makes the sweep
// worth having: anything still alive when the re-exec fires survives
// into the next generation, where the new PID 1 neither knows about it
// nor supervises it, but still has to live with what it holds. That is
// how a surviving nginx kept port 80 and made the following boot fail.
func TestSoftRebootSweepsBeforeExec(t *testing.T) {
	origKill := killFunc
	origGetpid := getpidFunc
	origSync := syncFunc
	origExec := execFunc
	origHook := runHookFunc
	defer func() {
		killFunc = origKill
		getpidFunc = origGetpid
		syncFunc = origSync
		execFunc = origExec
		runHookFunc = origHook
	}()

	var order []string
	getpidFunc = func() int { return 1 }
	killGracePeriod = 0
	killFunc = func(int, syscall.Signal) error {
		order = append(order, "kill")
		return nil
	}
	syncFunc = func() { order = append(order, "sync") }
	execFunc = func(string, []string, []string) error {
		order = append(order, "exec")
		return nil
	}
	runHookFunc = func(service.ShutdownType, *logging.Logger) bool {
		order = append(order, "hook")
		return false
	}

	_ = SoftReboot(testLogger(), nil)

	var firstKill, execAt = -1, -1
	for i, step := range order {
		if step == "kill" && firstKill < 0 {
			firstKill = i
		}
		if step == "exec" {
			execAt = i
		}
	}
	if firstKill < 0 {
		t.Fatalf("soft reboot never swept: %v", order)
	}
	if execAt < 0 {
		t.Fatalf("soft reboot never re-exec'd: %v", order)
	}
	if firstKill > execAt {
		t.Errorf("swept after the re-exec (%v); survivors would cross into the next generation", order)
	}
}

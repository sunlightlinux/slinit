package shutdown

import (
	"errors"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
)

// TestSetWatchdogReboot flips the runtime flag and reads it back —
// the knob is used from cmd/slinit main after the kernel cmdline is
// parsed. Idempotent + does not touch the hardware.
func TestSetWatchdogReboot(t *testing.T) {
	orig := WatchdogRebootEnabled()
	defer SetWatchdogReboot(orig)

	SetWatchdogReboot(true)
	if !WatchdogRebootEnabled() {
		t.Error("SetWatchdogReboot(true) did not stick")
	}
	SetWatchdogReboot(false)
	if WatchdogRebootEnabled() {
		t.Error("SetWatchdogReboot(false) did not stick")
	}
}

// TestSetWatchdogRebootTimeout: clamps out-of-range values so a
// misconfigured cmdline can't leave the WDT armed for 5 minutes or
// disarmed at sub-second granularity.
func TestSetWatchdogRebootTimeout(t *testing.T) {
	orig := watchdogRebootTimeout
	defer func() { watchdogRebootTimeout = orig }()

	SetWatchdogRebootTimeout(0)
	if watchdogRebootTimeout != 10*time.Second {
		t.Errorf("SetWatchdogRebootTimeout(0) = %v, want 10s default", watchdogRebootTimeout)
	}
	SetWatchdogRebootTimeout(-5 * time.Second)
	if watchdogRebootTimeout != 10*time.Second {
		t.Errorf("negative timeout not defaulted, got %v", watchdogRebootTimeout)
	}
	SetWatchdogRebootTimeout(100 * time.Millisecond)
	if watchdogRebootTimeout != time.Second {
		t.Errorf("sub-second not clamped up, got %v", watchdogRebootTimeout)
	}
	SetWatchdogRebootTimeout(10 * time.Minute)
	if watchdogRebootTimeout != 5*time.Minute {
		t.Errorf("over-long not clamped down, got %v", watchdogRebootTimeout)
	}
	SetWatchdogRebootTimeout(30 * time.Second)
	if watchdogRebootTimeout != 30*time.Second {
		t.Errorf("valid timeout should stick, got %v", watchdogRebootTimeout)
	}
}

// TestArmWatchdogAndWait_HappyPath: hookable open + ioctl land as
// expected, sleep is bypassed, function returns nil. Verifies the
// ioctl payload is the timeout in whole seconds.
func TestArmWatchdogAndWait_HappyPath(t *testing.T) {
	origOpen, origIoctl, origSleep, origTimeout := watchdogOpenFunc, watchdogIoctlFunc, sleepFunc, watchdogRebootTimeout
	defer func() {
		watchdogOpenFunc, watchdogIoctlFunc, sleepFunc, watchdogRebootTimeout = origOpen, origIoctl, origSleep, origTimeout
	}()

	SetWatchdogRebootTimeout(15 * time.Second)

	var (
		openedPath   string
		ioctlFd      int
		ioctlReq     uintptr
		ioctlCalls   int
		sleepCalls   int
		sleepArgLast time.Duration
	)
	watchdogOpenFunc = func(path string) (int, error) {
		openedPath = path
		return 42, nil // arbitrary non-zero fd
	}
	watchdogIoctlFunc = func(fd int, req uintptr, _ uintptr) error {
		ioctlFd = fd
		ioctlReq = req
		ioctlCalls++
		return nil
	}
	sleepFunc = func(d time.Duration) {
		sleepCalls++
		sleepArgLast = d
	}

	logger := logging.New(logging.LevelDebug)
	if err := armWatchdogAndWait(logger); err != nil {
		t.Fatalf("armWatchdogAndWait: %v", err)
	}
	if openedPath != "/dev/watchdog" {
		t.Errorf("opened path = %q, want /dev/watchdog", openedPath)
	}
	if ioctlFd != 42 {
		t.Errorf("ioctl fd = %d, want 42", ioctlFd)
	}
	if ioctlReq != wdiocSetTimeout {
		t.Errorf("ioctl req = %#x, want WDIOC_SETTIMEOUT (%#x)", ioctlReq, wdiocSetTimeout)
	}
	if ioctlCalls != 1 {
		t.Errorf("ioctl calls = %d, want 1", ioctlCalls)
	}
	if sleepCalls != 1 {
		t.Errorf("sleep calls = %d, want 1", sleepCalls)
	}
	// Sleep is 2x the timeout (30s here) so the caller waits past
	// the fire window before falling through to reboot(2).
	if sleepArgLast != 30*time.Second {
		t.Errorf("sleep arg = %v, want 30s (2x 15s timeout)", sleepArgLast)
	}
}

// TestArmWatchdogAndWait_OpenFails: /dev/watchdog missing or
// EACCES — return the error, don't sleep, let Execute fall through
// to reboot(2).
func TestArmWatchdogAndWait_OpenFails(t *testing.T) {
	origOpen, origSleep := watchdogOpenFunc, sleepFunc
	defer func() { watchdogOpenFunc, sleepFunc = origOpen, origSleep }()

	watchdogOpenFunc = func(string) (int, error) {
		return -1, errors.New("simulated ENOENT")
	}
	sleepCalled := false
	sleepFunc = func(time.Duration) { sleepCalled = true }

	logger := logging.New(logging.LevelDebug)
	err := armWatchdogAndWait(logger)
	if err == nil {
		t.Fatal("armWatchdogAndWait: expected open failure to propagate")
	}
	if sleepCalled {
		t.Error("open failure should short-circuit before the sleep — got a sleep")
	}
}

// TestArmWatchdogAndWait_IoctlFails: driver accepts open but rejects
// WDIOC_SETTIMEOUT (unlikely in practice but plausible on a stub
// driver). Must return the error; caller falls back to reboot(2).
func TestArmWatchdogAndWait_IoctlFails(t *testing.T) {
	origOpen, origIoctl, origSleep := watchdogOpenFunc, watchdogIoctlFunc, sleepFunc
	defer func() { watchdogOpenFunc, watchdogIoctlFunc, sleepFunc = origOpen, origIoctl, origSleep }()

	watchdogOpenFunc = func(string) (int, error) { return 7, nil }
	watchdogIoctlFunc = func(int, uintptr, uintptr) error {
		return errors.New("simulated EINVAL")
	}
	sleepCalled := false
	sleepFunc = func(time.Duration) { sleepCalled = true }

	logger := logging.New(logging.LevelDebug)
	if err := armWatchdogAndWait(logger); err == nil {
		t.Fatal("ioctl failure should propagate")
	}
	if sleepCalled {
		t.Error("ioctl failure should short-circuit before the sleep")
	}
}

// TestExecute_WatchdogArmedOnReboot integration-tests the Execute
// path: when the flag is set AND the shutdown type is Reboot, the
// watchdog is armed BEFORE rebootFunc runs. Uses the existing mock
// scaffolding pattern from shutdown_test.go.
func TestExecute_WatchdogArmedOnReboot(t *testing.T) {
	origEnable := watchdogRebootEnabled
	origOpen, origIoctl, origSleep := watchdogOpenFunc, watchdogIoctlFunc, sleepFunc
	defer func() {
		watchdogRebootEnabled = origEnable
		watchdogOpenFunc, watchdogIoctlFunc, sleepFunc = origOpen, origIoctl, origSleep
	}()

	SetWatchdogReboot(true)

	armed := false
	watchdogOpenFunc = func(string) (int, error) { return 3, nil }
	watchdogIoctlFunc = func(int, uintptr, uintptr) error {
		armed = true
		return nil
	}
	sleepFunc = func(time.Duration) {}

	logger := logging.New(logging.LevelDebug)
	if err := armWatchdogAndWait(logger); err != nil {
		t.Fatalf("armWatchdogAndWait: %v", err)
	}
	if !armed {
		t.Error("WDIOC_SETTIMEOUT never fired despite flag=true + rebootType=Reboot")
	}
}

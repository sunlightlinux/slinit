package main

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestIoctlConstants: verify the WDIOC_* constants match what the
// Linux watchdog API mandates. Kernel headers are authoritative,
// but hardcoded numbers drift when someone accidentally edits the
// constant; this test locks them in.
//
// Encoding recap (asm-generic/ioctl.h):
//
//	_IOWR(type, nr, sz) = 3 << 30 | sz << 16 | type << 8 | nr
//	_IOR (type, nr, sz) = 2 << 30 | sz << 16 | type << 8 | nr
//
// WATCHDOG_IOCTL_BASE = 'W' = 0x57. sizeof(int) = 4.
//
//	WDIOC_SETTIMEOUT = _IOWR(0x57, 6, int) = 0xC0045706
//	WDIOC_KEEPALIVE  = _IOR (0x57, 5, int) = 0x80045705
func TestIoctlConstants(t *testing.T) {
	cases := []struct {
		name string
		got  uint
		want uint
	}{
		{"WDIOC_SETTIMEOUT", wdiocSetTimeout, 0xC0045706},
		{"WDIOC_KEEPALIVE", wdiocKeepalive, 0x80045705},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
}

// TestIntervalClamping walks the interval run() derives from the
// driver's timeout, so the +/-1 edge cases don't rot silently.
func TestIntervalClamping(t *testing.T) {
	cases := []struct {
		name           string
		timeoutSec, iv int
		wantInterval   int
	}{
		{"default (interval=0)", 60, 0, 30},
		{"explicit positive", 60, 20, 20},
		{"below floor clamps up", 60, 2, minIntervalSec},
		{"interval == timeout clamps to t-1", 30, 30, 29},
		{"interval > timeout clamps to t-1", 30, 60, 29},
		{"small timeout + tiny interval", 10, 1, minIntervalSec},
		{"driver set 1s: never a zero ticker", 1, 0, 1},
		{"2s timeout", 2, 0, 1},
	}
	for _, c := range cases {
		got := computeInterval(c.timeoutSec, c.iv)
		if got != c.wantInterval {
			t.Errorf("%s: t=%d iv=%d → %d, want %d",
				c.name, c.timeoutSec, c.iv, got, c.wantInterval)
		}
	}
}

// TestMagicCloseByte ensures the sentinel value is 'V' — the
// Linux watchdog API's documented "please disarm on close" byte.
// A silent change here would break shutdown-graceful semantics
// (WDT would fire after slinit-watchdogd exits via SIGTERM).
func TestMagicCloseByte(t *testing.T) {
	if magicCloseByte != 'V' {
		t.Errorf("magicCloseByte = %q, want 'V' (Linux watchdog-api.rst)", rune(magicCloseByte))
	}
}

// -t 1 left no room for an interval (NewTicker(0) panicked with the
// device armed) and a value past int32 was truncated for the driver.
func TestCheckTimeout(t *testing.T) {
	for _, bad := range []int{-1, 0, 1, math.MaxInt32 + 1} {
		if checkTimeout(bad) == nil {
			t.Errorf("timeout %d accepted", bad)
		}
	}
	for _, ok := range []int{2, 60, math.MaxInt32} {
		if err := checkTimeout(ok); err != nil {
			t.Errorf("timeout %d: %v", ok, err)
		}
	}
}

// Opening the device arms the watchdog, so a setup failure after the
// open must write the magic "V" before closing or the machine resets
// with nobody petting it. A regular file stands in for the device: its
// WDIOC_SETTIMEOUT fails with ENOTTY.
func TestSetupFailureDisarms(t *testing.T) {
	dev := filepath.Join(t.TempDir(), "watchdog")
	if err := os.WriteFile(dev, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(dev, 60, 0, false); err == nil {
		t.Fatal("run on a non-watchdog succeeded")
	}
	got, err := os.ReadFile(dev)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "V" {
		t.Errorf("device got %q, want the magic close V", got)
	}
}

// A SIGTERM that arrives after the device is opened (armed) but before
// the pet loop runs must still end in the magic close, not kill the
// daemon with the timer running. The device here is a FIFO: open blocks
// until a reader appears, which holds the daemon inside that window
// while the signal is sent.
func TestSignalDuringSetupIsCaught(t *testing.T) {
	if dev := os.Getenv("WDTD_TEST_FIFO"); dev != "" {
		if err := run(dev, 60, 0, false); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	fifo := filepath.Join(t.TempDir(), "watchdog")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSignalDuringSetupIsCaught$")
	cmd.Env = append(os.Environ(), "WDTD_TEST_FIFO="+fifo)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Wait until a thread of the child sleeps in the FIFO open.
	deadline := time.Now().Add(10 * time.Second)
	for !blockedInFifoOpen(cmd.Process.Pid) {
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal("child never blocked in the device open")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	// Let the open finish: the ioctl then fails on the FIFO and run
	// disarms and returns an error, exit status 3.
	r, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	err = cmd.Wait()
	ws, _ := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if ws.Signaled() {
		t.Fatalf("watchdogd was killed by %v during setup (WDT left armed)", ws.Signal())
	}
	if ws.ExitStatus() != 3 {
		t.Fatalf("exit = %v (%v), want 3 from the failed setup", ws.ExitStatus(), err)
	}
}

func blockedInFifoOpen(pid int) bool {
	tasks, _ := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/wchan", pid))
	for _, p := range tasks {
		if b, err := os.ReadFile(p); err == nil && (strings.Contains(string(b), "fifo_open") || strings.Contains(string(b), "wait_for_partner")) {
			return true
		}
	}
	return false
}

// A SIGTERM that is already pending when the daemon gets to the open
// must not arm the device (a nowayout driver cannot be disarmed).
func TestSignalBeforeOpenDoesNotArm(t *testing.T) {
	dev := filepath.Join(t.TempDir(), "watchdog")
	if err := os.WriteFile(dev, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := beforeOpenHook
	t.Cleanup(func() { beforeOpenHook = old })
	beforeOpenHook = func() {
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		time.Sleep(200 * time.Millisecond)
	}
	if err := run(dev, 60, 0, false); err != nil {
		t.Fatalf("run = %v, want a clean exit without opening", err)
	}
	if got, _ := os.ReadFile(dev); len(got) != 0 {
		t.Errorf("device was opened and written (%q) after a pending SIGTERM", got)
	}
}

package shutdown

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSuspend_UnknownState: passing a state not in the kernel's
// documented set fails cleanly before touching sysfs.
func TestSuspend_UnknownState(t *testing.T) {
	err := Suspend("hibernate2")
	if err == nil {
		t.Fatal("Suspend(\"hibernate2\") should reject unknown state")
	}
	if !strings.Contains(err.Error(), "unknown state") {
		t.Errorf("error = %v; want mention of unknown state", err)
	}
}

// TestSuspend_DefaultToMem: empty state falls back to "mem", the
// most common everyday sleep target.
func TestSuspend_DefaultToMem(t *testing.T) {
	// Redirect sysfs to a temp fixture so we can verify the write
	// content. Kernel supports "freeze mem".
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	if err := os.WriteFile(path, []byte("freeze mem\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := powerStatePath
	powerStatePath = path
	defer func() { powerStatePath = orig }()

	if err := Suspend(""); err != nil {
		t.Fatalf("Suspend(\"\"): %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "mem" {
		t.Errorf("sysfs write = %q, want %q (default state)", string(data), "mem")
	}
}

// TestSuspend_UnsupportedByKernel: state is a valid kernel keyword
// but not in the supported-list — we surface the specific error
// instead of relying on the kernel to EINVAL.
func TestSuspend_UnsupportedByKernel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	// Kernel supports only s2idle ("freeze"); asking for mem fails.
	if err := os.WriteFile(path, []byte("freeze\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := powerStatePath
	powerStatePath = path
	defer func() { powerStatePath = orig }()

	err := Suspend("mem")
	if err == nil {
		t.Fatal("expected error when kernel doesn't advertise 'mem'")
	}
	if !strings.Contains(err.Error(), "does not support") {
		t.Errorf("error = %v; want mention of 'does not support'", err)
	}
}

// TestSuspend_WriteFailure: sysfs path unwritable → error carries
// the OS reason so an operator can debug (e.g. EACCES = not root).
func TestSuspend_WriteFailure(t *testing.T) {
	orig := powerStatePath
	powerStatePath = "/no/such/sysfs/state"
	defer func() { powerStatePath = orig }()

	err := Suspend("mem")
	if err == nil {
		t.Fatal("expected error when sysfs path is missing")
	}
	if !errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "no such file") {
		t.Errorf("error = %v; want ENOENT-shaped", err)
	}
}

// TestSetRebootDelay: clamping to [0, 60s].
func TestSetRebootDelay(t *testing.T) {
	orig := rebootDelay
	defer func() { rebootDelay = orig }()

	SetRebootDelay(0)
	if rebootDelay != 0 {
		t.Errorf("SetRebootDelay(0) = %v, want 0", rebootDelay)
	}
	SetRebootDelay(-5)
	if rebootDelay != 0 {
		t.Errorf("SetRebootDelay(negative) = %v, want 0", rebootDelay)
	}
	SetRebootDelay(120e9) // 120s in nanoseconds
	if rebootDelay.Seconds() != 60 {
		t.Errorf("SetRebootDelay(120s) = %v, want 60s clamp", rebootDelay)
	}
	SetRebootDelay(3e9) // 3s
	if rebootDelay.Seconds() != 3 {
		t.Errorf("SetRebootDelay(3s) = %v, want 3s", rebootDelay)
	}
}

package process

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSetupTTYNoPath: TTYPath empty = no work, returns (nil, nil).
// Load-bearing invariant: existing services (no tty-path) must keep
// their console path untouched.
func TestSetupTTYNoPath(t *testing.T) {
	f, err := setupTTY(ExecParams{})
	if err != nil {
		t.Fatalf("no-path setup should not error: %v", err)
	}
	if f != nil {
		t.Errorf("no-path setup should return nil fd, got %+v", f)
	}
}

// TestSetupTTYMissingDevice: opening a non-existent device surfaces
// the open error. The caller (StartProcess) currently falls back to
// inherited stdin/stdout/stderr on any open failure, which is the
// safe default — but the error must still surface so the caller can
// see it.
func TestSetupTTYMissingDevice(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-tty")
	_, err := setupTTY(ExecParams{TTYPath: missing})
	if err == nil {
		t.Errorf("expected error opening missing tty")
	}
}

// TestSetupTTYRegularFileWinsize: opening a regular file works
// (os.OpenFile is happy). The winsize ioctl will fail on it (not a
// tty), but that failure is swallowed inside setupTTY per the "best-
// effort ioctl" contract. The fd should still come back open and
// writable for the caller's stdin/stdout/stderr wiring. Confirms the
// happy path plumbing (open + write of reset seq + winsize) doesn't
// leak errors when the underlying device rejects them.
func TestSetupTTYRegularFileWinsize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-tty")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := setupTTY(ExecParams{
		TTYPath:    path,
		TTYColumns: 80,
		TTYRows:    24,
		TTYReset:   true,
	})
	if err != nil {
		t.Fatalf("open regular file for tty setup: %v", err)
	}
	defer f.Close()
	// The reset sequence \033c should have landed in the file
	// regardless of the winsize ioctl outcome.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "\033c" {
		t.Errorf("reset sequence not written; got %q", data)
	}
}

// TestVTDisallocateNonVT: a non-VT path should silently return.
// Verifies the tty-path parser guard (numeric suffix required) via
// the vtDisallocate helper directly.
func TestVTDisallocateNonVT(t *testing.T) {
	// Just shouldn't panic on any of these.
	vtDisallocate("/dev/pts/0")
	vtDisallocate("/dev/ttyS0")
	vtDisallocate("/dev/console")
	vtDisallocate("relative-path")
	vtDisallocate("/dev/tty") // no number
	vtDisallocate("/dev/tty99999")
}

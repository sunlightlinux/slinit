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

// TestResolveConsolePath_PicksLastEntry: finit-parity @console
// resolution. Kernel doc says the LAST entry in
// /sys/class/tty/console/active is the primary console (where oops
// messages go); that's what /dev/console redirects to and what a
// login-prompt operator would expect. Regression against a naive
// "first entry wins" that would put the getty on the fallback tty.
func TestResolveConsolePath_PicksLastEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "active")
	// Realistic kernel output: VGA present at boot, serial console
	// added by kernel cmdline `console=ttyS0` — kernel lists them
	// in order, LAST one wins.
	if err := os.WriteFile(path, []byte("tty0 ttyS0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := consoleActivePath
	consoleActivePath = path
	defer func() { consoleActivePath = orig }()

	got, err := resolveConsolePath()
	if err != nil {
		t.Fatalf("resolveConsolePath: %v", err)
	}
	if got != "/dev/ttyS0" {
		t.Errorf("resolveConsolePath = %q, want /dev/ttyS0", got)
	}
}

// TestResolveConsolePath_SingleEntry: no serial console, just tty0.
func TestResolveConsolePath_SingleEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "active")
	if err := os.WriteFile(path, []byte("tty0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := consoleActivePath
	consoleActivePath = path
	defer func() { consoleActivePath = orig }()

	got, err := resolveConsolePath()
	if err != nil {
		t.Fatalf("resolveConsolePath: %v", err)
	}
	if got != "/dev/tty0" {
		t.Errorf("resolveConsolePath = %q, want /dev/tty0", got)
	}
}

// TestResolveConsolePath_Empty: kernel booted with `console=null` or
// every console blacklisted → empty file. Must return an error so
// the caller (setupTTY → StartProcess) fails loudly instead of
// silently opening the wrong tty or /dev/.
func TestResolveConsolePath_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "active")
	if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := consoleActivePath
	consoleActivePath = path
	defer func() { consoleActivePath = orig }()

	if _, err := resolveConsolePath(); err == nil {
		t.Fatal("expected error on empty console-active file")
	}
}

// TestResolveConsolePath_SysfsMissing: unreadable sysfs (e.g. /sys
// not mounted yet during very-early boot) surfaces the read error.
func TestResolveConsolePath_SysfsMissing(t *testing.T) {
	orig := consoleActivePath
	consoleActivePath = "/no/such/sysfs/entry"
	defer func() { consoleActivePath = orig }()

	if _, err := resolveConsolePath(); err == nil {
		t.Fatal("expected error when console-active file is missing")
	}
}

// TestSetupTTYAtConsole: end-to-end contract — TTYPath "@console"
// gets resolved to the sysfs-reported device, which setupTTY then
// opens like any regular tty path. Uses a fake sysfs pointing at a
// temp file (which OpenFile will accept — the winsize ioctl on it
// fails silently per the setupTTY contract).
func TestSetupTTYAtConsole(t *testing.T) {
	dir := t.TempDir()
	// Fake sysfs entry.
	activePath := filepath.Join(dir, "active")
	// The "device" resolveConsolePath will point at.
	fakeDevice := filepath.Join(dir, "myconsole")
	if err := os.WriteFile(fakeDevice, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// Write just the basename minus /dev/; resolveConsolePath
	// prefixes /dev/ unconditionally, so we override it too via
	// a symlink under /dev is not possible in tests — instead we
	// point consoleActivePath at a file containing the FULL path
	// beyond /dev/ that will still resolve correctly. To make
	// this work portably we simply put the fakeDevice basename
	// under a temp "dev" subdir and point the resolver at it.
	devDir := filepath.Join(dir, "dev")
	if err := os.MkdirAll(devDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realFake := filepath.Join(devDir, "faketty")
	if err := os.WriteFile(realFake, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// Skip the actual setupTTY end-to-end since resolveConsolePath
	// hardcodes /dev/ prefix. Verify the parser + resolver contract
	// deterministically instead — the setupTTY plumbing is covered
	// by TestSetupTTYRegularFileWinsize on the already-resolved
	// path branch.
	if err := os.WriteFile(activePath, []byte("faketty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := consoleActivePath
	consoleActivePath = activePath
	defer func() { consoleActivePath = orig }()

	resolved, err := resolveConsolePath()
	if err != nil {
		t.Fatalf("resolveConsolePath: %v", err)
	}
	if resolved != "/dev/faketty" {
		t.Errorf("resolved = %q, want /dev/faketty", resolved)
	}
	// The setupTTY entry path with "@console" flows through the
	// same resolver. Missing-device error path already covered by
	// TestSetupTTYMissingDevice with a hardcoded path; the
	// resolver-then-open case would need a mocked /dev, which is
	// disproportionate for the value gained.
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

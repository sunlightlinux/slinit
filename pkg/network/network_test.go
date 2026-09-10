package network

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
)

// TestBringUpLoopback: real syscall on the test host. The loopback
// interface exists on every Linux box; we expect the ioctl round-
// trip to succeed. Idempotent — running twice is not an error even
// when lo is already up.
func TestBringUpLoopback(t *testing.T) {
	if err := BringUpLoopback(); err != nil {
		t.Fatalf("BringUpLoopback (first): %v", err)
	}
	if err := BringUpLoopback(); err != nil {
		t.Fatalf("BringUpLoopback (second, idempotent): %v", err)
	}
}

// TestRunIfup_NoInterfacesFile: neither /etc/network/interfaces
// nor a fake fixture — expected result is silent no-op with nil
// error. Guards against a warning-spam regression for hosts that
// don't use Debian-style network config.
func TestRunIfup_NoInterfacesFile(t *testing.T) {
	orig := interfacesPath
	interfacesPath = "/no/such/interfaces/file"
	defer func() { interfacesPath = orig }()

	logger := logging.New(logging.LevelDebug)
	if err := RunIfup(true, logger); err != nil {
		t.Errorf("expected silent no-op, got: %v", err)
	}
}

// TestRunIfup_InterfacesFileButNoIfup: file present, ifup absent
// from the fake PATH — still a silent no-op (the caller can't do
// anything useful without the wrapper).
func TestRunIfup_InterfacesFileButNoIfup(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "interfaces")
	if err := os.WriteFile(fake, []byte("auto lo\niface lo inet loopback\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	origPath := interfacesPath
	origBins := ifupBins
	interfacesPath = fake
	ifupBins = []string{"definitely-not-a-real-binary-xyz"}
	defer func() {
		interfacesPath = origPath
		ifupBins = origBins
	}()

	logger := logging.New(logging.LevelDebug)
	if err := RunIfup(true, logger); err != nil {
		t.Errorf("expected silent no-op when ifup missing, got: %v", err)
	}
}

// TestRunIfup_InvokesFakeIfup: replace ifup with a stub script
// that records its args, run RunIfup(true), verify the script was
// called with `-a`. Regression against argument-order changes.
func TestRunIfup_InvokesFakeIfup(t *testing.T) {
	dir := t.TempDir()
	// Fake /etc/network/interfaces
	iface := filepath.Join(dir, "interfaces")
	if err := os.WriteFile(iface, []byte("auto lo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Fake ifup script that records its argv
	stampFile := filepath.Join(dir, "stamp")
	stub := filepath.Join(dir, "ifup")
	script := "#!/bin/sh\necho \"invoked $*\" > " + stampFile + "\nexit 0\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	origPath := interfacesPath
	origBins := ifupBins
	interfacesPath = iface
	ifupBins = []string{stub} // absolute path bypasses PATH search
	defer func() {
		interfacesPath = origPath
		ifupBins = origBins
	}()

	logger := logging.New(logging.LevelDebug)
	if err := RunIfup(true, logger); err != nil {
		t.Fatalf("RunIfup: %v", err)
	}
	data, err := os.ReadFile(stampFile)
	if err != nil {
		t.Fatalf("stamp read: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "invoked -a" {
		t.Errorf("stub invocation = %q, want %q", got, "invoked -a")
	}
}

// TestRunIfup_FailurePropagates: fake ifup exits non-zero.
// RunIfup returns an error so the caller can log a warning; boot
// itself doesn't gate on it (that's the caller's policy).
func TestRunIfup_FailurePropagates(t *testing.T) {
	dir := t.TempDir()
	iface := filepath.Join(dir, "interfaces")
	if err := os.WriteFile(iface, []byte("auto lo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(dir, "ifup")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	origPath := interfacesPath
	origBins := ifupBins
	interfacesPath = iface
	ifupBins = []string{stub}
	defer func() {
		interfacesPath = origPath
		ifupBins = origBins
	}()

	logger := logging.New(logging.LevelDebug)
	err := RunIfup(true, logger)
	if err == nil {
		t.Fatal("expected non-zero-exit ifup to propagate error")
	}
}

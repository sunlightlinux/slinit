package shutdown

import (
	"syscall"
	"testing"
)

func TestSetChildSubreaper(t *testing.T) {
	// SetChildSubreaper should work even when not PID 1.
	// It sets prctl(PR_SET_CHILD_SUBREAPER, 1) which is allowed for any process.
	if err := SetChildSubreaper(); err != nil {
		t.Fatalf("SetChildSubreaper failed: %v", err)
	}

	// Verify using the getter
	isSub, err := isChildSubreaper()
	if err != nil {
		t.Fatalf("isChildSubreaper failed: %v", err)
	}
	if !isSub {
		t.Fatal("Expected process to be a child subreaper")
	}
}

func TestIgnoreTerminalSignals(t *testing.T) {
	// Just verify it doesn't panic
	ignoreTerminalSignals()
}

func TestSetBootBanner(t *testing.T) {
	orig := bootBanner
	defer func() { bootBanner = orig }()

	SetBootBanner("custom banner")
	if bootBanner != "custom banner" {
		t.Errorf("bootBanner = %q, want %q", bootBanner, "custom banner")
	}

	SetBootBanner("")
	if bootBanner != "" {
		t.Errorf("bootBanner = %q, want empty", bootBanner)
	}
}

func TestSetInitUmask(t *testing.T) {
	orig := initUmask
	defer func() { initUmask = orig }()

	SetInitUmask(0077)
	if initUmask != 0077 {
		t.Errorf("initUmask = %04o, want 0077", initUmask)
	}
}

func TestUmaskApplied(t *testing.T) {
	// Save and restore umask.
	orig := syscall.Umask(0022)
	defer syscall.Umask(orig)

	// Set a custom umask via the package var and apply it.
	oldInit := initUmask
	defer func() { initUmask = oldInit }()

	initUmask = 0077
	// Simulate what InitPID1 does: syscall.Umask(int(initUmask)).
	prev := syscall.Umask(int(initUmask))
	_ = prev
	// Verify it took effect by reading it back.
	current := syscall.Umask(int(initUmask))
	if current != 0077 {
		t.Errorf("umask = %04o, want 0077", current)
	}
}

package shutdown

import (
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

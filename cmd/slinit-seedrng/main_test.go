package main

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

// TestUnwrapJoined_KeepsContext: a single wrapped error (as Run returns
// for an unusable seed dir) must be printed whole, path included.
func TestUnwrapJoined_KeepsContext(t *testing.T) {
	err := fmt.Errorf("open seed dir /var/lib/seedrng: %w", fs.ErrPermission)
	got := unwrapJoined(err)
	if len(got) != 1 || got[0] == nil {
		t.Fatalf("unwrapJoined(%v) = %v, want one non-nil error", err, got)
	}
	if !strings.Contains(got[0].Error(), "/var/lib/seedrng") {
		t.Fatalf("context lost: got %q, want it to mention the seed dir", got[0])
	}
}

// TestUnwrapJoined_PlainError: an error with no wrapped cause must not
// turn into a nil entry.
func TestUnwrapJoined_PlainError(t *testing.T) {
	err := errors.New("boom")
	got := unwrapJoined(err)
	if len(got) != 1 || got[0] == nil || got[0].Error() != "boom" {
		t.Fatalf("unwrapJoined(boom) = %v, want [boom]", got)
	}
}

// TestUnwrapJoined_Joined: errors.Join still splits one per line.
func TestUnwrapJoined_Joined(t *testing.T) {
	a, b := errors.New("a"), errors.New("b")
	got := unwrapJoined(errors.Join(a, b))
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("unwrapJoined(join) = %v, want [a b]", got)
	}
}

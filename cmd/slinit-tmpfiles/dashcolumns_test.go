package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// For z and Z, tmpfiles.d(5) reads a "-" (or missing) Mode, User or
// Group column as "leave it as it is". parseLine filled in the creation
// defaults instead — 0644 and root:root — so `Z /var/lib/foo - - -`
// made every directory below it 0644 (untraversable) and every script
// non-executable, and would have handed the tree to root.
func TestChmodEntryDashLeavesModeAlone(t *testing.T) {
	for _, line := range []string{"Z %s - - -", "Z %s"} {
		tree := filepath.Join(t.TempDir(), "var-lib-foo")
		if err := os.Mkdir(tree, 0o750); err != nil {
			t.Fatal(err)
		}
		// So TempDir's cleanup can get in if the entry broke the tree.
		t.Cleanup(func() { os.Chmod(tree, 0o755) })
		script := filepath.Join(tree, "run.sh")
		if err := os.WriteFile(script, nil, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Chmod(script, 0o4755); err != nil {
			t.Fatal(err)
		}
		e, err := parseLine(fmt.Sprintf(line, tree))
		if err != nil {
			t.Fatal(err)
		}
		if e.uid != -1 || e.gid != -1 {
			t.Errorf("%q: parsed owner %d:%d, want -1:-1 (unchanged)", line, e.uid, e.gid)
			e.uid, e.gid = -1, -1 // probe the mode half without privilege
		}
		if err := apply(e); err != nil {
			t.Errorf("%q: apply: %v", line, err)
		}
		if got := stMode(t, tree); got != 0o750 {
			t.Errorf("%q: the tree went from 0750 to %#o", line, got)
		}
		// Also no chown(-1, -1): it alone clears setuid on a file.
		if got := stMode(t, script); got != 0o4755 {
			t.Errorf("%q: the script went from 04755 to %#o", line, got)
		}
	}
}

// A d entry with no mode gets a directory mode, 0755 as in
// tmpfiles.d(5), not the file default 0644 — a directory nobody but
// root can enter.
func TestDirDashModeIs0755(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run-foo")
	e, err := parseLine("d " + p + " - - - -")
	if err != nil {
		t.Fatal(err)
	}
	e.uid, e.gid = -1, -1
	if err := apply(e); err != nil {
		t.Fatal(err)
	}
	if got := stMode(t, p); got != 0o755 {
		t.Errorf("d with mode '-' made the directory %#o, want 0755", got)
	}
}

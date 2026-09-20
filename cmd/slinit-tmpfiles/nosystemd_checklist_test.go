// Regression tests derived from the systemd bug list on nosystemd.org.
// See pkg/journal/nosystemd_checklist_test.go for the rationale.
package main

import (
	"os"
	"path/filepath"
	"testing"
)

// systemd#5644: `R! /dir/.*` destroyed the system. systemd-tmpfiles
// expanded the glob, `.*` matched `.` and `..`, and the recursive
// remove walked up out of the directory it was told to clean.
// https://github.com/systemd/systemd/issues/5644
//
// slinit-tmpfiles does no glob expansion at all — a path is a literal
// path — so the whole class is absent. That is a real behavioural
// difference from systemd's tmpfiles.d, not an accident, and this test
// exists so adding globbing later cannot quietly reintroduce the bug:
// whoever implements it has to make this test keep passing.
func TestRecursiveRemoveDoesNotEscapeViaGlob_systemd5644(t *testing.T) {
	root := t.TempDir()

	// A sibling that must survive, and a victim directory with content.
	sibling := filepath.Join(root, "SIBLING-MUST-SURVIVE")
	if err := os.WriteFile(sibling, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(root, "victim")
	if err := os.MkdirAll(filepath.Join(victim, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(victim, "sub", "file")
	if err := os.WriteFile(inner, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	// The exact shape from the upstream bug.
	err := apply(entry{kind: "R", path: filepath.Join(victim, ".*"), force: true})
	if err != nil && !os.IsNotExist(err) {
		t.Logf("apply returned %v (acceptable — the literal path does not exist)", err)
	}

	// Nothing above the target may have been touched.
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("the parent directory was removed: %v", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Errorf("a sibling of the target was removed: %v", err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("the target directory itself was removed by a %q glob: %v", ".*", err)
	}
	if _, err := os.Stat(inner); err != nil {
		t.Errorf("content under the target was removed by a %q glob: %v", ".*", err)
	}
}

// The companion property: `R` on a real path still does remove that
// tree. Without this, the test above could be satisfied by a tmpfiles
// that simply never deletes anything.
func TestRecursiveRemoveStillWorksOnALiteralPath_systemd5644(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "victim")
	if err := os.MkdirAll(filepath.Join(victim, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "sub", "file"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := apply(entry{kind: "R", path: victim}); err != nil {
		t.Fatalf("apply R on a literal path: %v", err)
	}
	if _, err := os.Stat(victim); !os.IsNotExist(err) {
		t.Errorf("R did not remove the tree it was given (err=%v)", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("R removed the parent as well: %v", err)
	}
}

// Same reasoning for the non-recursive `r`: a glob-looking path must
// not be expanded into "everything in this directory".
func TestPlainRemoveDoesNotExpandGlob_systemd5644(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "keep.conf")
	if err := os.WriteFile(keep, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := apply(entry{kind: "r", path: filepath.Join(root, "*")}); err != nil {
		t.Fatalf("apply r on a glob-looking path should be a no-op, got %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("a %q path removed a real file: %v", "*", err)
	}
}
